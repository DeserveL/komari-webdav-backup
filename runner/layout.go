package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

var prefixPattern = regexp.MustCompile(`^([0-9]{8}_[0-9]{6}[+-][0-9]{4})_([a-f0-9]{8,32})$`)

func validPrefix(prefix, runID string) bool {
	parts := prefixPattern.FindStringSubmatch(prefix)
	if parts == nil || (parts[2] != runID && parts[2] != shortRunID(runID)) {
		return false
	}
	_, err := time.Parse("20060102_150405-0700", parts[1])
	return err == nil
}
func shortRunID(id string) string         { return id[:min(8, len(id))] }
func manifestName(prefix string) string   { return prefix + ".manifest.json" }
func archiveNameFor(prefix string) string { return prefix + ".tar.gz" }

// 列举不完整时不能确认目录归属，也不能安全选择新的文件名。
func (d *DAV) listStrict(ctx context.Context, dir string) ([]davEntry, error) {
	result, err := d.readListing(ctx, dir)
	if err != nil {
		return nil, err
	}
	if len(result.Other) != 0 {
		return nil, errors.New("unrecognized listing content")
	}
	base := strings.TrimRight(d.target(dir).Path, "/")
	self := false
	seen := map[string]bool{}
	entries := []davEntry{}
	for _, item := range result.Responses {
		href, e := url.Parse(item.Href)
		if e != nil || item.Href == "" || href.RawQuery != "" || href.Fragment != "" || href.User != nil || (href.Host != "" && (href.Host != d.base.Host || href.Scheme != d.base.Scheme)) || len(item.Other) != 0 || item.Status != "" || len(item.Propstat) != 1 {
			return nil, errors.New("unrecognized directory entry")
		}
		if !strings.HasPrefix(href.Path, "/") {
			href = d.target(dir + "/").ResolveReference(href)
		}
		p := strings.TrimSuffix(href.Path, "/")
		if seen[p] {
			return nil, errors.New("duplicate directory entry")
		}
		seen[p] = true
		prop := item.Propstat[0]
		status := strings.Fields(prop.Status)
		if len(status) < 2 || status[1] != "200" || prop.Prop.Type == nil || len(prop.Other) != 0 || len(prop.Prop.Other) != 0 || len(prop.Prop.Type.Other) != 0 {
			return nil, errors.New("directory properties unavailable")
		}
		collection := prop.Prop.Type.Collection != nil
		if p == base && collection {
			self = true
			continue
		}
		if !strings.HasPrefix(p, base+"/") {
			return nil, errors.New("entry outside task directory")
		}
		name := strings.TrimPrefix(p, base+"/")
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
			return nil, errors.New("unsafe directory entry")
		}
		entries = append(entries, davEntry{name, collection})
	}
	if !self {
		return nil, errors.New("directory listing incomplete")
	}
	return entries, nil
}

func validManifest(m Manifest, c Config, name string, now time.Time) bool {
	prefix := strings.TrimSuffix(name, ".manifest.json")
	return name == manifestName(prefix) && m.Owner == owner && m.Version == 2 && m.NodeID == c.NodeID && m.JobID == c.JobID && validPrefix(prefix, m.RunID) && m.Archive == archiveNameFor(prefix) && m.Size > 0 && validDigest(m.SHA256) && !m.CompletedAt.IsZero() && !m.CompletedAt.After(now.Add(5*time.Minute))
}

func (d *DAV) choosePrefix(ctx context.Context, c Config) (string, error) {
	if !validPrefix(c.FilePrefix, c.RunID) {
		return "", errors.New("invalid backup file prefix")
	}
	entries, err := d.listStrict(ctx, c.RemoteDir)
	if err != nil {
		return "", err
	}
	owned := len(entries) == 0
	occupied := map[string]bool{}
	for _, entry := range entries {
		occupied[entry.name] = true
		if entry.collection || !strings.HasSuffix(entry.name, ".manifest.json") {
			continue
		}
		var m Manifest
		if d.getJSON(ctx, path.Join(c.RemoteDir, entry.name), &m) != nil {
			continue
		}
		if m.Owner == owner && m.Version == 2 && (m.NodeID != c.NodeID || m.JobID != c.JobID) {
			return "", errors.New("task directory belongs to another task")
		}
		if validManifest(m, c, entry.name, time.Now().UTC()) {
			owned = true
		}
	}
	if !owned {
		return "", errors.New("cannot confirm task directory ownership; choose a dedicated empty base directory or inspect its contents")
	}
	for _, prefix := range []string{c.FilePrefix, c.FilePrefix[:strings.LastIndex(c.FilePrefix, "_")] + "_" + c.RunID} {
		if !occupied[archiveNameFor(prefix)] && !occupied[manifestName(prefix)] {
			return prefix, nil
		}
	}
	return "", errors.New("backup file names already exist; existing files were not overwritten")
}

