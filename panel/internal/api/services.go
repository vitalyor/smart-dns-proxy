package api

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"smartdns/panel/internal/rules"
	"smartdns/panel/internal/store"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$`)

func (s *Server) listServices(w http.ResponseWriter, r *http.Request) error {
	type row struct {
		store.Service
		RuleSetName *string `db:"rule_set_name" json:"rule_set_name"`
		// Ноды сервиса в том порядке, в котором их пробует точка входа.
		Nodes       []svcNode `db:"nodes" json:"nodes"`
		RuleCount   *int      `db:"rule_count" json:"rule_count"`
		RuleSetHash *string   `db:"rule_set_hash" json:"rule_set_hash"`
		// True when a probe hostname is set but is not among the service's managed
		// domains — such a probe hits the SNI proxy as "unmanaged" and always fails.
		ProbeInSet bool `db:"probe_in_set" json:"probe_in_set"`
		// Domains are the service's own hand-entered list, surfaced so the service
		// window can edit them directly — no separate "lists" page.
		Domains []string `db:"domains" json:"domains"`
	}
	rows, err := store.Many[row](r.Context(), s.DB, `
		SELECT sv.*, rs.name AS rule_set_name,
		       COALESCE((SELECT jsonb_agg(jsonb_build_object(
		           'id', n.id, 'name', n.name, 'role', n.role,
		           'country', COALESCE(n.country,''), 'status', n.status)
		         ORDER BY sn.priority, n.name)
		         FROM service_nodes sn JOIN nodes n ON n.id = sn.node_id
		         WHERE sn.service_id = sv.id), '[]'::jsonb) AS nodes,
		       (SELECT count(*)::int FROM rule_entries re WHERE re.version_id = rs.active_version_id) AS rule_count,
		       rsv.content_hash AS rule_set_hash,
		       COALESCE(rs.manual_include, '{}') AS domains,
		       COALESCE(sv.probe->>'hostname','') = '' OR EXISTS(
		         SELECT 1 FROM rule_entries re
		         WHERE re.version_id = rs.active_version_id AND re.value = sv.probe->>'hostname') AS probe_in_set
		FROM services sv
		LEFT JOIN rule_sets rs ON rs.id = sv.rule_set_id
		LEFT JOIN rule_set_versions rsv ON rsv.id = rs.active_version_id
		ORDER BY sv.name`)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
	return nil
}

// searchServices отвечает на вопрос «какой сервис отвечает за этот домен».
//
// Ищем по правилам активной версии, а не по ручному списку сервиса: у части
// сервисов домены приходят из источников и регулярок, и поиск по ручному
// списку молчал бы там, где совпадение есть.
//
// Два вида совпадения. Подстрока — чтобы «goog» показывал всё гугловое. И
// обратное вхождение: запрос «www.google.com» должен находить сервис, где
// записан «google.com», потому что домен покрывает поддомены. Без второго
// поиск отвечал бы «никто не обслуживает» ровно там, где обслуживает.
func (s *Server) searchServices(w http.ResponseWriter, r *http.Request) error {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	q = strings.TrimPrefix(q, "*.")
	q = strings.Trim(q, ".")
	if len([]rune(q)) < 2 {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return nil
	}
	// % и _ — подстановочные знаки LIKE: без экранирования «_» совпадал бы с
	// любым символом, а «%» — с любым остатком.
	esc := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(q)
	// Обратное вхождение имеет смысл только для похожего на имя хоста запроса.
	host := ""
	if validHostname(q) {
		host = q
	}
	type row struct {
		ID      string   `db:"id" json:"id"`
		Name    string   `db:"name" json:"name"`
		Slug    string   `db:"slug" json:"slug"`
		Matched []string `db:"matched" json:"matched"`
		Total   int      `db:"total" json:"total"`
	}
	rows, err := store.Many[row](r.Context(), s.DB, `
		SELECT sv.id::text, sv.name, sv.slug,
		       (array_agg(DISTINCT re.value))[1:8] AS matched,
		       count(DISTINCT re.value)::int AS total
		FROM services sv
		JOIN rule_sets rs ON rs.id = sv.rule_set_id
		JOIN rule_entries re ON re.version_id = rs.active_version_id
		WHERE re.kind IN ('exact','suffix','regex')
		  AND (re.value LIKE '%' || $1 || '%' ESCAPE '\'
		       OR ($2 <> '' AND re.kind IN ('exact','suffix')
		           AND ($2 = re.value OR $2 LIKE '%.' || re.value)))
		GROUP BY sv.id, sv.name, sv.slug
		ORDER BY sv.name`, esc, host)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
	return nil
}

type serviceRequest struct {
	Name         string         `json:"name"`
	Slug         string         `json:"slug"`
	Description  string         `json:"description"`
	Enabled      *bool          `json:"enabled"`
	RuleSetID    *string        `json:"rule_set_id"`
	AllowedPorts []int32        `json:"allowed_ports"`
	UDPMode      string         `json:"udp_mode"`
	DNSTTL       int            `json:"dns_ttl"`
	Priority     int            `json:"priority"`
	Notes        string         `json:"notes"`
	Probe        map[string]any `json:"probe"`
	// NodeIDs — ноды сервиса в порядке предпочтения: первая живая и берётся.
	// Групп больше нет, список принадлежит сервису.
	NodeIDs []string `json:"node_ids"`
}

// svcNode — нода сервиса в ответе API.
type svcNode struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Country string `json:"country"`
	Status  string `json:"status"`
}

// checkServiceNodes не пускает в один сервис выходные ноды разных стран.
//
// Ради этого всё и затевалось: список — это порядок отказа, и если в нём рядом
// Германия и Испания, то падение первой ноды молча меняет страну под живым
// аккаунтом. Сайт видит переезд, и claim прилетает не нам, а владельцу
// аккаунта. Пусть лучше сервис ждёт и горит тревогой.
func (s *Server) checkServiceNodes(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	type row struct {
		Role    string `db:"role"`
		Country string `db:"country"`
		Name    string `db:"name"`
	}
	rows, err := store.Many[row](ctx, s.DB,
		`SELECT role, COALESCE(country,'') AS country, name FROM nodes WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	if len(rows) != len(ids) {
		return badRequest("в списке нод сервиса есть несуществующая нода")
	}
	byCountry := map[string][]string{}
	for _, r := range rows {
		// Входная нода в списке значит «выходить прямо отсюда, без туннеля».
		// Страна у неё учитывается наравне с остальными: смешать выход с входа
		// и заграничный выход — это та же смена географии под одним аккаунтом.
		byCountry[r.Country] = append(byCountry[r.Country], r.Name)
	}
	if len(byCountry) > 1 {
		var parts []string
		for c, names := range byCountry {
			label := c
			if label == "" {
				label = "страна не указана"
			}
			parts = append(parts, label+": "+strings.Join(names, ", "))
		}
		sort.Strings(parts)
		return badRequest("ноды выхода одного сервиса должны быть из одной страны, а выбраны разные — %s. "+
			"Иначе отказ основной ноды переносит трафик в другую страну, и сайт видит смену географии под тем же аккаунтом",
			strings.Join(parts, "; "))
	}
	return nil
}

