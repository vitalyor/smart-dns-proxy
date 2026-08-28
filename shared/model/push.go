package model

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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
func (b Bundle) Encode() string {
	raw, _ := json.Marshal(b)
	return base64.StdEncoding.EncodeToString(raw)
}

// DecodeBundle parses the pasted blob.
func DecodeBundle(s string) (*Bundle, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("bundle is not valid base64: %w", err)
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
	UptimeS int64  `json:"uptime_s"`
	LastErr string `json:"last_error,omitempty"`

	// DNS-plane signals (ingress). Zero values are fine when a field does not
	// apply to the role.
	QueriesPerSec   float64 `json:"queries_per_sec"`
	RejectRate      float64 `json:"reject_rate"`
	UpstreamOK      bool    `json:"upstream_ok"`
	EgressReachable bool    `json:"egress_reachable"`
	CertDaysLeft    int     `json:"cert_days_left"`

	// Egress-plane signals.
	AllowlistDenials int64 `json:"allowlist_denials"`
	ResolveErrors    int64 `json:"resolve_errors"`
}
