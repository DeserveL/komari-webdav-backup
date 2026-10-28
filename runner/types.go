package main

import (
	"errors"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const owner = "komari-webdav-backup/v2"

var errAlreadyRunning = errors.New("another backup is running on this node")

type Config struct {
	Protocol       int      `json:"protocol"`
	Mode           string   `json:"mode"`
	RunID          string   `json:"runId"`
	JobID          string   `json:"jobId"`
	NodeID         string   `json:"nodeId"`
	Sources        []string `json:"sources"`
	Excludes       []string `json:"excludes"`
	RemoteDir      string   `json:"remoteDir"`
	FilePrefix     string   `json:"filePrefix"`
	RetentionDays  int      `json:"retentionDays"`
	WebDAVURL      string   `json:"webdavUrl"`
	Username       string   `json:"username"`
	Password       string   `json:"password"`
	TimeoutMinutes int      `json:"timeoutMinutes"`
	ReportURL      string   `json:"reportUrl,omitempty"`
	ReportToken    string   `json:"reportToken,omitempty"`
}

type Report struct {
	RunID        string   `json:"runId"`
	Status       string   `json:"status"`
	Stage        string   `json:"stage"`
	Bytes        int64    `json:"bytes,omitempty"`
	SHA256       string   `json:"sha256,omitempty"`
	RemotePath   string   `json:"remotePath,omitempty"`
	ManifestPath string   `json:"manifestPath,omitempty"`
	Warnings     []string `json:"warnings"`
	Error        string   `json:"error,omitempty"`
	PID          int      `json:"pid,omitempty"`
	UpdatedAt    string   `json:"updatedAt,omitempty"`
}

type Manifest struct {
	Owner       string    `json:"owner"`
	Version     int       `json:"version"`
	RunID       string    `json:"runId"`
	JobID       string    `json:"jobId"`
	NodeID      string    `json:"nodeId"`
	CompletedAt time.Time `json:"completedAt"`
	Archive     string    `json:"archive"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	Sources     []string  `json:"sources"`
	Warnings    []string  `json:"warnings"`
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func validateURL(raw string, allowLoopback bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	if allowLoopback && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" || u.Hostname() == "localhost") {
		return nil
	}
	return errors.New("HTTPS is required")
}

func (c Config) Validate(allowLoopback bool) error {
	if c.Protocol != 2 || (c.Mode != "backup" && c.Mode != "test") || !validID.MatchString(c.RunID) || !validID.MatchString(c.JobID) || !validID.MatchString(c.NodeID) || !validPrefix(c.FilePrefix, c.RunID) {
		return errors.New("invalid protocol, mode or identity")
	}
	if err := validateURL(c.WebDAVURL, allowLoopback); err != nil {
		return err
	}
	if c.TimeoutMinutes < 5 || c.TimeoutMinutes > 1440 || c.RetentionDays < 0 || c.RetentionDays > 36500 {
		return errors.New("invalid retention or timeout")
	}
	if strings.HasPrefix(c.RemoteDir, "/") || strings.Contains(c.RemoteDir, "\\") || path.Clean(c.RemoteDir) != c.RemoteDir || c.RemoteDir == "." || c.RemoteDir == ".." || strings.HasPrefix(c.RemoteDir, "../") {
		return errors.New("invalid remote directory")
	}
	if len(c.Sources) == 0 || len(c.Sources) > 128 {
		return errors.New("at least one source is required")
	}
	for _, source := range c.Sources {
		if !filepath.IsAbs(source) || filepath.Clean(source) == string(filepath.Separator) || strings.ContainsRune(source, 0) {
			return errors.New("sources must be absolute non-root paths")
		}
	}
	for _, pattern := range c.Excludes {
		if _, err := compileGlob(pattern); err != nil {
			return err
		}
	}
	return nil
}

func redact(message, secret string) string {
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	if len(message) > 4000 {
		message = message[:4000]
	}
	return message
}
