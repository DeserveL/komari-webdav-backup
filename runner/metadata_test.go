package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func metadataXML(href, properties string) string {
	return `<D:multistatus xmlns:D="DAV:"><D:response><D:href>` + href + `</D:href><D:propstat><D:prop>` + properties + `</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`
}
func writeMetadata(w http.ResponseWriter, href, size string) {
	w.WriteHeader(207)
	_, _ = io.WriteString(w, metadataXML(href, `<D:getcontentlength>`+size+`</D:getcontentlength><D:resourcetype/>`))
}

func TestFileMetadataRejectsUntrustedSizes(t *testing.T) {
	props := `<D:getcontentlength>123</D:getcontentlength><D:resourcetype/>`
	good := metadataXML("/dav/%E9%85%8D%E7%BD%AE.tar.gz", props)
	for _, test := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"valid", good, 207, true},
		{"other property unavailable", strings.Replace(good, `</D:response>`, `<D:propstat><D:prop><D:displayname/></D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat></D:response>`, 1), 207, true},
		{"wrong file", metadataXML("/dav/other", props), 207, false},
		{"other host", metadataXML("https://other.example/dav/file", props), 207, false},
		{"query", metadataXML("/dav/%E9%85%8D%E7%BD%AE.tar.gz?key=1", props), 207, false},
		{"directory", strings.Replace(good, `<D:resourcetype/>`, `<D:resourcetype><D:collection/></D:resourcetype>`, 1), 207, false},
		{"invalid resource type", strings.Replace(good, `<D:resourcetype/>`, `<D:resourcetype>unknown</D:resourcetype>`, 1), 207, false},
		{"size property 404", strings.Replace(good, "200 OK", "404 Not Found", 1), 207, false},
		{"duplicate size", metadataXML("/dav/%E9%85%8D%E7%BD%AE.tar.gz", props+`<D:getcontentlength>123</D:getcontentlength>`), 207, false},
		{"duplicate property container", strings.Replace(good, `</D:prop>`, `</D:prop><D:prop/>`, 1), 207, false},
		{"duplicate href", strings.Replace(good, `</D:href>`, `</D:href><D:href>/other</D:href>`, 1), 207, false},
		{"missing type", strings.Replace(good, `<D:resourcetype/>`, "", 1), 207, false},
		{"negative", strings.Replace(good, ">123<", ">-123<", 1), 207, false},
		{"overflow", strings.Replace(good, ">123<", ">9223372036854775808<", 1), 207, false},
		{"malformed", good[:len(good)-3], 207, false},
		{"second document", good + good, 207, false},
		{"wrong namespace", strings.ReplaceAll(good, "DAV:", "other:"), 207, false},
		{"multiple files", strings.Replace(good, `</D:multistatus>`, `<D:response><D:href>/another</D:href></D:response></D:multistatus>`, 1), 207, false},
		{"error status with size header", good, 500, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "PROPFIND" || r.Header.Get("Depth") != "0" || r.Header.Get("Cache-Control") == "" {
					t.Error("not an uncached metadata query")
				}
				w.Header().Set("Content-Length", fmt.Sprint(len(test.body)))
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer s.Close()
			d, _ := newDAV(Config{WebDAVURL: s.URL + "/dav", TimeoutMinutes: 1})
			d.policy.retry = []time.Duration{0, 0}
			d.policy.jitter = func() time.Duration { return 0 }
			size, err := d.fileSize(context.Background(), "配置.tar.gz")
			if test.ok && (err != nil || size != 123) {
				t.Fatal(size, err)
			}
			if !test.ok && err == nil {
				t.Fatal("untrusted metadata accepted")
			}
		})
	}
}

func TestProperty404DoesNotConfirmDeletion(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "DELETE" {
					w.WriteHeader(202)
					return
				}
				w.WriteHeader(207)
				if missing {
					_, _ = io.WriteString(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>/file</d:href><d:status>HTTP/1.1 404 Not Found</d:status></d:response></d:multistatus>`)
					return
				}
				_, _ = io.WriteString(w, strings.Replace(metadataXML("/file", `<D:getcontentlength/>`), "200 OK", "404 Not Found", 1))
			}))
			defer s.Close()
			d, _ := newDAV(Config{WebDAVURL: s.URL, TimeoutMinutes: 1})
			err := d.deleteConfirmed(context.Background(), "file")
			if missing && err != nil {
				t.Fatal(err)
			}
			if !missing && !errors.Is(err, errInvalidMetadata) {
				t.Fatal("property 404 was treated as file absence", err)
			}
		})
	}
}

func TestHEADProxyFailureDoesNotBlockBackup(t *testing.T) {
	base, _ := fixtureDAV(t)
	heads, archiveGets := 0, 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			heads++
			w.WriteHeader(500)
			return
		}
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, ".tar.gz") {
			archiveGets++
			w.WriteHeader(500)
			return
		}
		base.ServeHTTP(w, r)
	}))
	defer s.Close()
	c, _, state := fixtureConfig(t, s.URL)
	r := execute(context.Background(), c, state, func(Report) {})
	if r.Status != "success" || heads != 0 || archiveGets != 0 {
		t.Fatalf("%+v; HEAD=%d archive GET=%d", r, heads, archiveGets)
	}
	if len(base.files[remoteArchive(c)]) == 0 || len(base.files[remoteManifest(c)]) == 0 {
		t.Fatal("backup not published")
	}
}

func TestActualHEADFailureIsNotHiddenFromDiagnostics(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("CF-Ray", "head-test-ray")
		w.WriteHeader(500)
	}))
	defer s.Close()
	d, _ := newDAV(Config{WebDAVURL: s.URL, TimeoutMinutes: 1})
	d.policy.retry = []time.Duration{0, 0}
	d.policy.jitter = func() time.Duration { return 0 }
	if _, err := d.request(context.Background(), "HEAD", "file", nil, nil); err == nil {
		t.Fatal("error ignored")
	}
	text := strings.Join(d.diagnostics, " ")
	if !strings.Contains(text, "HEAD") || !strings.Contains(text, "HTTP 500") || !strings.Contains(text, "head-test-ray") {
		t.Fatal(text)
	}
}
