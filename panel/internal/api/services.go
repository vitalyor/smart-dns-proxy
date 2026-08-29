package api

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"smartdns/panel/internal/rules"
	"smartdns/panel/internal/store"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$`)

func (s *Server) listServices(w http.ResponseWriter, r *http.Request) error {
	type row struct {
		store.Service
		RuleSetName *string `db:"rule_set_name" json:"rule_set_name"`
		IngressName *string `db:"ingress_group_name" json:"ingress_group_name"`
		EgressName  *string `db:"egress_group_name" json:"egress_group_name"`
		RuleCount   *int    `db:"rule_count" json:"rule_count"`
		RuleSetHash *string `db:"rule_set_hash" json:"rule_set_hash"`
		// True when a probe hostname is set but is not among the service's managed
		// domains — such a probe hits the SNI proxy as "unmanaged" and always fails.
		ProbeInSet bool `db:"probe_in_set" json:"probe_in_set"`
		// Domains are the service's own hand-entered list, surfaced so the service
		// window can edit them directly — no separate "lists" page.
		Domains []string `db:"domains" json:"domains"`
	}
	rows, err := store.Many[row](r.Context(), s.DB, `
		SELECT sv.*, rs.name AS rule_set_name, ig.name AS ingress_group_name, eg.name AS egress_group_name,
		       (SELECT count(*)::int FROM rule_entries re WHERE re.version_id = rs.active_version_id) AS rule_count,
		       rsv.content_hash AS rule_set_hash,
		       COALESCE(rs.manual_include, '{}') AS domains,
		       COALESCE(sv.probe->>'hostname','') = '' OR EXISTS(
		         SELECT 1 FROM rule_entries re
		         WHERE re.version_id = rs.active_version_id AND re.value = sv.probe->>'hostname') AS probe_in_set
		FROM services sv
		LEFT JOIN rule_sets rs ON rs.id = sv.rule_set_id
		LEFT JOIN rule_set_versions rsv ON rsv.id = rs.active_version_id
		LEFT JOIN ingress_groups ig ON ig.id = sv.ingress_group_id
		LEFT JOIN egress_groups eg ON eg.id = sv.egress_group_id
		ORDER BY sv.name`)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
	return nil
}

type serviceRequest struct {
	Name           string         `json:"name"`
	Slug           string         `json:"slug"`
	Description    string         `json:"description"`
	Enabled        *bool          `json:"enabled"`
	RuleSetID      *string        `json:"rule_set_id"`
	IngressGroupID *string        `json:"ingress_group_id"`
	EgressGroupID  *string        `json:"egress_group_id"`
	AllowedPorts   []int32        `json:"allowed_ports"`
	UDPMode        string         `json:"udp_mode"`
	DNSTTL         int            `json:"dns_ttl"`
	Priority       int            `json:"priority"`
	Notes          string         `json:"notes"`
	Probe          map[string]any `json:"probe"`
}

func (s *Server) createService(w http.ResponseWriter, r *http.Request) error {
	var req serviceRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Slug == "" {
		req.Slug = slugify(req.Name)
	}
	if req.Name == "" {
		return badRequest("укажите название сервиса")
	}
	if !slugRe.MatchString(req.Slug) {
		return badRequest("Идентификатор строится из названия и может содержать только латиницу, цифры и дефисы — задайте название латиницей")
	}
	if req.DNSTTL == 0 {
		req.DNSTTL = 60
	}
	if req.DNSTTL < 30 || req.DNSTTL > 300 {
		return badRequest("TTL должен быть в диапазоне 30–300 секунд")
	}
	if req.Priority == 0 {
		req.Priority = 100
	}
	if len(req.AllowedPorts) == 0 {
		req.AllowedPorts = []int32{443}
	}
	if req.UDPMode == "" {
		req.UDPMode = "disabled_fallback"
	}
	if !contains([]string{"disabled_fallback", "proxy", "separate_ip"}, req.UDPMode) {
		return badRequest("недопустимый режим UDP")
	}
	if req.Probe == nil {
		req.Probe = map[string]any{}
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	sv, err := store.One[store.Service](r.Context(), s.DB, `
		INSERT INTO services (name, slug, description, enabled, rule_set_id, ingress_group_id,
			egress_group_id, allowed_ports, udp_mode, dns_ttl, priority, notes, probe)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING *`,
		req.Name, req.Slug, req.Description, enabled, req.RuleSetID, req.IngressGroupID,
		req.EgressGroupID, req.AllowedPorts, req.UDPMode, req.DNSTTL, req.Priority, req.Notes, req.Probe)
	if err != nil {
		return err
	}
	s.audit(r.Context(), r, "service.created", "service", sv.ID, nil, sv)
	writeJSON(w, http.StatusCreated, sv)
	return nil
}

func (s *Server) patchService(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Name           *string        `json:"name"`
		Description    *string        `json:"description"`
		Enabled        *bool          `json:"enabled"`
		RuleSetID      *string        `json:"rule_set_id"`
		IngressGroupID *string        `json:"ingress_group_id"`
		EgressGroupID  *string        `json:"egress_group_id"`
		AllowedPorts   []int32        `json:"allowed_ports"`
		UDPMode        *string        `json:"udp_mode"`
		DNSTTL         *int           `json:"dns_ttl"`
		Priority       *int           `json:"priority"`
		Notes          *string        `json:"notes"`
		Probe          map[string]any `json:"probe"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.DNSTTL != nil && (*req.DNSTTL < 30 || *req.DNSTTL > 300) {
		return badRequest("TTL должен быть в диапазоне 30–300 секунд")
	}
	ver, err := ifMatch(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	before, err := store.One[store.Service](r.Context(), s.DB, `SELECT * FROM services WHERE id=$1`, id)
	if err != nil {
		return err
	}
	n, err := s.DB.ExecN(r.Context(), `
		UPDATE services SET
			name=COALESCE($3,name), description=COALESCE($4,description), enabled=COALESCE($5,enabled),
			rule_set_id=COALESCE($6,rule_set_id), ingress_group_id=COALESCE($7,ingress_group_id),
			egress_group_id=COALESCE($8,egress_group_id), allowed_ports=COALESCE($9,allowed_ports),
			udp_mode=COALESCE($10,udp_mode), dns_ttl=COALESCE($11,dns_ttl), priority=COALESCE($12,priority),
			notes=COALESCE($13,notes), probe=COALESCE($14,probe),
			updated_at=now(), version=version+1
		WHERE id=$1 AND ($2 = 0 OR version = $2)`,
		id, ver, req.Name, req.Description, req.Enabled, req.RuleSetID, req.IngressGroupID,
		req.EgressGroupID, req.AllowedPorts, req.UDPMode, req.DNSTTL, req.Priority, req.Notes, req.Probe)
	if err != nil {
		return err
	}
	if err := checkVersion(n, ver); err != nil {
		return err
	}
	after, _ := store.One[store.Service](r.Context(), s.DB, `SELECT * FROM services WHERE id=$1`, id)
	s.audit(r.Context(), r, "service.updated", "service", id, before, after)
	writeJSON(w, http.StatusOK, after)
	return nil
}

