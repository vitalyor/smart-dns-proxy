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
	hc := &http.Client{Timeout: 15 * time.Second, Transport: tr}
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
