package dnsfe

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// counters tallies queries per device token. Deliberately only "how many" and
// "when": no domain ever enters this structure. The query log is an in-memory
// ring that is never persisted, and this must not quietly become the durable
// per-device browsing history the product exists to avoid (ADR 0012).
//
// The values live in memory and reset when the process restarts. The panel
// therefore treats them as a monotonic source and keeps the cumulative total
// itself, spotting a restart by the counter going down.
type counters struct {
	mu sync.Mutex
	m  map[string]*DeviceCount
}

// DeviceCount is one device's tally.
type DeviceCount struct {
	Queries  int64 `json:"queries"`
	LastSeen int64 `json:"last_seen"` // unix milliseconds
}

func newCounters() *counters { return &counters{m: map[string]*DeviceCount{}} }

// hit records one *served* query for a token. An empty token (plain DNS, DoT, or
// DoH without a personal path) is not attributed to anybody, and neither are
// refused queries: counting those let a stranger who guessed a token burn its
// owner's quota.
func (c *counters) hit(token string, ts time.Time) {
	if token == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	d := c.m[token]
	if d == nil {
		d = &DeviceCount{}
		c.m[token] = d
	}
	d.Queries++
	d.LastSeen = ts.UnixMilli()
}

// retain drops tallies for tokens that are no longer valid. Without it the map
// grows with every token the panel revokes and never shrinks until a restart.
func (c *counters) retain(tokens []string) {
	keep := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		keep[strings.ToLower(strings.TrimSpace(t))] = true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if !keep[k] {
			delete(c.m, k)
		}
	}
}

func (c *counters) snapshot() map[string]DeviceCount {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]DeviceCount, len(c.m))
	for k, v := range c.m {
		out[k] = *v
	}
	return out
}

// CountersHandler serves the per-token tallies to the node-agent.
func (s *Server) CountersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": s.counts.snapshot()})
	}
}
