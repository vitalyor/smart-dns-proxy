package proxy

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"smartdns/shared/domainset"
	"smartdns/shared/metrics"
	"smartdns/shared/model"
	"smartdns/shared/sniff"
	"smartdns/shared/tunnel"
)

var (
	mConn   = metrics.Counter("smartdns_ingress_connections_total", "Ingress connections by service and outcome")
	mReject = metrics.Counter("smartdns_ingress_rejected_total", "Rejected ingress connections by reason")
	mActive = metrics.Gauge("smartdns_ingress_active_sessions", "Active proxied sessions")
	mBytes  = metrics.Counter("smartdns_ingress_bytes_total", "Proxied bytes by service and direction")
)

type svcRoute struct {
	svc     *model.Service
	matcher *domainset.Matcher
	pool    *pool
}

// Proxy is the ingress TCP:443 SNI dispatcher.
type Proxy struct {
	mu      sync.RWMutex
	routes  []svcRoute
	cfg     *model.NodeConfig
	tlsCfg  *tls.Config
	active  atomic.Int64
	maxSess int64
}

// New builds a proxy. tlsCfg must carry the node's client certificate so the
// egress relay can authenticate this ingress.
func New(c *model.NodeConfig, tlsCfg *tls.Config) *Proxy {
	p := &Proxy{tlsCfg: tlsCfg}
	p.Apply(c)
	return p
}

// Apply swaps in a new revision, preserving local egress health state.
func (p *Proxy) Apply(c *model.NodeConfig) {
	p.mu.Lock()
	prev := map[string]*pool{}
	for _, r := range p.routes {
		prev[r.svc.Slug] = r.pool
	}
	routes := make([]svcRoute, 0, len(c.Services))
	for i := range c.Services {
		s := &c.Services[i]
		routes = append(routes, svcRoute{
			svc:     s,
			matcher: s.Match.Compile(),
			pool:    newPool(s.Egress, prev[s.Slug]),
		})
	}
	p.routes, p.cfg = routes, c
	p.maxSess = int64(c.Ingress.MaxSessions)
	if p.maxSess <= 0 {
		p.maxSess = 10000
	}
	p.mu.Unlock()
	slog.Info("ingress routes applied", "revision", c.RevisionID, "services", len(routes))
}

func (p *Proxy) lookup(host string) *svcRoute {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var best *svcRoute
	bestSpec, bestPrio := -1, -1
	for i := range p.routes {
		spec := p.routes[i].matcher.Specificity(host)
		if spec < 0 {
			continue
		}
		prio := p.routes[i].svc.Priority
		if spec > bestSpec || (spec == bestSpec && prio > bestPrio) {
			best, bestSpec, bestPrio = &p.routes[i], spec, prio
		}
	}
	return best
}

func (p *Proxy) settings() model.IngressConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s := p.cfg.Ingress
	if s.ClientHelloTimeoutMs <= 0 {
		s.ClientHelloTimeoutMs = 3000
	}
	if s.MaxPreReadBytes <= 0 {
		s.MaxPreReadBytes = 16384
	}
	if s.DialTimeoutMs <= 0 {
		s.DialTimeoutMs = 8000
	}
	if s.IdleTimeoutSec <= 0 {
		s.IdleTimeoutSec = 300
	}
	return s
}

// StartProbes keeps local egress health fresh without user traffic.
func (p *Proxy) StartProbes() {
	go func() {
		for {
			p.mu.RLock()
			routes := append([]svcRoute(nil), p.routes...)
			p.mu.RUnlock()
			interval := 30 * time.Second
			for _, r := range routes {
				if r.pool.policy.ProbeInterval > 0 {
					interval = time.Duration(r.pool.policy.ProbeInterval) * time.Second
				}
				r.pool.probe(p.tlsCfg, 5*time.Second)
			}
			time.Sleep(interval)
		}
	}()
}

// Serve accepts connections until l is closed.
func (p *Proxy) Serve(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go p.handle(c)
	}
}

func (p *Proxy) handle(c net.Conn) {
	defer c.Close()
	if n := p.active.Add(1); n > p.maxSess {
		p.active.Add(-1)
		mReject.Inc("reason", "max_sessions")
		return
	}
	defer func() { mActive.Set(p.active.Add(-1)) }()
	mActive.Set(p.active.Load())

	st := p.settings()
	_ = c.SetReadDeadline(time.Now().Add(time.Duration(st.ClientHelloTimeoutMs) * time.Millisecond))
	sni, raw, err := sniff.PeekSNI(c, st.MaxPreReadBytes)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		mReject.Inc("reason", reasonOf(err))
		return
	}
	host, err := domainset.NormalizeHost(sni)
	if err != nil {
		// IP literals and malformed names are never proxied.
		mReject.Inc("reason", "invalid_sni")
		return
	}
	route := p.lookup(host)
	if route == nil {
		// This is the guard that stops the ingress being an open TCP proxy.
		mReject.Inc("reason", "sni_not_managed")
		// The server name appears only at debug level: it is metadata about
		// what a user is browsing, so it must not reach steady-state logs.
		slog.Debug("rejected unmanaged SNI", "sni", host)
		return
	}
	port := 443
	if la, ok := c.LocalAddr().(*net.TCPAddr); ok && la.Port != 0 {
		port = la.Port
	}
	if !portAllowed(route.svc.AllowedPorts, port) {
		mReject.Inc("reason", "port_not_allowed")
		return
	}

	up, tgt, err := route.pool.dial(p.tlsCfg, host, port, time.Duration(st.DialTimeoutMs)*time.Millisecond)
	if err != nil {
		mConn.Inc("service", route.svc.Slug, "result", "no_egress")
		slog.Warn("no usable egress", "service", route.svc.Slug, "err", err)
		return
	}
	// Replay the ClientHello bytes verbatim: TLS is never terminated here.
	if _, err := up.Write(raw); err != nil {
		_ = up.Close()
		mConn.Inc("service", route.svc.Slug, "result", "write_failed")
		return
	}
	mConn.Inc("service", route.svc.Slug, "result", "ok")
	slog.Debug("proxying", "service", route.svc.Slug, "sni", host, "port", port, "egress", tgt.Name)
	a2b, b2a := tunnel.Splice(c, up, time.Duration(st.IdleTimeoutSec)*time.Second)
	mBytes.Add(a2b+int64(len(raw)), "service", route.svc.Slug, "direction", "up")
	mBytes.Add(b2a, "service", route.svc.Slug, "direction", "down")
	_ = tgt
}

func reasonOf(err error) string {
	switch {
	case errors.Is(err, sniff.ErrNotTLS):
		return "not_tls"
	case errors.Is(err, sniff.ErrNoSNI):
		return "no_sni"
	case errors.Is(err, sniff.ErrTooLarge):
		return "too_large"
	case errors.Is(err, io.EOF), errors.Is(err, sniff.ErrIncomplete):
		return "incomplete"
	default:
		return "read_error"
	}
}

func portAllowed(allowed []int, port int) bool {
	if len(allowed) == 0 {
		return port == 443
	}
	for _, p := range allowed {
		if p == port {
			return true
		}
	}
	return false
}

// Status renders a short human-readable health line per service.
func (p *Proxy) Status() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var b strings.Builder
	for _, r := range p.routes {
		fmt.Fprintf(&b, "%s: rules=%d", r.svc.Slug, r.matcher.Size())
		for _, t := range r.pool.targets {
			h, l := t.snapshot()
			fmt.Fprintf(&b, " [%s healthy=%v %.1fms]", t.Name, h, l)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
