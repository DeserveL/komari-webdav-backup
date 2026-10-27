package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"time"
)

func TestDirectoryDeleteFailureRetainsManifestAndRetries(t *testing.T) {
	for _, code := range []int{403, 202, 207} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			d, s := fixtureDAV(t)
			c, _, _ := fixtureConfig(t, s.URL)
			dav, _ := newDAV(c)
			_ = dav.mkdir(context.Background(), c.RemoteDir)
			old := addManifest(t, d, c, "old", historyAge, true)
			latest := addManifest(t, d, c, "latest", time.Hour, true)
			d.directoryDeleteCode = code
			if warnings := dav.cleanup(context.Background(), c, time.Now()); len(warnings) == 0 {
				t.Fatal("directory deletion failure not reported")
			}
			if !d.dirs[old] || len(d.files[path.Join(old, "manifest.json")]) == 0 {
				t.Fatal("retry identity lost")
			}
			if len(d.files[path.Join(old, "backup.tar.gz")]) != 0 {
				t.Fatal("expired archive not deleted")
			}
			d.directoryDeleteCode = 0
			if warnings := dav.cleanup(context.Background(), c, time.Now()); len(warnings) > 0 {
				t.Fatal(warnings)
			}
			if d.dirs[old] || len(d.files[path.Join(old, "manifest.json")]) != 0 {
				t.Fatal("expired directory not fully removed on retry")
			}
			if !d.dirs[latest] || !d.dirs["/"+c.RemoteDir] {
				t.Fatal("protected directory deleted")
			}
		})
	}
}

func TestRetentionRechecksContentsBeforeCollectionDelete(t *testing.T) {
	d, s := fixtureDAV(t)
	c, _, _ := fixtureConfig(t, s.URL)
	dav, _ := newDAV(c)
	_ = dav.mkdir(context.Background(), c.RemoteDir)
	old := addManifest(t, d, c, "old", historyAge, true)
	addManifest(t, d, c, "latest", time.Hour, true)
	d.injectOnArchiveDelete = true
	if warnings := dav.cleanup(context.Background(), c, time.Now()); len(warnings) == 0 {
		t.Fatal("new unknown file not reported")
	}
	if !d.dirs[old] || len(d.files[path.Join(old, "new-user-file.txt")]) == 0 || len(d.files[path.Join(old, "manifest.json")]) == 0 {
		t.Fatal("unknown file or retry manifest deleted")
	}
}

func TestStrictDirectoryListingRejectsUnexplainedContent(t *testing.T) {
	self := `<d:response><d:href>/job/old/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`
	manifest := `<d:response><d:href>/job/old/manifest.json</d:href><d:propstat><d:prop><d:resourcetype/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`
	entry := func(href, status, kind string) string {
		return `<d:response><d:href>` + href + `</d:href><d:propstat><d:prop><d:resourcetype>` + kind + `</d:resourcetype></d:prop><d:status>HTTP/1.1 ` + status + `</d:status></d:propstat></d:response>`
	}
	for name, extra := range map[string]string{
		"unknown":           entry("/job/old/notes.txt", "200 OK", ""),
		"subdirectory":      entry("/job/old/backup.tar.gz/", "200 OK", "<d:collection/>"),
		"foreign":           entry("https://foreign.invalid/job/old/file", "200 OK", ""),
		"scheme-relative":   entry("//foreign.invalid/job/old/file", "200 OK", ""),
		"traversal":         entry("/job/old/%2e%2e/outside", "200 OK", ""),
		"nested":            entry("/job/old/nested/file", "200 OK", ""),
		"failed-property":   entry("/job/old/file", "403 Forbidden", ""),
		"duplicate":         manifest,
		"no-properties":     `<d:response><d:href>/job/old/unknown</d:href></d:response>`,
		"unknown-namespace": `<x:response xmlns:x="unknown"><x:href>/job/old/file</x:href></x:response>`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(207)
				_, _ = io.WriteString(w, `<d:multistatus xmlns:d="DAV:">`+self+manifest+extra+`</d:multistatus>`)
			}))
			defer server.Close()
			dav, _ := newDAV(Config{WebDAVURL: server.URL})
			if err := dav.checkRunContents(context.Background(), "job/old", true); err == nil {
				t.Fatal("unsafe listing accepted")
			}
		})
	}
	for _, body := range []string{`<broken`, `<multistatus/>`, `<d:multistatus xmlns:d="DAV:">` + manifest + `</d:multistatus>`, `<d:multistatus xmlns:d="DAV:">` + self + `</d:multistatus>`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(207); _, _ = io.WriteString(w, body) }))
		dav, _ := newDAV(Config{WebDAVURL: server.URL})
		if err := dav.checkRunContents(context.Background(), "job/old", true); err == nil {
			t.Fatal("incomplete listing accepted")
		}
		server.Close()
	}
}

func TestRetentionZeroAndUnownedDirectoriesUntouched(t *testing.T) {
	d, s := fixtureDAV(t)
	c, _, _ := fixtureConfig(t, s.URL)
	dav, _ := newDAV(c)
	_ = dav.mkdir(context.Background(), c.RemoteDir)
	old := addManifest(t, d, c, "old", historyAge, true)
	foreign := addManifest(t, d, c, "foreign", historyAge, false)
	addManifest(t, d, c, "latest", time.Hour, true)
	empty := "/" + path.Join(c.RemoteDir, "legacy-empty")
	d.dirs[empty] = true
	c.RetentionDays = 0
	if warnings := dav.cleanup(context.Background(), c, time.Now()); len(warnings) > 0 {
		t.Fatal(warnings)
	}
	if len(d.deleted) > 0 {
		t.Fatal("zero retention deleted remote data")
	}
	c.RetentionDays = 7
	if warnings := dav.cleanup(context.Background(), c, time.Now()); len(warnings) > 0 {
		t.Fatal(warnings)
	}
	if d.dirs[old] || !d.dirs[foreign] || !d.dirs[empty] {
		t.Fatal("directory ownership isolation failed")
	}
	for _, deleted := range d.deleted {
		if !strings.HasPrefix(deleted, old) {
			t.Fatalf("unowned deletion: %s", deleted)
		}
	}
}
