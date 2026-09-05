package api

import (
	"net/http"
	"strings"

	"smartdns/panel/internal/rules"
	"smartdns/panel/internal/store"
)

// catalogEntry is a ready-made service the wizard can create in one click:
// a built-in domain preset plus a sensible probe host.
type catalogEntry struct {
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Preset    string `json:"preset"`
	ProbeHost string `json:"probe_host"`
	Domains   int    `json:"domains"`
}

// catalog is the shipped service catalogue. Preset must match a file under
// rules/presets. Kept in code because it changes with releases, not at runtime.
var catalog = []catalogEntry{
	{Slug: "chatgpt", Name: "ChatGPT", Preset: "openai", ProbeHost: "chatgpt.com"},
	{Slug: "gemini", Name: "Gemini", Preset: "gemini", ProbeHost: "gemini.google.com"},
	{Slug: "claude", Name: "Claude", Preset: "claude", ProbeHost: "claude.ai"},
	{Slug: "cursor", Name: "Cursor", Preset: "cursor", ProbeHost: "cursor.com"},
}

func (s *Server) serviceCatalog(w http.ResponseWriter, r *http.Request) error {
	presets := rules.Presets()
	out := make([]catalogEntry, 0, len(catalog))
	for _, c := range catalog {
		c.Domains = countDomains(presets[c.Preset])
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
	return nil
}

func countDomains(body string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			n++
		}
	}
	return n
}

type wizardRequest struct {
	Name           string  `json:"name"`
	Slug           string  `json:"slug"`
	IngressGroupID *string `json:"ingress_group_id"`
	EgressGroupID  *string `json:"egress_group_id"`
	// Domain sources (any combination). Manual lines go straight into the
	// rule set's manual_include; the rest become fetched sources.
	Domains []string `json:"domains"`
	Preset  string   `json:"preset"`
	Repo    string   `json:"repo"`
	Path    string   `json:"path"`
	Ref     string   `json:"ref"`
	URL     string   `json:"url"`
	// Service tuning (optional; defaults applied when zero).
	DNSTTL       int     `json:"dns_ttl"`
	Priority     int     `json:"priority"`
	AllowedPorts []int32 `json:"allowed_ports"`
	UDPMode      string  `json:"udp_mode"`
	ProbeHost    string  `json:"probe_host"`
}

// serviceWizard creates a rule set (with its sources), builds and activates the
// first version, and creates the service bound to it — all from one request, so
// the operator never has to visit three pages to turn a service on.
func (s *Server) serviceWizard(w http.ResponseWriter, r *http.Request) error {
	var req wizardRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return badRequest("укажите название сервиса")
	}
	slug := req.Slug
	if slug == "" {
		slug = slugify(req.Name)
	}
	if !slugRe.MatchString(slug) {
		return badRequest("Идентификатор строится из названия и может содержать только латиницу, цифры и дефисы — задайте название латиницей")
	}
	manual := cleanLines(req.Domains)
	hasRemote := req.Preset != "" || req.Repo != "" || req.URL != ""
	if len(manual) == 0 && !hasRemote {
		return badRequest("добавьте хотя бы один домен: списком, из каталога, с GitHub или по ссылке")
	}
	if req.Preset != "" {
		if _, ok := rules.Presets()[req.Preset]; !ok {
			return badRequest("встроенный список не найден: %s", req.Preset)
		}
	}
	if req.URL != "" && !strings.HasPrefix(req.URL, "https://") {
		return badRequest("разрешены только HTTPS-ссылки")
	}
	if req.Repo != "" && req.Path == "" {
		return badRequest("для GitHub укажите путь к файлу")
	}

	ctx := r.Context()
	updateMode := "manual_only"
	if hasRemote {
		updateMode = "auto_apply"
	}
	rs, err := store.One[store.RuleSet](ctx, s.DB, `
		INSERT INTO rule_sets (name, description, update_mode, interval_sec, allow_regex, priority, manual_include, manual_exclude)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING *`,
		req.Name, "Домены сервиса "+req.Name, updateMode, 21600, false, 100, nonNil(manual), []string{})
	if err != nil {
		return err
	}

	// On any failure past this point, drop the half-built rule set so the wizard
	// can be retried cleanly.
	fail := func(e error) error {
		_, _ = s.DB.Exec(ctx, `DELETE FROM rule_sets WHERE id=$1`, rs.ID)
		return e
	}

	addSrc := func(name, typ, url, repo, ref, path string) error {
		_, err := s.DB.Exec(ctx, `
			INSERT INTO rule_sources (rule_set_id, name, type, url, repo, ref, path, mode, expected_sha256, enabled)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'include','',true)`,
			rs.ID, name, typ, url, repo, ref, path)
		return err
	}
	if req.Preset != "" {
		if err := addSrc("Каталог: "+req.Preset, "preset", "", "", "", req.Preset); err != nil {
			return fail(err)
		}
	}
	if req.Repo != "" {
		if err := addSrc(req.Repo, "github_repo", "", req.Repo, orDefault(req.Ref, "main"), req.Path); err != nil {
			return fail(err)
		}
	}
	if req.URL != "" {
		if err := addSrc(req.URL, "https", req.URL, "", "", ""); err != nil {
			return fail(err)
		}
	}

	if _, err := s.builder().Build(ctx, rs.ID); err != nil {
		return fail(errorf(http.StatusBadGateway, "build_failed", "не удалось собрать список доменов: %v", err))
	}

	ttl := req.DNSTTL
	if ttl == 0 {
		ttl = 60
	}
	if ttl < 30 || ttl > 300 {
		return fail(badRequest("TTL должен быть в диапазоне 30–300 секунд"))
	}
	priority := req.Priority
	if priority == 0 {
		priority = 100
	}
	ports := req.AllowedPorts
	if len(ports) == 0 {
		ports = []int32{443}
	}
	udp := orDefault(req.UDPMode, "disabled_fallback")
	if err := checkUDPMode(&udp); err != nil {
		return fail(err)
	}
	probe := map[string]any{}
	if h := strings.TrimSpace(req.ProbeHost); h != "" {
		probe = map[string]any{"hostname": h, "port": 443}
	}

	sv, err := store.One[store.Service](ctx, s.DB, `
		INSERT INTO services (name, slug, description, enabled, rule_set_id, ingress_group_id,
			egress_group_id, allowed_ports, udp_mode, dns_ttl, priority, notes, probe)
		VALUES ($1,$2,'',true,$3,$4,$5,$6,$7,$8,$9,'',$10) RETURNING *`,
		req.Name, slug, rs.ID, req.IngressGroupID, req.EgressGroupID, ports, udp, ttl, priority, probe)
	if err != nil {
		return fail(err)
	}
	s.audit(ctx, r, "service.wizard", "service", sv.ID, nil,
		map[string]any{"rule_set_id": rs.ID, "slug": slug})
	writeJSON(w, http.StatusCreated, map[string]any{"service": sv, "rule_set": rs})
	return nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func cleanLines(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range in {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || seen[line] {
				continue
			}
			seen[line] = true
			out = append(out, line)
		}
	}
	return out
}
