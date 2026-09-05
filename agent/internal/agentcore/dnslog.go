package agentcore

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"
)

var dnsLogClient = &http.Client{Timeout: 4 * time.Second}

// handleDNSLog proxies the dns-frontend's live query-log ring to the panel.
// It carries the ?after=<seq> cursor through so the panel streams incrementally.
// A node without a dns-frontend (egress) reports availability:false rather than
// erroring, so the panel can say "this node only relays".
func (a *Agent) handleDNSLog(w http.ResponseWriter, r *http.Request) error {
	if a.cfg.DNSLogURL == "" {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "seq": 0, "entries": []any{}})
		return nil
	}
	u := a.cfg.DNSLogURL
	if after := r.URL.Query().Get("after"); after != "" {
		u += "?after=" + url.QueryEscape(after)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := dnsLogClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	return nil
}

// handleDNSCounters proxies the per-device tallies. Same shape as the log proxy:
// an egress node has no dns-frontend and reports unavailability rather than
// erroring, so the panel simply skips it.
func (a *Agent) handleDNSCounters(w http.ResponseWriter, r *http.Request) error {
	if a.cfg.DNSCountersURL == "" {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "devices": map[string]any{}})
		return nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.DNSCountersURL, nil)
	if err != nil {
		return err
	}
	resp, err := dnsLogClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	return nil
}
