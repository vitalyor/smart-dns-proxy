// Package jobs is the durable background worker. The queue lives in
// PostgreSQL: one less moving part than Redis, and jobs survive a restart.
package jobs

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"smartdns/panel/internal/fetcher"
	"smartdns/panel/internal/rules"
	"smartdns/panel/internal/store"
	"smartdns/shared/metrics"
)

var (
	mJobs   = metrics.Counter("smartdns_panel_jobs_total", "Background jobs by type and result")
	mJobDur = metrics.Histogram("smartdns_panel_job_duration_seconds", "Background job duration", metrics.DefBuckets)
	mProbe  = metrics.Counter("smartdns_service_probe_total", "Synthetic service probes by service and result")
)

// Runner leases and executes jobs.
type Runner struct {
	DB           *store.DB
	Owner        string
	Fetch        *fetcher.Client
	LabMode      bool
	HealthRetain time.Duration
	AuditRetain  time.Duration
}

// Enqueue adds a job, optionally deduplicated.
func Enqueue(ctx context.Context, db *store.DB, typ string, payload any, runAt time.Time, dedupe string) error {
	b, _ := json.Marshal(payload)
	if payload == nil {
		b = []byte("{}")
	}
	var dk any
	if dedupe != "" {
		dk = dedupe
	}
	_, err := db.Exec(ctx, `INSERT INTO jobs (type, payload, run_at, dedupe_key)
		VALUES ($1,$2,$3,$4) ON CONFLICT (dedupe_key) DO NOTHING`, typ, b, runAt, dk)
	return err
}

// Start runs the worker loop plus the periodic schedulers until ctx is done.
func (r *Runner) Start(ctx context.Context) {
	go r.loop(ctx, 2*time.Second, r.runOnce)
	go r.loop(ctx, 60*time.Second, r.scheduleRuleFetches)
	go r.loop(ctx, 30*time.Second, r.scheduleProbes)
	go r.loop(ctx, 5*time.Minute, r.markStaleNodes)
	go r.loop(ctx, time.Hour, r.retention)
	go r.loop(ctx, 20*time.Second, r.PublishMetrics)
}

func (r *Runner) loop(ctx context.Context, every time.Duration, fn func(context.Context) error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("scheduler tick failed", "err", err)
			}
		}
	}
}

// runOnce leases a single job and executes it.
func (r *Runner) runOnce(ctx context.Context) error {
	type job struct {
		ID       string          `db:"id"`
		Type     string          `db:"type"`
		Payload  json.RawMessage `db:"payload"`
		Attempts int             `db:"attempts"`
		Max      int             `db:"max_attempts"`
	}
	j, err := store.One[job](ctx, r.DB, `
		UPDATE jobs SET state='running', lease_owner=$1, lease_until=now() + interval '5 minutes',
			attempts=attempts+1, updated_at=now()
		WHERE id = (
			SELECT id FROM jobs
			WHERE (state='queued' AND run_at <= now())
			   OR (state='running' AND lease_until < now())
			ORDER BY run_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id::text, type, payload, attempts, max_attempts`, r.Owner)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	start := time.Now()
	err = r.execute(ctx, j.Type, j.Payload)
	mJobDur.Observe(time.Since(start).Seconds(), "type", j.Type)
	if err != nil {
		mJobs.Inc("type", j.Type, "result", "error")
		state := "queued"
		if j.Attempts >= j.Max {
			state = "failed"
		}
		backoff := time.Duration(1<<min(j.Attempts, 6)) * time.Minute
		_, _ = r.DB.Exec(ctx, `UPDATE jobs SET state=$2, last_error=$3, run_at=now()+$4::interval,
			lease_owner='', lease_until=NULL, updated_at=now() WHERE id=$1`,
			j.ID, state, truncate(err.Error(), 500), fmt.Sprintf("%d seconds", int(backoff.Seconds())))
		slog.Warn("job failed", "type", j.Type, "attempt", j.Attempts, "err", err)
		return nil
	}
	mJobs.Inc("type", j.Type, "result", "ok")
	_, _ = r.DB.Exec(ctx, `UPDATE jobs SET state='done', lease_owner='', lease_until=NULL,
		last_error='', updated_at=now() WHERE id=$1`, j.ID)
	return nil
}

