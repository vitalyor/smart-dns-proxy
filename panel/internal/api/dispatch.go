package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"smartdns/panel/internal/pusher"
	"smartdns/panel/internal/store"
	"smartdns/shared/metrics"
)

// mApplyFailures backs the restart-loop alert in docs/alerts.yml.
var mApplyFailures = metrics.Counter("smartdns_agent_apply_failures_total",
	"Failed config pushes recorded by the panel")

// targetFor builds a pusher.Target from a node row: its management address plus
// the fingerprint of the server cert we minted, so a hijacked address is caught.
func (s *Server) targetFor(ctx contextT, nodeID string) (pusher.Target, error) {
	var t pusher.Target
	row, err := store.One[struct {
		Name string `db:"name"`
		Mgmt string `db:"mgmt_address"`
		FP   string `db:"fingerprint"`
	}](ctx, s.DB, `
		SELECT n.name, n.mgmt_address, i.fingerprint
		FROM nodes n JOIN node_identities i ON i.node_id = n.id
		WHERE n.id=$1 AND i.revoked_at IS NULL ORDER BY i.created_at DESC LIMIT 1`, nodeID)
	if err != nil {
		return t, err
	}
	if row.Mgmt == "" {
		return t, fmt.Errorf("нода %s не имеет адреса управления", row.Name)
	}
	return pusher.Target{NodeID: nodeID, Name: row.Name, MgmtAddress: row.Mgmt, NodeCertFP: row.FP}, nil
}

// pushRevision delivers a revision's per-node artifacts to every target node.
// It runs in the background: a node being unreachable never blocks a deploy and
// is reconciled by the next poll.
func (s *Server) pushRevision(revisionID string) {
	if s.Cfg.Pusher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	arts, err := store.Many[struct {
		NodeID  string `db:"node_id"`
		Content []byte `db:"content"`
	}](ctx, s.DB, `SELECT node_id::text, content FROM revision_artifacts WHERE revision_id=$1`, revisionID)
	if err != nil {
		return
	}
	for _, a := range arts {
		s.pushOne(ctx, revisionID, a.NodeID, a.Content)
	}
}

func (s *Server) pushOne(ctx contextT, revisionID, nodeID string, artifact []byte) {
	t, err := s.targetFor(ctx, nodeID)
	if err != nil {
		s.recordPush(ctx, nodeID, revisionID, "failed", "no_target", err.Error())
		return
	}
	res, err := s.Cfg.Pusher.PushConfig(ctx, t, artifact)
	if err != nil {
		// Unreachable is expected and transient; the poll loop retries.
		s.recordPush(ctx, nodeID, revisionID, "failed", "unreachable", err.Error())
		return
	}
	s.recordPush(ctx, nodeID, revisionID, "applied", "", "")
	_, _ = s.DB.Exec(ctx, `UPDATE nodes SET applied_revision_id=$2, applied_sequence=$3,
		last_error='', updated_at=now() WHERE id=$1`, nodeID, res.AppliedRevisionID, res.AppliedSequence)
	s.reconcileRevisionState(ctx, revisionID)
}

func (s *Server) recordPush(ctx contextT, nodeID, revisionID, state, code, detail string) {
	_, _ = s.DB.Exec(ctx, `
		INSERT INTO node_deployments (node_id, revision_id, state, error_code, error_detail, finished_at)
		VALUES ($1,$2,$3,$4,$5, CASE WHEN $3 IN ('applied','failed') THEN now() END)
		ON CONFLICT (node_id, revision_id) DO UPDATE SET state=EXCLUDED.state,
			error_code=EXCLUDED.error_code, error_detail=EXCLUDED.error_detail, finished_at=EXCLUDED.finished_at`,
		nodeID, revisionID, state, code, truncate(detail, 1000))
	if state == "failed" {
		mApplyFailures.Inc("code", code)
	}
}

// PollNodes runs the health-poll loop until ctx is cancelled. Every interval it
// asks each node with a desired revision for its health, records the DNS
// signals, and re-pushes to any node that has drifted behind.
func (s *Server) PollNodes(ctx context.Context, interval time.Duration) {
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
			s.pollOnce(ctx)
		}
	}
}

func (s *Server) pollOnce(ctx context.Context) {
	nodes, err := store.Many[struct {
		ID      string  `db:"id"`
		Name    string  `db:"name"`
		Mgmt    string  `db:"mgmt_address"`
		FP      string  `db:"fingerprint"`
		Desired *string `db:"desired_revision_id"`
	}](ctx, s.DB, `
		SELECT n.id::text, n.name, n.mgmt_address, i.fingerprint, n.desired_revision_id::text
		FROM nodes n JOIN node_identities i ON i.node_id = n.id
		WHERE n.mgmt_address <> '' AND i.revoked_at IS NULL AND n.status <> 'disabled'`)
	if err != nil {
		return
	}
	for _, n := range nodes {
		t := pusher.Target{NodeID: n.ID, Name: n.Name, MgmtAddress: n.Mgmt, NodeCertFP: n.FP}
		h, err := s.Cfg.Pusher.Poll(ctx, t)
		if err != nil {
			_, _ = s.DB.Exec(ctx, `UPDATE nodes SET status='unhealthy',
				last_error=$2, updated_at=now() WHERE id=$1 AND status NOT IN ('maintenance','disabled')`,
				n.ID, truncate(err.Error(), 300))
			continue
		}
		status := h.Status
		if status != "healthy" && status != "degraded" {
			status = "unknown"
		}
		hj, _ := json.Marshal(h)
		_, _ = s.DB.Exec(ctx, `UPDATE nodes SET
			status = CASE WHEN status IN ('maintenance','disabled') THEN status ELSE $2 END,
			applied_revision_id = COALESCE(NULLIF($3,'')::uuid, applied_revision_id),
			applied_sequence = $4, health = $5, last_seen_at = now(),
			last_error = $6, updated_at = now()
			WHERE id=$1`, n.ID, status, h.AppliedRevisionID, h.AppliedSequence, hj, h.LastErr)
		_, _ = s.DB.Exec(ctx, `INSERT INTO health_samples (node_id, kind, success, latency_ms)
			VALUES ($1,'poll',$2,0)`, n.ID, status == "healthy")

		// Drift: the node is behind the revision it should run — re-push.
		if n.Desired != nil && *n.Desired != "" && h.AppliedRevisionID != *n.Desired {
			var content []byte
			if err := s.DB.QueryRow(ctx,
				`SELECT content FROM revision_artifacts WHERE revision_id=$1 AND node_id=$2`,
				*n.Desired, n.ID).Scan(&content); err == nil {
				s.pushOne(ctx, *n.Desired, n.ID, content)
			}
		}
	}
}
