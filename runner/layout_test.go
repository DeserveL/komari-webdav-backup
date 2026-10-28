package main

import (
	"context"
	"strings"
	"testing"
)

func TestMultipleBackupsStayFlatAndPaired(t *testing.T) {
	d, s := fixtureDAV(t)
	c, _, state := fixtureConfig(t, s.URL)
	first := execute(context.Background(), c, state, func(Report) {})
	if first.Status != "success" || !strings.HasSuffix(first.ManifestPath, ".manifest.json") {
		t.Fatalf("%+v", first)
	}
	directories := len(d.dirs)
	c.RunID = testID("second-backup")
	c.FilePrefix = testPrefix(c.RunID)
	second := execute(context.Background(), c, state, func(Report) {})
	if second.Status != "success" || second.RemotePath == first.RemotePath {
		t.Fatalf("%+v", second)
	}
	if len(d.dirs) != directories {
		t.Fatal("a per-run directory was created")
	}
	archives, manifests := 0, 0
	for name := range d.files {
		if strings.HasSuffix(name, ".tar.gz") {
			archives++
		}
		if strings.HasSuffix(name, ".manifest.json") {
			manifests++
		}
	}
	if archives != 2 || manifests != 2 {
		t.Fatalf("archives=%d manifests=%d", archives, manifests)
	}
}
func TestFullPrefixCollisionStopsAndInvalidPrefixesRejected(t *testing.T) {
	d, s := fixtureDAV(t)
	c, _, _ := fixtureConfig(t, s.URL)
	dav, _ := newDAV(c)
	_ = dav.mkdir(context.Background(), c.RemoteDir)
	addManifest(t, d, c, "owned", 0, true)
	c.FilePrefix = "20261028_020000+0800_" + c.RunID
	d.files[remoteArchive(c)] = []byte("keep")
	if _, err := dav.choosePrefix(context.Background(), c); err == nil {
		t.Fatal("occupied full prefix accepted")
	}
	for _, prefix := range []string{"../escape", "20269928_020000+0800_" + shortRunID(c.RunID), "20261028_020000+9999_" + shortRunID(c.RunID), "20261028_020000+0800_deadbeef"} {
		if validPrefix(prefix, c.RunID) {
			t.Fatal("unsafe prefix accepted:", prefix)
		}
	}
}
