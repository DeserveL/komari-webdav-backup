package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const historyLimit = 500
const historyAge = 30 * 24 * time.Hour
const diagnosticLimit = 16 * 1024

func boundReport(r Report) Report {
	const marker = "…[已截断]"
	values := append([]string{r.Error}, r.Warnings[:min(100, len(r.Warnings))]...)
	total := 0
	for _, value := range values {
		total += len(value)
	}
	truncated := total > diagnosticLimit
	remaining := diagnosticLimit
	if truncated {
		remaining -= len(marker)
	}
	take := func(s string) string {
		if len(s) <= remaining {
			remaining -= len(s)
			return s
		}
		end := remaining
		for end > 0 && !utf8.RuneStart(s[end]) {
			end--
		}
		remaining = 0
		return s[:end]
	}
	last := 0
	for i, value := range values {
		values[i] = take(value)
		if values[i] != "" {
			last = i
		}
	}
	if truncated {
		values[last] += marker
	}
	r.Error = values[0]
	r.Warnings = []string{}
	for _, value := range values[1:] {
		if value != "" {
			r.Warnings = append(r.Warnings, value)
		}
	}
	return r
}

var cachedRunner = regexp.MustCompile(`^runner-(amd64|arm64)-[a-f0-9]{64}$`)

// 调用者持有 node.lock；只维护可识别的终态记录和执行器，不跟随软链接。
func maintainLocal(dir, currentRun, executable string, now time.Time) []string {
	warnings := []string{}
	remove := func(file string) {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			warnings = append(warnings, "local maintenance could not remove "+filepath.Base(file))
		}
	}
	withResultLock := func(id string, operation func(string)) {
		lock := filepath.Join(dir, "results", id+".lock")
		if info, e := os.Lstat(lock); e == nil && !info.Mode().IsRegular() {
			return
		}
		unlock, e := maintenanceLock(lock)
		if e != nil {
			if e != errAlreadyRunning {
				warnings = append(warnings, "local history lock unavailable: "+id)
			}
			return
		}
		defer unlock()
		operation(lock)
	}
	removeResult := func(id string) {
		withResultLock(id, func(lock string) {
			remove(filepath.Join(dir, "results", id+".json"))
			if _, e := os.Lstat(filepath.Join(dir, "results", id+".json")); os.IsNotExist(e) {
				remove(lock)
			}
		})
	}
	type completed struct {
		id       string
		finished time.Time
	}
	done := []completed{}
	limit := historyLimit
	if data, e := os.ReadFile(filepath.Join(dir, "results", currentRun+".json")); e == nil {
		var current Report
		if json.Unmarshal(data, &current) == nil && (current.Status == "success" || current.Status == "warning" || current.Status == "failed") {
			limit--
		}
	}
	results := filepath.Join(dir, "results")
	entries, err := os.ReadDir(results)
	if err != nil {
		return []string{"local history maintenance unavailable"}
	}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") || !validID.MatchString(id) || id == currentRun {
			continue
		}
		info, e := entry.Info()
		if e != nil || info.Size() > 1<<20 {
			continue
		}
		data, e := os.ReadFile(filepath.Join(results, entry.Name()))
		var r Report
		if e != nil || json.Unmarshal(data, &r) != nil || r.RunID != id || (r.Status != "success" && r.Status != "warning" && r.Status != "failed") {
			continue
		}
		finished, e := time.Parse(time.RFC3339, r.UpdatedAt)
		if e != nil {
			continue
		}
		bounded := boundReport(r)
		original, _ := json.Marshal(r)
		limited, _ := json.Marshal(bounded)
		if string(original) != string(limited) {
			withResultLock(id, func(string) {
				if e := writeReport(dir, bounded); e != nil {
					warnings = append(warnings, "local diagnostics could not be bounded: "+id)
				}
			})
		}
		done = append(done, completed{id, finished})
	}
	sort.Slice(done, func(i, j int) bool {
		if done[i].finished.Equal(done[j].finished) {
			return done[i].id < done[j].id
		}
		return done[i].finished.After(done[j].finished)
	})
	for i, item := range done {
		if i >= limit || item.finished.Before(now.Add(-historyAge)) {
			removeResult(item.id)
		}
	}
	// 无结果的旧锁也有上限；仍有记录（包括未确认状态）的锁始终保留。
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".lock")
		if !strings.HasSuffix(entry.Name(), ".lock") || id == currentRun || !validID.MatchString(id) || !entry.Type().IsRegular() {
			continue
		}
		info, e := entry.Info()
		if e != nil || !info.ModTime().Before(now.Add(-historyAge)) {
			continue
		}
		if _, e = os.Lstat(filepath.Join(results, id+".json")); os.IsNotExist(e) {
			removeResult(id)
		}
	}
	bin := filepath.Join(dir, "bin")
	entries, err = os.ReadDir(bin)
	if os.IsNotExist(err) {
		return warnings
	}
	if err != nil {
		return append(warnings, "runner cache maintenance unavailable")
	}
	type binary struct {
		name     string
		modified time.Time
	}
	binaries := []binary{}
	// 只有确实从缓存启动时才裁剪缓存，防止本地测试或外部调用误认当前版本。
	if filepath.Dir(executable) != bin || !cachedRunner.MatchString(filepath.Base(executable)) {
		return warnings
	}
	for _, entry := range entries {
		if entry.Name() == filepath.Base(executable) || !entry.Type().IsRegular() || !cachedRunner.MatchString(entry.Name()) {
			continue
		}
		if info, e := entry.Info(); e == nil {
			binaries = append(binaries, binary{entry.Name(), info.ModTime()})
		}
	}
	sort.Slice(binaries, func(i, j int) bool {
		if binaries[i].modified.Equal(binaries[j].modified) {
			return binaries[i].name < binaries[j].name
		}
		return binaries[i].modified.After(binaries[j].modified)
	})
	for _, old := range binaries[min(1, len(binaries)):] {
		remove(filepath.Join(bin, old.name))
	}
	return warnings
}
