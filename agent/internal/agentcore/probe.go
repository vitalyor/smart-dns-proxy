package agentcore

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"time"

	"smartdns/shared/model"
)

// probe fills the DNS-meaningful health fields with cheap local checks. These
// are the signals that predict user-facing breakage — not CPU or RAM, which the
// operator already sees in their host view. The sneaky one is EgressReachable:
// DNS can answer perfectly while unblocking is silently broken because the
// tunnel to egress is down.
func (a *Agent) probe(h *model.Health) {
	cfg, err := a.ActiveConfig()
	if err != nil {
		return
	}
	switch cfg.Role {
	case "ingress":
		h.UpstreamOK = dialOK(cfg.DNS.Upstream, 1500*time.Millisecond)
		h.EgressReachable = ingressCanReachEgress(cfg)
		h.CertDaysLeft = certDaysLeft(a.cfg.certPath())
	case "egress":
		// The relay's own resolver reachability stands in for resolve health.
		h.UpstreamOK = dialOK(cfg.Egress.Resolver, 1500*time.Millisecond)
	}
}

// ingressCanReachEgress returns true if at least one egress target of any
// service accepts a TCP connection. One reachable path is enough to unblock.
func ingressCanReachEgress(cfg *model.NodeConfig) bool {
	seen := map[string]bool{}
	any := false
	for _, s := range cfg.Services {
		for _, t := range s.Egress.Targets {
			if t.Endpoint == "" || seen[t.Endpoint] {
				continue
			}
			seen[t.Endpoint] = true
			any = true
			if dialOK(t.Endpoint, 2*time.Second) {
				return true
			}
		}
	}
	// No targets configured at all is not an egress-reachability failure.
	return !any
}

func dialOK(addr string, timeout time.Duration) bool {
	if addr == "" {
		return false
	}
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		// A bare host:port for UDP DNS (e.g. unbound:53) still answers a TCP
		// dial in our images; if not, treat unknown as not-ok.
		return false
	}
	_ = c.Close()
	return true
}

// certDaysLeft reports days until the earliest-expiring cert in the PEM file,
// or -1 if there is no readable cert. Used to warn before DoT/DoH silently dies.
func certDaysLeft(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	earliest := time.Time{}
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			continue
		}
		if earliest.IsZero() || c.NotAfter.Before(earliest) {
			earliest = c.NotAfter
		}
	}
	if earliest.IsZero() {
		return -1
	}
	return int(time.Until(earliest).Hours() / 24)
}
