package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ** 跨目录，* 和 ? 只匹配单层；归档路径以去掉开头 / 的原始路径为准。
func compileGlob(pattern string) (*regexp.Regexp, error) {
	if pattern == "" || strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "\\") || strings.ContainsRune(pattern, 0) {
		return nil, errors.New("invalid exclude pattern")
	}
	for _, part := range strings.Split(pattern, "/") {
		if part == "." || part == ".." {
			return nil, errors.New("invalid exclude pattern")
		}
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					b.WriteString("(?:.*/)?")
					i++
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func archiveName(source string) string {
	name := filepath.ToSlash(filepath.Clean(source))
	name = strings.TrimPrefix(name, filepath.VolumeName(source))
	return strings.TrimLeft(name, "/")
}

func normalizedSources(sources []string) []string {
	all := append([]string(nil), sources...)
	for i := range all {
		all[i] = filepath.Clean(all[i])
	}
	sort.Strings(all)
	out := []string{}
	for _, source := range all {
		covered := false
		for _, parent := range out {
			info, err := os.Lstat(parent)
			if source == parent || (err == nil && info.IsDir() && strings.HasPrefix(source, parent+string(filepath.Separator))) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, source)
		}
	}
	return out
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func createArchive(ctx context.Context, config Config, target, workRoot string) (size int64, digest string, warnings []string, err error) {
	warnings = []string{}
	patterns := []*regexp.Regexp{}
	for _, p := range config.Excludes {
		re, e := compileGlob(p)
		if e != nil {
			return 0, "", warnings, e
		}
		patterns = append(patterns, re)
	}
	for _, source := range config.Sources {
		if _, e := os.Lstat(source); e != nil {
			return 0, "", warnings, fmt.Errorf("source unavailable %s: %w", source, e)
		}
		// 不跟随来源路径的父级软链接，避免归档时逃出用户选择的目录。
		parent, e := filepath.EvalSymlinks(filepath.Dir(source))
		if e != nil || filepath.Clean(parent) != filepath.Clean(filepath.Dir(source)) {
			return 0, "", warnings, fmt.Errorf("source parent contains a symlink: %s", source)
		}
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return 0, "", warnings, err
	}
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(target)
		}
	}()
	hash := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(f, hash))
	tw := tar.NewWriter(gz)
	count := 0
	addWarning := func(s string) {
		if len(warnings) < 100 {
			warnings = append(warnings, s)
		}
	}
	for _, source := range normalizedSources(config.Sources) {
		err = filepath.WalkDir(source, func(file string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if e := ctx.Err(); e != nil {
				return e
			}
			name := archiveName(file)
			if file == workRoot || strings.HasPrefix(file, workRoot+string(filepath.Separator)) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			for _, p := range patterns {
				if p.MatchString(name) || (entry.IsDir() && p.MatchString(name+"/")) {
					if entry.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			info, e := os.Lstat(file)
			if e != nil {
				return e
			}
			link := ""
			if info.Mode()&os.ModeSymlink != 0 {
				link, e = os.Readlink(file)
				if e != nil {
					return e
				}
			}
			if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				addWarning("skipped special file: " + name)
				return nil
			}
			header, e := tar.FileInfoHeader(info, link)
			if e != nil {
				return e
			}
			header.Name = name
			if info.IsDir() {
				header.Name += "/"
			}
			if !info.Mode().IsRegular() {
				if e = tw.WriteHeader(header); e == nil {
					count++
				}
				return e
			}
			input, e := os.Open(file)
			if e != nil {
				return e
			}
			defer input.Close()
			opened, e := input.Stat()
			if e != nil {
				return e
			}
			if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
				return fmt.Errorf("file replaced while archiving: %s", name)
			}
			header.Size = opened.Size()
			if e = tw.WriteHeader(header); e != nil {
				return e
			}
			_, e = io.CopyN(tw, ctxReader{ctx, input}, opened.Size())
			if e != nil {
				return fmt.Errorf("file changed or unreadable: %s: %w", name, e)
			}
			after, e := input.Stat()
			if e != nil {
				return e
			}
			current, e := os.Lstat(file)
			if e != nil || !os.SameFile(opened, current) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
				addWarning("file changed while reading: " + name)
			}
			count++
			return nil
		})
		if err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return 0, "", warnings, err
		}
	}
	if count == 0 {
		_ = tw.Close()
		_ = gz.Close()
		return 0, "", warnings, errors.New("no files or directories remained after exclusions")
	}
	if err = tw.Close(); err != nil {
		return 0, "", warnings, err
	}
	if err = gz.Close(); err != nil {
		return 0, "", warnings, err
	}
	if err = f.Sync(); err != nil {
		return 0, "", warnings, err
	}
	info, err := f.Stat()
	if err != nil {
		return 0, "", warnings, err
	}
	return info.Size(), hex.EncodeToString(hash.Sum(nil)), warnings, nil
}
