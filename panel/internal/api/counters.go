package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"smartdns/panel/internal/pusher"
	"smartdns/panel/internal/store"
)

// PollCounters keeps per-device usage fresh. It runs on its own, slower ticker
// than the health poll: usage does not need ten-second resolution, and every
// tick costs one request per ingress node.
func (s *Server) PollCounters(ctx context.Context, interval time.Duration) {
	if s.Cfg.Pusher == nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.collectCounters(ctx)
			s.resetElapsedPeriods(ctx)
		}
	}
}

type counterResp struct {
	Available *bool                  `json:"available"`
	Devices   map[string]deviceTally `json:"devices"`
}

type deviceTally struct {
	Queries  int64 `json:"queries"`
	LastSeen int64 `json:"last_seen"`
}

func (s *Server) collectCounters(ctx context.Context) {
	nodes, err := store.Many[pollRow](ctx, s.DB, `
		SELECT n.id::text, n.name, n.mgmt_address, i.fingerprint, n.desired_revision_id::text, n.role
		FROM nodes n JOIN node_identities i ON i.node_id = n.id
		WHERE n.role='ingress' AND n.mgmt_address <> '' AND i.revoked_at IS NULL AND n.status <> 'disabled'`)
	if err != nil {
		return
	}
	for _, n := range nodes {
		t := pusher.Target{NodeID: n.ID, Name: n.Name, MgmtAddress: n.Mgmt, NodeCertFP: n.FP}
		raw, err := s.Cfg.Pusher.FetchCounters(ctx, t)
		if err != nil {
			continue
		}
		var resp counterResp
		if err := json.Unmarshal(raw, &resp); err != nil {
			continue
		}
		if resp.Available != nil && !*resp.Available {
			continue
		}
		for tokenHash, tally := range resp.Devices {
			s.applyTally(ctx, n.ID, tokenHash, tally)
		}
	}
}

// applyTally folds one node's reading into the cumulative totals. The node's
// counter is monotonic only until the process restarts, so a value that went
// down means "restarted": the whole new value is then the delta. Without this
// the quota would be resettable by bouncing a container.
func (s *Server) applyTally(ctx context.Context, nodeID, tokenHash string, t deviceTally) {
	var deviceID string
	var subID *string
	if err := s.DB.QueryRow(ctx,
		`SELECT id::text, subscriber_id::text FROM device_profiles WHERE token_hash=$1`,
		tokenHash).Scan(&deviceID, &subID); err != nil {
		return // token of a device that no longer exists
	}
	var prev int64
	_ = s.DB.QueryRow(ctx,
		`SELECT last_raw FROM device_counters WHERE device_id=$1 AND node_id=$2`,
		deviceID, nodeID).Scan(&prev)

	delta := t.Queries - prev
	if delta < 0 {
		delta = t.Queries
	}
	if _, err := s.DB.Exec(ctx, `
		INSERT INTO device_counters (device_id, node_id, last_raw, updated_at)
		VALUES ($1,$2,$3,now())
		ON CONFLICT (device_id, node_id)
		DO UPDATE SET last_raw = EXCLUDED.last_raw, updated_at = now()`,
		deviceID, nodeID, t.Queries); err != nil {
		return
	}
	if delta <= 0 && t.LastSeen == 0 {
		return
	}
	var seen *time.Time
	if t.LastSeen > 0 {
		ts := time.UnixMilli(t.LastSeen)
		seen = &ts
	}
	if _, err := s.DB.Exec(ctx, `
		UPDATE device_profiles
		SET queries_total = queries_total + $2,
		    last_seen_at  = GREATEST(COALESCE(last_seen_at, to_timestamp(0)), COALESCE($3, to_timestamp(0))),
		    updated_at    = now()
		WHERE id=$1`, deviceID, delta, seen); err != nil {
		return
	}
	if subID != nil && delta > 0 {
		_, _ = s.DB.Exec(ctx,
			`UPDATE subscribers SET queries_used = queries_used + $2, updated_at = now() WHERE id=$1`,
			*subID, delta)
	}
}

// resetElapsedPeriods rolls the usage window over. Subscribers on "never" keep a
// lifetime cap and are left alone.
func (s *Server) resetElapsedPeriods(ctx context.Context) {
	n, err := s.DB.ExecN(ctx, `
		UPDATE subscribers
		SET queries_used = 0, period_started_at = now(), updated_at = now()
		WHERE (query_period = 'day'   AND period_started_at < now() - interval '1 day')
		   OR (query_period = 'month' AND period_started_at < now() - interval '1 month')`)
	if err == nil && n > 0 {
		slog.Info("usage periods reset", "subscribers", n)
	}
}
