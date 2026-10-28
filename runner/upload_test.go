package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// 锁独立于请求上下文：模拟客户端断开以后，网盘上传仍继续运行。
type uploadFixture struct {
	base          *mockDAV
	mu            sync.Mutex
	puts          map[string]int
	gets, heads   int
	locked        bool
	mode          string
	headsOverride []string
}

func uploadServer(t *testing.T, mode string) (*uploadFixture, *DAV, Config, string) {
	t.Helper()
	base, _ := fixtureDAV(t)
	f := &uploadFixture{base: base, puts: map[string]int{}, mode: mode}
	s := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(s.Close)
	c, _, state := fixtureConfig(t, s.URL)
	d, err := newDAV(c)
	if err != nil {
		t.Fatal(err)
	}
	d.policy = requestPolicy{retry: []time.Duration{time.Millisecond, time.Millisecond}, jitter: func() time.Duration { return 0 }, interval: 3 * time.Millisecond, confirmation: 200 * time.Millisecond}
	t.Cleanup(func() { d.client.CloseIdleConnections(); d.uploadClient.CloseIdleConnections() })
	return f, d, c, state
}

func (f *uploadFixture) serve(w http.ResponseWriter, r *http.Request) {
	archive := strings.HasSuffix(r.URL.Path, ".tar.gz")
	manifest := strings.HasSuffix(r.URL.Path, ".manifest.json")
	if r.Method == "GET" && archive {
		f.mu.Lock()
		f.gets++
		f.mu.Unlock()
		w.WriteHeader(500)
		return
	}
	if r.Method == "PUT" && (archive || manifest) {
		f.mu.Lock()
		f.puts[r.URL.Path]++
		wasLocked := f.locked
		f.mu.Unlock()
		if archive && wasLocked {
			w.WriteHeader(423)
			return
		}
		if archive && (f.mode == "slow" || f.mode == "detached") {
			f.mu.Lock()
			f.locked = true
			f.mu.Unlock()
			body, _ := io.ReadAll(r.Body)
			delay := 70 * time.Millisecond
			if f.mode == "slow" {
				delay = 61 * time.Second
			}
			time.Sleep(delay)
			f.base.mu.Lock()
			f.base.files[r.URL.Path] = body
			f.base.mu.Unlock()
			f.mu.Lock()
			f.locked = false
			f.mu.Unlock()
			w.WriteHeader(201)
			return
		}
		if archive && (f.mode == "missing" || f.mode == "locked" || f.mode == "forbidden" || f.mode == "too-large" || f.mode == "conflict") {
			code := 502
			switch f.mode {
			case "locked":
				code = 423
			case "forbidden":
				code = 403
			case "too-large":
				code = 413
			case "conflict":
				code = 412
			}
			w.WriteHeader(code)
			return
		}
		if archive && (f.mode == "lost" || f.mode == "524" || f.mode == "same-size-corrupt" || f.mode == "sequence" || f.mode == "missing-size" || f.mode == "check-forbidden") {
			data, _ := io.ReadAll(r.Body)
			if f.mode == "same-size-corrupt" {
				data = bytes.Repeat([]byte("x"), len(data))
			}
			f.base.mu.Lock()
			f.base.files[r.URL.Path] = data
			f.base.mu.Unlock()
			w.Header().Set("CF-Ray", "example-ray")
			code := 502
			if f.mode == "524" {
				code = 524
			}
			w.WriteHeader(code)
			return
		}
		if manifest && strings.HasPrefix(f.mode, "manifest-") {
			data, _ := io.ReadAll(r.Body)
			if f.mode == "manifest-conflict" {
				var m Manifest
				_ = json.Unmarshal(data, &m)
				m.Sources = []string{"/different"}
				data, _ = json.Marshal(m)
			}
			if f.mode != "manifest-missing" {
				f.base.mu.Lock()
				f.base.files[r.URL.Path] = data
				f.base.mu.Unlock()
			}
			w.WriteHeader(524)
			return
		}
	}
	if archive && r.Method == "HEAD" {
		f.mu.Lock()
		f.heads++
		locked := f.locked
		if len(f.headsOverride) > 0 {
			value := f.headsOverride[0]
			f.headsOverride = f.headsOverride[1:]
			f.mu.Unlock()
			if value == "423" {
				w.WriteHeader(423)
				return
			}
			if value == "wrong" {
				w.Header().Set("Content-Length", "1")
				w.WriteHeader(200)
				return
			}
		} else {
			f.mu.Unlock()
		}
		if locked {
			w.WriteHeader(423)
			return
		}
		if f.mode == "missing-size" {
			w.WriteHeader(200)
			return
		}
		if f.mode == "check-forbidden" {
			w.WriteHeader(403)
			return
		}
	}
	f.base.ServeHTTP(w, r)
}

