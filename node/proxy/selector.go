// Package proxy implements the ingress SNI proxy: it sniffs the TLS
// ClientHello, checks the domain against the compiled rule-set revision and
// forwards the untouched byte stream to an egress relay.
package proxy

import (
	"crypto/tls"
	"math/rand"
	"net"
	"sort"
	"sync"
	"time"

	"smartdns/shared/metrics"
	"smartdns/shared/model"
	"smartdns/shared/tunnel"
)

var (
	mTunnel    = metrics.Counter("smartdns_tunnel_connect_total", "Tunnel connection attempts by egress and result")
	mTunnelLat = metrics.Histogram("smartdns_tunnel_connect_duration_seconds", "Tunnel setup latency", metrics.DefBuckets)
	mFailover  = metrics.Counter("smartdns_egress_failover_total", "Egress health transitions")
	mEgressUp  = metrics.Gauge("smartdns_egress_healthy", "Egress target health (1 healthy, 0 unhealthy)")
)

// target tracks the local health of one egress relay. This state lives on the
// node so failover keeps working while the panel is offline.
type target struct {
	model.EgressTarget
	mu        sync.Mutex
	healthy   bool
	fails     int
	successes int
	latencyMs float64 // EWMA
	lastCheck time.Time
}

func (t *target) snapshot() (bool, float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.healthy, t.latencyMs
}

func (t *target) observe(ok bool, d time.Duration, fail, rise int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastCheck = time.Now()
	if ok {
		t.fails = 0
		t.successes++
		ms := float64(d.Microseconds()) / 1000
		if t.latencyMs == 0 {
			t.latencyMs = ms
		} else {
			t.latencyMs = 0.7*t.latencyMs + 0.3*ms
		}
		if !t.healthy && t.successes >= rise {
			t.healthy = true
			mFailover.Inc("egress", t.Name, "to", "healthy")
		}
	} else {
		t.successes = 0
		t.fails++
		if t.healthy && t.fails >= fail {
			t.healthy = false
			mFailover.Inc("egress", t.Name, "to", "unhealthy")
		}
	}
	if t.healthy {
		mEgressUp.Set(1, "egress", t.Name)
	} else {
		mEgressUp.Set(0, "egress", t.Name)
	}
}

// pool holds the egress targets of one service plus its policy.
type pool struct {
	policy  model.EgressPolicy
	targets []*target
	mu      sync.Mutex
	sticky  string // currently preferred target for lowest_latency hysteresis
}

func newPool(p model.EgressPolicy, prev *pool) *pool {
	po := &pool{policy: p}
	for _, t := range p.Targets {
		nt := &target{EgressTarget: t, healthy: true}
		if prev != nil {
			for _, old := range prev.targets {
				if old.NodeID == t.NodeID {
					h, l := old.snapshot()
					nt.healthy, nt.latencyMs = h, l
				}
			}
		}
		po.targets = append(po.targets, nt)
	}
	sort.SliceStable(po.targets, func(i, j int) bool { return po.targets[i].Priority < po.targets[j].Priority })
	return po
}

func (p *pool) thresholds() (fail, rise int) {
	fail, rise = p.policy.FailThreshold, p.policy.RiseThreshold
	if fail <= 0 {
		fail = 3
	}
	if rise <= 0 {
		rise = 2
	}
	return
}

// order returns candidate targets, healthy first, according to the policy.
func (p *pool) order() []*target {
	var healthy, unhealthy []*target
	for _, t := range p.targets {
		if h, _ := t.snapshot(); h {
			healthy = append(healthy, t)
		} else {
			unhealthy = append(unhealthy, t)
		}
	}
	switch p.policy.Mode {
	case "weighted":
		healthy = weightedShuffle(healthy)
	case "lowest_latency":
		healthy = p.byLatency(healthy)
	case "manual_fixed":
		if len(healthy) > 1 {
			healthy = healthy[:1]
		}
	default: // primary_fallback: already sorted by priority
	}
	// Unhealthy members stay as a last resort: a stale local health verdict
	// must not make the service unreachable.
	return append(healthy, unhealthy...)
}

