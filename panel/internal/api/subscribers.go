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

type subscriberRequest struct {
	Name        string  `json:"name"`
	Note        string  `json:"note"`
	Enabled     *bool   `json:"enabled"`
	ExpiresAt   *string `json:"expires_at"`   // RFC3339, "" — снять срок
	DeviceLimit *int    `json:"device_limit"` // null — общий
	QueryLimit  *int64  `json:"query_limit"`  // null — безлимит
	QueryPeriod string  `json:"query_period"`
}

func (s *Server) createSubscriber(w http.ResponseWriter, r *http.Request) error {
	var req subscriberRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.Name) == "" {
		return badRequest("укажите имя подписчика")
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
		strings.TrimSpace(req.Name), req.Note, newShortID(), exp, req.DeviceLimit, req.QueryLimit, period)
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
		id, strings.TrimSpace(req.Name), req.Note, req.Enabled,
		req.ExpiresAt != nil, exp,
		true, req.DeviceLimit,
		true, req.QueryLimit,
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
			tok := auth.RandomToken(18)
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
