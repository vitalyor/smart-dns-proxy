package model

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Bundle is what the panel mints when an operator creates a node and what the
// operator pastes into the agent (base64, exactly like remnanode's SECRET_KEY).
// It carries everything the node needs to (a) present itself as a TLS server
// and (b) trust exactly one panel — the panel never signs config, so the pinned
// client fingerprint is the whole trust anchor for who may push.
type Bundle struct {
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
	Role   string `json:"role"` // ingress|egress

	// NodeCertPEM/NodeKeyPEM: the node's TLS server identity, minted by the CA.
	NodeCertPEM string `json:"node_cert_pem"`
	NodeKeyPEM  string `json:"node_key_pem"`
	// CACertPEM anchors verification of the panel's client certificate.
	CACertPEM string `json:"ca_cert_pem"`
	// PanelClientFP pins the one panel allowed to push: any client cert with a
	// different SHA-256 is rejected even if the CA signed it.
	PanelClientFP string `json:"panel_client_fp"`
}

// Encode renders the bundle as the single base64 blob the operator pastes.
// The JSON is gzipped first: it's mostly three PEM certificates (low-entropy,
// repeated base64 alphabet and identical headers), so gzip roughly halves the
// blob — the pasted install command shrinks accordingly.
func (b Bundle) Encode() string {
	raw, _ := json.Marshal(b)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// DecodeBundle parses the pasted blob. It accepts both the gzipped form and the
// older plain-JSON form (gzip magic 0x1f8b tells them apart), so nodes carrying
// a pre-gzip bundle keep decoding after an image update.
func DecodeBundle(s string) (*Bundle, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("bundle is not valid base64: %w", err)
	}
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("bundle gzip header: %w", err)
		}
		if raw, err = io.ReadAll(zr); err != nil {
			return nil, fmt.Errorf("bundle gzip body: %w", err)
		}
	}
	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("bundle is not valid JSON: %w", err)
	}
	if b.NodeCertPEM == "" || b.NodeKeyPEM == "" || b.CACertPEM == "" || b.PanelClientFP == "" {
		return nil, fmt.Errorf("bundle is missing required fields")
	}
	return &b, nil
}

// Health is what a node returns to the panel on every poll. Only DNS-meaningful
// signals live here; system telemetry (CPU/RAM) is deliberately absent — the
// operator already sees that in their node hosting/Remnawave view.
type Health struct {
	// AppliedSequence lets the panel detect drift and re-push if a node is
	// behind. Zero means the node has never applied a config.
	AppliedRevisionID string `json:"applied_revision_id"`
	AppliedSequence   int64  `json:"applied_sequence"`

	Status  string `json:"status"` // healthy|degraded
	Role    string `json:"role"`
	Version string `json:"version"` // agent build version, surfaced in the panel
	UptimeS int64  `json:"uptime_s"`
	LastErr string `json:"last_error,omitempty"`

	// DNS-plane signals (ingress). Zero values are fine when a field does not
	// apply to the role.
	QueriesPerSec   float64 `json:"queries_per_sec"`
	RejectRate      float64 `json:"reject_rate"`
	UpstreamOK      bool    `json:"upstream_ok"`
	EgressReachable bool    `json:"egress_reachable"`
	// CertDaysLeft — управляющий сертификат ноды (mTLS с панелью), не публичный.
	CertDaysLeft int `json:"cert_days_left"`
	// ResolverCertFP и ResolverCertDaysLeft — про сертификат, который нода
	// реально отдаёт клиентам на DoH/DoT. Панель по отпечатку понимает, доехало
	// ли продление, и досылает его без нового обращения к Let's Encrypt.
	ResolverCertFP       string `json:"resolver_cert_fp,omitempty"`
	ResolverCertDaysLeft int    `json:"resolver_cert_days_left,omitempty"`

	// AccessHash is the digest of the DoH token set the node currently holds.
	// The panel compares it with its own on every poll and re-pushes on drift,
	// so the two converge without a separate tracking mechanism.
	AccessHash string `json:"access_hash,omitempty"`

	// Egress-plane signals.
	AllowlistDenials int64 `json:"allowlist_denials"`
	ResolveErrors    int64 `json:"resolve_errors"`
}

// AccessSet is the complete set of DoH path tokens (sha256 hex of the URL path
// segment) a node should accept. The panel always sends the full set rather than
// a delta: the operation is then idempotent and self-healing.
type AccessSet struct {
	Tokens []string `json:"tokens"`
}

// AccessHash digests a token set order-independently, so panel and node agree on
// "same set" regardless of how either assembled it.
func AccessHash(tokens []string) string {
	norm := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			norm = append(norm, t)
		}
	}
	sort.Strings(norm)
	sum := sha256.Sum256([]byte(strings.Join(norm, "\n")))
	return hex.EncodeToString(sum[:])
}
