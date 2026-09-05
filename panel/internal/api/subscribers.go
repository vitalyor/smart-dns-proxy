package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"smartdns/panel/internal/auth"
	"smartdns/panel/internal/store"
)

// shortIDBytes yields exactly 16 base64url characters (96 bits) — the address of
// a subscriber's page. The link itself is never stored: it is composed as
// {subscription_page_url}/{short_id}, so moving the page to another domain
// touches no rows (ADR 0012).
const shortIDBytes = 12

func newShortID() string { return auth.RandomToken(shortIDBytes) }

func (s *Server) subscriptionURL(ctx contextT, shortID string) string {
	base := strings.TrimRight(getSetting(ctx, s.DB, "subscription_page_url", ""), "/")
	if base == "" {
		return ""
	}
	return base + "/" + shortID
}

func (s *Server) defaultDeviceLimit(ctx contextT) int {
	return getSettingInt(ctx, s.DB, "device_limit_default", 3)
}

type subscriberView struct {
	store.Subscriber
	URL         string `json:"url"`
	DeviceCount int    `json:"device_count"`
	Effective   int    `json:"effective_device_limit"`
}

func (s *Server) listSubscribers(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	rows, err := store.Many[store.Subscriber](ctx, s.DB, `SELECT * FROM subscribers ORDER BY created_at DESC`)
	if err != nil {
		return err
	}
	def := s.defaultDeviceLimit(ctx)
	out := make([]subscriberView, 0, len(rows))
	for _, sub := range rows {
		var n int
		_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM device_profiles WHERE subscriber_id=$1`, sub.ID).Scan(&n)
		lim := def
		if sub.DeviceLimit != nil {
			lim = *sub.DeviceLimit
		}
		out = append(out, subscriberView{Subscriber: sub, URL: s.subscriptionURL(ctx, sub.ShortID),
			DeviceCount: n, Effective: lim})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":                out,
		"device_limit_default": def,
		"page_url_configured":  getSetting(ctx, s.DB, "subscription_page_url", "") != "",
	})
	return nil
}

// optional tells "поле не прислали" apart from "прислали null". PATCH обязан
// их различать: без этого запрос «поменяй имя» снимал оба лимита и стирал
// заметку, потому что незаполненные поля структуры выглядели как «сбросить».
type optional[T any] struct {
	Set   bool
	Value *T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

type subscriberRequest struct {
	Name        *string         `json:"name"`
	Note        *string         `json:"note"`
	Enabled     *bool           `json:"enabled"`
	ExpiresAt   *string         `json:"expires_at"`   // nil — не трогать, "" — снять срок
	DeviceLimit optional[int]   `json:"device_limit"` // null — общий лимит
	QueryLimit  optional[int64] `json:"query_limit"`  // null — безлимит
	QueryPeriod string          `json:"query_period"`
}

func (s *Server) createSubscriber(w http.ResponseWriter, r *http.Request) error {
	var req subscriberRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
		return badRequest("укажите имя пользователя")
	}
	period := req.QueryPeriod
	if period == "" {
		period = "month"
	}
	if !contains([]string{"day", "month", "never"}, period) {
		return badRequest("недопустимый период сброса лимита")
	}
	exp, err := parseExpiry(req.ExpiresAt)
	if err != nil {
		return err
	}
	sub, err := store.One[store.Subscriber](r.Context(), s.DB, `
		INSERT INTO subscribers (name, note, short_id, expires_at, device_limit, query_limit, query_period)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING *`,
		strings.TrimSpace(*req.Name), strOr(req.Note, ""), newShortID(), exp,
		req.DeviceLimit.Value, req.QueryLimit.Value, period)
	if err != nil {
		return err
	}
	s.audit(r.Context(), r, "subscriber.created", "subscriber", sub.ID, nil, map[string]any{"name": sub.Name})
	writeJSON(w, http.StatusCreated, map[string]any{
		"subscriber": sub, "url": s.subscriptionURL(r.Context(), sub.ShortID)})
	return nil
}

func (s *Server) patchSubscriber(w http.ResponseWriter, r *http.Request) error {
	var req subscriberRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	id := r.PathValue("id")
	if req.QueryPeriod != "" && !contains([]string{"day", "month", "never"}, req.QueryPeriod) {
		return badRequest("недопустимый период сброса лимита")
	}
	exp, err := parseExpiry(req.ExpiresAt)
	if err != nil {
		return err
	}
	sub, err := store.One[store.Subscriber](r.Context(), s.DB, `
		UPDATE subscribers SET
			name          = COALESCE(NULLIF($2,''), name),
			note          = COALESCE($3, note),
			enabled       = COALESCE($4, enabled),
			expires_at    = CASE WHEN $5::boolean THEN $6 ELSE expires_at END,
			device_limit  = CASE WHEN $7::boolean THEN $8 ELSE device_limit END,
			query_limit   = CASE WHEN $9::boolean THEN $10 ELSE query_limit END,
			query_period  = COALESCE(NULLIF($11,''), query_period),
			version       = version + 1,
			updated_at    = now()
		WHERE id = $1 RETURNING *`,
		id, strings.TrimSpace(strOr(req.Name, "")), req.Note, req.Enabled,
		req.ExpiresAt != nil, exp,
		req.DeviceLimit.Set, req.DeviceLimit.Value,
		req.QueryLimit.Set, req.QueryLimit.Value,
		req.QueryPeriod)
	if err != nil {
		return err
	}
	s.audit(r.Context(), r, "subscriber.updated", "subscriber", id, nil, nil)
	writeJSON(w, http.StatusOK, map[string]any{"subscriber": sub})
	return nil
}

func (s *Server) deleteSubscriber(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	// Devices cascade; the access reconcile drops their tokens on the next poll.
	n, err := s.DB.ExecN(r.Context(), `DELETE FROM subscribers WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("subscriber")
	}
	s.audit(r.Context(), r, "subscriber.deleted", "subscriber", id, nil, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	return nil
}

// rotateSubscriber issues a new short_id. With reset_devices it also re-mints
// every device token, which is the only way to kill configs that already leaked
// — changing the link alone does not revoke what was already downloaded.
func (s *Server) rotateSubscriber(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		ResetDevices bool `json:"reset_devices"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	id := r.PathValue("id")
	sub, err := store.One[store.Subscriber](ctx, s.DB, `
		UPDATE subscribers SET short_id=$2, version=version+1, updated_at=now()
		WHERE id=$1 RETURNING *`, id, newShortID())
	if err != nil {
		return err
	}
	rotated := 0
	if req.ResetDevices {
		devs, _ := store.Many[store.DeviceProfile](ctx, s.DB,
			`SELECT * FROM device_profiles WHERE subscriber_id=$1`, id)
		for _, d := range devs {
			old, _ := d.Config["path_token"].(string)
			if old == "" {
				continue // DoT-only profile: no token to rotate
			}
			tok := newDeviceToken()
			d.Config["path_token"] = tok
			b, _ := json.Marshal(d.Config)
			if _, err := s.DB.Exec(ctx,
				`UPDATE device_profiles SET config=$2, token_hash=$3, version=version+1, updated_at=now() WHERE id=$1`,
				d.ID, b, hashToken(tok)); err != nil {
				return err
			}
			rotated++
		}
	}
	s.audit(ctx, r, "subscriber.rotated", "subscriber", id, nil,
		map[string]any{"reset_devices": req.ResetDevices, "rotated": rotated})
	writeJSON(w, http.StatusOK, map[string]any{
		"subscriber": sub, "url": s.subscriptionURL(ctx, sub.ShortID), "devices_rotated": rotated})
	return nil
}

func strOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func parseExpiry(v *string) (*time.Time, error) {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, *v)
	if err != nil {
		return nil, badRequest("дата окончания должна быть в формате RFC3339")
	}
	return &t, nil
}

// --- devices of a subscriber ------------------------------------------------
//
// Devices exist only inside a user: there is no standalone device list any more,
// so every route below is scoped by the owner's id and a wrong id yields 404
// rather than someone else's profile.

func (s *Server) listSubscriberDevices(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	sub, err := store.One[store.Subscriber](ctx, s.DB, `SELECT * FROM subscribers WHERE id=$1`, r.PathValue("id"))
	if err != nil {
		return notFound("subscriber")
	}
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
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "device_limit": limit, "types": deviceTypes})
	return nil
}

// addSubscriberDevice is the operator's own way in — for the person who does not
// open the public page (starting with the operator's own phone).
//
// ponytail: no device-limit check here. The limit protects the public page from
// a subscriber adding endlessly; an operator adding a device is a deliberate act
// and the count is shown right next to the button.
func (s *Server) addSubscriberDevice(w http.ResponseWriter, r *http.Request) error {
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
	subID := r.PathValue("id")
	var exists bool
	if err := s.DB.QueryRow(ctx, `SELECT true FROM subscribers WHERE id=$1`, subID).Scan(&exists); err != nil {
		return notFound("subscriber")
	}
	cfg, _, tokenHash := s.buildDeviceConfig(ctx, req.Type)
	b, _ := json.Marshal(cfg)
	p, err := store.One[store.DeviceProfile](ctx, s.DB, `
		INSERT INTO device_profiles (name, type, config, token_hash, subscriber_id)
		VALUES ($1,$2,$3,$4,$5) RETURNING *`, req.Name, req.Type, b, tokenHash, subID)
	if err != nil {
		return err
	}
	doh, dot, _ := deviceAddressesFull(p)
	s.audit(ctx, r, "subscriber.device.added", "device_profile", p.ID, nil,
		map[string]any{"subscriber": subID, "type": req.Type})
	writeJSON(w, http.StatusCreated, map[string]any{
		"device": subDeviceView{ID: p.ID, Name: p.Name, Type: p.Type, CreatedAt: p.CreatedAt,
			DoHURL: doh, DoTHost: dot},
		"pending": true,
	})
	return nil
}

func (s *Server) deleteSubscriberDevice(w http.ResponseWriter, r *http.Request) error {
	n, err := s.DB.ExecN(r.Context(), `DELETE FROM device_profiles WHERE id=$1 AND subscriber_id=$2`,
		r.PathValue("device_id"), r.PathValue("id"))
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("device")
	}
	s.audit(r.Context(), r, "subscriber.device.deleted", "device_profile", r.PathValue("device_id"), nil,
		map[string]any{"subscriber": r.PathValue("id")})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	return nil
}

func (s *Server) downloadSubscriberDevice(w http.ResponseWriter, r *http.Request) error {
	p, err := store.One[store.DeviceProfile](r.Context(), s.DB,
		`SELECT * FROM device_profiles WHERE id=$1 AND subscriber_id=$2`,
		r.PathValue("device_id"), r.PathValue("id"))
	if err != nil {
		return notFound("device")
	}
	ct, name, body := renderDeviceArtifact(p)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write(body)
	return nil
}
