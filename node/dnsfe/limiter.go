package dnsfe

import (
	"net/netip"
	"sync"
	"time"
)

// limiter is a per-client-prefix token bucket. IPv4 is bucketed by /24 and
// IPv6 by /64 so a single client cannot trivially rotate addresses, and no
// raw client IP is retained beyond the bucket key.
type limiter struct {
	mu     sync.Mutex
	qps    int
	burst  int
	tokens map[netip.Prefix]*bucket
	last   time.Time
}

type bucket struct {
	tokens float64
	ts     time.Time
}

func newLimiter(qps, burst int) *limiter {
	l := &limiter{tokens: map[netip.Prefix]*bucket{}, last: time.Now()}
	l.configure(qps, burst)
	return l
}

func (l *limiter) configure(qps, burst int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if qps <= 0 {
		qps = 100
	}
	if burst <= 0 {
		burst = qps * 5
	}
	l.qps, l.burst = qps, burst
}

func (l *limiter) key(ip netip.Addr) netip.Prefix {
	bits := 24
	if ip.Is6() {
		bits = 64
	}
	p, err := ip.Prefix(bits)
	if err != nil {
		return netip.PrefixFrom(ip, ip.BitLen())
	}
	return p
}

func (l *limiter) allow(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	now := time.Now()
	k := l.key(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.last) > time.Minute {
		l.gc(now)
		l.last = now
	}
	b, ok := l.tokens[k]
	if !ok {
		b = &bucket{tokens: float64(l.burst), ts: now}
		l.tokens[k] = b
	}
	b.tokens += now.Sub(b.ts).Seconds() * float64(l.qps)
	if b.tokens > float64(l.burst) {
		b.tokens = float64(l.burst)
	}
	b.ts = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *limiter) gc(now time.Time) {
	for k, b := range l.tokens {
		if now.Sub(b.ts) > 5*time.Minute {
			delete(l.tokens, k)
		}
	}
}
