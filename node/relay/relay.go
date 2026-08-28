// Package relay implements the egress side of the tunnel. It is deliberately
// not a general proxy: every destination must be present in the compiled
// allowlist of the active revision and must resolve to a public address.
package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"smartdns/shared/domainset"
	"smartdns/shared/metrics"
	"smartdns/shared/model"
	"smartdns/shared/netguard"
	"smartdns/shared/tunnel"
)

var (
	mReq    = metrics.Counter("smartdns_egress_requests_total", "Egress CONNECT requests by result")
	mBytes  = metrics.Counter("smartdns_egress_bytes_total", "Relayed bytes by direction")
	mActive = metrics.Gauge("smartdns_egress_active_sessions", "Active relayed sessions")
	mDial   = metrics.Histogram("smartdns_egress_dial_duration_seconds", "Origin dial latency", metrics.DefBuckets)
)

// Relay serves authenticated ingress peers.
type Relay struct {
	mu       sync.RWMutex
	allow    *domainset.Matcher
	ports    map[int]bool
	resolver *net.Resolver
	cfg      model.EgressConfig
	active   atomic.Int64
	maxSess  int64
}

// New builds a relay from a node config.
func New(c *model.NodeConfig) *Relay {
	r := &Relay{}
	r.Apply(c)
	return r
}

// Apply swaps in a new revision atomically.
func (r *Relay) Apply(c *model.NodeConfig) {
	// The egress allowlist is the union of every service's rule-set in the
	// same revision that drives DNS and ingress routing: one source of truth.
	ports := map[int]bool{}
	for _, p := range c.Egress.AllowedPorts {
		ports[p] = true
	}
	if len(ports) == 0 {
		ports[443] = true
	}
	res := net.DefaultResolver
	if c.Egress.Resolver != "" {
		addr := c.Egress.Resolver
		res = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, addr)
		}}
	}
	r.mu.Lock()
	r.allow = c.Egress.Allow.Compile()
	r.ports = ports
	r.resolver = res
	r.cfg = c.Egress
	r.maxSess = int64(c.Egress.MaxSessions)
	if r.maxSess <= 0 {
		r.maxSess = 10000
	}
	r.mu.Unlock()
	slog.Info("egress allowlist applied", "revision", c.RevisionID, "rules", r.allow.Size(), "ports", c.Egress.AllowedPorts)
	if c.Egress.AllowPrivateDestinations {
		slog.Warn("allow_private_destinations is enabled: the SSRF/open-proxy IP guard is OFF (lab mode)")
	}
}

// Allowed reports whether host:port may be dialled. Exported for tests.
func (r *Relay) Allowed(host string, port int) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ports[port] && r.allow.Match(host)
}

// Serve accepts mutually authenticated tunnel connections.
func (r *Relay) Serve(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go r.handle(c)
	}
}

func (r *Relay) handle(c net.Conn) {
	defer c.Close()
	if n := r.active.Add(1); n > r.maxSess {
		r.active.Add(-1)
		mReq.Inc("result", "max_sessions")
		return
	}
	defer func() { mActive.Set(r.active.Add(-1)) }()
	mActive.Set(r.active.Load())

	tc, ok := c.(*tls.Conn)
	if !ok {
		mReq.Inc("result", "not_tls")
		return
	}
	if err := tc.HandshakeContext(context.Background()); err != nil {
		mReq.Inc("result", "handshake_failed")
		return
	}
	st := tc.ConnectionState()
	if len(st.PeerCertificates) == 0 {
		mReq.Inc("result", "no_client_cert")
		return
	}
	peer := st.PeerCertificates[0].Subject.CommonName

	host, port, err := tunnel.ReadConnect(c)
	if err != nil {
		mReq.Inc("result", "bad_frame")
		return
	}
	host, err = domainset.NormalizeHost(host)
	if err != nil {
		_ = tunnel.WriteStatus(c, tunnel.StatusDenied)
		mReq.Inc("result", "bad_host")
		return
	}
	if !r.Allowed(host, port) {
		_ = tunnel.WriteStatus(c, tunnel.StatusDenied)
		mReq.Inc("result", "denied")
		slog.Warn("destination refused by allowlist", "peer", peer, "port", port)
		return
	}

	r.mu.RLock()
	res, cfg := r.resolver, r.cfg
	r.mu.RUnlock()
	dialTO := time.Duration(cfg.DialTimeoutMs) * time.Millisecond
	if dialTO <= 0 {
		dialTO = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), dialTO)
	defer cancel()

	// Resolution happens here, never on the ingress, and every resolved
	// address is re-checked to defeat DNS rebinding.
	addrs, err := netguard.Resolve(ctx, res, host, cfg.AllowPrivateDestinations)
	if err != nil {
		_ = tunnel.WriteStatus(c, tunnel.StatusDNS)
		mReq.Inc("result", "resolve_blocked")
		slog.Warn("resolution refused", "err", err)
		return
	}
	start := time.Now()
	var up net.Conn
	d := netguard.SafeDialerPolicy(cfg.AllowPrivateDestinations)
	d.Timeout = dialTO
	for _, a := range addrs {
		up, err = d.DialContext(ctx, "tcp", net.JoinHostPort(a.String(), strconv.Itoa(port)))
		if err == nil {
			break
		}
	}
	if up == nil {
		_ = tunnel.WriteStatus(c, tunnel.StatusDial)
		mReq.Inc("result", "dial_failed")
		return
	}
	mDial.Observe(time.Since(start).Seconds())
	if err := tunnel.WriteStatus(c, tunnel.StatusOK); err != nil {
		_ = up.Close()
		return
	}
	mReq.Inc("result", "ok")
	idle := time.Duration(cfg.IdleTimeoutSec) * time.Second
	if idle <= 0 {
		idle = 300 * time.Second
	}
	// The relay copies encrypted bytes only; no payload is ever inspected.
	a2b, b2a := tunnel.Splice(c, up, idle)
	mBytes.Add(a2b, "direction", "out")
	mBytes.Add(b2a, "direction", "in")
}
