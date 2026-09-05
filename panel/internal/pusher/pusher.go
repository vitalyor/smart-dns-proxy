// Package pusher is the panel's outbound side of the push model: it connects to
// each node's management port over mutual TLS, delivers compiled config and
// polls health. Nodes never dial the panel. A node being unreachable is normal
// (it may be rebooting or briefly offline) and never blocks the panel.
package pusher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"smartdns/shared/model"
)

// Target is one node the panel talks to.
type Target struct {
	NodeID      string
	Name        string
	MgmtAddress string // host:port, default port 3333
	// NodeCertFP pins the node's server certificate so a hijacked address
	// cannot impersonate the node.
	NodeCertFP string
}

// Client pushes to nodes using the panel's client certificate. The CA anchors
// the node server certs; the per-node fingerprint pins the exact node.
type Client struct {
	clientCert tls.Certificate
	caPool     *x509.CertPool
	hc         *http.Client
}

// New builds a pusher from the panel's client keypair (PEM) and the CA pool.
func New(clientCertPEM, clientKeyPEM, caPEM []byte) (*Client, error) {
	cert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("panel client keypair: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("CA PEM is not a valid certificate")
	}
	c := &Client{clientCert: cert, caPool: pool}
	c.hc = &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     60 * time.Second,
			DialContext:         (&net.Dialer{Timeout: 6 * time.Second}).DialContext,
		},
	}
	return c, nil
}

// tlsFor builds a per-target TLS config that trusts the CA and, when a
// fingerprint is known, pins the exact node certificate. ServerName is left to
// the node cert's SANs via InsecureSkipVerify+manual verify so a bare IP works.
func (c *Client) tlsFor(t Target) *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{c.clientCert},
		RootCAs:            c.caPool,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // verified manually below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("node presented no certificate")
			}
			leaf := cs.PeerCertificates[0]
			opts := x509.VerifyOptions{Roots: c.caPool, Intermediates: x509.NewCertPool()}
			for _, ic := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(ic)
			}
			if _, err := leaf.Verify(opts); err != nil {
				return fmt.Errorf("node cert not signed by our CA: %w", err)
			}
			if t.NodeCertFP != "" {
				sum := sha256.Sum256(leaf.Raw)
				if hex.EncodeToString(sum[:]) != t.NodeCertFP {
					return fmt.Errorf("node cert fingerprint mismatch (address may be hijacked)")
				}
			}
			return nil
		},
	}
}

func (c *Client) do(ctx context.Context, t Target, method, path string, body []byte) ([]byte, int, error) {
	return c.doTimeout(ctx, t, method, path, body, 15*time.Second)
}

func (c *Client) doTimeout(ctx context.Context, t Target, method, path string, body []byte, timeout time.Duration) ([]byte, int, error) {
	addr := t.MgmtAddress
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "3333")
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+addr+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// A per-request transport carries the per-node pin without sharing state.
	tr := &http.Transport{
		TLSClientConfig:     c.tlsFor(t),
		DialContext:         (&net.Dialer{Timeout: 6 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 1,
	}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Timeout: timeout, Transport: tr}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return raw, resp.StatusCode, nil
}

// PushResult is what the node acknowledged.
type PushResult struct {
	AppliedRevisionID string `json:"applied_revision_id"`
	AppliedSequence   int64  `json:"applied_sequence"`
	Status            string `json:"status"`
}

// PushConfig delivers one compiled artifact to a node.
func (c *Client) PushConfig(ctx context.Context, t Target, artifact []byte) (*PushResult, error) {
	raw, code, err := c.do(ctx, t, http.MethodPost, "/v1/config", artifact)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("node rejected config (HTTP %d): %s", code, string(raw))
	}
	var out PushResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CertRequest asks a node to issue a DoT/DoH certificate via ACME HTTP-01.
type CertRequest struct {
	Domain  string `json:"domain"`
	Email   string `json:"email,omitempty"`
	Force   bool   `json:"force,omitempty"`
	Staging bool   `json:"staging,omitempty"`
}

// CertResult is the node's answer. OK=false with Error set is a normal
// issuance failure (e.g. :80 unreachable), not a transport error.
type CertResult struct {
	OK       bool   `json:"ok"`
	Domain   string `json:"domain,omitempty"`
	NotAfter string `json:"not_after,omitempty"`
	Error    string `json:"error,omitempty"`
}

// IssueCert asks a node to run an ACME order. HTTP-01 validation plus finalize
// can take a couple of minutes, so this uses a long timeout.
func (c *Client) IssueCert(ctx context.Context, t Target, req CertRequest) (*CertResult, error) {
	body, _ := json.Marshal(req)
	raw, code, err := c.doTimeout(ctx, t, http.MethodPost, "/v1/cert/issue", body, 4*time.Minute)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("node rejected cert request (HTTP %d): %s", code, string(raw))
	}
	var out CertResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PushAccess sends the complete DoH token set. The full set travels every time
// rather than a delta, so the call is idempotent and a missed push is repaired by
// the next reconcile instead of leaving the node subtly wrong (ADR 0012).
func (c *Client) PushAccess(ctx context.Context, t Target, set model.AccessSet) error {
	body, _ := json.Marshal(set)
	raw, code, err := c.doTimeout(ctx, t, http.MethodPost, "/v1/access/tokens", body, 15*time.Second)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("node rejected access set (HTTP %d): %s", code, string(raw))
	}
	return nil
}

// FetchDNSLog returns the node's live query-log JSON (raw), passing the
// incremental cursor through. Short timeout: it is polled continuously.
func (c *Client) FetchDNSLog(ctx context.Context, t Target, after uint64) ([]byte, error) {
	path := "/v1/dns/log"
	if after > 0 {
		path += "?after=" + strconv.FormatUint(after, 10)
	}
	raw, code, err := c.doTimeout(ctx, t, http.MethodGet, path, nil, 6*time.Second)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("node returned HTTP %d", code)
	}
	return raw, nil
}

// PushCert installs a certificate the panel obtained itself. One wildcard serves
// the whole fleet, so this replaces per-node HTTP-01 issuance — and the zone
// token that DNS-01 needs never leaves the panel (ADR 0012).
func (c *Client) PushCert(ctx context.Context, t Target, certPEM, keyPEM []byte) error {
	body, _ := json.Marshal(map[string]string{"cert_pem": string(certPEM), "key_pem": string(keyPEM)})
	raw, code, err := c.doTimeout(ctx, t, http.MethodPost, "/v1/cert/install", body, 30*time.Second)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("node rejected certificate (HTTP %d): %s", code, string(raw))
	}
	return nil
}

// FetchCounters returns the node's per-device tallies (raw JSON).
func (c *Client) FetchCounters(ctx context.Context, t Target) ([]byte, error) {
	raw, code, err := c.doTimeout(ctx, t, http.MethodGet, "/v1/dns/counters", nil, 8*time.Second)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("node returned HTTP %d", code)
	}
	return raw, nil
}

// Poll fetches a node's health.
func (c *Client) Poll(ctx context.Context, t Target) (*model.Health, error) {
	raw, code, err := c.do(ctx, t, http.MethodGet, "/v1/health", nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("node health returned HTTP %d", code)
	}
	var h model.Health
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, err
	}
	return &h, nil
}