func TestArchiveResponseLostUsesSizeOnlyAndNeverDeletes(t *testing.T) {
	for _, mode := range []string{"lost", "524", "same-size-corrupt", "sequence"} {
		t.Run(mode, func(t *testing.T) {
			f, d, c, state := uploadServer(t, mode)
			_ = d.mkdir(context.Background(), c.RemoteDir)
			old := addManifest(t, f.base, c, "old", historyAge, true)
			if mode == "sequence" {
				f.headsOverride = []string{"ok", "wrong", "ok", "423", "ok", "ok"}
			}
			r := executeWithDAV(context.Background(), c, state, func(Report) {}, d)
			if r.Status != "warning" || !strings.Contains(strings.Join(r.Warnings, " "), sizeOnlyWarning) {
				t.Fatalf("%+v", r)
			}
			if len(f.base.deleted) != 1 || !strings.Contains(f.base.deleted[0], ".probe-") {
				t.Fatalf("unexpected delete: %v", f.base.deleted)
			}
			if len(f.base.files[old+".tar.gz"]) == 0 {
				t.Fatal("old backup removed")
			}
			var m Manifest
			_ = json.Unmarshal(f.base.files[remoteManifest(c)], &m)
			if len(m.Warnings) == 0 || m.Warnings[0] != sizeOnlyWarning {
				t.Fatal("size-only caveat missing from manifest")
			}
			if f.gets != 0 || f.puts[remoteArchive(c)] != 1 {
				t.Fatal("archive GET or repeated PUT")
			}
			if mode == "sequence" && f.heads < 6 {
				t.Fatal("nonconsecutive sizes accepted")
			}
			if !strings.Contains(strings.Join(r.RequestDiagnostics, " "), "CF-Ray=example-ray") {
				t.Fatal("proxy diagnostic missing")
			}
		})
	}
}

func TestUncertainAndExplicitFailuresDoNotDeleteOrPublish(t *testing.T) {
	for _, mode := range []string{"missing", "locked", "missing-size", "check-forbidden", "forbidden", "too-large", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			f, d, c, state := uploadServer(t, mode)
			r := executeWithDAV(context.Background(), c, state, func(Report) {}, d)
			if r.Status != "failed" {
				t.Fatalf("%+v", r)
			}
			if f.puts[remoteArchive(c)] != 1 || f.puts[remoteManifest(c)] != 0 || f.gets != 0 {
				t.Fatal("unsafe upload retry or publication")
			}
			for _, p := range f.base.deleted {
				if strings.HasSuffix(p, ".tar.gz") || strings.HasSuffix(p, ".manifest.json") {
					t.Fatal("uncertain upload deleted")
				}
			}
			if strings.HasPrefix(mode, "missing") || mode == "locked" || mode == "check-forbidden" {
				if r.Stage != "上传结果未确认" {
					t.Fatal(r.Stage)
				}
			}
		})
	}
}

func TestManifestResponseLostMatchesAllFields(t *testing.T) {
	for _, mode := range []string{"manifest-lost", "manifest-missing", "manifest-conflict"} {
		t.Run(mode, func(t *testing.T) {
			f, d, c, state := uploadServer(t, mode)
			r := executeWithDAV(context.Background(), c, state, func(Report) {}, d)
			want := "failed"
			if mode == "manifest-lost" {
				want = "success"
			}
			if r.Status != want {
				t.Fatalf("%+v", r)
			}
			if f.puts[remoteArchive(c)] != 1 || f.puts[remoteManifest(c)] != 1 || f.gets != 0 {
				t.Fatal("write replayed")
			}
			if len(f.base.files[remoteArchive(c)]) == 0 {
				t.Fatal("archive removed after ambiguous manifest")
			}
		})
	}
}