func (r *Runner) execute(ctx context.Context, typ string, payload json.RawMessage) error {
	switch typ {
	case "rule_fetch":
		var p struct {
			RuleSetID string `json:"rule_set_id"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return err
		}
		b := &rules.Builder{DB: r.DB, Fetch: r.Fetch, Thresholds: rules.DefaultThresholds, AllowPrivate: r.LabMode}
		res, err := b.Build(ctx, p.RuleSetID)
		if err != nil {
			r.event(ctx, "warn", "rules", "scheduled_fetch_failed",
				"Плановое обновление списка не удалось; активная версия сохранена: "+err.Error(), nil)
			return err
		}
		if !res.Unchanged {
			r.event(ctx, "info", "rules", "scheduled_fetch",
				fmt.Sprintf("Плановое обновление: %s (+%d / −%d)", res.Status, res.Added, res.Removed), nil)
		}
		return nil
	case "service_probe":
		var p struct {
			ServiceID string `json:"service_id"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return err
		}
		return r.probeService(ctx, p.ServiceID)
	default:
		return fmt.Errorf("unknown job type %q", typ)
	}
}

// scheduleRuleFetches enqueues due rule sets. `manual_only` sets are skipped.
func (r *Runner) scheduleRuleFetches(ctx context.Context) error {
	type row struct {
		ID string `db:"id"`
	}
	rows, err := store.Many[row](ctx, r.DB, `
		SELECT id::text FROM rule_sets
		WHERE update_mode <> 'manual_only'
		  AND (last_fetch_at IS NULL OR last_fetch_at < now() - (interval_sec * interval '1 second'))`)
	if err != nil {
		return err
	}
	for _, x := range rows {
		key := "rule_fetch:" + x.ID + ":" + time.Now().UTC().Format("2006-01-02T15:04")
		if err := Enqueue(ctx, r.DB, "rule_fetch", map[string]string{"rule_set_id": x.ID}, time.Now(), key); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) scheduleProbes(ctx context.Context) error {
	type row struct {
		ID string `db:"id"`
	}
	rows, err := store.Many[row](ctx, r.DB,
		`SELECT id::text FROM services WHERE enabled AND probe->>'hostname' IS NOT NULL`)
	if err != nil {
		return err
	}
	for _, x := range rows {
		key := "probe:" + x.ID + ":" + time.Now().UTC().Format("2006-01-02T15:04")
		_ = Enqueue(ctx, r.DB, "service_probe", map[string]string{"service_id": x.ID}, time.Now(), key)
	}
	return nil
}

// probeService performs a synthetic check over the real path: it connects to
// the ingress address with the service hostname as SNI and verifies that the
// certificate presented belongs to the origin. A listening port alone is never
// treated as healthy.
func (r *Runner) probeService(ctx context.Context, serviceID string) error {
	type row struct {
		Name    string         `db:"name"`
		Slug    string         `db:"slug"`
		Probe   map[string]any `db:"probe"`
		GroupID *string        `db:"ingress_group_id"`
	}
	sv, err := store.One[row](ctx, r.DB,
		`SELECT name, slug, probe, ingress_group_id FROM services WHERE id=$1`, serviceID)
	if err != nil {
		return err
	}
	hostname, _ := sv.Probe["hostname"].(string)
	if hostname == "" {
		return nil
	}
	port := 443
	if p, ok := sv.Probe["port"].(float64); ok && p > 0 {
		port = int(p)
	}
	type nodeRow struct {
		ID   string  `db:"id"`
		Name string  `db:"name"`
		IPv4 *string `db:"public_ipv4"`
	}
	if sv.GroupID == nil {
		return nil
	}
	nodes, err := store.Many[nodeRow](ctx, r.DB, `
		SELECT n.id::text, n.name, n.public_ipv4 FROM ingress_group_members m
		JOIN nodes n ON n.id=m.node_id WHERE m.group_id=$1 AND m.enabled AND n.status <> 'disabled'`, *sv.GroupID)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if n.IPv4 == nil {
			continue
		}
		start := time.Now()
		code, detail := probeTLS(ctx, *n.IPv4, port, hostname)
		latency := int(time.Since(start).Milliseconds())
		success := code == ""
		_, _ = r.DB.Exec(ctx, `
			INSERT INTO health_samples (node_id, service_id, kind, success, latency_ms, error_code, detail)
			VALUES ($1,$2,'service_probe',$3,$4,$5,$6)`,
			n.ID, serviceID, success, latency, code, truncate(detail, 300))
		if success {
			mProbe.Inc("service", sv.Slug, "result", "ok")
		} else {
			mProbe.Inc("service", sv.Slug, "result", code)
			r.event(ctx, "warn", "health", "probe_failed",
				fmt.Sprintf("Проверка сервиса %s через %s не прошла: %s", sv.Name, n.Name, detail), &n.ID)
		}
	}
	return nil
}

