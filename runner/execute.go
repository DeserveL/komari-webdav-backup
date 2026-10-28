package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"time"
)

func execute(ctx context.Context, c Config, stateDir string, emit func(Report)) Report {
	unlock, err := acquireLock(filepath.Join(stateDir, "node.lock"))
	if err != nil {
		return Report{RunID: c.RunID, Status: "failed", Stage: "检查节点运行锁", Error: err.Error(), Warnings: []string{}}
	}
	defer unlock()
	return executeLocked(ctx, c, stateDir, emit)
}

func executeLocked(ctx context.Context, c Config, stateDir string, emit func(Report)) Report {
	d, err := newDAV(c)
	if err != nil {
		return Report{RunID: c.RunID, Status: "failed", Stage: "检查备份来源", Error: err.Error(), Warnings: []string{}}
	}
	defer d.client.CloseIdleConnections()
	defer d.uploadClient.CloseIdleConnections()
	return executeWithDAV(ctx, c, stateDir, emit, d)
}

func executeWithDAV(ctx context.Context, c Config, stateDir string, emit func(Report), d *DAV) (r Report) {
	r = Report{RunID: c.RunID, Status: "running", Stage: "检查备份来源", Warnings: []string{}}
	defer func() { r.RequestDiagnostics = append([]string(nil), d.diagnostics...); r = boundReport(r) }()
	incompleteFile := ""
	published := false
	preserveRemote := false
	fail := func(err error) Report {
		r.Status = "failed"
		r.Error = redact(err.Error(), c.Password)
		if incompleteFile != "" && !published && !preserveRemote {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if e := d.deleteFile(cleanup, incompleteFile); e != nil {
				r.Warnings = append(r.Warnings, "incomplete remote upload may remain: "+incompleteFile)
			}
		}
		return r
	}
	var err error
	emit(r)
	workRoot := filepath.Join(stateDir, "work")
	if err = os.MkdirAll(workRoot, 0700); err != nil {
		return fail(err)
	}
	// 已持有节点锁，只清理由本执行器创建、超过一天的运行目录。
	if entries, e := os.ReadDir(workRoot); e == nil {
		for _, item := range entries {
			if !item.IsDir() || !validID.MatchString(item.Name()) || item.Name() == c.RunID {
				continue
			}
			if info, e := item.Info(); e == nil && time.Since(info.ModTime()) > 24*time.Hour {
				_ = os.RemoveAll(filepath.Join(workRoot, item.Name()))
			}
		}
	}
	work := filepath.Join(workRoot, c.RunID)
	if err = os.Mkdir(work, 0700); err != nil {
		return fail(fmt.Errorf("run directory already exists or is unavailable: %w", err))
	}
	defer os.RemoveAll(work)
	for _, source := range c.Sources {
		info, e := os.Lstat(source)
		if e != nil {
			return fail(fmt.Errorf("source unavailable %s: %w", source, e))
		}
		parent, e := filepath.EvalSymlinks(filepath.Dir(source))
		if e != nil || filepath.Clean(parent) != filepath.Clean(filepath.Dir(source)) {
			return fail(fmt.Errorf("source parent contains a symlink: %s", source))
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if !info.Mode().IsRegular() && !info.IsDir() {
				return fail(fmt.Errorf("unsupported source type: %s", source))
			}
			f, e := os.Open(source)
			if e != nil {
				return fail(fmt.Errorf("source unreadable %s: %w", source, e))
			}
			if info.IsDir() {
				_, e = f.ReadDir(1)
			}
			_ = f.Close()
			if e != nil && e != io.EOF {
				return fail(fmt.Errorf("source unreadable %s: %w", source, e))
			}
		}
	}
	r.Stage = "检查 WebDAV 读写权限"
	emit(r)
	if err = d.mkdir(ctx, c.RemoteDir); err != nil {
		return fail(err)
	}
	prefix, err := d.choosePrefix(ctx, c)
	if err != nil {
		return fail(err)
	}
	probe := path.Join(c.RemoteDir, ".probe-"+c.RunID)
	probeBytes := []byte("komari-webdav-backup connection test\n")
	if err = d.put(ctx, probe, bytesBody(probeBytes)); err != nil {
		return fail(err)
	}
	if err = d.verifySize(ctx, probe, int64(len(probeBytes))); err != nil {
		_ = d.deleteFile(ctx, probe)
		return fail(err)
	}
	if _, err = d.list(ctx, c.RemoteDir); err != nil {
		_ = d.deleteFile(ctx, probe)
		return fail(err)
	}
	if err = d.deleteFile(ctx, probe); err != nil {
		return fail(fmt.Errorf("WebDAV DELETE permission test failed: %w", err))
	}
	if c.Mode == "test" {
		r.Status = "success"
		r.Stage = "节点来源及 WebDAV 读写检查通过"
		return r
	}
	r.Stage = "检查本地空间"
	emit(r)
	var estimated int64
	patterns := []*regexp.Regexp{}
	for _, pattern := range c.Excludes {
		re, e := compileGlob(pattern)
		if e != nil {
			return fail(e)
		}
		patterns = append(patterns, re)
	}
	for _, source := range normalizedSources(c.Sources) {
		err = filepath.WalkDir(source, func(p string, e fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if p == stateDir {
				return filepath.SkipDir
			}
			name := archiveName(p)
			for _, pattern := range patterns {
				if pattern.MatchString(name) || (e.IsDir() && pattern.MatchString(name+"/")) {
					if e.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			info, err := e.Info()
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				estimated += info.Size()
			}
			return nil
		})
		if err != nil {
			return fail(err)
		}
	}
	if err = checkFreeSpace(work, estimated+estimated/20+64*1024*1024); err != nil {
		return fail(err)
	}
	r.Stage = "正在打包"
	emit(r)
	archive := filepath.Join(work, "backup.tar.gz")
	r.Bytes, r.SHA256, r.Warnings, err = createArchive(ctx, c, archive, stateDir)
	if err != nil {
		return fail(err)
	}
	r = boundReport(r)
	r.Stage = "正在上传"
	emit(r)
	archiveRemote := path.Join(c.RemoteDir, archiveNameFor(prefix))
	manifestRemote := path.Join(c.RemoteDir, manifestName(prefix))
	r.RemotePath = d.target(archiveRemote).String()
	r.ManifestPath = d.target(manifestRemote).String()
	if err = d.putNew(ctx, archiveRemote, func() (io.ReadCloser, int64, error) {
		f, e := os.Open(archive)
		if e != nil {
			return nil, 0, e
		}
		info, e := f.Stat()
		if e != nil {
			_ = f.Close()
			return nil, 0, e
		}
		return f, info.Size(), nil
	}); err != nil {
		if !isUncertain(err) {
			return fail(err)
		}
		preserveRemote = true
		r.Stage = "等待确认上传结果"
		emit(r)
		r.Stage = "正在核对远端大小"
		emit(r)
		original := err
		if err = d.confirmArchive(ctx, archiveRemote, r.Bytes); err != nil {
			r.Stage = "上传结果未确认"
			r.Warnings = append(r.Warnings, "远端文件可能仍在处理，请检查归档位置；本次未删除远端文件")
			return fail(fmt.Errorf("%v；核对未完成：%w", original, err))
		}
		r.Warnings = append([]string{sizeOnlyWarning}, r.Warnings...)
	}
	incompleteFile = archiveRemote
	r.Stage = "核对远端文件大小"
	emit(r)
	if !preserveRemote {
		if err = d.verifySize(ctx, archiveRemote, r.Bytes); err != nil {
			return fail(err)
		}
	}
	m := Manifest{Owner: owner, Version: 2, RunID: c.RunID, JobID: c.JobID, NodeID: c.NodeID,
		CompletedAt: time.Now().UTC(), Archive: archiveNameFor(prefix), Size: r.Bytes, SHA256: r.SHA256, Sources: c.Sources, Warnings: r.Warnings}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fail(err)
	}
	r.Stage = "发布完成清单"
	emit(r)
	manifestConfirmed := false
	if err = d.putNew(ctx, manifestRemote, bytesBody(data)); err != nil {
		// PUT 的响应丢失时，完成清单可能已经写入，保留归档供人工核对。
		published = true
		if !isUncertain(err) {
			return fail(err)
		}
		original := err
		r.Stage = "等待确认完成清单"
		emit(r)
		if err = d.confirmManifest(ctx, manifestRemote, m); err != nil {
			r.Stage = "完成清单发布结果未确认"
			return fail(fmt.Errorf("%v；核对未完成：%w", original, err))
		}
		manifestConfirmed = true
	}
	published = true
	if !manifestConfirmed {
		if err = d.matchManifest(ctx, manifestRemote, m); err != nil {
			return fail(err)
		}
	}
	r.RemotePath = d.target(archiveRemote).String()
	r.ManifestPath = d.target(manifestRemote).String()
	r.Stage = "正在清理过期备份"
	emit(r)
	// 有变化警告的归档仍可下载，但不能用来淘汰上一次完整成功的备份。
	if len(r.Warnings) == 0 {
		r.Warnings = append(r.Warnings, d.cleanup(ctx, c, time.Now().UTC())...)
	}
	r.Status = "success"
	r.Stage = "备份完成"
	if len(r.Warnings) > 0 {
		r.Status = "warning"
		r.Stage = "备份完成，有警告"
	}
	return r
}
