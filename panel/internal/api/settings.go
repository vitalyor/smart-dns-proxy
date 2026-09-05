package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"smartdns/shared/logging"
)

// defaultSettings seed a fresh installation; the wizard overwrites them.
var defaultSettings = map[string]any{
	"dns_upstream":         "unbound:53",
	"dns_access_mode":      "allowlist",
	"dns_allowed_cidrs":    []string{},
	"dns_rate_limit_qps":   50,
	"dns_rate_limit_burst": 250,
	"dns_max_concurrent":   2048,
	"doh_hostname":         "",
	"dot_hostname":         "",
	"doh_path":             "/dns-query",
	"publish_aaaa":         "false",
	"egress_resolver":      "1.1.1.1:53",
	"timezone":             "Europe/Moscow",
	"quic_policy":          "disabled_fallback",
	"backup_dir":           "/var/backups/smartdns",
	"log_level":            "info",
	"node_log_level":       "info",
	// Подписка: общий лимит устройств и адрес публичной страницы, из которого
	// собирается ссылка {адрес}/{short_id} (ADR 0012).
	"device_limit_default":  3,
	"subscription_page_url": "",
	// Оформление публичной страницы: имя над заголовком и контакт поддержки
	// внизу. Меняются на лету — страница читает их из статуса подписчика.
	"subscription_brand":   "",
	"subscription_support": "",
}

// SeedSettings inserts missing defaults without touching existing values.
func (s *Server) SeedSettings(ctx contextT) error {
	for k, v := range defaultSettings {
		b, _ := json.Marshal(v)
		if _, err := s.DB.Exec(ctx,
			`INSERT INTO settings (key, value) VALUES ($1,$2) ON CONFLICT (key) DO NOTHING`, k, b); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) error {
	type row struct {
		Key   string          `db:"key"`
		Value json.RawMessage `db:"value"`
	}
	rows, err := s.DB.Query(r.Context(), `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		// Path tokens are stored hashed, but never expose them at all.
		if k == "doh_path_tokens" {
			var list []string
			_ = json.Unmarshal(v, &list)
			b, _ := json.Marshal(len(list))
			out["doh_path_token_count"] = b
			continue
		}
		out[k] = v
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": out, "lab_mode": s.Cfg.LabMode, "version": s.Cfg.Version})
	return nil
}

var settableKeys = map[string]bool{
	"dns_upstream": true, "dns_access_mode": true, "dns_allowed_cidrs": true,
	"dns_rate_limit_qps": true, "dns_rate_limit_burst": true, "dns_max_concurrent": true,
	"doh_hostname": true, "dot_hostname": true, "doh_path": true,
	"publish_aaaa": true, "egress_resolver": true, "timezone": true,
	"quic_policy": true, "backup_dir": true,
	"log_level": true, "node_log_level": true,
	"device_limit_default": true, "subscription_page_url": true,
	"subscription_brand": true, "subscription_support": true,
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) error {
	var req map[string]json.RawMessage
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	// Сначала проверяем всё, и только потом пишем: раньше цикл успевал записать
	// часть полей и упасть на следующем, оставляя настройки полусохранёнными.
	for k, v := range req {
		if !settableKeys[k] {
			return badRequest("параметр %q нельзя изменить через API", k)
		}
		if err := validateSetting(k, v); err != nil {
			return err
		}
	}
	ctx := r.Context()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for k, v := range req {
		if _, err := tx.Exec(ctx,
			`INSERT INTO settings (key, value) VALUES ($1,$2)
			 ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, k, []byte(v)); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	// The panel's own level takes effect immediately; node levels travel with
	// the next revision, so the change stays auditable and reversible.
	if v, ok := req["log_level"]; ok && s.Cfg.Level != nil {
		var lvl string
		_ = json.Unmarshal(v, &lvl)
		_ = s.Cfg.Level.Set(lvl)
	}
	s.audit(ctx, r, "settings.updated", "settings", "", nil, req)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
	return nil
}

// validateSetting rejects a value before anything is written.
func validateSetting(k string, v json.RawMessage) error {
	switch k {
	case "dns_access_mode":
		var mode string
		_ = json.Unmarshal(v, &mode)
		if !contains([]string{"allowlist", "doh-token", "mtls", "restricted-public-dot"}, mode) {
			return badRequest("недопустимый режим доступа к DNS")
		}
	case "log_level", "node_log_level":
		var lvl string
		_ = json.Unmarshal(v, &lvl)
		if _, err := logging.Parse(lvl); err != nil {
			return badRequest("недопустимый уровень логирования %q: допустимы debug, info, warn, error", lvl)
		}
	case "device_limit_default":
		var n int
		if err := json.Unmarshal(v, &n); err != nil || n < 1 {
			return badRequest("лимит устройств по умолчанию — целое число не меньше 1")
		}
	case "subscription_page_url":
		var u string
		_ = json.Unmarshal(v, &u)
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return badRequest("адрес страницы подписки должен начинаться с http:// или https://")
		}
	}
	return nil
}

func (s *Server) appendSettingList(ctx contextT, key, value string) error {
	list := getSettingList(ctx, s.DB, key)
	for _, v := range list {
		if strings.EqualFold(v, value) {
			return nil
		}
	}
	list = append(list, value)
	b, _ := json.Marshal(list)
	_, err := s.DB.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1,$2)
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, key, b)
	return err
}

func (s *Server) removeSettingList(ctx contextT, key, value string) error {
	list := getSettingList(ctx, s.DB, key)
	out := list[:0]
	for _, v := range list {
		if !strings.EqualFold(v, value) {
			out = append(out, v)
		}
	}
	b, _ := json.Marshal(out)
	_, err := s.DB.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1,$2)
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, key, b)
	return err
}

// ApplyStoredLogLevel restores the level chosen in the panel after a restart.
// A local LOG_LEVEL still wins, so an operator debugging the container is not
// overridden by the stored value.
func (s *Server) ApplyStoredLogLevel(ctx contextT) {
	if s.Cfg.Level == nil {
		return
	}
	s.Cfg.Level.Follow(getSetting(ctx, s.DB, "log_level", ""))
}
