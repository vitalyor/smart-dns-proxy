package api

import (
	"net/http"
	"strings"

	"smartdns/panel/internal/rules"
	"smartdns/panel/internal/store"
	"smartdns/shared/domainset"
)

func (s *Server) builder() *rules.Builder {
	return &rules.Builder{DB: s.DB, Fetch: s.Fetcher, Thresholds: rules.DefaultThresholds, AllowPrivate: s.Cfg.LabMode}
}

func (s *Server) listRuleSets(w http.ResponseWriter, r *http.Request) error {
	type row struct {
		store.RuleSet
		ActiveSequence *int64  `db:"active_sequence" json:"active_sequence"`
		ActiveHash     *string `db:"active_hash" json:"active_hash"`
		EntryCount     int     `db:"entry_count" json:"entry_count"`
		SourceCount    int     `db:"source_count" json:"source_count"`
		PendingID      *string `db:"pending_version_id" json:"pending_version_id"`
		UsedByServices int     `db:"used_by_services" json:"used_by_services"`
	}
	rows, err := store.Many[row](r.Context(), s.DB, `
		SELECT rs.*, v.sequence AS active_sequence, v.content_hash AS active_hash,
		  (SELECT count(*)::int FROM rule_entries e WHERE e.version_id = rs.active_version_id) AS entry_count,
		  (SELECT count(*)::int FROM rule_sources src WHERE src.rule_set_id = rs.id) AS source_count,
		  (SELECT id::text FROM rule_set_versions p WHERE p.rule_set_id = rs.id
		     AND p.status='awaiting_approval' ORDER BY p.sequence DESC LIMIT 1) AS pending_version_id,
		  (SELECT count(*)::int FROM services sv WHERE sv.rule_set_id = rs.id) AS used_by_services
		FROM rule_sets rs LEFT JOIN rule_set_versions v ON v.id = rs.active_version_id
		ORDER BY rs.name`)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows, "presets": presetNames()})
	return nil
}

func presetNames() []string {
	out := []string{}
	for k := range rules.Presets() {
		out = append(out, k)
	}
	return out
}

func (s *Server) getRuleSet(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	rs, err := store.One[store.RuleSet](r.Context(), s.DB, `SELECT * FROM rule_sets WHERE id=$1`, id)
	if err != nil {
		return err
	}
	sources, err := store.Many[store.RuleSource](r.Context(), s.DB,
		`SELECT * FROM rule_sources WHERE rule_set_id=$1 ORDER BY created_at`, id)
	if err != nil {
		return err
	}
	versions, err := store.Many[store.RuleSetVersion](r.Context(), s.DB,
		`SELECT * FROM rule_set_versions WHERE rule_set_id=$1 ORDER BY sequence DESC LIMIT 25`, id)
	if err != nil {
		return err
	}
	type fetchRow struct {
		SourceID   string `db:"source_id" json:"source_id"`
		Status     string `db:"status" json:"status"`
		HTTPStatus int    `db:"http_status" json:"http_status"`
		Entries    int    `db:"entries" json:"entries"`
		Error      string `db:"error" json:"error"`
		StartedAt  string `db:"started_at" json:"started_at"`
	}
	fetches, _ := store.Many[fetchRow](r.Context(), s.DB, `
		SELECT source_id::text, status, http_status, entries, error, started_at::text
		FROM rule_fetches WHERE source_id IN (SELECT id FROM rule_sources WHERE rule_set_id=$1)
		ORDER BY started_at DESC LIMIT 30`, id)

	var entries []domainset.Entry
	if rs.ActiveVersionID != nil {
		entries, _ = s.builder().Entries(r.Context(), *rs.ActiveVersionID)
	}
	preview := make([]string, 0, 200)
	for i, e := range entries {
		if i >= 200 {
			break
		}
		preview = append(preview, string(e.Kind)+":"+e.Value)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rule_set": rs, "sources": sources, "versions": versions,
		"fetches": fetches, "preview": preview, "entry_count": len(entries),
	})
	return nil
}

type ruleSetRequest struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	UpdateMode    string   `json:"update_mode"`
	IntervalSec   int      `json:"interval_sec"`
	AllowRegex    bool     `json:"allow_regex"`
	Priority      int      `json:"priority"`
	ManualInclude []string `json:"manual_include"`
	ManualExclude []string `json:"manual_exclude"`
}

