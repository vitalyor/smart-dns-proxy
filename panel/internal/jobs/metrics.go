package jobs

import (
	"context"
	"time"

	"smartdns/panel/internal/store"
	"smartdns/shared/metrics"
)

// These gauges are what docs/alerts.yml alerts on. They are published from the
// database so an alert never depends on a node being reachable to fire.
var (
	gDrift     = metrics.Gauge("smartdns_panel_nodes_with_drift", "Nodes whose applied revision differs from the desired one")
	gHeartbeat = metrics.Gauge("smartdns_agent_last_heartbeat_seconds", "Unix time of the last heartbeat, by node")
	gCertLeft  = metrics.Gauge("smartdns_node_certificate_expiry_seconds", "Seconds until the node certificate expires")
	gFetch     = metrics.Gauge("smartdns_rule_set_last_fetch_seconds", "Unix time of the last rule-set fetch")
	gInterval  = metrics.Gauge("smartdns_rule_set_interval_seconds", "Configured fetch interval of a rule set")
	gNodes     = metrics.Gauge("smartdns_panel_nodes", "Registered nodes by role and status")
	gServices  = metrics.Gauge("smartdns_panel_services_enabled", "Enabled services")
	gRules     = metrics.Gauge("smartdns_panel_rules_active", "Active rule entries by rule set")
	gPending   = metrics.Gauge("smartdns_panel_rule_versions_awaiting_approval", "Rule-set versions waiting for approval")
)

// PublishMetrics refreshes the panel gauges. Started by Runner.Start.
func (r *Runner) PublishMetrics(ctx context.Context) error {
	drift, err := store.Value[int](ctx, r.DB, `
		SELECT count(*)::int FROM nodes
		WHERE desired_revision_id IS NOT NULL AND desired_revision_id IS DISTINCT FROM applied_revision_id`)
	if err != nil {
		return err
	}
	gDrift.Set(int64(drift))

	type nodeRow struct {
		Name     string     `db:"name"`
		Role     string     `db:"role"`
		Status   string     `db:"status"`
		LastSeen *time.Time `db:"last_seen_at"`
		NotAfter *time.Time `db:"not_after"`
	}
	nodes, err := store.Many[nodeRow](ctx, r.DB, `
		SELECT n.name, n.role, n.status, n.last_seen_at,
		       (SELECT max(i.not_after) FROM node_identities i
		         WHERE i.node_id = n.id AND i.revoked_at IS NULL) AS not_after
		FROM nodes n`)
	if err != nil {
		return err
	}
	counts := map[[2]string]int64{}
	now := time.Now()
	for _, n := range nodes {
		counts[[2]string{n.Role, n.Status}]++
		if n.LastSeen != nil {
			gHeartbeat.Set(n.LastSeen.Unix(), "node", n.Name)
		}
		if n.NotAfter != nil {
			gCertLeft.Set(int64(n.NotAfter.Sub(now).Seconds()), "node", n.Name)
		}
	}
	for k, v := range counts {
		gNodes.Set(v, "role", k[0], "status", k[1])
	}

	type rsRow struct {
		Name     string     `db:"name"`
		Interval int        `db:"interval_sec"`
		Fetched  *time.Time `db:"last_fetch_at"`
		Entries  int        `db:"entries"`
	}
	sets, err := store.Many[rsRow](ctx, r.DB, `
		SELECT rs.name, rs.interval_sec, rs.last_fetch_at,
		       (SELECT count(*)::int FROM rule_entries e WHERE e.version_id = rs.active_version_id) AS entries
		FROM rule_sets rs`)
	if err != nil {
		return err
	}
	for _, s := range sets {
		gInterval.Set(int64(s.Interval), "rule_set", s.Name)
		gRules.Set(int64(s.Entries), "rule_set", s.Name)
		if s.Fetched != nil {
			gFetch.Set(s.Fetched.Unix(), "rule_set", s.Name)
		}
	}

	if n, err := store.Value[int](ctx, r.DB, `SELECT count(*)::int FROM services WHERE enabled`); err == nil {
		gServices.Set(int64(n))
	}
	if n, err := store.Value[int](ctx, r.DB,
		`SELECT count(*)::int FROM rule_set_versions WHERE status='awaiting_approval'`); err == nil {
		gPending.Set(int64(n))
	}
	return nil
}
