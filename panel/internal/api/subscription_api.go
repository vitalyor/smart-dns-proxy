package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"smartdns/panel/internal/store"
)

// Endpoints the subscription page service calls with a scoped key. They are
// always addressed by the subscriber's short_id, so the key alone never lets the
// service walk other subscribers (ADR 0012).

type subDeviceView struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Type         string     `json:"type"`
	CreatedAt    time.Time  `json:"created_at"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	QueriesTotal int64      `json:"queries_total"`
	DoHURL       string     `json:"doh_url,omitempty"`
	DoTHost      string     `json:"dot_host,omitempty"`
}

func (s *Server) subscriberByShortID(r *http.Request) (store.Subscriber, error) {
	return store.One[store.Subscriber](r.Context(), s.DB,
		`SELECT * FROM subscribers WHERE short_id=$1`, r.PathValue("short_id"))
}

// subStatus returns the subscriber and their devices. Enabled/expired state is
// reported rather than hidden, so the page can explain why access stopped.
func (s *Server) subStatus(w http.ResponseWriter, r *http.Request) error {
	sub, err := s.subscriberByShortID(r)
	if err != nil {
		return notFound("subscriber")
	}
	ctx := r.Context()
	devs, err := store.Many[store.DeviceProfile](ctx, s.DB,
		`SELECT * FROM device_profiles WHERE subscriber_id=$1 ORDER BY created_at`, sub.ID)
	if err != nil {
		return err
	}
	out := make([]subDeviceView, 0, len(devs))
	for _, d := range devs {
		doh, dot, _ := deviceAddressesFull(d)
		out = append(out, subDeviceView{ID: d.ID, Name: d.Name, Type: d.Type, CreatedAt: d.CreatedAt,
			LastSeenAt: d.LastSeenAt, QueriesTotal: d.QueriesTotal, DoHURL: doh, DoTHost: dot})
	}
	limit := s.defaultDeviceLimit(ctx)
	if sub.DeviceLimit != nil {
		limit = *sub.DeviceLimit
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":         sub.Name,
		"enabled":      sub.Enabled,
		"expires_at":   sub.ExpiresAt,
		"active":       subscriberActive(sub),
		"device_limit": limit,
		"queries_used": sub.QueriesUsed,
		"query_limit":  sub.QueryLimit,
		"devices":      out,
		"types":        deviceTypes,
	})
	return nil
}

func subscriberActive(sub store.Subscriber) bool {
	if !sub.Enabled {
		return false
	}
	if sub.ExpiresAt != nil && sub.ExpiresAt.Before(time.Now()) {
		return false
	}
	if sub.QueryLimit != nil && sub.QueriesUsed >= *sub.QueryLimit {
		return false
	}
	return true
}

// subAddDevice creates a device for the subscriber. The limit check and the
// insert happen inside one transaction with the subscriber row locked: checking
// the count and then inserting without a lock is exactly the race that let
// Remnawave users exceed their device limit (CVE-2026-39880).
func (s *Server) subAddDevice(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return badRequest("укажите название устройства")
	}
	if !contains(deviceTypes, req.Type) {
		return badRequest("недопустимый тип устройства")
	}
	ctx := r.Context()
	cfg, _, tokenHash := s.buildDeviceConfig(ctx, req.Type)
	b, _ := json.Marshal(cfg)

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var (
		id      string
		enabled bool
		expires *time.Time
		lim     *int
	)
	if err := tx.QueryRow(ctx,
		`SELECT id, enabled, expires_at, device_limit FROM subscribers WHERE short_id=$1 FOR UPDATE`,
		r.PathValue("short_id")).Scan(&id, &enabled, &expires, &lim); err != nil {
		return notFound("subscriber")
	}
	if !enabled {
		return errorf(http.StatusForbidden, "disabled", "подписка отключена")
	}
	if expires != nil && expires.Before(time.Now()) {
		return errorf(http.StatusForbidden, "expired", "срок подписки истёк")
	}
	limit := s.defaultDeviceLimit(ctx)
	if lim != nil {
		limit = *lim
	}
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM device_profiles WHERE subscriber_id=$1`, id).Scan(&n); err != nil {
		return err
	}
	if n >= limit {
		return errorf(http.StatusConflict, "device_limit",
			"достигнут лимит устройств: %d. Удалите ненужное устройство.", limit)
	}
	var devID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO device_profiles (name, type, config, token_hash, subscriber_id)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		req.Name, req.Type, b, tokenHash, id).Scan(&devID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Nothing to publish here: the access reconcile recomputes the live set from
	// device_profiles on the next poll, so there is one source of truth.
	p, err := store.One[store.DeviceProfile](ctx, s.DB, `SELECT * FROM device_profiles WHERE id=$1`, devID)
	if err != nil {
		return err
	}
	doh, dot, _ := deviceAddressesFull(p)
	s.audit(ctx, r, "subscriber.device.added", "device_profile", devID, nil,
		map[string]any{"subscriber": id, "type": req.Type})
	writeJSON(w, http.StatusCreated, map[string]any{
		"device": subDeviceView{ID: p.ID, Name: p.Name, Type: p.Type, CreatedAt: p.CreatedAt,
			DoHURL: doh, DoTHost: dot},
		// Honest about propagation: the token still has to reach the nodes.
		"pending": true,
	})
	return nil
}

func (s *Server) subDeleteDevice(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	sub, err := s.subscriberByShortID(r)
	if err != nil {
		return notFound("subscriber")
	}
	n, err := s.DB.ExecN(ctx, `DELETE FROM device_profiles WHERE id=$1 AND subscriber_id=$2`,
		r.PathValue("device_id"), sub.ID)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("device")
	}
	s.audit(ctx, r, "subscriber.device.deleted", "device_profile", r.PathValue("device_id"), nil,
		map[string]any{"subscriber": sub.ID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	return nil
}

// subDeviceConfig hands the person their own setup file. Scoped by short_id, so
// the service's key cannot pull a config belonging to a different subscriber.
func (s *Server) subDeviceConfig(w http.ResponseWriter, r *http.Request) error {
	sub, err := s.subscriberByShortID(r)
	if err != nil {
		return notFound("subscriber")
	}
	p, err := store.One[store.DeviceProfile](r.Context(), s.DB,
		`SELECT * FROM device_profiles WHERE id=$1 AND subscriber_id=$2`,
		r.PathValue("device_id"), sub.ID)
	if err != nil {
		return notFound("device")
	}
	ct, name, body := renderDeviceArtifact(p)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write(body)
	return nil
}
