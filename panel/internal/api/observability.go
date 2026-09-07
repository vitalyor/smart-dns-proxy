package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"smartdns/panel/internal/store"
)

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	type nodeStat struct {
		Role       string     `db:"role" json:"role"`
		Status     string     `db:"status" json:"status"`
		Count      int        `db:"count" json:"count"`
		LastSeenAt *time.Time `db:"last_seen" json:"last_seen"`
	}
	nodeStats, err := store.Many[nodeStat](ctx, s.DB,
		`SELECT role, status, count(*)::int AS count, max(last_seen_at) AS last_seen
		 FROM nodes GROUP BY role, status ORDER BY role, status`)
	if err != nil {
		return err
	}
	type svcStat struct {
		ID      string `db:"id" json:"id"`
		Name    string `db:"name" json:"name"`
		Slug    string `db:"slug" json:"slug"`
		Enabled bool   `db:"enabled" json:"enabled"`
		Rules   int    `db:"rules" json:"rules"`
		// Куда сервис выходит: страна и нода. Раньше здесь было имя группы —
		// оно ничего не говорило о том, из какой страны увидят пользователя.
		Egress    *string `db:"egress" json:"egress"`
		LastProbe *bool   `db:"last_probe" json:"last_probe"`
		LatencyMs *int    `db:"latency_ms" json:"latency_ms"`
	}
	svcStats, err := store.Many[svcStat](ctx, s.DB, `
		SELECT sv.id::text, sv.name, sv.slug, sv.enabled,
		  (SELECT count(*)::int FROM rule_entries re WHERE re.version_id = rs.active_version_id) AS rules,
		  (SELECT string_agg(DISTINCT COALESCE(NULLIF(n.country,''),'?'), ', ')
		     FROM service_nodes sn JOIN nodes n ON n.id = sn.node_id
		     WHERE sn.service_id = sv.id AND n.role = 'egress') AS egress,
		  (SELECT success FROM health_samples h WHERE h.service_id = sv.id ORDER BY observed_at DESC LIMIT 1) AS last_probe,
		  (SELECT latency_ms FROM health_samples h WHERE h.service_id = sv.id ORDER BY observed_at DESC LIMIT 1) AS latency_ms
		FROM services sv
		LEFT JOIN rule_sets rs ON rs.id = sv.rule_set_id
		ORDER BY sv.name`)
	if err != nil {
		return err
	}
	// Pointer so "no active revision" serialises as null, not {} — the setup
	// checklist keys "revision applied" off active_revision !== null.
	var activeRev *store.Revision
	if rev, err := store.One[store.Revision](ctx, s.DB,
		`SELECT * FROM revisions WHERE state IN ('active','partially_active') ORDER BY sequence DESC LIMIT 1`); err == nil {
		activeRev = &rev
	}
	pending, _ := store.Value[int](ctx, s.DB,
		`SELECT count(*)::int FROM rule_set_versions WHERE status='awaiting_approval'`)
	drift, _ := store.Value[int](ctx, s.DB, `
		SELECT count(*)::int FROM nodes
		WHERE desired_revision_id IS NOT NULL AND desired_revision_id IS DISTINCT FROM applied_revision_id`)
	stale, _ := store.Value[int](ctx, s.DB,
		`SELECT count(*)::int FROM nodes WHERE last_seen_at IS NULL OR last_seen_at < now() - interval '60 seconds'`)
	// Состояние выката одной строкой: сколько нод уже на назначенной
	// конфигурации и кто отстал. Раньше это можно было понять только косвенно,
	// по счётчику расхождения без имён.
	type deployNode struct {
		Name    string `db:"name" json:"name"`
		Role    string `db:"role" json:"role"`
		Applied *int64 `db:"applied_sequence" json:"applied_sequence"`
		Desired *int64 `db:"desired_sequence" json:"desired_sequence"`
		Behind  bool   `db:"behind" json:"behind"`
		Stale   bool   `db:"stale" json:"stale"`
	}
	deployNodes, _ := store.Many[deployNode](ctx, s.DB, `
		SELECT n.name, n.role, ar.sequence AS applied_sequence, dr.sequence AS desired_sequence,
		       (n.desired_revision_id IS NOT NULL
		        AND n.desired_revision_id IS DISTINCT FROM n.applied_revision_id) AS behind,
		       (n.last_seen_at IS NULL OR n.last_seen_at < now() - interval '60 seconds') AS stale
		FROM nodes n
		LEFT JOIN revisions ar ON ar.id = n.applied_revision_id
		LEFT JOIN revisions dr ON dr.id = n.desired_revision_id
		WHERE n.status <> 'disabled'
		ORDER BY n.role, n.name`)

	events, err := store.Many[store.Event](ctx, s.DB, `SELECT * FROM events ORDER BY created_at DESC LIMIT 20`)
	if err != nil {
		return err
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"nodes":                  nodeStats,
		"services":               svcStats,
		"active_revision":        activeRev,
		"pending_rule_approvals": pending,
		"nodes_with_drift":       drift,
		"nodes_stale":            stale,
		"deploy_nodes":           deployNodes,
		"events":                 events,
		"alerts":                 s.alerts(ctx, drift, stale, pending),
		"lab_mode":               s.Cfg.LabMode,
	})
	return nil
}

