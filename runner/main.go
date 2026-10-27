package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("state directory must be a real directory")
	}
	return os.Chmod(dir, 0700)
}

func saveReport(dir string, r Report) error {
	r.PID = os.Getpid()
	r.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	file := filepath.Join(dir, "results", r.RunID+".json")
	tmp, err := os.CreateTemp(filepath.Dir(file), ".result-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

func claim(ctx context.Context, endpoint, runID, grant string, allowLoopback bool) (Config, error) {
	var c Config
	if err := validateURL(endpoint, allowLoopback); err != nil {
		return c, err
	}
	data, _ := json.Marshal(map[string]string{"runId": runID})
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(data)))
	if err != nil {
		return c, err
	}
	req.Header.Set("Authorization", "Bearer "+grant)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return c, errors.New("cannot retrieve task configuration")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return c, fmt.Errorf("configuration claim HTTP %d", res.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&c); err != nil {
		return c, errors.New("invalid task configuration response")
	}
	if c.RunID != runID {
		return c, errors.New("run identity mismatch")
	}
	if err = c.Validate(allowLoopback); err != nil {
		return c, err
	}
	if err = validateURL(c.ReportURL, allowLoopback); err != nil {
		return c, err
	}
	claimURL, _ := url.Parse(endpoint)
	reportURL, _ := url.Parse(c.ReportURL)
	if claimURL.Host != reportURL.Host || claimURL.Scheme != reportURL.Scheme {
		return c, errors.New("report endpoint must use the panel origin")
	}
	return c, nil
}

func postReport(c Config, r Report) {
	data, _ := json.Marshal(r)
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		req, err := http.NewRequestWithContext(ctx, "POST", c.ReportURL, strings.NewReader(string(data)))
		if err != nil {
			cancel()
			return
		}
		req.Header.Set("Authorization", "Bearer "+c.ReportToken)
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := client.Do(req)
		if err == nil {
			ok := res.StatusCode == 200
			closeResponse(res)
			cancel()
			if ok {
				return
			}
		} else {
			cancel()
		}
	}
}

func output(r Report) { b, _ := json.Marshal(r); fmt.Println("KWB_RESULT:" + string(b)) }

func main() { os.Exit(mainCommand(os.Args[1:])) }
func mainCommand(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: runner run|status --run-id ID --state-dir PATH")
		return 2
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	runID := flags.String("run-id", "", "run identity")
	dir := flags.String("state-dir", "", "private state directory")
	endpoint := flags.String("claim-url", "", "panel configuration endpoint")
	grant := flags.String("grant", "", "short-lived single-use authorization")
	allowLoopback := flags.Bool("allow-loopback-http", false, "allow HTTP loopback addresses for local tests only")
	if flags.Parse(args[1:]) != nil || !validID.MatchString(*runID) || !filepath.IsAbs(*dir) {
		return 2
	}
	base, err := filepath.Abs(*dir)
	if err != nil {
		return 2
	}
	for _, p := range []string{base, filepath.Join(base, "results"), filepath.Join(base, "work")} {
		if err = privateDir(p); err != nil {
			output(Report{RunID: *runID, Status: "failed", Stage: "检查本地目录", Error: err.Error(), Warnings: []string{}})
			return 1
		}
	}
	resultPath := filepath.Join(base, "results", *runID+".json")
	if args[0] == "status" {
		var r Report
		data, err := os.ReadFile(resultPath)
		if err != nil {
			output(Report{RunID: *runID, Status: "failed", Stage: "未找到本地执行记录", Error: "no local execution record; this run was not restarted", Warnings: []string{}})
			return 1
		}
		if json.Unmarshal(data, &r) != nil || r.RunID != *runID {
			return 1
		}
		if r.Status == "running" && !processAlive(r.PID) {
			r.Status = "failed"
			r.Stage = "备份进程已中断"
			r.Error = "process exited before recording a complete result"
			_ = saveReport(base, r)
		}
		output(r)
		return 0
	}
	if args[0] != "run" {
		return 2
	}
	// 在领取一次性配置前加运行锁，防止重复投递覆盖正在执行的结果。
	unlockRun, lockErr := acquireLock(filepath.Join(base, "results", *runID+".lock"))
	if lockErr != nil {
		if errors.Is(lockErr, errAlreadyRunning) {
			output(Report{RunID: *runID, Status: "running", Stage: "相同任务已在执行中", Warnings: []string{}})
			return 0
		}
		output(Report{RunID: *runID, Status: "failed", Stage: "检查本地运行锁", Error: lockErr.Error(), Warnings: []string{}})
		return 1
	}
	defer unlockRun()
	// 同一运行号只能执行一次。终态返回原结果，未结束的记录留给 status 核对。
	if data, err := os.ReadFile(resultPath); err == nil {
		var r Report
		if json.Unmarshal(data, &r) == nil && r.RunID == *runID {
			output(r)
			return 0
		}
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c, err := claim(ctx, *endpoint, *runID, *grant, *allowLoopback)
	if err != nil {
		r := Report{RunID: *runID, Status: "failed", Stage: "获取任务配置", Error: err.Error(), Warnings: []string{}}
		_ = saveReport(base, r)
		output(r)
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutMinutes)*time.Minute)
	defer cancel()
	var mu sync.Mutex
	current := Report{RunID: c.RunID, Status: "running", Stage: "准备备份", Warnings: []string{}}
	if err = saveReport(base, current); err != nil {
		current.Status = "failed"
		current.Error = err.Error()
		output(current)
		return 1
	}
	// 本地结果为核对依据，回传失败不改变备份是否成功。进度请求放在独立循环。
	emit := func(r Report) { mu.Lock(); current = r; _ = saveReport(base, r); mu.Unlock() }
	done := make(chan struct{})
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				mu.Lock()
				r := current
				mu.Unlock()
				postReport(c, r)
			}
		}
	}()
	r := execute(ctx, c, base, emit)
	close(done)
	<-workerDone
	if err = saveReport(base, r); err != nil {
		r.Status = "failed"
		r.Stage = "保存本地执行结果"
		r.Error = err.Error()
	}
	postReport(c, r)
	output(r)
	if r.Status == "failed" {
		return 1
	}
	return 0
}