// probeTLS returns an empty code on success, otherwise a stable error code.
// It deliberately distinguishes a network failure from a geo/account denial.
func probeTLS(ctx context.Context, ip string, port int, hostname string) (code, detail string) {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, fmt.Sprint(port)))
	if err != nil {
		return "tcp_unreachable", err.Error()
	}
	defer conn.Close()
	tc := tls.Client(conn, &tls.Config{ServerName: hostname, MinVersion: tls.VersionTLS12})
	_ = tc.SetDeadline(time.Now().Add(8 * time.Second))
	if err := tc.HandshakeContext(ctx); err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "certificate"):
			// A certificate error here means we are not seeing the real origin:
			// either the SNI was rejected or something terminated TLS.
			return "certificate_invalid", msg
		case strings.Contains(msg, "EOF"), strings.Contains(msg, "reset"):
			return "tunnel_refused", msg
		default:
			return "handshake_failed", msg
		}
	}
	st := tc.ConnectionState()
	if len(st.PeerCertificates) == 0 {
		return "no_certificate", "peer presented no certificate"
	}
	if err := st.PeerCertificates[0].VerifyHostname(hostname); err != nil {
		return "wrong_certificate", err.Error()
	}
	return "", st.PeerCertificates[0].Issuer.CommonName
}

// markStaleNodes degrades nodes that stopped sending heartbeats. The data
// plane keeps running on its last-known-good configuration regardless.
func (r *Runner) markStaleNodes(ctx context.Context) error {
	n, err := r.DB.ExecN(ctx, `
		UPDATE nodes SET status='unhealthy', updated_at=now()
		WHERE status NOT IN ('maintenance','disabled','unhealthy')
		  AND (last_seen_at IS NULL OR last_seen_at < now() - interval '120 seconds')`)
	if err != nil {
		return err
	}
	if n > 0 {
		r.event(ctx, "error", "health", "heartbeat_lost",
			fmt.Sprintf("%d нод перестали отправлять heartbeat", n), nil)
	}
	return nil
}

// retention trims high-volume tables in batches.
func (r *Runner) retention(ctx context.Context) error {
	hr := r.HealthRetain
	if hr == 0 {
		hr = 14 * 24 * time.Hour
	}
	ar := r.AuditRetain
	if ar == 0 {
		ar = 365 * 24 * time.Hour
	}
	for _, q := range []struct {
		sql string
		arg any
	}{
		{`DELETE FROM health_samples WHERE id IN (SELECT id FROM health_samples WHERE observed_at < now() - $1::interval LIMIT 50000)`, fmt.Sprintf("%d hours", int(hr.Hours()))},
		{`DELETE FROM events WHERE id IN (SELECT id FROM events WHERE created_at < now() - $1::interval LIMIT 50000)`, fmt.Sprintf("%d hours", int(hr.Hours()))},
		{`DELETE FROM audit_events WHERE id IN (SELECT id FROM audit_events WHERE created_at < now() - $1::interval LIMIT 50000)`, fmt.Sprintf("%d hours", int(ar.Hours()))},
		{`DELETE FROM jobs WHERE state IN ('done','cancelled') AND updated_at < now() - $1::interval`, "24 hours"},
		{`DELETE FROM idempotency_keys WHERE created_at < now() - $1::interval`, "48 hours"},
		{`DELETE FROM sessions WHERE expires_at < now() - $1::interval`, "24 hours"},
	} {
		if _, err := r.DB.Exec(ctx, q.sql, q.arg); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) event(ctx context.Context, level, component, code, msg string, nodeID *string) {
	_, _ = r.DB.Exec(ctx, `INSERT INTO events (level, component, node_id, code, message)
		VALUES ($1,$2,$3,$4,$5)`, level, component, nodeID, code, msg)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
