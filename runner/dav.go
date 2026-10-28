package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type DAV struct {
	base           *url.URL
	user, password string
	client         *http.Client
	uploadClient   *http.Client
	policy         requestPolicy
	diagnostics    []string
}
type davEntry struct {
	name       string
	collection bool
}
type davListing struct {
	XMLName   xml.Name   `xml:"DAV: multistatus"`
	Other     []struct{} `xml:",any"`
	Responses []struct {
		Other    []struct{} `xml:",any"`
		Href     string     `xml:"DAV: href"`
		Status   string     `xml:"DAV: status"`
		Propstat []struct {
			Other  []struct{} `xml:",any"`
			Status string     `xml:"DAV: status"`
			Prop   struct {
				Other []struct{} `xml:",any"`
				Type  *struct {
					Collection *struct{}  `xml:"DAV: collection"`
					Other      []struct{} `xml:",any"`
				} `xml:"DAV: resourcetype"`
			} `xml:"DAV: prop"`
		} `xml:"DAV: propstat"`
	} `xml:"DAV: response"`
}

func newDAV(c Config) (*DAV, error) {
	u, err := url.Parse(strings.TrimRight(c.WebDAVURL, "/") + "/")
	if err != nil {
		return nil, err
	}
	client := func(wait time.Duration) *http.Client {
		return &http.Client{Timeout: time.Duration(c.TimeoutMinutes) * time.Minute,
			Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: wait},
			// 拒绝重定向，避免凭据外泄或 PUT 被转换为 GET。
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &DAV{base: u, user: c.Username, password: c.Password, client: client(time.Minute), uploadClient: client(5 * time.Minute),
		policy: requestPolicy{retry: []time.Duration{10 * time.Second, 30 * time.Second}, jitter: func() time.Duration { return time.Duration(rand.Int64N(int64(3*time.Second) + 1)) }, interval: 10 * time.Second, confirmation: 5 * time.Minute}}, nil
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
	var delay time.Duration
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := waitContext(ctx, delay); err != nil {
				return nil, err
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
		var connected atomic.Bool
		requestCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { connected.Store(true) }})
		req, err := http.NewRequestWithContext(requestCtx, method, d.target(relative).String(), reader)
		if err != nil {
			if reader != nil {
				_ = reader.Close()
			}
			return nil, err
		}
		req.ContentLength = length
		req.SetBasicAuth(d.user, d.password)
		req.Header.Set("Cache-Control", "no-cache, no-store")
		req.Header.Set("Pragma", "no-cache")
		req.Header.Set("Accept-Encoding", "identity")
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		client := d.client
		if method == "PUT" {
			client = d.uploadClient
		}
		started := time.Now()
		res, err := client.Do(req)
		if err != nil {
			last = errors.New("WebDAV network request failed: " + diagnosticText(err.Error(), d.password))
			d.record(fmt.Sprintf("%s %s 第 %d 次，耗时 %s：%s", started.UTC().Format(time.RFC3339), method, attempt+1, time.Since(started).Round(time.Millisecond), last))
			if method == "PUT" && connected.Load() {
				return nil, &uploadUncertainError{last}
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			delay = d.policy.retry[min(attempt, 1)] + d.policy.jitter()
			continue
		}
		if res.StatusCode >= 400 {
			ray := res.Header.Get("CF-Ray")
			if !safeRay.MatchString(ray) {
				ray = ""
			}
			d.record(fmt.Sprintf("%s %s 第 %d 次，耗时 %s：HTTP %d%s", started.UTC().Format(time.RFC3339), method, attempt+1, time.Since(started).Round(time.Millisecond), res.StatusCode, raySuffix(ray)))
		}
		if method == "PUT" && (res.StatusCode == 423 || res.StatusCode == 408 || res.StatusCode == 429 || res.StatusCode >= 500) {
			last = fmt.Errorf("WebDAV PUT HTTP %d", res.StatusCode)
			closeResponse(res)
			return nil, &uploadUncertainError{last}
		}
		if res.StatusCode != 408 && res.StatusCode != 429 && res.StatusCode < 500 {
			return res, nil
		}
		last = fmt.Errorf("WebDAV %s HTTP %d", method, res.StatusCode)
		delay = d.policy.retry[min(attempt, 1)] + d.policy.jitter()
		if value, ok := retryAfter(res.Header.Get("Retry-After"), time.Now()); ok {
			delay = value
		}
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
		return &checkStatusError{res.StatusCode}
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
		return &checkStatusError{res.StatusCode}
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

func (d *DAV) readListing(ctx context.Context, dir string) (davListing, error) {
	var result davListing
	query := []byte(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>`)
	res, err := d.request(ctx, "PROPFIND", dir+"/", bytesBody(query), map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	if err != nil {
		return result, err
	}
	defer closeResponse(res)
	if res.StatusCode != 207 {
		return result, fmt.Errorf("WebDAV PROPFIND HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return result, errors.New("WebDAV listing exceeds limit")
	}
	if err = xml.Unmarshal(data, &result); err != nil {
		return result, errors.New("invalid WebDAV XML response")
	}
	return result, nil
}

func (d *DAV) list(ctx context.Context, dir string) ([]davEntry, error) {
	result, err := d.readListing(ctx, dir)
	if err != nil {
		return nil, err
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
			if len(parts) >= 2 && parts[1] == "200" && prop.Prop.Type != nil {
				entries = append(entries, davEntry{name, prop.Prop.Type.Collection != nil})
				seen[name] = true
				break
			}
		}
	}
	return entries, nil
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
