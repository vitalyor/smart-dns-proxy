package dnsfe

import (
	"encoding/json"
	"net/http"
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

// hit records one query for a token. An empty token (plain DNS, DoT, or DoH
// without a personal path) is not attributed to anybody.
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
