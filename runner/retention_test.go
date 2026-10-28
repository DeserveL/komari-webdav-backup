package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"time"
)

func TestFlatRetentionRetryAndUnknownFiles(t *testing.T) {
	d, s := fixtureDAV(t)
	c, _, _ := fixtureConfig(t, s.URL)
	dav, _ := newDAV(c)
	_ = dav.mkdir(context.Background(), c.RemoteDir)
	old := addManifest(t, d, c, "old", historyAge, true)
	latest := addManifest(t, d, c, "latest", time.Hour, true)
	unknown := "/" + path.Join(c.RemoteDir, "notes.txt")
	d.files[unknown] = []byte("keep")
	d.fileDeleteCode = map[string]int{old + ".manifest.json": 403}
	if warnings := dav.cleanup(context.Background(), c, time.Now()); len(warnings) == 0 {
		t.Fatal("deletion failure hidden")
	}
	if len(d.files[old+".manifest.json"]) == 0 || len(d.files[old+".tar.gz"]) != 0 {
		t.Fatal("retry manifest lost")
	}
	d.fileDeleteCode = nil
	if warnings := dav.cleanup(context.Background(), c, time.Now()); len(warnings) > 0 {
		t.Fatal(warnings)
	}
	if len(d.files[old+".manifest.json"]) != 0 || len(d.files[latest+".tar.gz"]) == 0 || len(d.files[unknown]) == 0 {
		t.Fatal("pair cleanup isolation failed")
	}
	for _, name := range d.deleted {
		if !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".manifest.json") {
			t.Fatal("collection or unknown file deleted")
		}
	}
}
func TestFlatRetentionRejectsUnownedAndDangerousManifests(t *testing.T) {
	d, s := fixtureDAV(t)
	c, _, _ := fixtureConfig(t, s.URL)
	dav, _ := newDAV(c)
	_ = dav.mkdir(context.Background(), c.RemoteDir)
	foreign := addManifest(t, d, c, "foreign", historyAge, false)
	bad := addManifest(t, d, c, "bad", historyAge, true)
	addManifest(t, d, c, "latest", time.Hour, true)
	var m Manifest
	_ = json.Unmarshal(d.files[bad+".manifest.json"], &m)
	m.Archive = "../outside.tar.gz"
	d.files[bad+".manifest.json"], _ = json.Marshal(m)
	if warnings := dav.cleanup(context.Background(), c, time.Now()); len(warnings) > 0 {
		t.Fatal(warnings)
	}
	if len(d.deleted) != 0 || len(d.files[foreign+".tar.gz"]) == 0 {
		t.Fatal("unsafe manifest deleted")
	}
	c.RetentionDays = 0
	if warnings := dav.cleanup(context.Background(), c, time.Now()); len(warnings) > 0 {
		t.Fatal(warnings)
	}
}
func TestTaskDirectoryOwnershipAndFileCollision(t *testing.T) {
	d, s := fixtureDAV(t)
	c, _, _ := fixtureConfig(t, s.URL)
	dav, _ := newDAV(c)
	_ = dav.mkdir(context.Background(), c.RemoteDir)
	prefix, err := dav.choosePrefix(context.Background(), c)
	if err != nil || prefix != c.FilePrefix {
		t.Fatal(prefix, err)
	}
	d.files["/"+path.Join(c.RemoteDir, "unknown.txt")] = []byte("unknown")
	if _, err = dav.choosePrefix(context.Background(), c); err == nil {
		t.Fatal("unowned directory accepted")
	}
	addManifest(t, d, c, "owned", time.Hour, true)
	d.files[remoteArchive(c)] = []byte("existing")
	prefix, err = dav.choosePrefix(context.Background(), c)
	if err != nil || !strings.HasSuffix(prefix, c.RunID) {
		t.Fatal("full run ID fallback failed", prefix, err)
	}
	d.files["/"+path.Join(c.RemoteDir, archiveNameFor(prefix))] = []byte("also existing")
	if _, err = dav.choosePrefix(context.Background(), c); err == nil {
		t.Fatal("both occupied names accepted")
	}
	addManifest(t, d, c, "foreign", time.Hour, false)
	if _, err = dav.choosePrefix(context.Background(), c); err == nil {
		t.Fatal("foreign task directory accepted")
	}
}
func TestConditionalUploadNeverOverwrites(t *testing.T) {
	d, s := fixtureDAV(t)
	c, _, _ := fixtureConfig(t, s.URL)
	dav, _ := newDAV(c)
	_ = dav.mkdir(context.Background(), c.RemoteDir)
	name := path.Join(c.RemoteDir, "existing.tar.gz")
	d.files["/"+name] = []byte("original")
	if err := dav.putNew(context.Background(), name, bytesBody([]byte("replacement"))); err == nil {
		t.Fatal("overwrite accepted")
	}
	if string(d.files["/"+name]) != "original" {
		t.Fatal("existing archive replaced")
	}
}
func TestStrictListingRejectsMalformedOrOutsideEntries(t *testing.T) {
	self := `<d:response><d:href>/job/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`
	for _, extra := range []string{`<d:response><d:href>https://foreign.invalid/file</d:href></d:response>`, `<d:response><d:href>/job/%2e%2e/outside</d:href></d:response>`, `<x:response xmlns:x="unknown"/>`, self} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(207)
			_, _ = io.WriteString(w, `<d:multistatus xmlns:d="DAV:">`+self+extra+`</d:multistatus>`)
		}))
		dav, _ := newDAV(Config{WebDAVURL: server.URL})
		if _, err := dav.listStrict(context.Background(), "job"); err == nil {
			t.Fatal("unsafe listing accepted")
		}
		server.Close()
	}
}
func TestUnconfirmedFileDeletionIsWarning(t *testing.T) {
	for _, code := range []int{202, 207, 403} {
		d, s := fixtureDAV(t)
		c, _, _ := fixtureConfig(t, s.URL)
		dav, _ := newDAV(c)
		_ = dav.mkdir(context.Background(), c.RemoteDir)
		old := addManifest(t, d, c, "old", historyAge, true)
		addManifest(t, d, c, "new", time.Hour, true)
		d.fileDeleteCode = map[string]int{old + ".tar.gz": code}
		if warnings := dav.cleanup(context.Background(), c, time.Now()); len(warnings) == 0 {
			t.Fatal("unconfirmed deletion reported success")
		}
		if len(d.files[old+".manifest.json"]) == 0 {
			t.Fatal("manifest removed before confirmed archive deletion")
		}
	}
}