func (s *Server) createRuleSet(w http.ResponseWriter, r *http.Request) error {
	var req ruleSetRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return badRequest("укажите название списка доменов")
	}
	if req.UpdateMode == "" {
		req.UpdateMode = "manual_approve"
	}
	if !contains([]string{"auto_apply", "manual_approve", "manual_only"}, req.UpdateMode) {
		return badRequest("недопустимый режим обновления")
	}
	if req.IntervalSec < 300 {
		req.IntervalSec = 21600
	}
	if req.Priority == 0 {
		req.Priority = 100
	}
	rs, err := store.One[store.RuleSet](r.Context(), s.DB, `
		INSERT INTO rule_sets (name, description, update_mode, interval_sec, allow_regex, priority, manual_include, manual_exclude)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING *`,
		req.Name, req.Description, req.UpdateMode, req.IntervalSec, req.AllowRegex, req.Priority,
		nonNil(req.ManualInclude), nonNil(req.ManualExclude))
	if err != nil {
		return err
	}
	s.audit(r.Context(), r, "rule_set.created", "rule_set", rs.ID, nil, rs)
	writeJSON(w, http.StatusCreated, rs)
	return nil
}

func (s *Server) patchRuleSet(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Name          *string  `json:"name"`
		Description   *string  `json:"description"`
		UpdateMode    *string  `json:"update_mode"`
		IntervalSec   *int     `json:"interval_sec"`
		AllowRegex    *bool    `json:"allow_regex"`
		Priority      *int     `json:"priority"`
		ManualInclude []string `json:"manual_include"`
		ManualExclude []string `json:"manual_exclude"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ver, err := ifMatch(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	before, err := store.One[store.RuleSet](r.Context(), s.DB, `SELECT * FROM rule_sets WHERE id=$1`, id)
	if err != nil {
		return err
	}
	n, err := s.DB.ExecN(r.Context(), `
		UPDATE rule_sets SET name=COALESCE($3,name), description=COALESCE($4,description),
			update_mode=COALESCE($5,update_mode), interval_sec=COALESCE($6,interval_sec),
			allow_regex=COALESCE($7,allow_regex), priority=COALESCE($8,priority),
			manual_include=COALESCE($9,manual_include), manual_exclude=COALESCE($10,manual_exclude),
			updated_at=now(), version=version+1
		WHERE id=$1 AND ($2 = 0 OR version = $2)`,
		id, ver, req.Name, req.Description, req.UpdateMode, req.IntervalSec, req.AllowRegex,
		req.Priority, req.ManualInclude, req.ManualExclude)
	if err != nil {
		return err
	}
	if err := checkVersion(n, ver); err != nil {
		return err
	}
	after, _ := store.One[store.RuleSet](r.Context(), s.DB, `SELECT * FROM rule_sets WHERE id=$1`, id)
	s.audit(r.Context(), r, "rule_set.updated", "rule_set", id, before, after)
	writeJSON(w, http.StatusOK, after)
	return nil
}

func (s *Server) deleteRuleSet(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	users, err := store.Many[struct {
		Kind string `db:"kind" json:"kind"`
		Name string `db:"name" json:"name"`
	}](r.Context(), s.DB, `SELECT 'service' AS kind, name FROM services WHERE rule_set_id=$1`, id)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		e := conflictErr("список доменов используется сервисами")
		e.Details = users
		return e
	}
	n, err := s.DB.ExecN(r.Context(), `DELETE FROM rule_sets WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("rule set")
	}
	s.audit(r.Context(), r, "rule_set.deleted", "rule_set", id, nil, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	return nil
}

type sourceRequest struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	URL            string `json:"url"`
	Repo           string `json:"repo"`
	Ref            string `json:"ref"`
	Path           string `json:"path"`
	Mode           string `json:"mode"`
	ExpectedSHA256 string `json:"expected_sha256"`
	Body           string `json:"body"`
	Enabled        *bool  `json:"enabled"`
}

// validateSource normalises and checks a source request in place. Shared by the
// shared-list and service-scoped source endpoints.
func validateSource(req *sourceRequest) error {
	if !contains([]string{"github_raw", "github_repo", "https", "manual", "preset", "singbox_json"}, req.Type) {
		return badRequest("недопустимый тип источника")
	}
	if req.Mode == "" {
		req.Mode = "include"
	}
	if req.Type == "manual" {
		req.URL = req.Body
	}
	if req.Type == "github_repo" && (req.Repo == "" || req.Path == "") {
		return badRequest("для GitHub-репозитория укажите owner/repo и путь к файлу")
	}
	if (req.Type == "github_raw" || req.Type == "https" || req.Type == "singbox_json") && !strings.HasPrefix(req.URL, "https://") {
		return badRequest("разрешены только HTTPS-источники")
	}
	return nil
}

