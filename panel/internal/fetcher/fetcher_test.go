package fetcher

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOnlyHTTPSAllowed(t *testing.T) {
	c := New(DefaultLimits)
	_, err := c.Fetch(context.Background(), Request{URL: "http://example.com/list.txt"})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("plain HTTP must be refused, got %v", err)
	}
}

func TestLoopbackDestinationBlocked(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("example.com\n"))
	}))
	defer srv.Close()
	c := New(DefaultLimits)
	_, err := c.Fetch(context.Background(), Request{URL: srv.URL})
	if err == nil {
		t.Fatal("a loopback source must be refused: this is the SSRF guard")
	}
}

func TestMetadataEndpointBlocked(t *testing.T) {
	c := New(DefaultLimits)
	for _, u := range []string{
		"https://169.254.169.254/latest/meta-data/",
		"https://[fd00::1]/x",
		"https://10.0.0.1/list.txt",
	} {
		if _, err := c.Fetch(context.Background(), Request{URL: u}); err == nil {
			t.Fatalf("%s must be refused", u)
		}
	}
}

func TestHTMLPayloadRejected(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("<!DOCTYPE html><html><body>404</body></html>"))
	}))
	defer srv.Close()
	c := New(DefaultLimits)
	// AllowPrivate mirrors the lab escape hatch so the guard does not mask the
	// behaviour under test.
	_, err := c.Fetch(context.Background(), Request{URL: srv.URL, AllowPrivate: true, RootCAs: pool(srv)})
	if err == nil || !strings.Contains(err.Error(), "HTML") {
		t.Fatalf("an HTML error page must not be accepted as a rule list, got %v", err)
	}
}

func TestOversizePayloadRejected(t *testing.T) {
	big := strings.Repeat("example.com\n", 200000)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(big))
	}))
	defer srv.Close()
	lim := DefaultLimits
	lim.MaxDecompressedBytes = 1024
	c := New(lim)
	if _, err := c.Fetch(context.Background(), Request{URL: srv.URL, AllowPrivate: true, RootCAs: pool(srv), Limits: lim}); err == nil {
		t.Fatal("payload above the configured limit must be refused")
	}
}

func TestChecksumMismatchRejected(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("example.com\n"))
	}))
	defer srv.Close()
	c := New(DefaultLimits)
	_, err := c.Fetch(context.Background(), Request{
		URL: srv.URL, AllowPrivate: true, RootCAs: pool(srv),
		ExpectedSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
	})
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("checksum mismatch must be refused, got %v", err)
	}
}

func TestNotModifiedIsReported(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte("example.com\n"))
	}))
	defer srv.Close()
	c := New(DefaultLimits)
	res, err := c.Fetch(context.Background(), Request{URL: srv.URL, ETag: `"v1"`, AllowPrivate: true, RootCAs: pool(srv)})
	if err != nil || !res.NotModified {
		t.Fatalf("conditional request should report 304, got %+v %v", res, err)
	}
}

func TestGitHubRawURL(t *testing.T) {
	u, err := GitHubRawURL("owner/repo", "v1.2.3", "rules/list.txt")
	if err != nil || u != "https://raw.githubusercontent.com/owner/repo/v1.2.3/rules/list.txt" {
		t.Fatalf("got %q %v", u, err)
	}
	if _, err := GitHubRawURL("owner/repo", "main", "../../etc/passwd"); err == nil {
		t.Fatal("path traversal must be refused")
	}
	if _, err := GitHubRawURL("norepo", "main", "a.txt"); err == nil {
		t.Fatal("repo must be owner/name")
	}
}

func pool(srv *httptest.Server) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(srv.Certificate())
	return p
}

var _ = tls.VersionTLS12