// setServiceNodes заменяет список нод сервиса целиком.
//
// Одним оператором, чтобы сбой на середине не оставил сервис без нод. Но не
// «удалить всё и вставить заново»: части одного оператора работают на общем
// снимке данных, и вставка не видит только что удалённых строк — уникальный
// ключ срабатывает на них же. Поэтому вставка с обновлением приоритета, а
// удаление — только тех, кого в новом списке нет.
func (s *Server) setServiceNodes(ctx context.Context, serviceID string, ids []string) error {
	_, err := s.DB.Exec(ctx, `
		WITH ins AS (
			INSERT INTO service_nodes (service_id, node_id, priority)
			SELECT $1, x.node_id, x.ord FROM unnest($2::uuid[]) WITH ORDINALITY AS x(node_id, ord)
			ON CONFLICT (service_id, node_id) DO UPDATE SET priority = EXCLUDED.priority
			RETURNING node_id
		)
		DELETE FROM service_nodes WHERE service_id=$1 AND node_id <> ALL($2::uuid[])`,
		serviceID, ids)
	return err
}

// checkUDPMode accepts only what the data plane actually implements. proxy и
// separate_ip остались в схеме от задуманного UDP-прокси, которого в нодах нет:
// принимать их значило бы обещать поведение, которого не будет.
func checkUDPMode(m *string) error {
	if m == nil || *m == "" {
		return nil
	}
	if *m != "disabled_fallback" {
		return badRequest("режим UDP %q пока не поддерживается нодами: доступен только disabled_fallback", *m)
	}
	return nil
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
	if err := checkUDPMode(&req.UDPMode); err != nil {
		return err
	}
	if req.Probe == nil {
		req.Probe = map[string]any{}
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if err := s.checkServiceNodes(r.Context(), req.NodeIDs); err != nil {
		return err
	}
	// Сервис и его ноды создаются одним запросом: сервис без нод не собирается
	// в ревизию, и оставлять такой огрызок после половины операции незачем.
	sv, err := store.One[store.Service](r.Context(), s.DB, `
		WITH s AS (
			INSERT INTO services (name, slug, description, enabled, rule_set_id,
				allowed_ports, udp_mode, dns_ttl, priority, notes, probe)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING *
		), n AS (
			INSERT INTO service_nodes (service_id, node_id, priority)
			SELECT s.id, x.node_id, x.ord FROM s, unnest($12::uuid[]) WITH ORDINALITY AS x(node_id, ord)
		)
		SELECT * FROM s`,
		req.Name, req.Slug, req.Description, enabled, req.RuleSetID,
		req.AllowedPorts, req.UDPMode, req.DNSTTL, req.Priority, req.Notes, req.Probe, req.NodeIDs)
	if err != nil {
		return err
	}
	s.audit(r.Context(), r, "service.created", "service", sv.ID, nil, sv)
	writeJSON(w, http.StatusCreated, sv)
	return nil
}

func (s *Server) patchService(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Name         *string        `json:"name"`
		Description  *string        `json:"description"`
		Enabled      *bool          `json:"enabled"`
		RuleSetID    *string        `json:"rule_set_id"`
		AllowedPorts []int32        `json:"allowed_ports"`
		UDPMode      *string        `json:"udp_mode"`
		DNSTTL       *int           `json:"dns_ttl"`
		Priority     *int           `json:"priority"`
		Notes        *string        `json:"notes"`
		Probe        map[string]any `json:"probe"`
		// Указатель, а не срез: отсутствие поля — «не трогать список»,
		// пустой список — «убрать все ноды».
		NodeIDs *[]string `json:"node_ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.DNSTTL != nil && (*req.DNSTTL < 30 || *req.DNSTTL > 300) {
		return badRequest("TTL должен быть в диапазоне 30–300 секунд")
	}
	if err := checkUDPMode(req.UDPMode); err != nil {
		return err
	}
	if req.NodeIDs != nil {
		if err := s.checkServiceNodes(r.Context(), *req.NodeIDs); err != nil {
			return err
		}
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
			rule_set_id=COALESCE($6,rule_set_id), allowed_ports=COALESCE($7,allowed_ports),
			udp_mode=COALESCE($8,udp_mode), dns_ttl=COALESCE($9,dns_ttl), priority=COALESCE($10,priority),
			notes=COALESCE($11,notes), probe=COALESCE($12,probe),
			updated_at=now(), version=version+1
		WHERE id=$1 AND ($2 = 0 OR version = $2)`,
		id, ver, req.Name, req.Description, req.Enabled, req.RuleSetID,
		req.AllowedPorts, req.UDPMode, req.DNSTTL, req.Priority, req.Notes, req.Probe)
	if err != nil {
		return err
	}
	if err := checkVersion(n, ver); err != nil {
		return err
	}
	if req.NodeIDs != nil {
		if err := s.setServiceNodes(r.Context(), id, *req.NodeIDs); err != nil {
			return err
		}
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