func (s *Server) addSource(w http.ResponseWriter, r *http.Request) error {
	var req sourceRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	id := r.PathValue("id")
	if err := validateSource(&req); err != nil {
		return err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	src, err := store.One[store.RuleSource](r.Context(), s.DB, `
		INSERT INTO rule_sources (rule_set_id, name, type, url, repo, ref, path, mode, expected_sha256, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING *`,
		id, req.Name, req.Type, req.URL, req.Repo, req.Ref, req.Path, req.Mode, req.ExpectedSHA256, enabled)
	if err != nil {
		return err
	}
	s.audit(r.Context(), r, "rule_source.created", "rule_source", src.ID, nil, src)
	writeJSON(w, http.StatusCreated, src)
	return nil
}

func (s *Server) deleteSource(w http.ResponseWriter, r *http.Request) error {
	sid := r.PathValue("source_id")
	n, err := s.DB.ExecN(r.Context(), `DELETE FROM rule_sources WHERE id=$1`, sid)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("source")
	}
	s.audit(r.Context(), r, "rule_source.deleted", "rule_source", sid, nil, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	return nil
}

func (s *Server) fetchRuleSet(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	return s.idempotent(w, r, "rule_set_fetch", func() (int, any, error) {
		res, err := s.builder().Build(r.Context(), id)
		if err != nil {
			// The active version is deliberately left untouched on failure.
			s.event(r.Context(), "error", "rules", "fetch_failed", err.Error(), nil, map[string]any{"rule_set_id": id})
			return 0, nil, errorf(http.StatusBadGateway, "fetch_failed", "%v", err)
		}
		s.audit(r.Context(), r, "rule_set.fetched", "rule_set", id, nil,
			map[string]any{"status": res.Status, "added": res.Added, "removed": res.Removed, "hash": res.ContentHash})
		s.event(r.Context(), "info", "rules", "fetch_done",
			"Обновление списка завершено: "+res.Status, nil, map[string]any{"rule_set_id": id, "added": res.Added, "removed": res.Removed})
		return http.StatusOK, res, nil
	})
}

func (s *Server) diffRuleSet(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	target := r.URL.Query().Get("version_id")
	rs, err := store.One[store.RuleSet](r.Context(), s.DB, `SELECT * FROM rule_sets WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if target == "" {
		v, err := store.Value[string](r.Context(), s.DB, `
			SELECT id::text FROM rule_set_versions WHERE rule_set_id=$1 AND status='awaiting_approval'
			ORDER BY sequence DESC LIMIT 1`, id)
		if err != nil {
			return notFound("pending version")
		}
		target = v
	}
	b := s.builder()
	newEntries, err := b.Entries(r.Context(), target)
	if err != nil {
		return err
	}
	var oldEntries []domainset.Entry
	if rs.ActiveVersionID != nil {
		oldEntries, _ = b.Entries(r.Context(), *rs.ActiveVersionID)
	}
	added, removed := domainset.Diff(oldEntries, newEntries)
	version, err := store.One[store.RuleSetVersion](r.Context(), s.DB, `SELECT * FROM rule_set_versions WHERE id=$1`, target)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": version,
		"counts":  map[string]int{"before": len(oldEntries), "after": len(newEntries), "added": len(added), "removed": len(removed)},
		"added":   flatten(added, 500),
		"removed": flatten(removed, 500),
	})
	return nil
}

func (s *Server) approveRuleSet(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		VersionID string `json:"version_id"`
		Reject    bool   `json:"reject"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	id := r.PathValue("id")
	if req.VersionID == "" {
		v, err := store.Value[string](r.Context(), s.DB, `
			SELECT id::text FROM rule_set_versions WHERE rule_set_id=$1 AND status='awaiting_approval'
			ORDER BY sequence DESC LIMIT 1`, id)
		if err != nil {
			return notFound("pending version")
		}
		req.VersionID = v
	}
	if req.Reject {
		if _, err := s.DB.Exec(r.Context(),
			`UPDATE rule_set_versions SET status='rejected' WHERE id=$1 AND rule_set_id=$2`, req.VersionID, id); err != nil {
			return err
		}
		s.audit(r.Context(), r, "rule_set.rejected", "rule_set", id, nil, map[string]any{"version_id": req.VersionID})
		writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
		return nil
	}
	if err := s.builder().Approve(r.Context(), id, req.VersionID); err != nil {
		return err
	}
	s.audit(r.Context(), r, "rule_set.approved", "rule_set", id, nil, map[string]any{"version_id": req.VersionID})
	s.event(r.Context(), "info", "rules", "version_activated", "Новая версия списка доменов активирована", nil,
		map[string]any{"rule_set_id": id, "version_id": req.VersionID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
	return nil
}

func flatten(es []domainset.Entry, max int) []string {
	out := make([]string, 0, min(len(es), max))
	for i, e := range es {
		if i >= max {
			break
		}
		out = append(out, string(e.Kind)+":"+e.Value)
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