// ensureServiceRuleSet returns the id of the service's private domain store,
// creating one on first use. The rule set is a 1:1 backing store the operator
// never sees directly — they edit "the service's domains" and "the service's
// auto-update sources", and this is where both live.
func (s *Server) ensureServiceRuleSet(ctx context.Context, sv *store.Service) (string, error) {
	if sv.RuleSetID != nil && *sv.RuleSetID != "" {
		return *sv.RuleSetID, nil
	}
	rs, err := store.One[store.RuleSet](ctx, s.DB, `
		INSERT INTO rule_sets (name, description, update_mode, interval_sec, allow_regex, priority, manual_include, manual_exclude)
		VALUES ($1,$2,'manual_only',21600,false,100,'{}','{}') RETURNING *`,
		sv.Name, "Домены сервиса "+sv.Name)
	if err != nil {
		return "", err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE services SET rule_set_id=$2 WHERE id=$1`, sv.ID, rs.ID); err != nil {
		return "", err
	}
	sv.RuleSetID = &rs.ID
	return rs.ID, nil
}

// rebuildActivate rebuilds a service's domain set and activates the result at
// once. Unlike shared lists, a service has no approval step — the operator's own
// edits (typed domains, added sources) take effect immediately after a rebuild.
func (s *Server) rebuildActivate(ctx context.Context, rsID string) (*rules.BuildResult, error) {
	res, err := s.builder().Build(ctx, rsID)
	if err != nil {
		return nil, errorf(http.StatusBadGateway, "build_failed", "не удалось собрать домены: %v", err)
	}
	if !res.Unchanged && res.Status != "active" && res.VersionID != "" {
		if err := s.builder().Approve(ctx, rsID, res.VersionID); err != nil {
			return nil, err
		}
		res.Status = "active"
	}
	return res, nil
}

// setServiceDomains replaces a service's own hand-typed domain list and rebuilds.
func (s *Server) setServiceDomains(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Domains []string `json:"domains"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	sv, err := store.One[store.Service](ctx, s.DB, `SELECT * FROM services WHERE id=$1`, r.PathValue("id"))
	if err != nil {
		return err
	}
	domains := cleanLines(req.Domains)
	rsID, err := s.ensureServiceRuleSet(ctx, &sv)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE rule_sets SET manual_include=$2, updated_at=now() WHERE id=$1`,
		rsID, nonNil(domains)); err != nil {
		return err
	}
	res, err := s.rebuildActivate(ctx, rsID)
	if err != nil {
		return err
	}
	s.audit(ctx, r, "service.domains", "service", sv.ID, nil, map[string]any{"count": len(domains)})
	writeJSON(w, http.StatusOK, map[string]any{"count": len(domains), "build": res})
	return nil
}

// listServiceSources returns the auto-update sources behind a service and their
// last fetch outcome, so the service window can show and manage them. A service
// with no rule set yet simply has no sources.
func (s *Server) listServiceSources(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	sv, err := store.One[store.Service](ctx, s.DB, `SELECT * FROM services WHERE id=$1`, r.PathValue("id"))
	if err != nil {
		return err
	}
	if sv.RuleSetID == nil || *sv.RuleSetID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"sources": []any{}, "fetches": []any{}})
		return nil
	}
	rsID := *sv.RuleSetID
	sources, err := store.Many[store.RuleSource](ctx, s.DB,
		`SELECT * FROM rule_sources WHERE rule_set_id=$1 ORDER BY created_at`, rsID)
	if err != nil {
		return err
	}
	type fetchRow struct {
		SourceID  string `db:"source_id" json:"source_id"`
		Status    string `db:"status" json:"status"`
		Entries   int    `db:"entries" json:"entries"`
		Error     string `db:"error" json:"error"`
		StartedAt string `db:"started_at" json:"started_at"`
	}
	fetches, _ := store.Many[fetchRow](ctx, s.DB, `
		SELECT source_id::text, status, entries, error, started_at::text
		FROM rule_fetches WHERE source_id IN (SELECT id FROM rule_sources WHERE rule_set_id=$1)
		ORDER BY started_at DESC LIMIT 30`, rsID)
	writeJSON(w, http.StatusOK, map[string]any{"sources": sources, "fetches": fetches})
	return nil
}

// addServiceSource attaches an auto-update source to a service and rebuilds so
// its domains are pulled in immediately.
func (s *Server) addServiceSource(w http.ResponseWriter, r *http.Request) error {
	var req sourceRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := validateSource(&req); err != nil {
		return err
	}
	ctx := r.Context()
	sv, err := store.One[store.Service](ctx, s.DB, `SELECT * FROM services WHERE id=$1`, r.PathValue("id"))
	if err != nil {
		return err
	}
	rsID, err := s.ensureServiceRuleSet(ctx, &sv)
	if err != nil {
		return err
	}
	src, err := store.One[store.RuleSource](ctx, s.DB, `
		INSERT INTO rule_sources (rule_set_id, name, type, url, repo, ref, path, mode, expected_sha256, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,true) RETURNING *`,
		rsID, req.Name, req.Type, req.URL, req.Repo, req.Ref, req.Path, req.Mode, req.ExpectedSHA256)
	if err != nil {
		return err
	}
	res, err := s.rebuildActivate(ctx, rsID)
	if err != nil {
		return err
	}
	s.audit(ctx, r, "service.source.added", "service", sv.ID, nil, src)
	writeJSON(w, http.StatusCreated, map[string]any{"source": src, "build": res})
	return nil
}

func (s *Server) deleteServiceSource(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	sv, err := store.One[store.Service](ctx, s.DB, `SELECT * FROM services WHERE id=$1`, r.PathValue("id"))
	if err != nil {
		return err
	}
	if sv.RuleSetID == nil || *sv.RuleSetID == "" {
		return notFound("source")
	}
	n, err := s.DB.ExecN(ctx, `DELETE FROM rule_sources WHERE id=$1 AND rule_set_id=$2`,
		r.PathValue("source_id"), *sv.RuleSetID)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("source")
	}
	res, err := s.rebuildActivate(ctx, *sv.RuleSetID)
	if err != nil {
		return err
	}
	s.audit(ctx, r, "service.source.deleted", "service", sv.ID, nil, map[string]any{"source_id": r.PathValue("source_id")})
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "build": res})
	return nil
}

// refreshService re-pulls every source and activates the result — the "обновить
// сейчас" button in the service window.
func (s *Server) refreshService(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	sv, err := store.One[store.Service](ctx, s.DB, `SELECT * FROM services WHERE id=$1`, r.PathValue("id"))
	if err != nil {
		return err
	}
	if sv.RuleSetID == nil || *sv.RuleSetID == "" {
		return badRequest("у сервиса пока нет источников для обновления")
	}
	res, err := s.rebuildActivate(ctx, *sv.RuleSetID)
	if err != nil {
		return err
	}
	s.audit(ctx, r, "service.refreshed", "service", sv.ID, nil,
		map[string]any{"added": res.Added, "removed": res.Removed, "status": res.Status})
	writeJSON(w, http.StatusOK, map[string]any{"build": res})
	return nil
}

func (s *Server) deleteService(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	n, err := s.DB.ExecN(r.Context(), `DELETE FROM services WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("service")
	}
	s.audit(r.Context(), r, "service.deleted", "service", id, nil, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	return nil
}

// translitRU maps Cyrillic to latin so a Russian service name still yields a
// usable slug instead of an empty one (which failed slugRe at the last step).
var translitRU = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "i", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if t, ok := translitRU[r]; ok {
			b.WriteString(t)
			prevDash = false
			continue
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 { // slugRe caps at 40 chars; trim a trailing dash left by the cut
		out = strings.Trim(out[:40], "-")
	}
	return out
}
