package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type requestPolicy struct {
	retry                  []time.Duration
	jitter                 func() time.Duration
	interval, confirmation time.Duration
}

// 超时或代理错误不等于服务端没有接收写入；这类请求只能核对，不能盲目重放。
type uploadUncertainError struct{ cause error }

func (e *uploadUncertainError) Error() string { return e.cause.Error() }
func (e *uploadUncertainError) Unwrap() error { return e.cause }
func isUncertain(err error) bool              { var e *uploadUncertainError; return errors.As(err, &e) }

var safeRay = regexp.MustCompile(`^[A-Za-z0-9-]{1,100}$`)
var diagnosticURL = regexp.MustCompile(`https?://[^\s"'<>]+`)
var diagnosticBearer = regexp.MustCompile(`(?i)Bearer\s+[^\s"'<>]+`)

func diagnosticText(value, password string) string {
	if password != "" {
		value = strings.ReplaceAll(value, password, "[已隐藏]")
	}
	value = diagnosticBearer.ReplaceAllString(value, "Bearer [已隐藏]")
	return diagnosticURL.ReplaceAllStringFunc(value, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[URL 已隐藏]"
		}
		u.User = nil
		u.RawQuery = ""
		u.ForceQuery = false
		u.Fragment = ""
		return u.String()
	})
}
func raySuffix(ray string) string {
	if ray != "" {
		return "，CF-Ray=" + ray
	}
	return ""
}

func limitedText(s string, size int) string {
	if len(s) <= size {
		return s
	}
	const marker = "…[已截断]"
	if size < len(marker) {
		return ""
	}
	end := size - len(marker)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + marker
}
func boundRequests(values []string) []string {
	if len(values) > 16 {
		values = append([]string{values[0]}, values[len(values)-15:]...)
	}
	result := []string{}
	remaining := 4096
	for _, value := range values {
		if remaining == 0 {
			break
		}
		value = limitedText(value, min(256, remaining))
		if value != "" {
			result = append(result, value)
			remaining -= len(value)
		}
	}
	return result
}
func (d *DAV) record(value string) {
	d.diagnostics = boundRequests(append(d.diagnostics, diagnosticText(value, d.password)))
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func retryAfter(value string, now time.Time) (time.Duration, bool) {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 && seconds <= int64((1<<63-1)/time.Second) {
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(0, date.Sub(now)), true
	}
	return 0, false
}

const sizeOnlyWarning = "上传响应异常，仅核对文件大小，未核验远端内容摘要"

// 只查元数据，连续两次大小一致也不宣称远端内容通过摘要校验。
func (d *DAV) confirmArchive(ctx context.Context, name string, size int64) error {
	ctx, cancel := context.WithTimeout(ctx, d.policy.confirmation)
	defer cancel()
	consecutive := 0
	for {
		if err := waitContext(ctx, d.policy.interval); err != nil {
			return fmt.Errorf("上传结果未确认：%w", err)
		}
		if err := d.verifySize(ctx, name, size); err == nil {
			consecutive++
			d.record(fmt.Sprintf("远端大小与本地一致，第 %d 次连续确认（未核验远端内容摘要）", consecutive))
			if consecutive == 2 {
				return nil
			}
		} else {
			consecutive = 0
			d.record("远端大小尚未确认：" + err.Error())
			if isPermanentCheckError(err) {
				return err
			}
		}
	}
}

func isPermanentCheckError(err error) bool {
	var status *checkStatusError
	return errors.As(err, &status) && (status.code == 401 || status.code == 403 || status.code == 413)
}

type checkStatusError struct{ code int }

func (e *checkStatusError) Error() string { return fmt.Sprintf("WebDAV check HTTP %d", e.code) }

func (d *DAV) confirmManifest(ctx context.Context, name string, expected Manifest) error {
	ctx, cancel := context.WithTimeout(ctx, d.policy.confirmation)
	defer cancel()
	for {
		if err := waitContext(ctx, d.policy.interval); err != nil {
			return fmt.Errorf("完成清单发布结果未确认：%w", err)
		}
		if err := d.matchManifest(ctx, name, expected); err == nil {
			d.record("完成清单与本次预期内容一致，已确认发布")
			return nil
		} else {
			d.record("完成清单尚未确认：" + err.Error())
			if isPermanentCheckError(err) || errors.Is(err, errManifestConflict) {
				return err
			}
		}
	}
}

var errManifestConflict = errors.New("remote manifest does not match this upload")

func (d *DAV) matchManifest(ctx context.Context, name string, expected Manifest) error {
	var actual Manifest
	if err := d.getJSON(ctx, name, &actual); err != nil {
		return err
	}
	// 完成时间按时间值比较，避免不同 JSON 时区表示影响相同时间的判断。
	if !actual.CompletedAt.Equal(expected.CompletedAt) {
		return errManifestConflict
	}
	actual.CompletedAt = expected.CompletedAt
	if !reflect.DeepEqual(actual, expected) {
		return errManifestConflict
	}
	return nil
}