func (p *pool) byLatency(ts []*target) []*target {
	if len(ts) < 2 {
		return ts
	}
	sorted := append([]*target(nil), ts...)
	sort.SliceStable(sorted, func(i, j int) bool {
		_, li := sorted[i].snapshot()
		_, lj := sorted[j].snapshot()
		if li == 0 {
			return false
		}
		if lj == 0 {
			return true
		}
		return li < lj
	})
	pct := p.policy.HysteresisPct
	if pct <= 0 {
		pct = 20
	}
	ms := float64(p.policy.HysteresisMs)
	if ms <= 0 {
		ms = 20
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sticky == "" {
		p.sticky = sorted[0].NodeID
		return sorted
	}
	var cur *target
	for _, t := range ts {
		if t.NodeID == p.sticky {
			cur = t
		}
	}
	if cur == nil {
		p.sticky = sorted[0].NodeID
		return sorted
	}
	_, curL := cur.snapshot()
	_, bestL := sorted[0].snapshot()
	// Only switch when the candidate is faster by both margins.
	if curL > 0 && bestL > 0 && bestL < curL*(1-float64(pct)/100) && curL-bestL > ms {
		p.sticky = sorted[0].NodeID
		return sorted
	}
	out := []*target{cur}
	for _, t := range sorted {
		if t.NodeID != cur.NodeID {
			out = append(out, t)
		}
	}
	return out
}

func weightedShuffle(ts []*target) []*target {
	rest := append([]*target(nil), ts...)
	out := make([]*target, 0, len(rest))
	for len(rest) > 0 {
		total := 0
		for _, t := range rest {
			w := t.Weight
			if w <= 0 {
				w = 1
			}
			total += w
		}
		n := rand.Intn(total)
		idx := 0
		for i, t := range rest {
			w := t.Weight
			if w <= 0 {
				w = 1
			}
			if n < w {
				idx = i
				break
			}
			n -= w
		}
		out = append(out, rest[idx])
		rest = append(rest[:idx], rest[idx+1:]...)
	}
	return out
}

// probe performs a cheap TLS handshake against a target to keep local health
// fresh even when there is no user traffic.
func (p *pool) probe(tlsCfg *tls.Config, timeout time.Duration) {
	fail, rise := p.thresholds()
	for _, t := range p.targets {
		start := time.Now()
		d := &net.Dialer{Timeout: timeout}
		conn, err := d.Dial("tcp", t.Endpoint)
		if err == nil {
			c := tlsCfg.Clone()
			c.ServerName = t.SNI
			tc := tls.Client(conn, c)
			_ = tc.SetDeadline(time.Now().Add(timeout))
			err = tc.Handshake()
			_ = tc.Close()
		}
		t.observe(err == nil, time.Since(start), fail, rise)
		if err == nil {
			mTunnel.Inc("egress", t.Name, "result", "probe_ok")
		} else {
			mTunnel.Inc("egress", t.Name, "result", "probe_fail")
		}
	}
}

// dial establishes a tunnel to the first usable target for host:port.
func (p *pool) dial(tlsCfg *tls.Config, host string, port int, timeout time.Duration) (net.Conn, *target, error) {
	fail, rise := p.thresholds()
	var lastErr error
	for _, t := range p.order() {
		start := time.Now()
		d := &net.Dialer{Timeout: timeout}
		raw, err := d.Dial("tcp", t.Endpoint)
		if err == nil {
			c := tlsCfg.Clone()
			c.ServerName = t.SNI
			tc := tls.Client(raw, c)
			_ = tc.SetDeadline(time.Now().Add(timeout))
			if err = tc.Handshake(); err == nil {
				err = tunnel.WriteConnect(tc, host, port)
			}
			_ = tc.SetDeadline(time.Time{})
			if err == nil {
				t.observe(true, time.Since(start), fail, rise)
				mTunnel.Inc("egress", t.Name, "result", "ok")
				mTunnelLat.Observe(time.Since(start).Seconds(), "egress", t.Name)
				return tc, t, nil
			}
			_ = tc.Close()
		}
		lastErr = err
		// A destination refused by policy is not an egress health problem.
		if err != nil && isPolicyRefusal(err) {
			mTunnel.Inc("egress", t.Name, "result", "denied")
			return nil, t, err
		}
		t.observe(false, time.Since(start), fail, rise)
		mTunnel.Inc("egress", t.Name, "result", "fail")
	}
	return nil, nil, lastErr
}

func isPolicyRefusal(err error) bool {
	return err != nil && (contains(err.Error(), "destination not allowed") || contains(err.Error(), "resolution failed"))
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
