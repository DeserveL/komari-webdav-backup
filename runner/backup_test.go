package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockDAV struct {
	fileDeleteCode        map[string]int
	mu                    sync.Mutex
	files                 map[string][]byte
	dirs                  map[string]bool
	deleted               []string
	retries               int
	failPUT               bool
	wrongSize             bool
	directoryDeleteCode   int
	injectOnArchiveDelete bool
}

func fixtureDAV(t *testing.T) (*mockDAV, *httptest.Server) {
	t.Helper()
	d := &mockDAV{files: map[string][]byte{}, dirs: map[string]bool{"/": true}}
	server := httptest.NewServer(d)
	t.Cleanup(server.Close)
	return d, server
}
func (d *mockDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if u, p, ok := r.BasicAuth(); !ok || u != "user" || p != "secret" {
		w.WriteHeader(401)
		return
	}
	p := path.Clean(r.URL.Path)
	switch r.Method {
	case "MKCOL":
		if d.dirs[p] {
			w.WriteHeader(405)
		} else if !d.dirs[path.Dir(p)] {
			w.WriteHeader(409)
		} else {
			d.dirs[p] = true
			w.WriteHeader(201)
		}
	case "PUT":
		if r.Header.Get("If-None-Match") == "*" {
			if _, ok := d.files[p]; ok {
				w.WriteHeader(412)
				return
			}
		}
		if d.failPUT && strings.HasSuffix(p, ".tar.gz") {
			w.WriteHeader(403)
			return
		}
		if strings.HasSuffix(p, ".tar.gz") && d.retries > 0 {
			d.retries--
			w.WriteHeader(503)
			return
		}
		if !d.dirs[path.Dir(p)] {
			w.WriteHeader(409)
			return
		}
		data, _ := io.ReadAll(r.Body)
		d.files[p] = data
		w.WriteHeader(201)
	case "HEAD":
		data, ok := d.files[p]
		if !ok {
			w.WriteHeader(404)
			return
		}
		n := len(data)
		if d.wrongSize && strings.HasSuffix(p, ".tar.gz") {
			n++
		}
		w.Header().Set("Content-Length", fmt.Sprint(n))
		w.WriteHeader(200)
	case "GET":
		data, ok := d.files[p]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write(data)
	case "DELETE":
		if code := d.fileDeleteCode[p]; code != 0 {
			w.WriteHeader(code)
			return
		}
		if d.dirs[p] {
			if d.directoryDeleteCode != 0 {
				w.WriteHeader(d.directoryDeleteCode)
				return
			}
			for file := range d.files {
				if strings.HasPrefix(file, p+"/") {
					delete(d.files, file)
				}
			}
			for dir := range d.dirs {
				if dir == p || strings.HasPrefix(dir, p+"/") {
					delete(d.dirs, dir)
				}
			}
			d.deleted = append(d.deleted, p)
			w.WriteHeader(204)
			return
		}
		delete(d.files, p)
		if d.injectOnArchiveDelete && strings.HasSuffix(p, ".tar.gz") {
			d.files[path.Join(path.Dir(p), "new-user-file.txt")] = []byte("concurrent content")
		}
		d.deleted = append(d.deleted, p)
		w.WriteHeader(204)
	case "PROPFIND":
		if !d.dirs[p] {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(207)
		_, _ = io.WriteString(w, `<D:multistatus xmlns:D="DAV:">`)
		response := func(file string, dir bool) {
			u := url.URL{Path: file}
			if dir {
				u.Path += "/"
			}
			href := u.String()
			var b strings.Builder
			_ = xml.EscapeText(&b, []byte(href))
			_, _ = fmt.Fprintf(w, `<D:response><D:href>%s</D:href><D:propstat><D:prop><D:resourcetype>`, b.String())
			if dir {
				_, _ = io.WriteString(w, `<D:collection/>`)
			}
			_, _ = io.WriteString(w, `</D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
		}
		response(p, true)
		for file := range d.dirs {
			if file != "/" && path.Dir(file) == p {
				response(file, true)
			}
		}
		for file := range d.files {
			if path.Dir(file) == p {
				response(file, false)
			}
		}
		_, _ = io.WriteString(w, `</D:multistatus>`)
	default:
		w.WriteHeader(405)
	}
}
func fixtureConfig(t *testing.T, endpoint string) (Config, string, string) {
	t.Helper()
	source := t.TempDir()
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "配置 文件.txt"), []byte("可恢复的文件内容\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "debug.log"), []byte("exclude me"), 0600); err != nil {
		t.Fatal(err)
	}
	c := Config{Protocol: 2, Mode: "backup", RunID: testID("run-new"), JobID: "job-a", NodeID: "node-a", Sources: []string{source}, Excludes: []string{"**/*.log"}, RemoteDir: "备份/东京_配置_a81c2f90", FilePrefix: testPrefix(testID("run-new")), RetentionDays: 7, WebDAVURL: endpoint, Username: "user", Password: "secret", TimeoutMinutes: 5}
	return c, source, state
}
func testID(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])[:32]
}
func testPrefix(id string) string { return "20261028_020000+0800_" + shortRunID(id) }
func remoteArchive(c Config) string {
	return "/" + path.Join(c.RemoteDir, archiveNameFor(c.FilePrefix))
}
func remoteManifest(c Config) string { return "/" + path.Join(c.RemoteDir, manifestName(c.FilePrefix)) }
func addManifest(t *testing.T, d *mockDAV, c Config, id string, age time.Duration, valid bool) string {
	t.Helper()
	id = testID(id)
	prefix := testPrefix(id)
	base := "/" + path.Join(c.RemoteDir, prefix)
	data := []byte("old archive")
	hash := sha256.Sum256(data)
	m := Manifest{Owner: owner, Version: 2, RunID: id, NodeID: c.NodeID, JobID: c.JobID, CompletedAt: time.Now().UTC().Add(-age), Archive: archiveNameFor(prefix), Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}
	if !valid {
		m.NodeID = "another-node"
	}
	d.files[base+".tar.gz"] = data
	d.files[base+".manifest.json"], _ = json.Marshal(m)
	return base
}
func TestBackupRoundTripAndRetentionIsolation(t *testing.T) {
	d, s := fixtureDAV(t)
	c, source, state := fixtureConfig(t, s.URL)
	dav, _ := newDAV(c)
	if err := dav.mkdir(context.Background(), c.RemoteDir); err != nil {
		t.Fatal(err)
	}
	old := addManifest(t, d, c, "run-old", 10*24*time.Hour, true)
	cleanOld := addManifest(t, d, c, "run-clean-old", 12*24*time.Hour, true)
	foreign := addManifest(t, d, c, "run-foreign", time.Hour, true)
	recent := addManifest(t, d, c, "run-recent", time.Hour, true)
	d.files["/"+path.Join(c.RemoteDir, "unrelated.tar.gz")] = []byte("leave alone")
	d.files["/"+path.Join(c.RemoteDir, "notes.txt")] = []byte("not owned")
	r := execute(context.Background(), c, state, func(Report) {})
	if r.Status != "success" {
		t.Fatalf("%+v", r)
	}
	if _, ok := d.files[cleanOld+".tar.gz"]; ok {
		t.Fatal("expired owned backup was not removed")
	}
	for dir := range d.dirs {
		if strings.HasPrefix(dir, "/"+c.RemoteDir+"/") {
			t.Fatal("unexpected per-run directory")
		}
	}
	if _, ok := d.files[old+".tar.gz"]; ok {
		t.Fatal("expired archive retained")
	}
	for _, file := range []string{"/" + path.Join(c.RemoteDir, "notes.txt"), foreign + ".tar.gz", recent + ".tar.gz", "/" + path.Join(c.RemoteDir, "unrelated.tar.gz")} {
		if _, ok := d.files[file]; !ok {
			t.Fatalf("unrelated or recent file removed: %s", file)
		}
	}
	download, err := dav.request(context.Background(), "GET", strings.TrimPrefix(remoteArchive(c), "/"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := io.ReadAll(download.Body)
	closeResponse(download)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(archive)
	if hex.EncodeToString(hash[:]) != r.SHA256 {
		t.Fatal("digest mismatch")
	}
	f := filepath.Join(t.TempDir(), "download.tar.gz")
	_ = os.WriteFile(f, archive, 0600)
	in, _ := os.Open(f)
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(h.Name, "debug.log") {
			t.Fatal("excluded file included")
		}
		if h.Name == archiveName(filepath.Join(source, "配置 文件.txt")) {
			data, _ := io.ReadAll(tr)
			if string(data) != "可恢复的文件内容\n" {
				t.Fatal("restored contents differ")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("source missing from archive")
	}
	entries, _ := os.ReadDir(filepath.Join(state, "work"))
	if len(entries) != 0 {
		t.Fatal("temporary archive not cleaned")
	}
}
func TestMissingSourceFailsWithoutCompletedManifest(t *testing.T) {
	d, s := fixtureDAV(t)
	c, _, state := fixtureConfig(t, s.URL)
	c.Sources = []string{filepath.Join(t.TempDir(), "missing")}
	r := execute(context.Background(), c, state, func(Report) {})
	if r.Status != "failed" {
		t.Fatalf("%+v", r)
	}
	for name := range d.files {
		if strings.HasSuffix(name, ".manifest.json") {
			t.Fatal("manifest written after failure")
		}
	}
}
func TestUploadFailureAndWrongSizeNeverCleanOldBackups(t *testing.T) {
	for _, kind := range []string{"upload", "size"} {
		t.Run(kind, func(t *testing.T) {
			d, s := fixtureDAV(t)
			c, _, state := fixtureConfig(t, s.URL)
			dav, _ := newDAV(c)
			_ = dav.mkdir(context.Background(), c.RemoteDir)
			old := addManifest(t, d, c, "run-old", 20*24*time.Hour, true)
			d.failPUT = kind == "upload"
			d.wrongSize = kind == "size"
			r := execute(context.Background(), c, state, func(Report) {})
			if r.Status != "failed" {
				t.Fatalf("%+v", r)
			}
			if _, ok := d.files[old+".tar.gz"]; !ok {
				t.Fatal("old backup removed on failure")
			}
			if _, ok := d.files[remoteManifest(c)]; ok {
				t.Fatal("failure published as complete")
			}
		})
	}
}
func TestTestModeAndRetentionZero(t *testing.T) {
	d, s := fixtureDAV(t)
	c, _, state := fixtureConfig(t, s.URL)
	c.Mode = "test"
	r := execute(context.Background(), c, state, func(Report) {})
	if r.Status != "success" || len(d.files) != 0 {
		t.Fatalf("probe left files: %+v", r)
	}
	c.Mode = "backup"
	c.RunID = testID("another-run")
	c.FilePrefix = testPrefix(c.RunID)
	c.RetentionDays = 0
	old := addManifest(t, d, c, "run-old", 365*24*time.Hour, true)
	r = execute(context.Background(), c, state, func(Report) {})
	if r.Status != "success" {
		t.Fatalf("%+v", r)
	}
	if _, ok := d.files[old+".tar.gz"]; !ok {
		t.Fatal("retention=0 deleted backup")
	}
}
func TestAuthenticationAndCancelledContext(t *testing.T) {
	_, s := fixtureDAV(t)
	c, _, state := fixtureConfig(t, s.URL)
	c.Password = "wrong"
	r := execute(context.Background(), c, state, func(Report) {})
	if r.Status != "failed" || strings.Contains(r.Error, "wrong") {
		t.Fatalf("%+v", r)
	}
	c.Password = "secret"
	c.RunID = testID("cancelled")
	c.FilePrefix = testPrefix(c.RunID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = execute(ctx, c, state, func(Report) {})
	if r.Status != "failed" {
		t.Fatalf("%+v", r)
	}
}
func TestExcludesAndDuplicateSources(t *testing.T) {
	_, s := fixtureDAV(t)
	c, source, state := fixtureConfig(t, s.URL)
	c.Sources = []string{source, source, filepath.Join(source, "配置 文件.txt")}
	_, _, _, err := createArchive(context.Background(), c, filepath.Join(state, "test.tar.gz"), state)
	if err != nil {
		t.Fatal(err)
	}
	c.Excludes = []string{"**"}
	_, _, _, err = createArchive(context.Background(), c, filepath.Join(state, "empty.tar.gz"), state)
	if err == nil {
		t.Fatal("all-excluded backup succeeded")
	}
}
func TestConfigRejectsUnsafeURLsAndPaths(t *testing.T) {
	_, s := fixtureDAV(t)
	c, _, _ := fixtureConfig(t, s.URL)
	if c.Validate(false) == nil {
		t.Fatal("HTTP accepted")
	}
	if c.Validate(true) != nil {
		t.Fatal("loopback test mode rejected")
	}
	for _, dir := range []string{"../x", "/x", "a/../b", "a\\b"} {
		copy := c
		copy.RemoteDir = dir
		if copy.Validate(true) == nil {
			t.Fatalf("unsafe directory accepted: %s", dir)
		}
	}
}
func TestSymlinkPreservedWithoutFollowing(t *testing.T) {
	_, s := fixtureDAV(t)
	c, source, state := fixtureConfig(t, s.URL)
	link := filepath.Join(source, "outside-link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skip("symlink creation unavailable")
	}
	file := filepath.Join(state, "a.tar.gz")
	_, _, _, err := createArchive(context.Background(), c, file, state)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(file)
	defer f.Close()
	gz, _ := gzip.NewReader(f)
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if h.Name == archiveName(link) {
			if h.Typeflag != tar.TypeSymlink {
				t.Fatal("symlink followed")
			}
			return
		}
	}
	t.Fatal("symlink missing")
}
func TestDAVRejectsRedirect(t *testing.T) {
	hits := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 307) }))
	defer redirect.Close()
	d, _ := newDAV(Config{WebDAVURL: redirect.URL, Username: "user", Password: "secret"})
	if d.put(context.Background(), "file", bytesBody([]byte("data"))) == nil || hits != 0 {
		t.Fatal("redirect followed or accepted")
	}
}

func TestMainCommandClaimsReportsAndReusesResult(t *testing.T) {
	d, davServer := fixtureDAV(t)
	c, _, state := fixtureConfig(t, davServer.URL)
	claims, reports := 0, 0
	var panel *httptest.Server
	panel = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/claim":
			if r.Header.Get("Authorization") != "Bearer single-use" || claims > 0 {
				w.WriteHeader(403)
				return
			}
			claims++
			c.ReportURL = panel.URL + "/report"
			c.ReportToken = "report-only"
			_ = json.NewEncoder(w).Encode(c)
		case "/report":
			if r.Header.Get("Authorization") != "Bearer report-only" {
				w.WriteHeader(403)
				return
			}
			var report Report
			_ = json.NewDecoder(r.Body).Decode(&report)
			if report.Status != "success" {
				t.Errorf("unexpected report: %+v", report)
			}
			reports++
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer panel.Close()
	args := []string{"run", "--run-id", c.RunID, "--state-dir", state, "--claim-url", panel.URL + "/claim", "--grant", "single-use", "--allow-loopback-http"}
	if result := mainCommand(args); result != 0 {
		t.Fatalf("runner exit %d", result)
	}
	if claims != 1 || reports != 1 {
		t.Fatalf("claims=%d reports=%d", claims, reports)
	}
	if result := mainCommand(args); result != 0 || claims != 1 {
		t.Fatal("same run executed twice")
	}
	if result := mainCommand([]string{"status", "--run-id", c.RunID, "--state-dir", state}); result != 0 {
		t.Fatal("status lookup failed")
	}
	data, _ := os.ReadFile(filepath.Join(state, "results", c.RunID+".json"))
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "report-only") {
		t.Fatal("credentials persisted in local result")
	}
	if _, ok := d.files[remoteManifest(c)]; !ok {
		t.Fatal("completed manifest missing")
	}
}

func TestSingleFileBackup(t *testing.T) {
	d, s := fixtureDAV(t)
	c, source, state := fixtureConfig(t, s.URL)
	c.Sources = []string{filepath.Join(source, "配置 文件.txt")}
	r := execute(context.Background(), c, state, func(Report) {})
	if r.Status != "success" {
		t.Fatalf("%+v", r)
	}
	if len(d.files[remoteArchive(c)]) == 0 {
		t.Fatal("single file archive missing")
	}
}

func TestRetentionKeepsNewestSuccessfulBackupEvenWhenExpired(t *testing.T) {
	d, s := fixtureDAV(t)
	c, _, _ := fixtureConfig(t, s.URL)
	dav, _ := newDAV(c)
	if err := dav.mkdir(context.Background(), c.RemoteDir); err != nil {
		t.Fatal(err)
	}
	newer := addManifest(t, d, c, "newer", 20*24*time.Hour, true)
	older := addManifest(t, d, c, "older", 30*24*time.Hour, true)
	if warnings := dav.cleanup(context.Background(), c, time.Now()); len(warnings) > 0 {
		t.Fatal(warnings)
	}
	if _, ok := d.files[newer+".tar.gz"]; !ok {
		t.Fatal("newest success deleted")
	}
	if _, ok := d.files[older+".tar.gz"]; ok {
		t.Fatal("expired older backup retained")
	}
}

func TestDAVListingOnlyReturnsDirectValidChildren(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(207)
		_, _ = io.WriteString(w, `<x:multistatus xmlns:x="DAV:">
<x:response><x:href>/dav/job/run-valid/</x:href><x:propstat><x:prop><x:resourcetype><x:collection/></x:resourcetype></x:prop><x:status>HTTP/1.1 200 OK</x:status></x:propstat></x:response>
<x:response><x:href>relative-run/</x:href><x:propstat><x:prop><x:resourcetype><x:collection/></x:resourcetype></x:prop><x:status>HTTP/1.1 200 OK</x:status></x:propstat></x:response>
<x:response><x:href>https://foreign.invalid/dav/job/foreign/</x:href><x:propstat><x:prop/><x:status>HTTP/1.1 200 OK</x:status></x:propstat></x:response>
<x:response><x:href>/dav/job/%2e%2e/outside/</x:href><x:propstat><x:prop/><x:status>HTTP/1.1 200 OK</x:status></x:propstat></x:response>
<x:response><x:href>/dav/job/nested/child/</x:href><x:propstat><x:prop/><x:status>HTTP/1.1 200 OK</x:status></x:propstat></x:response>
<x:response><x:href>/dav/job/not-found/</x:href><x:propstat><x:prop/><x:status>HTTP/1.1 404 Not Found</x:status></x:propstat></x:response>
</x:multistatus>`)
	}))
	defer server.Close()
	dav, _ := newDAV(Config{WebDAVURL: server.URL + "/dav"})
	entries, err := dav.list(context.Background(), "job")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].name != "run-valid" || entries[1].name != "relative-run" {
		t.Fatalf("unexpected listing: %+v", entries)
	}
}

func TestRunLockPreventsDuplicateClaim(t *testing.T) {
	file := filepath.Join(t.TempDir(), "run.lock")
	unlock, err := acquireLock(file)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if release, err := acquireLock(file); err == nil {
		release()
		t.Fatal("duplicate lock acquired")
	} else if !errors.Is(err, errAlreadyRunning) {
		t.Fatal(err)
	}
}
