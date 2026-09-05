// Package dnsfe implements the ingress DNS frontend: plain DNS, DoT and DoH.
// It rewrites only managed domains and hands every other query to Unbound
// unchanged. It is never itself a recursive resolver.
package dnsfe

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"strings"
	"sync"

	"smartdns/shared/domainset"
	"smartdns/shared/model"
)

// route is the compiled per-service routing entry.
type route struct {
	svc     *model.Service
	matcher *domainset.Matcher
}

// Router resolves a qname to the managed service that owns it.
type Router struct {
	mu     sync.RWMutex
	routes []route
	cfg    *model.NodeConfig
	acl    []netip.Prefix
	// tokens is owned by the access channel, not by the config: applying a
	// configuration must never clobber a newer token set (ADR 0012).
	tokens    map[string]bool
	tokenHash string
}

// NewRouter compiles a routing table from a node config.
func NewRouter(c *model.NodeConfig) *Router {
	r := &Router{}
	r.Apply(c)
	return r
}

// Apply swaps in a new compiled table atomically.
func (r *Router) Apply(c *model.NodeConfig) {
	routes := make([]route, 0, len(c.Services))
	for i := range c.Services {
		s := &c.Services[i]
		routes = append(routes, route{svc: s, matcher: s.Match.Compile()})
	}
	var acl []netip.Prefix
	for _, s := range c.DNS.Access.AllowedCIDRs {
		if p, err := netip.ParsePrefix(strings.TrimSpace(s)); err == nil {
			acl = append(acl, p)
		}
	}
	r.mu.Lock()
	r.routes, r.cfg, r.acl = routes, c, acl
	r.mu.Unlock()
}

// SetTokens swaps the accepted DoH path token set. Separate from Apply on
// purpose: access changes far more often than configuration, and a config
// rollout must not undo a newer set.
func (r *Router) SetTokens(tokens []string) {
	m := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			m[t] = true
		}
	}
	h := model.AccessHash(tokens)
	r.mu.Lock()
	r.tokens, r.tokenHash = m, h
	r.mu.Unlock()
}

// KnownToken reports whether the hash belongs to the live set. Used to keep the
// per-token tallies bounded: only tokens the panel issued are ever counted.
func (r *Router) KnownToken(t string) bool {
	if t == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tokens[strings.ToLower(t)]
}

// TokensHash reports the digest of the current set so the node can tell the
// panel what it holds and the two can converge.
func (r *Router) TokensHash() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tokenHash
}

// TokenFromSNI recovers a device token from a DoT server name. Android's Private
// DNS accepts only a hostname and cannot carry a token in a path, so the token
// rides in the label: <token>.dns.example. Returns the same sha256 form the DoH
// path produces, or "" when the name is the bare resolver hostname.
//
// This is what lets Android participate in per-device access at all; without it
// turning on token enforcement would simply cut every Android user off.
func (r *Router) TokenFromSNI(sni string) string {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()
	if cfg == nil || sni == "" {
		return ""
	}
	base := strings.ToLower(strings.TrimSuffix(cfg.DNS.DoTHostname, "."))
	sni = strings.ToLower(strings.TrimSuffix(sni, "."))
	if base == "" || sni == base || !strings.HasSuffix(sni, "."+base) {
		return ""
	}
	label := strings.TrimSuffix(sni, "."+base)
	if label == "" || strings.Contains(label, ".") {
		return "" // only one level below the resolver name is a token
	}
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:])
}

// Config returns the active node config.
func (r *Router) Config() *model.NodeConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

// Lookup returns the managed service owning host, or nil.
// The most specific match wins; ties are broken by the compiled priority.
func (r *Router) Lookup(host string) *model.Service {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var best *model.Service
	bestSpec, bestPrio := -1, -1
	for i := range r.routes {
		spec := r.routes[i].matcher.Specificity(host)
		if spec < 0 {
			continue
		}
		p := r.routes[i].svc.Priority
		if spec > bestSpec || (spec == bestSpec && p > bestPrio) {
			best, bestSpec, bestPrio = r.routes[i].svc, spec, p
		}
	}
	return best
}

// AllowClient applies the configured DNS access mode.
func (r *Router) AllowClient(ip netip.Addr, dohToken string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.cfg == nil {
		return false
	}
	switch r.cfg.DNS.Access.Mode {
	case "doh-token":
		if dohToken != "" && r.tokens[strings.ToLower(dohToken)] {
			return true
		}
		return r.inACL(ip)
	case "mtls":
		// Enforced by the TLS layer; the ACL still applies to plain DNS.
		return r.inACL(ip)
	case "restricted-public-dot":
		return true // rate limiter is the control here
	default: // allowlist
		return r.inACL(ip)
	}
}

func (r *Router) inACL(ip netip.Addr) bool {
	if len(r.acl) == 0 {
		return false
	}
	for _, p := range r.acl {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// NormalizeQName lowercases and strips the trailing dot for matching.
func NormalizeQName(q string) string {
	q = strings.ToLower(strings.TrimSuffix(q, "."))
	return q
}
