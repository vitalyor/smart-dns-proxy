package dnsfe

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
)

// LogEntry is one resolved query, kept in a small in-memory ring so the panel
// can show live activity. Never persisted; bounded memory; no client IP (DoT/DoH
// arrive via an internal proxy, so it would be meaningless anyway).
type LogEntry struct {
	Seq      uint64 `json:"seq"`
	TS       int64  `json:"ts"` // unix milliseconds
	Client   string `json:"client"`
	Proto    string `json:"proto"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Decision string `json:"decision"` // "managed:<slug>" | "direct" | "denied:<reason>"
	Rcode    string `json:"rcode"`
	MS       int64  `json:"ms"`
}

// ring is a fixed-size FIFO of the most recent queries with a monotonic seq so
// the panel can poll incrementally (?after=<seq>).
type ring struct {
	mu   sync.Mutex
	buf  []LogEntry
	seq  uint64
	size int
}

func newRing(size int) *ring { return &ring{size: size} }

func (r *ring) add(e LogEntry) {
	r.mu.Lock()
	r.seq++
	e.Seq = r.seq
	r.buf = append(r.buf, e)
	if len(r.buf) > r.size {
		r.buf = r.buf[len(r.buf)-r.size:]
	}
	r.mu.Unlock()
}

func (r *ring) snapshot(after uint64) (uint64, []LogEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]LogEntry, 0, 128)
	for _, e := range r.buf {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return r.seq, out
}

// LogHandler serves recent queries as {"seq":N,"entries":[...]}; with ?after=N
// it returns only entries newer than seq N, so the panel streams cheaply.
func (s *Server) LogHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		after, _ := strconv.ParseUint(req.URL.Query().Get("after"), 10, 64)
		seq, entries := s.log.snapshot(after)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"seq": seq, "entries": entries})
	}
}
