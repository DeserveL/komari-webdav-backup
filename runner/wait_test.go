package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestArchiveWaitUsesOriginalTaskBudget(t *testing.T) {
	f, d, c, state := uploadServer(t, "cancel")
	if d.archiveClient.Transport.(*http.Transport).ResponseHeaderTimeout != 0 || d.uploadClient.Transport.(*http.Transport).ResponseHeaderTimeout != 5*time.Minute || d.client.Transport.(*http.Transport).ResponseHeaderTimeout != time.Minute {
		t.Fatal("archive and metadata wait policies are not separated")
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	// 模拟任务已经开始计时，上传不能重新获得完整时限。
	time.Sleep(40 * time.Millisecond)
	r := executeWithDAV(ctx, c, state, func(Report) {}, d)
	if r.Status != "failed" || !strings.Contains(r.Error, "任务总时间已用尽") || r.Stage != "上传结果未确认" {
		t.Fatalf("%+v", r)
	}
	if f.puts[remoteArchive(c)] != 1 || f.puts[remoteManifest(c)] != 0 || f.heads != 0 || f.gets != 0 {
		t.Fatal("continued after task deadline", f.puts, f.heads)
	}
	for _, name := range f.base.deleted {
		if !strings.Contains(name, ".probe-") {
			t.Fatal("deleted uncertain archive", name)
		}
	}
}

func TestCriticalUploadDiagnosisSurvivesRepeatedChecks(t *testing.T) {
	_, d, _, _ := uploadServer(t, "lost")
	d.recordRequest("PUT", "archive", "PUT HTTP 502 original-upload-error")
	for i := 0; i < 50; i++ {
		d.recordCheck("WebDAV check HTTP 404")
	}
	d.recordKey("archive-final", "远端结果未确认：核对时间已用尽", true)
	text := strings.Join(d.diagnostics, "\n")
	if !strings.Contains(text, "original-upload-error") || !strings.Contains(text, "累计 50 次") || !strings.Contains(text, "首次") || !strings.Contains(text, "末次") || len(d.diagnostics) != 3 {
		t.Fatal(text)
	}
	for i := 0; i < 60; i++ {
		d.record(time.Unix(int64(i), 0).String())
	}
	text = strings.Join(d.diagnostics, "\n")
	if !strings.Contains(text, "original-upload-error") || !strings.Contains(text, "核对时间已用尽") || len(text) > 4096 || len(d.diagnosticKeys) > 16 || len(d.criticalKeys) > 16 || len(d.checks) > 16 {
		t.Fatal("critical diagnostics lost or unbounded", text)
	}
}

func TestCanceledArchiveDoesNotContinueChecking(t *testing.T) {
	f, d, c, state := uploadServer(t, "cancel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(500*time.Millisecond, cancel)
	defer timer.Stop()
	r := executeWithDAV(ctx, c, state, func(Report) {}, d)
	if r.Status != "failed" || !strings.Contains(r.Error, "任务已取消") || f.puts[remoteArchive(c)] != 1 || f.heads != 0 || f.puts[remoteManifest(c)] != 0 {
		t.Fatalf("cancellation did not stop upload: %+v", r)
	}
}

func TestArchiveWaitBeyondFiveMinutes(t *testing.T) {
	if os.Getenv("KWB_LONG_UPLOAD_TEST") != "1" {
		t.Skip("set KWB_LONG_UPLOAD_TEST=1 to run 310-second integration test")
	}
	f, d, c, state := uploadServer(t, "long")
	ctx, stop := context.WithTimeout(context.Background(), 350*time.Second)
	defer stop()
	started := time.Now()
	r := executeWithDAV(ctx, c, state, func(Report) {}, d)
	if r.Status != "success" || time.Since(started) < 310*time.Second || f.puts[remoteArchive(c)] != 1 || f.puts[remoteManifest(c)] != 1 || f.gets != 0 {
		t.Fatalf("%+v; puts=%v", r, f.puts)
	}
	t.Logf("archive accepted after %s with one PUT", time.Since(started).Round(time.Millisecond))
}
