package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestLocalHistoryLimitsAndCache(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"results", "bin"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	write := func(id, status string, age time.Duration) {
		r := Report{RunID: id, Status: status, UpdatedAt: now.Add(-age).Format(time.RFC3339), Warnings: []string{}}
		data, _ := json.Marshal(r)
		if err := os.WriteFile(filepath.Join(dir, "results", id+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "results", id+".lock"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 505; i++ {
		write(fmt.Sprintf("run-%03d", i), "success", time.Duration(i)*time.Second)
	}
	write("expired", "failed", historyAge+time.Second)
	write("pending", "pending", historyAge*2)
	write("running", "running", historyAge*2)
	write("current", "success", 0)
	if err := os.WriteFile(filepath.Join(dir, "results", "unknown.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	current := "runner-amd64-" + strings.Repeat("a", 64)
	for i, hash := range []string{"a", "b", "c", "d"} {
		file := filepath.Join(dir, "bin", "runner-amd64-"+strings.Repeat(hash, 64))
		if err := os.WriteFile(file, []byte(hash), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(file, now.Add(-time.Duration(i)*time.Hour), now.Add(-time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if warnings := maintainLocal(dir, "current", filepath.Join(dir, "bin", current), now); len(warnings) > 0 {
		t.Fatal(warnings)
	}
	for _, id := range []string{"current", "running", "pending", "unknown", "run-000", "run-498"} {
		if _, err := os.Stat(filepath.Join(dir, "results", id+".json")); err != nil {
			t.Fatalf("protected record %s: %v", id, err)
		}
	}
	for _, id := range []string{"run-499", "run-504", "expired"} {
		for _, ext := range []string{".json", ".lock"} {
			if _, err := os.Stat(filepath.Join(dir, "results", id+ext)); !os.IsNotExist(err) {
				t.Fatalf("old record retained: %s%s", id, ext)
			}
		}
	}
	files, _ := os.ReadDir(filepath.Join(dir, "bin"))
	if len(files) != 2 {
		t.Fatalf("cache files=%d", len(files))
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "runner-amd64-"+strings.Repeat("b", 64))); err != nil {
		t.Fatal("most recent previous runner removed")
	}
	before, _ := os.Stat(filepath.Join(dir, "results", "run-000.json"))
	if warnings := maintainLocal(dir, "current", filepath.Join(dir, "bin", current), now); len(warnings) > 0 {
		t.Fatal(warnings)
	}
	after, _ := os.Stat(filepath.Join(dir, "results", "run-000.json"))
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged history rewritten")
	}
}

func TestLocalHistoryAgeBoundaryAndOrphanLocks(t *testing.T) {
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, "results"), 0700)
	now := time.Now().UTC().Truncate(time.Second)
	for _, id := range []string{"boundary", "expired"} {
		age := historyAge
		if id == "expired" {
			age += time.Second
		}
		r := Report{RunID: id, Status: "failed", UpdatedAt: now.Add(-age).Format(time.RFC3339)}
		data, _ := json.Marshal(r)
		_ = os.WriteFile(filepath.Join(dir, "results", id+".json"), data, 0600)
	}
	orphan := filepath.Join(dir, "results", "orphan.lock")
	_ = os.WriteFile(orphan, nil, 0600)
	_ = os.Chtimes(orphan, now.Add(-historyAge-time.Hour), now.Add(-historyAge-time.Hour))
	if warnings := maintainLocal(dir, "current", "", now); len(warnings) > 0 {
		t.Fatal(warnings)
	}
	if _, err := os.Stat(filepath.Join(dir, "results", "boundary.json")); err != nil {
		t.Fatal("30-day boundary removed")
	}
	for _, file := range []string{orphan, filepath.Join(dir, "results", "expired.json")} {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("expired item retained: %s", file)
		}
	}
}

func TestDiagnosticBudgetPreservesUTF8AndReason(t *testing.T) {
	r := Report{Error: strings.Repeat("原因", 2000), Warnings: []string{strings.Repeat("中文警告", 10000), "tail"}}
	r = boundReport(r)
	text := r.Error + strings.Join(r.Warnings, "")
	if len(text) > diagnosticLimit || !utf8.ValidString(text) || !strings.Contains(text, "已截断") || !strings.HasPrefix(text, "原因") {
		t.Fatal("invalid bounded diagnostics")
	}
	again := boundReport(r)
	if again.Error != r.Error || strings.Join(again.Warnings, "") != strings.Join(r.Warnings, "") {
		t.Fatal("diagnostic limit not idempotent")
	}
}
