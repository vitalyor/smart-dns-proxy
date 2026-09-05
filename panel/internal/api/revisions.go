package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"smartdns/panel/internal/compiler"
	"smartdns/panel/internal/store"
	"smartdns/shared/model"
)

func (s *Server) listRevisions(w http.ResponseWriter, r *http.Request) error {
	rows, err := store.Many[store.Revision](r.Context(), s.DB,
		`SELECT * FROM revisions ORDER BY sequence DESC LIMIT 50`)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
	return nil
}

func (s *Server) getRevision(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	rev, err := store.One[store.Revision](r.Context(), s.DB, `SELECT * FROM revisions WHERE id=$1`, id)
	if err != nil {
		return err
	}
	deps, err := store.Many[store.NodeDeployment](r.Context(), s.DB, `
		SELECT d.*, n.name AS node_name FROM node_deployments d JOIN nodes n ON n.id=d.node_id
		WHERE d.revision_id=$1 ORDER BY d.wave, n.name`, id)
	if err != nil {
		return err
	}
	type art struct {
		NodeID   string `db:"node_id" json:"node_id"`
		NodeName string `db:"node_name" json:"node_name"`
		SHA256   string `db:"sha256" json:"sha256"`
		Size     int64  `db:"size_bytes" json:"size_bytes"`
	}
	arts, err := store.Many[art](r.Context(), s.DB, `
		SELECT a.node_id::text, n.name AS node_name, a.sha256, a.size_bytes
		FROM revision_artifacts a JOIN nodes n ON n.id=a.node_id WHERE a.revision_id=$1 ORDER BY n.name`, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": rev, "deployments": deps, "artifacts": arts})
	return nil
}

func (s *Server) getRevisionArtifact(w http.ResponseWriter, r *http.Request) error {
	var content []byte
	err := s.DB.QueryRow(r.Context(),
		`SELECT content FROM revision_artifacts WHERE revision_id=$1 AND node_id=$2`,
		r.PathValue("id"), r.PathValue("node_id")).Scan(&content)
	if err != nil {
		return notFound("artifact")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(content)
	return nil
}

// compileRevision builds a new immutable revision from the current state.
func (s *Server) compileRevision(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Deploy bool `json:"deploy"`
		DryRun bool `json:"dry_run"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
	}
	return s.idempotent(w, r, "compile", func() (int, any, error) {
		out, revID, err := s.compile(r.Context(), req.DryRun)
		if err != nil {
			var ce *compiler.ConflictError
			if asConflictError(err, &ce) {
				e := errorf(http.StatusConflict, "rule_conflict",
					"обнаружены конфликты правил между сервисами; задайте разные приоритеты")
				e.Details = ce.Conflicts
				return 0, nil, e
			}
			return 0, nil, errorf(http.StatusUnprocessableEntity, "compile_failed", "%v", err)
		}
		body := map[string]any{
			"revision_id": revID,
			"manifest":    out.Manifest,
			"summary":     out.Summary,
			"warnings":    out.Warnings,
			"dry_run":     req.DryRun,
		}
		if req.DryRun {
			return http.StatusOK, body, nil
		}
		s.audit(r.Context(), r, "revision.compiled", "revision", revID, nil, out.Summary)
		if req.Deploy {
			if err := s.startRollout(r.Context(), revID); err != nil {
				return 0, nil, err
			}
			body["deploying"] = true
		}
		return http.StatusCreated, body, nil
	})
}

func asConflictError(err error, target **compiler.ConflictError) bool {
	c, ok := err.(*compiler.ConflictError)
	if ok {
		*target = c
	}
	return ok
}

// compile gathers desired state and stores the artifacts.
func (s *Server) compile(ctx context.Context, dryRun bool) (*compiler.Output, string, error) {
	nodes, err := store.Many[store.Node](ctx, s.DB, `SELECT * FROM nodes ORDER BY name`)
	if err != nil {
		return nil, "", err
	}
	if len(nodes) == 0 {
		return nil, "", fmt.Errorf("нет ни одной зарегистрированной ноды")
	}
	type svcRow struct {
		store.Service
		IngressMode     *string `db:"ingress_mode"`
		EgressMode      *string `db:"egress_mode"`
		ActiveVersionID *string `db:"active_version_id"`
		RuleSetHash     *string `db:"rule_set_hash"`
	}
	svcs, err := store.Many[svcRow](ctx, s.DB, `
		SELECT sv.*, ig.mode AS ingress_mode, eg.mode AS egress_mode,
		       rs.active_version_id, rv.content_hash AS rule_set_hash
		FROM services sv
		LEFT JOIN ingress_groups ig ON ig.id = sv.ingress_group_id
		LEFT JOIN egress_groups eg ON eg.id = sv.egress_group_id
		LEFT JOIN rule_sets rs ON rs.id = sv.rule_set_id
		LEFT JOIN rule_set_versions rv ON rv.id = rs.active_version_id
		WHERE sv.enabled ORDER BY sv.slug`)
	if err != nil {
		return nil, "", err
	}
	if len(svcs) == 0 {
		return nil, "", fmt.Errorf("нет ни одного включённого сервиса")
	}

	in := compiler.Input{
		Sequence:    0,
		LabMode:     s.Cfg.LabMode,
		SigningKey:  s.Cfg.SigningKey,
		MinAgentVer: "1.0.0",
		LogLevel:    getSetting(ctx, s.DB, "node_log_level", "info"),
		DNS:         s.dnsConfig(ctx),
		Ingress: model.IngressConfig{
			ClientHelloTimeoutMs: 3000, MaxPreReadBytes: 16384,
			DialTimeoutMs: 8000, IdleTimeoutSec: 300, MaxSessions: 10000,
		},
		EgressTuning: model.EgressConfig{
			Resolver:      getSetting(ctx, s.DB, "egress_resolver", "1.1.1.1:53"),
			DialTimeoutMs: 8000, IdleTimeoutSec: 300, MaxSessions: 10000,
		},
	}
	// A node in maintenance or explicitly unhealthy is excluded from new
	// answers and routes, but its configuration is preserved.
	for _, n := range nodes {
		in.Nodes = append(in.Nodes, compiler.NodeInput{
			ID: n.ID, Name: n.Name, Role: n.Role,
			PublicIPv4: deref(n.PublicIPv4), PublicIPv6: deref(n.PublicIPv6),
			RelayEndpoint: deref(n.RelayEndpoint), RelaySNI: deref(n.RelaySNI),
			Eligible: n.Status != "maintenance" && n.Status != "disabled" && n.Status != "unhealthy",
		})
	}

	b := s.builder()
	for _, sv := range svcs {
		if sv.ActiveVersionID == nil {
			return nil, "", fmt.Errorf("у сервиса %q нет активной версии списка доменов", sv.Name)
		}
		entries, err := b.Entries(ctx, *sv.ActiveVersionID)
		if err != nil {
			return nil, "", err
		}
		ingressNodes, err := memberIDs(ctx, s.DB, "ingress_group_members", sv.IngressGroupID)
		if err != nil {
			return nil, "", err
		}
		if len(ingressNodes) == 0 {
			return nil, "", fmt.Errorf("у сервиса %q не выбрана точка входа или она пуста", sv.Name)
		}
		egMembers, err := egressMembers(ctx, s.DB, sv.EgressGroupID)
		if err != nil {
			return nil, "", err
		}
		if len(egMembers) == 0 {
			return nil, "", fmt.Errorf("у сервиса %q не выбрана точка выхода или она пуста", sv.Name)
		}
		ports := make([]int, 0, len(sv.AllowedPorts))
		for _, p := range sv.AllowedPorts {
			ports = append(ports, int(p))
		}
		in.Services = append(in.Services, compiler.ServiceInput{
			ID: sv.ID, Slug: sv.Slug, Name: sv.Name, Priority: sv.Priority,
			TTL: uint32(sv.DNSTTL), AllowedPorts: ports, UDPMode: sv.UDPMode,
			Entries: entries, RuleSetHash: deref(sv.RuleSetHash),
			IngressNodes: ingressNodes, IngressMode: deref(sv.IngressMode),
			EgressMembers: egMembers, EgressMode: deref(sv.EgressMode),
		})
	}

	if dryRun {
		in.RevisionID = "00000000-0000-0000-0000-000000000000"
		out, err := compiler.Compile(in)
		return out, "", err
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)

	var revID string
	var seq int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO revisions (state) VALUES ('draft') RETURNING id, sequence`).Scan(&revID, &seq); err != nil {
		return nil, "", err
	}
	in.RevisionID, in.Sequence = revID, seq

	out, err := compiler.Compile(in)
	if err != nil {
		_, _ = s.DB.Exec(ctx, `UPDATE revisions SET state='validation_failed', error=$2, updated_at=now() WHERE id=$1`,
			revID, err.Error())
		return nil, revID, err
	}
	for nodeID, content := range out.Artifacts {
		if _, err := tx.Exec(ctx, `
			INSERT INTO revision_artifacts (revision_id, node_id, kind, content, sha256, size_bytes)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			revID, nodeID, model.ArtifactKind, content, model.SHA256Hex(content), len(content)); err != nil {
			return nil, revID, err
		}
	}
	manifestJSON, _ := json.Marshal(out.Manifest)
	summary := map[string]any{"summary": out.Summary, "warnings": out.Warnings}
	summaryJSON, _ := json.Marshal(summary)
	if _, err := tx.Exec(ctx, `
		UPDATE revisions SET state='compiled', model_hash=$2, manifest=$3, summary=$4, updated_at=now()
		WHERE id=$1`, revID, out.Manifest.ModelSHA256, manifestJSON, summaryJSON); err != nil {
		return nil, revID, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, revID, err
	}
	return out, revID, nil
}

func (s *Server) dnsConfig(ctx context.Context) model.DNSConfig {
	c := model.DNSConfig{
		Upstream:     getSetting(ctx, s.DB, "dns_upstream", "unbound:53"),
		MinTTL:       30,
		MaxTTL:       300,
		BlockHTTPSRR: true,
		DoHHostname:  getSetting(ctx, s.DB, "doh_hostname", ""),
		DoTHostname:  getSetting(ctx, s.DB, "dot_hostname", ""),
		Access: model.DNSAccess{
			Mode:           getSetting(ctx, s.DB, "dns_access_mode", "allowlist"),
			AllowedCIDRs:   getSettingList(ctx, s.DB, "dns_allowed_cidrs"),
			RateLimitQPS:   getSettingInt(ctx, s.DB, "dns_rate_limit_qps", 50),
			RateLimitBurst: getSettingInt(ctx, s.DB, "dns_rate_limit_burst", 250),
			MaxConcurrent:  getSettingInt(ctx, s.DB, "dns_max_concurrent", 2048),
		},
		PublishAAAA: getSetting(ctx, s.DB, "publish_aaaa", "false") == "true",
	}
	return c
}

func memberIDs(ctx context.Context, db *store.DB, table string, groupID *string) ([]string, error) {
	if groupID == nil {
		return nil, nil
	}
	type row struct {
		NodeID string `db:"node_id"`
	}
	rows, err := store.Many[row](ctx, db,
		fmt.Sprintf(`SELECT m.node_id::text FROM %s m JOIN nodes n ON n.id=m.node_id
			WHERE m.group_id=$1 AND m.enabled ORDER BY m.priority, n.name`, table), *groupID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.NodeID)
	}
	return out, nil
}

func egressMembers(ctx context.Context, db *store.DB, groupID *string) ([]compiler.EgressMember, error) {
	if groupID == nil {
		return nil, nil
	}
	type row struct {
		NodeID   string `db:"node_id"`
		Priority int    `db:"priority"`
		Weight   int    `db:"weight"`
	}
	rows, err := store.Many[row](ctx, db, `
		SELECT m.node_id::text, m.priority, m.weight FROM egress_group_members m
		JOIN nodes n ON n.id=m.node_id WHERE m.group_id=$1 AND m.enabled ORDER BY m.priority`, *groupID)
	if err != nil {
		return nil, err
	}
	out := make([]compiler.EgressMember, 0, len(rows))
	for _, r := range rows {
		out = append(out, compiler.EgressMember{NodeID: r.NodeID, Priority: r.Priority, Weight: r.Weight})
	}
	return out, nil
}

// --- rollout -----------------------------------------------------------------

func (s *Server) deployRevision(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	return s.idempotent(w, r, "deploy", func() (int, any, error) {
		if err := s.startRollout(r.Context(), id); err != nil {
			return 0, nil, err
		}
		s.audit(r.Context(), r, "revision.deploy_started", "revision", id, nil, nil)
		return http.StatusAccepted, map[string]string{"status": "deploying", "revision_id": id}, nil
	})
}

// startRollout marks one canary node per role, then the rest.
func (s *Server) startRollout(ctx context.Context, revisionID string) error {
	rev, err := store.One[store.Revision](ctx, s.DB, `SELECT * FROM revisions WHERE id=$1`, revisionID)
	if err != nil {
		return err
	}
	if rev.State == "validation_failed" {
		return conflictErr("конфигурация не прошла проверку и не может быть применена")
	}
	type art struct {
		NodeID string `db:"node_id"`
		Role   string `db:"role"`
	}
	arts, err := store.Many[art](ctx, s.DB, `
		SELECT a.node_id::text, n.role FROM revision_artifacts a JOIN nodes n ON n.id=a.node_id
		WHERE a.revision_id=$1 ORDER BY n.role, n.name`, revisionID)
	if err != nil {
		return err
	}
	if len(arts) == 0 {
		return conflictErr("у конфигурации нет данных для применения")
	}
	seenRole := map[string]bool{}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, a := range arts {
		wave := 1
		if !seenRole[a.Role] {
			// First node of each role is the canary; with a single node
			// installation the canary is that node.
			wave = 0
			seenRole[a.Role] = true
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO node_deployments (node_id, revision_id, state, wave) VALUES ($1,$2,'pending',$3)
			ON CONFLICT (node_id, revision_id) DO UPDATE SET state='pending', wave=EXCLUDED.wave,
			  error_code='', error_detail='', started_at=now(), finished_at=NULL`,
			a.NodeID, revisionID, wave); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE nodes SET desired_revision_id=$2, updated_at=now() WHERE id=$1`, a.NodeID, revisionID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE revisions SET state='deploying', updated_at=now() WHERE id=$1`, revisionID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.event(ctx, "info", "rollout", "deploy_started",
		fmt.Sprintf("Начато применение конфигурации #%d на %d нод", rev.Sequence, len(arts)), nil, nil)
	// Push to every target now; unreachable nodes are reconciled by the poll
	// loop. Detached from the request so a slow node never delays the response.
	go s.pushRevision(revisionID)
	return nil
}

func (s *Server) rollbackRevision(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	return s.idempotent(w, r, "rollback", func() (int, any, error) {
		// Roll back to the newest revision that was previously active.
		prev, err := store.Value[string](r.Context(), s.DB, `
			SELECT id::text FROM revisions
			WHERE state IN ('active','superseded','partially_active') AND id <> $1 AND activated_at IS NOT NULL
			ORDER BY activated_at DESC LIMIT 1`, id)
		if err != nil {
			return 0, nil, conflictErr("нет предыдущей конфигурации для отката")
		}
		if err := s.startRollout(r.Context(), prev); err != nil {
			return 0, nil, err
		}
		if _, err := s.DB.Exec(r.Context(),
			`UPDATE revisions SET state='rolled_back', updated_at=now() WHERE id=$1`, id); err != nil {
			return 0, nil, err
		}
		s.audit(r.Context(), r, "revision.rolled_back", "revision", id, nil, map[string]any{"target": prev})
		s.event(r.Context(), "warn", "rollout", "rollback", "Выполнен откат на предыдущую конфигурацию", nil, nil)
		return http.StatusAccepted, map[string]string{"status": "rolling_back", "target_revision_id": prev}, nil
	})
}

// reconcileRevisionState promotes a revision once every node has reported.
func (s *Server) reconcileRevisionState(ctx context.Context, revisionID string) {
	type counts struct {
		Total   int `db:"total"`
		Applied int `db:"applied"`
		Failed  int `db:"failed"`
	}
	c, err := store.One[counts](ctx, s.DB, `
		SELECT count(*)::int AS total,
		       count(*) FILTER (WHERE state='applied')::int AS applied,
		       count(*) FILTER (WHERE state IN ('failed','rolled_back'))::int AS failed
		FROM node_deployments WHERE revision_id=$1`, revisionID)
	if err != nil {
		return
	}
	state := "deploying"
	switch {
	case c.Applied == c.Total && c.Total > 0:
		state = "active"
	case c.Applied > 0 && c.Applied+c.Failed == c.Total:
		state = "partially_active"
	case c.Failed == c.Total && c.Total > 0:
		state = "validation_failed"
	default:
		return
	}
	_, _ = s.DB.Exec(ctx, `UPDATE revisions SET state=$2,
		activated_at=CASE WHEN $2 IN ('active','partially_active') THEN COALESCE(activated_at, now()) ELSE activated_at END,
		updated_at=now() WHERE id=$1`, revisionID, state)
	if state == "active" {
		_, _ = s.DB.Exec(ctx, `UPDATE revisions SET state='superseded', updated_at=now()
			WHERE state='active' AND id<>$1`, revisionID)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func getSetting(ctx context.Context, db *store.DB, key, def string) string {
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&raw); err != nil {
		return def
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return def
	}
	if s == "" {
		return def
	}
	return s
}

func getSettingInt(ctx context.Context, db *store.DB, key string, def int) int {
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&raw); err != nil {
		return def
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return def
	}
	return n
}

func getSettingList(ctx context.Context, db *store.DB, key string) []string {
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&raw); err != nil {
		return nil
	}
	var out []string
	_ = json.Unmarshal(raw, &out)
	return out
}
