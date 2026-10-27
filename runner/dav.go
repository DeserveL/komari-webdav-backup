package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

type DAV struct {
	base           *url.URL
	user, password string
	client         *http.Client
}
type davEntry struct {
	name       string
	collection bool
}

func newDAV(c Config) (*DAV, error) {
	u, err := url.Parse(strings.TrimRight(c.WebDAVURL, "/") + "/")
	if err != nil {
		return nil, err
	}
	return &DAV{u, c.Username, c.Password, &http.Client{Timeout: time.Duration(c.TimeoutMinutes) * time.Minute,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 60 * time.Second},
		// 跨站或同站重定向都显式拒绝，避免凭据外泄或 PUT 被转换为 GET。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (d *DAV) target(relative string) *url.URL {
	u := *d.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(relative, "/")
	u.RawPath = ""
	return &u
}

type bodyFactory func() (io.ReadCloser, int64, error)

func bytesBody(b []byte) bodyFactory {
	return func() (io.ReadCloser, int64, error) { return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil }
}

func (d *DAV) request(ctx context.Context, method, relative string, body bodyFactory, headers map[string]string) (*http.Response, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		var reader io.ReadCloser
		var length int64
		var err error
		if body != nil {
			reader, length, err = body()
			if err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, d.target(relative).String(), reader)
		if err != nil {
			if reader != nil {
				_ = reader.Close()
			}
			return nil, err
		}
		req.ContentLength = length
		req.SetBasicAuth(d.user, d.password)
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		res, err := d.client.Do(req)
		if err != nil {
			last = errors.New("WebDAV network request failed: " + redact(err.Error(), d.password))
			continue
		}
		if res.StatusCode != 408 && res.StatusCode != 429 && res.StatusCode < 500 {
			return res, nil
		}
		last = fmt.Errorf("WebDAV %s HTTP %d", method, res.StatusCode)
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
	}
	return nil, last
}

func closeResponse(res *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	_ = res.Body.Close()
}
func httpOK(res *http.Response, codes ...int) error {
	defer closeResponse(res)
	for _, code := range codes {
		if res.StatusCode == code {
			return nil
		}
	}
	return fmt.Errorf("WebDAV HTTP %d", res.StatusCode)
}

func (d *DAV) mkdir(ctx context.Context, dir string) error {
	current := ""
	for _, part := range strings.Split(dir, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("invalid WebDAV path")
		}
		current = path.Join(current, part)
		res, err := d.request(ctx, "MKCOL", current, nil, nil)
		if err != nil {
			return err
		}
		if res.StatusCode == http.StatusMethodNotAllowed {
			closeResponse(res)
			if _, err = d.list(ctx, current); err != nil {
				return err
			}
			continue
		}
		if err = httpOK(res, 200, 201, 204); err != nil {
			return err
		}
	}
	return nil
}

