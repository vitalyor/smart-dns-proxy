// Package fetcher downloads external rule sources. It is one of the two main
// SSRF surfaces in the system (the other is the egress relay), so every DNS
// resolution and every redirect hop is re-validated against blocked IP ranges.
package fetcher

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"smartdns/shared/netguard"
)

// Limits bound every download. All values are configurable per deployment.
type Limits struct {
	MaxCompressedBytes   int64
	MaxDecompressedBytes int64
	MaxRedirects         int
	ConnectTimeout       time.Duration
	TotalTimeout         time.Duration
}

// DefaultLimits match the technical specification.
var DefaultLimits = Limits{
	MaxCompressedBytes:   10 << 20,
	MaxDecompressedBytes: 50 << 20,
	MaxRedirects:         5,
	ConnectTimeout:       10 * time.Second,
	TotalTimeout:         60 * time.Second,
}

// Request describes one source download.
type Request struct {
	URL            string
	ETag           string
	LastModified   string
	Token          string // optional bearer/PAT, never persisted into artifacts
	ExpectedSHA256 string
	Limits         Limits
	AllowPrivate   bool // lab only
	// RootCAs pins an additional trust anchor, for a rule mirror published by
	// an internal certificate authority. Nil means the system trust store.
	RootCAs *x509.CertPool
}

// Result carries the downloaded payload plus caching metadata.
type Result struct {
	NotModified  bool
	StatusCode   int
	ETag         string
	LastModified string
	Body         string
	SHA256       string
	Size         int64
}

// ErrBlocked marks a refused destination.
var ErrBlocked = errors.New("blocked destination")

// Client performs guarded HTTPS downloads.
type Client struct{ limits Limits }

// New returns a client with the given limits (zero values fall back to defaults).
func New(l Limits) *Client {
	if l.MaxCompressedBytes == 0 {
		l = DefaultLimits
	}
	return &Client{limits: l}
}

// Fetch downloads a source with full SSRF protection.
func (c *Client) Fetch(ctx context.Context, req Request) (*Result, error) {
	lim := req.Limits
	if lim.MaxCompressedBytes == 0 {
		lim = c.limits
	}
	u, err := url.Parse(req.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("only https sources are allowed, got %q", u.Scheme)
	}

	dialer := netguard.SafeDialerPolicy(req.AllowPrivate)
	dialer.Timeout = lim.ConnectTimeout
	transport := &http.Transport{
		TLSClientConfig:       &tls.Config{RootCAs: req.RootCAs, MinVersion: tls.VersionTLS12},
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   lim.ConnectTimeout,
		ResponseHeaderTimeout: lim.ConnectTimeout,
		DisableCompression:    false,
		ForceAttemptHTTP2:     true,
	}
	hops := 0
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   lim.TotalTimeout,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			hops++
			if hops > lim.MaxRedirects {
				return fmt.Errorf("too many redirects (max %d)", lim.MaxRedirects)
			}
			if r.URL.Scheme != "https" {
				return fmt.Errorf("%w: redirect to %s", ErrBlocked, r.URL.Scheme)
			}
			// The dialer re-checks the IP, but refusing early gives a clearer error.
			if err := checkHost(r.Context(), r.URL.Hostname(), req.AllowPrivate); err != nil {
				return fmt.Errorf("%w: %v", ErrBlocked, err)
			}
			return nil
		},
	}

	if err := checkHost(ctx, u.Hostname(), req.AllowPrivate); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBlocked, err)
	}

	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return nil, err
	}
	hr.Header.Set("User-Agent", "smartdns-panel/1.0 (+rule-fetcher)")
	hr.Header.Set("Accept", "text/plain, application/json, */*;q=0.5")
	if req.ETag != "" {
		hr.Header.Set("If-None-Match", req.ETag)
	}
	if req.LastModified != "" {
		hr.Header.Set("If-Modified-Since", req.LastModified)
	}
	if req.Token != "" {
		hr.Header.Set("Authorization", "Bearer "+req.Token)
	}

	resp, err := httpClient.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	res := &Result{
		StatusCode:   resp.StatusCode,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	}
	if resp.StatusCode == http.StatusNotModified {
		res.NotModified = true
		return res, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return res, fmt.Errorf("source returned HTTP %d", resp.StatusCode)
	}

	var reader io.Reader = io.LimitReader(resp.Body, lim.MaxCompressedBytes+1)
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") ||
		strings.HasSuffix(strings.ToLower(u.Path), ".gz") {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return res, fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		reader = io.LimitReader(gz, lim.MaxDecompressedBytes+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return res, err
	}
	if int64(len(body)) > lim.MaxDecompressedBytes {
		return res, fmt.Errorf("payload exceeds %d bytes", lim.MaxDecompressedBytes)
	}
	sum := sha256.Sum256(body)
	res.SHA256 = hex.EncodeToString(sum[:])
	res.Size = int64(len(body))
	res.Body = string(body)

	if req.ExpectedSHA256 != "" && !strings.EqualFold(req.ExpectedSHA256, res.SHA256) {
		return res, fmt.Errorf("checksum mismatch: expected %s, got %s", req.ExpectedSHA256, res.SHA256)
	}
	// Content type alone is never the acceptance criterion, but an HTML page
	// where a list is expected is the classic silent-corruption case.
	if looksLikeHTML(res.Body) {
		return res, errors.New("source returned an HTML document, not a rule list")
	}
	return res, nil
}

func looksLikeHTML(s string) bool {
	head := strings.ToLower(strings.TrimSpace(s))
	if len(head) > 512 {
		head = head[:512]
	}
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}

func checkHost(ctx context.Context, host string, allowPrivate bool) error {
	if allowPrivate {
		return nil
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(c, "ip", host)
	if err != nil {
		return err
	}
	return netguard.CheckAddrs(addrs)
}

// GitHubRawURL builds a raw.githubusercontent.com URL from repo coordinates.
func GitHubRawURL(repo, ref, path string) (string, error) {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	if !strings.Contains(repo, "/") {
		return "", fmt.Errorf("repo must be owner/name, got %q", repo)
	}
	if ref == "" {
		ref = "main"
	}
	path = strings.TrimPrefix(strings.TrimSpace(path), "/")
	if path == "" {
		return "", errors.New("path is required")
	}
	if strings.Contains(path, "..") {
		return "", errors.New("path traversal is not allowed")
	}
	return "https://raw.githubusercontent.com/" + repo + "/" + ref + "/" + path, nil
}
