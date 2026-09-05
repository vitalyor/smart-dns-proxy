package subpage

import (
	"sync"
	"time"
)

// limiter is a per-IP token bucket. The page is addressed by a secret in the
// URL, so the point is not to stop a determined attacker — 96 bits already does
// that — but to make bulk guessing pointless and keep one client from hammering
// the panel through us.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	limit   int
	window  time.Duration
	swept   time.Time
}

type bucket struct {
	tokens float64
	ts     time.Time
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{buckets: map[string]*bucket{}, limit: limit, window: window, swept: time.Now()}
}

func (l *limiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	// Sweep occasionally so an idle process does not grow a bucket per visitor.
	if now.Sub(l.swept) > 10*l.window {
		for k, b := range l.buckets {
			if now.Sub(b.ts) > 10*l.window {
				delete(l.buckets, k)
			}
		}
		l.swept = now
	}

	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: float64(l.limit), ts: now}
		l.buckets[key] = b
	}
	rate := float64(l.limit) / l.window.Seconds()
	b.tokens += now.Sub(b.ts).Seconds() * rate
	if b.tokens > float64(l.limit) {
		b.tokens = float64(l.limit)
	}
	b.ts = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
