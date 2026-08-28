// Package dnsfe implements the ingress DNS frontend: plain DNS, DoT and DoH.
// It rewrites only managed domains and hands every other query to Unbound
// unchanged. It is never itself a recursive resolver.
package dnsfe

import (
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
	tokens map[string]bool
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
	tokens := make(map[string]bool, len(c.DNS.Access.DoHPathTokens))
	for _, t := range c.DNS.Access.DoHPathTokens {
		tokens[strings.ToLower(t)] = true
	}
	r.mu.Lock()
	r.routes, r.cfg, r.acl, r.tokens = routes, c, acl, tokens
	r.mu.Unlock()
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
