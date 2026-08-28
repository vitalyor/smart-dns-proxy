package api

import (
	"net/http"
	"regexp"
	"strings"

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
	}
	rows, err := store.Many[row](r.Context(), s.DB, `
		SELECT sv.*, rs.name AS rule_set_name, ig.name AS ingress_group_name, eg.name AS egress_group_name,
		       (SELECT count(*)::int FROM rule_entries re WHERE re.version_id = rs.active_version_id) AS rule_count,
		       rsv.content_hash AS rule_set_hash
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
		return badRequest("slug должен состоять из строчных латинских букв, цифр и дефисов")
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

func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
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
	return strings.Trim(b.String(), "-")
}