func (d *DAV) put(ctx context.Context, name string, body bodyFactory) error {
	res, err := d.request(ctx, "PUT", name, body, map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	return httpOK(res, 200, 201, 204)
}
func (d *DAV) putFile(ctx context.Context, name, file string) error {
	return d.put(ctx, name, func() (io.ReadCloser, int64, error) {
		f, err := os.Open(file)
		if err != nil {
			return nil, 0, err
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, 0, err
		}
		return f, info.Size(), nil
	})
}
func (d *DAV) verifySize(ctx context.Context, name string, expected int64) error {
	res, err := d.request(ctx, "HEAD", name, nil, nil)
	if err != nil {
		return err
	}
	defer closeResponse(res)
	if res.StatusCode != 200 {
		return fmt.Errorf("WebDAV size check HTTP %d", res.StatusCode)
	}
	actual, err := strconv.ParseInt(res.Header.Get("Content-Length"), 10, 64)
	if err != nil || actual != expected {
		return fmt.Errorf("remote archive size mismatch: expected %d", expected)
	}
	return nil
}
func (d *DAV) getJSON(ctx context.Context, name string, out any) error {
	res, err := d.request(ctx, "GET", name, nil, nil)
	if err != nil {
		return err
	}
	defer closeResponse(res)
	if res.StatusCode != 200 {
		return fmt.Errorf("WebDAV GET HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return errors.New("invalid or oversized manifest")
	}
	return json.Unmarshal(b, out)
}
func (d *DAV) deleteFile(ctx context.Context, name string) error {
	res, err := d.request(ctx, "DELETE", name, nil, nil)
	if err != nil {
		return err
	}
	return httpOK(res, 200, 202, 204, 404)
}

func (d *DAV) list(ctx context.Context, dir string) ([]davEntry, error) {
	query := []byte(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>`)
	res, err := d.request(ctx, "PROPFIND", dir+"/", bytesBody(query), map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	if err != nil {
		return nil, err
	}
	defer closeResponse(res)
	if res.StatusCode != 207 {
		return nil, fmt.Errorf("WebDAV PROPFIND HTTP %d", res.StatusCode)
	}
	var result struct {
		Responses []struct {
			Href     string `xml:"DAV: href"`
			Propstat []struct {
				Status string `xml:"DAV: status"`
				Prop   struct {
					Type struct {
						Collection *struct{} `xml:"DAV: collection"`
					} `xml:"DAV: resourcetype"`
				} `xml:"DAV: prop"`
			} `xml:"DAV: propstat"`
		} `xml:"DAV: response"`
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return nil, errors.New("WebDAV listing exceeds limit")
	}
	if err = xml.Unmarshal(data, &result); err != nil {
		return nil, errors.New("invalid WebDAV XML response")
	}
	prefix := strings.TrimRight(d.target(dir).Path, "/") + "/"
	entries := []davEntry{}
	seen := map[string]bool{}
	for _, item := range result.Responses {
		href, err := url.Parse(item.Href)
		if err != nil || (href.IsAbs() && (href.Scheme != d.base.Scheme || href.Host != d.base.Host)) || href.RawQuery != "" || href.Fragment != "" {
			continue
		}
		if !strings.HasPrefix(href.Path, "/") {
			href = d.target(dir + "/").ResolveReference(href)
		}
		p := strings.TrimSuffix(href.Path, "/")
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		name := strings.TrimPrefix(p, prefix)
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || seen[name] {
			continue
		}
		for _, prop := range item.Propstat {
			parts := strings.Fields(prop.Status)
			if len(parts) >= 2 && parts[1] == "200" {
				entries = append(entries, davEntry{name, prop.Prop.Type.Collection != nil})
				seen[name] = true
				break
			}
		}
	}
	return entries, nil
}

// 只删除清单验证通过的两个已知文件，不递归 DELETE 整个远端目录。
func (d *DAV) cleanup(ctx context.Context, c Config, now time.Time) []string {
	warnings := []string{}
	if c.RetentionDays == 0 {
		return warnings
	}
	entries, err := d.list(ctx, c.RemoteDir)
	if err != nil {
		return []string{"retention listing failed: " + err.Error()}
	}
	type candidate struct {
		dir      string
		manifest Manifest
	}
	valid := []candidate{}
	for _, item := range entries {
		if !item.collection || !validID.MatchString(item.name) {
			continue
		}
		var m Manifest
		if d.getJSON(ctx, path.Join(c.RemoteDir, item.name, "manifest.json"), &m) != nil {
			continue
		}
		if m.Owner != owner || m.Version != 1 || m.NodeID != c.NodeID || m.JobID != c.JobID || m.RunID != item.name || m.Archive != "backup.tar.gz" || m.Size <= 0 || !validDigest(m.SHA256) || m.CompletedAt.IsZero() || m.CompletedAt.After(now.Add(5*time.Minute)) {
			continue
		}
		valid = append(valid, candidate{item.name, m})
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].manifest.CompletedAt.After(valid[j].manifest.CompletedAt) })
	keep := ""
	for _, item := range valid {
		if len(item.manifest.Warnings) == 0 {
			keep = item.dir
			break
		}
	}
	if keep == "" {
		return []string{"no complete warning-free backup found; retention cleanup skipped"}
	}
	cutoff := now.Add(-time.Duration(c.RetentionDays) * 24 * time.Hour)
	for _, old := range valid {
		if old.dir == keep || !old.manifest.CompletedAt.Before(cutoff) {
			continue
		}
		base := path.Join(c.RemoteDir, old.dir)
		if err = d.deleteFile(ctx, path.Join(base, "backup.tar.gz")); err != nil {
			warnings = append(warnings, "retention archive delete failed: "+old.dir)
			continue
		}
		if err = d.deleteFile(ctx, path.Join(base, "manifest.json")); err != nil {
			warnings = append(warnings, "retention manifest delete failed: "+old.dir)
		}
	}
	return warnings
}

func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
