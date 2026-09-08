// Package proxy implements the ingress SNI proxy: it sniffs the TLS
// ClientHello, checks the domain against the compiled rule-set revision and
// forwards the untouched byte stream to an egress relay.
package proxy

import (
	"crypto/tls"
	"errors"
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

// order returns candidate targets, healthy first, in priority order.
//
// Порядок ровно один, режимов раздачи больше нет. Раздача по весам выбирала
// ноду заново на каждое соединение, поэтому браузер, открывающий к сайту
// десяток соединений сразу, уходил через несколько стран одновременно — под
// одним аккаунтом это выглядит как угон. Список нод сервиса собран из одной
// страны, и порядок в нём — это порядок отказа.
func (p *pool) order() []*target {
	var healthy, unhealthy []*target
	for _, t := range p.targets {
		if h, _ := t.snapshot(); h {
			healthy = append(healthy, t)
		} else {
			unhealthy = append(unhealthy, t)
		}
	}
	// Unhealthy members stay as a last resort: a stale local health verdict
	// must not make the service unreachable.
	return append(healthy, unhealthy...)
}

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
	// Пустой список целей — это не «всё получилось». Без этой ветки dial
	// возвращал nil-соединение и nil-ошибку, и вызывающий писал в пустоту.
	if lastErr == nil {
		lastErr = errors.New("у сервиса нет ни одной ноды выхода")
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
