//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMaintenancePreservesHeldRunLock(t *testing.T) {
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, "results"), 0700)
	file := filepath.Join(dir, "results", "held.lock")
	unlock, err := acquireLock(file)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	r := Report{RunID: "held", Status: "success", UpdatedAt: time.Now().Add(-historyAge - time.Hour).UTC().Format(time.RFC3339)}
	data, _ := json.Marshal(r)
	_ = os.WriteFile(filepath.Join(dir, "results", "held.json"), data, 0600)
	if warnings := maintainLocal(dir, "other", "", time.Now()); len(warnings) > 0 {
		t.Fatal(warnings)
	}
	if _, err := os.Stat(filepath.Join(dir, "results", "held.json")); err != nil {
		t.Fatal("held result deleted")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("held lock deleted")
	}
}

func TestDuplicateRunDuringClaimDoesNotReportFailure(t *testing.T) {
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, "results"), 0700)
	unlockNode, err := acquireLock(filepath.Join(dir, "node.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlockNode()
	unlockRun, err := acquireLock(filepath.Join(dir, "results", "same.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlockRun()
	if code := mainCommand([]string{"run", "--run-id", "same", "--state-dir", dir}); code != 0 {
		t.Fatal("duplicate run incorrectly failed")
	}
	if code := mainCommand([]string{"run", "--run-id", "other", "--state-dir", dir}); code != 1 {
		t.Fatal("another run entered occupied node")
	}
}