// 平铺布局只删除已验证的文件对，永远不 DELETE 集合或未知文件。
func (d *DAV) cleanup(ctx context.Context, c Config, now time.Time) []string {
	warnings := []string{}
	if c.RetentionDays == 0 {
		return warnings
	}
	entries, err := d.listStrict(ctx, c.RemoteDir)
	if err != nil {
		return []string{"retention listing failed: " + err.Error()}
	}
	type candidate struct {
		name string
		m    Manifest
	}
	valid := []candidate{}
	files := map[string]bool{}
	for _, entry := range entries {
		if !entry.collection {
			files[entry.name] = true
		}
	}
	for _, entry := range entries {
		if entry.collection || !strings.HasSuffix(entry.name, ".manifest.json") {
			continue
		}
		var m Manifest
		if d.getJSON(ctx, path.Join(c.RemoteDir, entry.name), &m) != nil || !validManifest(m, c, entry.name, now) {
			continue
		}

		valid = append(valid, candidate{entry.name, m})
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].m.CompletedAt.After(valid[j].m.CompletedAt) })
	keep := ""
	for _, item := range valid {
		if len(item.m.Warnings) == 0 && files[item.m.Archive] {
			keep = item.name
			break
		}
	}
	if keep == "" {
		return append(warnings, "no complete warning-free backup found; retention cleanup skipped")
	}
	for _, item := range valid {
		if item.name == keep || item.m.RunID == c.RunID || !item.m.CompletedAt.Before(now.Add(-time.Duration(c.RetentionDays)*24*time.Hour)) {
			continue
		}
		// 删除前重读清单，避免将已变更的内容当成之前校验通过的备份。
		var current Manifest
		if d.getJSON(ctx, path.Join(c.RemoteDir, item.name), &current) != nil || !validManifest(current, c, item.name, now) || current.Archive != item.m.Archive || current.SHA256 != item.m.SHA256 || !current.CompletedAt.Equal(item.m.CompletedAt) {
			warnings = append(warnings, "retention manifest changed: "+item.name)
			continue
		}
		if err = d.deleteConfirmed(ctx, path.Join(c.RemoteDir, item.m.Archive)); err != nil {
			warnings = append(warnings, "retention archive delete failed: "+item.name)
			continue
		}
		if err = d.deleteConfirmed(ctx, path.Join(c.RemoteDir, item.name)); err != nil {
			warnings = append(warnings, "retention manifest delete failed: "+item.name)
		}
	}
	return warnings
}

func (d *DAV) deleteConfirmed(ctx context.Context, name string) error {
	res, err := d.request(ctx, "DELETE", name, nil, nil)
	if err != nil {
		return err
	}
	code := res.StatusCode
	closeResponse(res)
	if code != 200 && code != 202 && code != 204 && code != 404 {
		return fmt.Errorf("WebDAV DELETE HTTP %d", code)
	}
	res, err = d.request(ctx, "HEAD", name, nil, nil)
	if err != nil {
		return err
	}
	defer closeResponse(res)
	if res.StatusCode != 404 {
		return errors.New("remote file deletion not confirmed")
	}
	return nil
}

func (d *DAV) putNew(ctx context.Context, name string, body bodyFactory) error {
	res, err := d.request(ctx, "PUT", name, body, map[string]string{"Content-Type": "application/octet-stream", "If-None-Match": "*"})
	if err != nil {
		return err
	}
	return httpOK(res, 200, 201, 204)
}
