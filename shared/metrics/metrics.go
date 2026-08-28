// Package metrics is a dependency-free Prometheus text-format registry.
// Only low-cardinality labels are accepted by construction: callers pass a
// fixed label set per metric, and raw qnames/client IPs are never used.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

type kind int

const (
	counter kind = iota
	gauge
	histogram
)

type series struct {
	labels string
	val    atomic.Int64 // counters/gauges (gauges use millis for floats)
	sum    atomic.Int64 // histogram sum in microseconds
	count  atomic.Int64
	bucket []atomic.Int64
}

type metric struct {
	name    string
	help    string
	k       kind
	bounds  []float64
	mu      sync.RWMutex
	byLabel map[string]*series
}

var (
	mu    sync.RWMutex
	reg   = map[string]*metric{}
	order []string
)

func register(name, help string, k kind, bounds []float64) *metric {
	mu.Lock()
	defer mu.Unlock()
	if m, ok := reg[name]; ok {
		return m
	}
	m := &metric{name: name, help: help, k: k, bounds: bounds, byLabel: map[string]*series{}}
	reg[name] = m
	order = append(order, name)
	return m
}

// Counter registers (or reuses) a counter.
func Counter(name, help string) *metric { return register(name, help, counter, nil) }

// Gauge registers (or reuses) a gauge.
func Gauge(name, help string) *metric { return register(name, help, gauge, nil) }

// Histogram registers a histogram with explicit second bounds.
func Histogram(name, help string, bounds []float64) *metric {
	return register(name, help, histogram, bounds)
}

func (m *metric) get(lbl ...string) *series {
	key := joinLabels(lbl)
	m.mu.RLock()
	s, ok := m.byLabel[key]
	m.mu.RUnlock()
	if ok {
		return s
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.byLabel[key]; ok {
		return s
	}
	s = &series{labels: key}
	if m.k == histogram {
		s.bucket = make([]atomic.Int64, len(m.bounds))
	}
	m.byLabel[key] = s
	return s
}

// Inc adds 1 to the series identified by alternating label key/value pairs.
func (m *metric) Inc(lbl ...string) { m.get(lbl...).val.Add(1) }

// Add adds n.
func (m *metric) Add(n int64, lbl ...string) { m.get(lbl...).val.Add(n) }

// Set replaces a gauge value.
func (m *metric) Set(n int64, lbl ...string) { m.get(lbl...).val.Store(n) }

// Observe records a duration in seconds.
func (m *metric) Observe(seconds float64, lbl ...string) {
	s := m.get(lbl...)
	s.count.Add(1)
	s.sum.Add(int64(seconds * 1e6))
	for i, b := range m.bounds {
		if seconds <= b {
			s.bucket[i].Add(1)
		}
	}
}

func joinLabels(lbl []string) string {
	if len(lbl) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(lbl)/2)
	for i := 0; i+1 < len(lbl); i += 2 {
		pairs = append(pairs, lbl[i]+`="`+escape(lbl[i+1])+`"`)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

func withLabel(base, extra string) string {
	if base == "" {
		return "{" + extra + "}"
	}
	return "{" + base + "," + extra + "}"
}

// Render writes the whole registry in Prometheus text format.
func Render(w *strings.Builder) {
	mu.RLock()
	names := append([]string(nil), order...)
	mu.RUnlock()
	sort.Strings(names)
	for _, n := range names {
		mu.RLock()
		m := reg[n]
		mu.RUnlock()
		typ := map[kind]string{counter: "counter", gauge: "gauge", histogram: "histogram"}[m.k]
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", m.name, m.help, m.name, typ)
		m.mu.RLock()
		keys := make([]string, 0, len(m.byLabel))
		for k := range m.byLabel {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := m.byLabel[k]
			lb := ""
			if k != "" {
				lb = "{" + k + "}"
			}
			switch m.k {
			case histogram:
				var cum int64
				for i, b := range m.bounds {
					cum += s.bucket[i].Load()
					fmt.Fprintf(w, "%s_bucket%s %d\n", m.name, withLabel(k, `le="`+strconv.FormatFloat(b, 'g', -1, 64)+`"`), cum)
				}
				fmt.Fprintf(w, "%s_bucket%s %d\n", m.name, withLabel(k, `le="+Inf"`), s.count.Load())
				fmt.Fprintf(w, "%s_sum%s %f\n", m.name, lb, float64(s.sum.Load())/1e6)
				fmt.Fprintf(w, "%s_count%s %d\n", m.name, lb, s.count.Load())
			default:
				fmt.Fprintf(w, "%s%s %d\n", m.name, lb, s.val.Load())
			}
		}
		m.mu.RUnlock()
	}
}

// Handler serves /metrics.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		Render(&b)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	})
}

// DefBuckets are latency buckets in seconds suitable for DNS and proxying.
var DefBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}