func TestDetachedUploadKeepsLockUntilBackendCompletes(t *testing.T) {
	f, d, c, state := uploadServer(t, "detached")
	d.uploadClient.Transport.(*http.Transport).ResponseHeaderTimeout = 15 * time.Millisecond
	r := executeWithDAV(context.Background(), c, state, func(Report) {}, d)
	if r.Status != "warning" || f.puts[remoteArchive(c)] != 1 || f.heads < 3 {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(strings.Join(r.RequestDiagnostics, " "), "423") {
		t.Fatal("backend lock not observed")
	}
}

func TestUploadWaitsBeyondOldMinuteLimit(t *testing.T) {
	t.Parallel()
	f, d, c, state := uploadServer(t, "slow")
	started := time.Now()
	r := executeWithDAV(context.Background(), c, state, func(Report) {}, d)
	if r.Status != "success" || time.Since(started) < time.Minute || f.puts[remoteArchive(c)] != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestRequestRetryAfterAndCancellation(t *testing.T) {
	for _, value := range []string{"2", time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)} {
		if delay, ok := retryAfter(value, time.Now()); !ok || delay <= 0 {
			t.Fatal(value)
		}
	}
	for _, value := range []string{"-1", "garbage", "99999999999999999999999"} {
		if _, ok := retryAfter(value, time.Now()); ok {
			t.Fatal(value)
		}
	}
	f, d, c, _ := uploadServer(t, "missing")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.confirmArchive(ctx, path.Join(c.RemoteDir, "archive.tar.gz"), 1); err == nil {
		t.Fatal("cancel ignored")
	}
	if f.heads != 0 {
		t.Fatal("request after cancellation")
	}
	var count int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Header().Set("Retry-After", "0")
		if count < 3 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer s.Close()
	u, _ := newDAV(Config{WebDAVURL: s.URL, TimeoutMinutes: 5})
	u.policy = d.policy
	res, err := u.request(context.Background(), "HEAD", "file", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	closeResponse(res)
	if count != 3 {
		t.Fatal(count)
	}
	count = 0
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(503)
	}))
	defer blocked.Close()
	u, _ = newDAV(Config{WebDAVURL: blocked.URL, TimeoutMinutes: 5})
	u.policy = d.policy
	budget, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if _, err = u.request(budget, "HEAD", "file", nil, nil); err == nil || count != 1 {
		t.Fatal("Retry-After ignored or exceeded task budget", count, err)
	}
}

func TestUploadRetriesOnlyBeforeConnection(t *testing.T) {
	_, d, _, _ := uploadServer(t, "lost")
	var requests int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(201)
	}))
	defer s.Close()
	u, _ := newDAV(Config{WebDAVURL: s.URL, TimeoutMinutes: 5})
	u.policy = d.policy
	var connections int
	u.uploadClient.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		connections++
		if connections < 3 {
			return nil, fmt.Errorf("temporary dial failure")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	if err := u.putNew(context.Background(), "archive.tar.gz", bytesBody([]byte("payload"))); err != nil {
		t.Fatal(err)
	}
	if connections != 3 || requests != 1 {
		t.Fatal("unsafe connection retry", connections, requests)
	}
}

type delayedBody struct {
	io.Reader
	delay time.Duration
}

func (b *delayedBody) Read(p []byte) (int, error) {
	if b.delay > 0 {
		time.Sleep(b.delay)
		b.delay = 0
	}
	return b.Reader.Read(p)
}

func TestResponseWaitDoesNotLimitSendingBody(t *testing.T) {
	_, d, _, _ := uploadServer(t, "lost")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); w.WriteHeader(201) }))
	defer s.Close()
	u, _ := newDAV(Config{WebDAVURL: s.URL, TimeoutMinutes: 5})
	u.policy = d.policy
	u.uploadClient.Transport.(*http.Transport).ResponseHeaderTimeout = 10 * time.Millisecond
	start := time.Now()
	err := u.putNew(context.Background(), "archive.tar.gz", func() (io.ReadCloser, int64, error) {
		return io.NopCloser(&delayedBody{Reader: strings.NewReader("payload"), delay: 35 * time.Millisecond}), 7, nil
	})
	if err != nil || time.Since(start) < 35*time.Millisecond {
		t.Fatal("response timeout counted body transmission", err)
	}
}

func TestDiagnosticsAreBoundedAndRedacted(t *testing.T) {
	_, d, _, _ := uploadServer(t, "lost")
	for i := 0; i < 40; i++ {
		d.record(fmt.Sprintf("%d %s https://user:pass@example.com/file?token=private Bearer private secret", i, strings.Repeat("中文", 100)))
	}
	r := boundReport(Report{Error: strings.Repeat("错误", 1000), Warnings: []string{strings.Repeat("警告", 1000)}, RequestDiagnostics: d.diagnostics})
	text := strings.Join(r.RequestDiagnostics, "")
	if len(r.RequestDiagnostics) > 16 || len(text) > 4096 || !utf8.ValidString(text) || strings.Contains(text, "private") || strings.Contains(text, "user:pass") || strings.Contains(text, "secret") {
		t.Fatal("unsafe diagnostic", text)
	}
	if len(text)+len(r.Error)+len(strings.Join(r.Warnings, "")) > diagnosticLimit {
		t.Fatal("combined budget exceeded")
	}
}
