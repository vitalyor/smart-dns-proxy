// Package model defines the immutable wire contract between the control plane
// and the nodes. Everything a node needs for one revision lives in NodeConfig.
package model

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"smartdns/shared/domainset"
)

const (
	CompilerVersion = "1.0.0"
	ArtifactKind    = "node-config"
)

// ArtifactRef is one file of a revision, bound to a single node.
type ArtifactRef struct {
	NodeID string `json:"node_id"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest is the signed index of a revision.
type Manifest struct {
	RevisionID      string        `json:"revision_id"`
	Sequence        int64         `json:"sequence"`
	CreatedAt       time.Time     `json:"created_at"`
	CompilerVersion string        `json:"compiler_version"`
	ModelSHA256     string        `json:"model_sha256"`
	MinAgentVersion string        `json:"min_agent_version"`
	Artifacts       []ArtifactRef `json:"artifacts"`
	Signature       string        `json:"signature,omitempty"`
}

// SigningPayload is the canonical byte form signed by the panel.
func (m Manifest) SigningPayload() []byte {
	c := m
	c.Signature = ""
	b, _ := json.Marshal(c)
	return b
}

// EgressTarget is one reachable egress relay endpoint.
type EgressTarget struct {
	NodeID   string `json:"node_id"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"` // host:port of the relay
	SNI      string `json:"sni"`      // TLS server name presented by the relay
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
}

// EgressPolicy describes how ingress picks among targets. The node applies it
// locally so failover keeps working with the panel offline.
type EgressPolicy struct {
	Mode          string         `json:"mode"` // primary_fallback|weighted|lowest_latency|manual_fixed
	Targets       []EgressTarget `json:"targets"`
	FailThreshold int            `json:"fail_threshold"`
	RiseThreshold int            `json:"rise_threshold"`
	ProbeInterval int            `json:"probe_interval_sec"`
	HysteresisPct int            `json:"hysteresis_pct"`
	HysteresisMs  int            `json:"hysteresis_ms"`
}

// Service is one managed logical service inside a compiled revision.
type Service struct {
	Slug         string        `json:"slug"`
	Name         string        `json:"name"`
	Priority     int           `json:"priority"`
	TTL          uint32        `json:"ttl"`
	AllowedPorts []int         `json:"allowed_ports"`
	UDPMode      string        `json:"udp_mode"` // disabled_fallback|proxy|separate_ip
	Match        domainset.Set `json:"match"`
	RuleSetHash  string        `json:"rule_set_hash"`
	IngressV4    []string      `json:"ingress_v4"`
	IngressV6    []string      `json:"ingress_v6"`
	Egress       EgressPolicy  `json:"egress"`
}

// DNSAccess describes how the DNS frontend authenticates clients.
type DNSAccess struct {
	Mode           string   `json:"mode"` // allowlist|doh-token|mtls|restricted-public-dot
	AllowedCIDRs   []string `json:"allowed_cidrs"`
	DoHPathTokens  []string `json:"doh_path_tokens"` // sha256 hex of the URL path token
	RateLimitQPS   int      `json:"rate_limit_qps"`
	RateLimitBurst int      `json:"rate_limit_burst"`
	MaxConcurrent  int      `json:"max_concurrent"`
}

// DNSConfig is the ingress DNS frontend section.
type DNSConfig struct {
	Upstream     string    `json:"upstream"` // unbound address
	Access       DNSAccess `json:"access"`
	MinTTL       uint32    `json:"min_ttl"`
	MaxTTL       uint32    `json:"max_ttl"`
	PublishAAAA  bool      `json:"publish_aaaa"`
	BlockHTTPSRR bool      `json:"block_https_rr"`
	LogQueries   bool      `json:"log_queries"`
	DoHHostname  string    `json:"doh_hostname"`
	DoTHostname  string    `json:"dot_hostname"`
}

// IngressConfig is the SNI proxy section.
type IngressConfig struct {
	ClientHelloTimeoutMs int `json:"client_hello_timeout_ms"`
	MaxPreReadBytes      int `json:"max_pre_read_bytes"`
	DialTimeoutMs        int `json:"dial_timeout_ms"`
	IdleTimeoutSec       int `json:"idle_timeout_sec"`
	MaxSessions          int `json:"max_sessions"`
}

// EgressConfig is the relay section: the allowlist that prevents open proxying.
type EgressConfig struct {
	Allow          domainset.Set `json:"allow"`
	AllowedPorts   []int         `json:"allowed_ports"`
	Resolver       string        `json:"resolver"`
	DialTimeoutMs  int           `json:"dial_timeout_ms"`
	IdleTimeoutSec int           `json:"idle_timeout_sec"`
	MaxSessions    int           `json:"max_sessions"`
	// AllowPrivateDestinations disables the SSRF / open-proxy IP guard.
	// Lab and integration use only; the panel marks any revision using it.
	AllowPrivateDestinations bool `json:"allow_private_destinations,omitempty"`
}

// NodeConfig is the complete per-node artifact for one revision.
type NodeConfig struct {
	SchemaVersion int           `json:"schema_version"`
	RevisionID    string        `json:"revision_id"`
	Sequence      int64         `json:"sequence"`
	NodeID        string        `json:"node_id"`
	NodeName      string        `json:"node_name"`
	Role          string        `json:"role"`
	Services      []Service     `json:"services"`
	DNS           DNSConfig     `json:"dns,omitempty"`
	Ingress       IngressConfig `json:"ingress,omitempty"`
	Egress        EgressConfig  `json:"egress,omitempty"`
	// LogLevel is the level the panel wants this node's processes to run at.
	// A node with LOG_LEVEL set locally ignores it, so on-node debugging is
	// never undone by the next revision.
	LogLevel string   `json:"log_level,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

// Canonical renders the artifact deterministically.
func (c NodeConfig) Canonical() ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}

// SHA256Hex hashes arbitrary bytes.
func SHA256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Verify checks the panel's ed25519 signature over the manifest.
func (m Manifest) Verify(pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("no panel public key configured")
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(pub, m.SigningPayload(), sig) {
		return errors.New("manifest signature is not valid")
	}
	return nil
}