type alert struct {
	Level   string `json:"level"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
	// Action и Href — куда идти чинить. Тревога без ответа на вопрос «и что
	// теперь делать» бесполезна: оператор читает её и остаётся на месте.
	Action string `json:"action,omitempty"`
	Href   string `json:"href,omitempty"`
}

func (s *Server) alerts(ctx contextT, drift, stale, pending int) []alert {
	var out []alert
	if stale > 0 {
		names, _ := store.Value[string](ctx, s.DB, `SELECT COALESCE(string_agg(name, ', ' ORDER BY name),'')
			FROM nodes WHERE last_seen_at IS NULL OR last_seen_at < now() - interval '60 seconds'`)
		out = append(out, alert{"error", "heartbeat_stale",
			"Не отвечает " + listOf(names, stale, "нода", "ноды", "нод"),
			"Трафик идёт по последней рабочей конфигурации, пользователи пока ничего не замечают. " +
				"Проверьте, жив ли сервер и запущен ли на нём агент.",
			"Открыть ноды", "/nodes"})
	}
	if drift > 0 {
		names, _ := store.Value[string](ctx, s.DB, `SELECT COALESCE(string_agg(name, ', ' ORDER BY name),'')
			FROM nodes WHERE desired_revision_id IS NOT NULL
			  AND desired_revision_id IS DISTINCT FROM applied_revision_id`)
		out = append(out, alert{"warn", "revision_drift",
			"Работает на старой конфигурации: " + listOf(names, drift, "нода", "ноды", "нод"),
			"Панель досылает конфигурацию сама и обычно догоняет за минуту. " +
				"Если не проходит — нода недоступна или отклоняет доставку.",
			"Смотреть выкат", "/revisions"})
	}
	if pending > 0 {
		names, _ := store.Value[string](ctx, s.DB, `SELECT COALESCE(string_agg(DISTINCT rs.name, ', '),'')
			FROM rule_set_versions v JOIN rule_sets rs ON rs.id = v.rule_set_id
			WHERE v.status = 'awaiting_approval'`)
		out = append(out, alert{"info", "rules_awaiting_approval",
			"Ждут вашей проверки " + listOf(names, pending, "список доменов", "списка доменов", "списков доменов"),
			"В них изменилась сразу большая часть доменов — больше трети, — и панель не стала применять " +
				"такое молча. Пока вы не подтвердите, ноды работают по прежним спискам.",
			"Посмотреть изменения", "/rule-sets"})
	}
	if s.Cfg.LabMode {
		out = append(out, alert{"warn", "lab_mode",
			"Включён лабораторный режим",
			"Точка выхода может обращаться к приватным адресам внутри сети. Это режим для стенда: " +
				"перед боевой работой выключите LAB_MODE.", "", ""})
	}
	// Плановое обновление списка поднимает активную версию, но само на ноды
	// ничего не выкатывает — и до сих пор об этом никто не сообщал. Список
	// молча обновлялся, ноды продолжали резолвить по старому, а «расхождение»
	// оставалось нулевым, потому что назначенную конфигурацию они применили.
	undeployed, _ := store.Value[int](ctx, s.DB, `
		SELECT count(*)::int
		FROM rule_sets rs
		JOIN rule_set_versions v ON v.id = rs.active_version_id
		JOIN services sv ON sv.rule_set_id = rs.id AND sv.enabled
		WHERE v.created_at > COALESCE(
			(SELECT created_at FROM revisions WHERE state = 'active' ORDER BY sequence DESC LIMIT 1),
			'epoch'::timestamptz)`)
	if undeployed > 0 {
		out = append(out, alert{"warn", "rules_not_deployed",
			fmt.Sprintf("Изменения в %s не доехали до нод", plural(undeployed, "списке", "списках", "списках")),
			"Списки обновились уже после последнего выката, а ноды резолвят по прежним. " +
				"Соберите конфигурацию — и изменения применятся без перезапуска.",
			"Собрать и выкатить", "/revisions"})
	}

	staleRules, _ := store.Value[int](ctx, s.DB, `
		SELECT count(*)::int FROM rule_sets
		WHERE update_mode <> 'manual_only'
		  AND (last_fetch_at IS NULL OR last_fetch_at < now() - (interval_sec * 2) * interval '1 second')`)
	if staleRules > 0 {
		out = append(out, alert{"warn", "rules_stale",
			fmt.Sprintf("Не удаётся обновить %s доменов", plural(staleRules, "список", "списка", "списков")),
			"Источник не отвечает дольше двух интервалов обновления. На работу это пока не влияет: " +
				"действует последняя успешно загруженная версия.",
			"Открыть списки", "/rule-sets"})
	}
	if out == nil {
		out = []alert{}
	}
	return out
}

func (s *Server) healthSummary(w http.ResponseWriter, r *http.Request) error {
	type row struct {
		NodeID     string     `db:"id" json:"node_id"`
		Name       string     `db:"name" json:"name"`
		Role       string     `db:"role" json:"role"`
		Status     string     `db:"status" json:"status"`
		LastSeenAt *time.Time `db:"last_seen_at" json:"last_seen_at"`
		Success    *int       `db:"success" json:"success_last_hour"`
		Failure    *int       `db:"failure" json:"failure_last_hour"`
		AvgLatency *float64   `db:"avg_latency" json:"avg_latency_ms"`
	}
	rows, err := store.Many[row](r.Context(), s.DB, `
		SELECT n.id::text, n.name, n.role, n.status, n.last_seen_at,
		  count(h.*) FILTER (WHERE h.success)::int AS success,
		  count(h.*) FILTER (WHERE NOT h.success)::int AS failure,
		  avg(h.latency_ms) FILTER (WHERE h.success AND h.latency_ms > 0) AS avg_latency
		FROM nodes n
		LEFT JOIN health_samples h ON h.node_id = n.id AND h.observed_at > now() - interval '1 hour'
		GROUP BY n.id ORDER BY n.role, n.name`)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
	return nil
}

func (s *Server) healthSamples(w http.ResponseWriter, r *http.Request) error {
	limit := clampInt(r.URL.Query().Get("limit"), 100, 1, 1000)
	nodeID := r.URL.Query().Get("node_id")
	rows, err := store.Many[store.HealthSample](r.Context(), s.DB, `
		SELECT * FROM health_samples
		WHERE ($1 = '' OR node_id = $1::uuid)
		ORDER BY observed_at DESC LIMIT $2`, nodeID, limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
	return nil
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) error {
	limit := clampInt(r.URL.Query().Get("limit"), 100, 1, 500)
	before := r.URL.Query().Get("cursor")
	rows, err := store.Many[store.Event](r.Context(), s.DB, `
		SELECT * FROM events WHERE ($1 = '' OR id < $1::bigint)
		ORDER BY id DESC LIMIT $2`, before, limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows, "next_cursor": cursorOf(rows)})
	return nil
}

func cursorOf(rows []store.Event) string {
	if len(rows) == 0 {
		return ""
	}
	return strconv.FormatInt(rows[len(rows)-1].ID, 10)
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) error {
	limit := clampInt(r.URL.Query().Get("limit"), 100, 1, 500)
	before := r.URL.Query().Get("cursor")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := store.Many[store.AuditEvent](r.Context(), s.DB, `
		SELECT * FROM audit_events
		WHERE ($1 = '' OR id < $1::bigint)
		  AND ($3 = '' OR action ILIKE '%'||$3||'%' OR actor ILIKE '%'||$3||'%' OR object_type ILIKE '%'||$3||'%')
		ORDER BY id DESC LIMIT $2`, before, limit, q)
	if err != nil {
		return err
	}
	next := ""
	if len(rows) > 0 {
		next = strconv.FormatInt(rows[len(rows)-1].ID, 10)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows, "next_cursor": next})
	return nil
}

func clampInt(s string, def, lo, hi int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// plural подбирает русскую форму по числу: 1 нода, 2 ноды, 5 нод.
func plural(n int, one, few, many string) string {
	w := many
	switch {
	case n%10 == 1 && n%100 != 11:
		w = one
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		w = few
	}
	return fmt.Sprintf("%d %s", n, w)
}

// listOf называет объекты поимённо, пока их немного: «нода usa» понятнее, чем
// «1 нода». Когда их много, имена не помещаются и остаётся счёт.
func listOf(names string, n int, one, few, many string) string {
	if names == "" || n > 3 {
		return plural(n, one, few, many)
	}
	return plural(n, one, few, many) + " — " + names
}
