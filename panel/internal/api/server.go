// Package api exposes the panel HTTP API and serves the web UI. The UI uses
// exactly the same public endpoints as any other client.
package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"smartdns/panel/internal/auth"
	"smartdns/panel/internal/fetcher"
	"smartdns/panel/internal/pusher"
	"smartdns/panel/internal/store"
	"smartdns/shared/logging"
	"smartdns/shared/metrics"
)

// Config holds everything the API needs from the environment.
type Config struct {
	PublicURL      string
	SecureCookies  bool
	SessionTTL     time.Duration
	LabMode        bool
	AgentPublicURL string
	SigningKey     ed25519.PrivateKey
	SigningPub     ed25519.PublicKey
	CACertPEM      []byte
	CAKeyPEM       []byte
	Version        string
	// Level lets the settings endpoint change the panel log level without a
	// restart. Nil in tests.
	Level *logging.Level
	// Push model: the panel's client fingerprint is pinned into every node
	// bundle, and Pusher is the outbound client that delivers config and polls
	// health. Both nil in tests that never talk to a node.
	PanelClientFP string
	Pusher        *pusher.Client
	// StateDir and DatabaseURL let the UI backup/restore endpoints pack the CA
	// and key material and dump/restore the database. Empty in tests.
	StateDir    string
	DatabaseURL string
}

// Server wires the API together.
type Server struct {
	DB      *store.DB
	Cfg     Config
	Fetcher *fetcher.Client
	web     http.Handler
}

var (
	mHTTP    = metrics.Counter("smartdns_panel_requests_total", "Panel API requests by route and status class")
	mHTTPDur = metrics.Histogram("smartdns_panel_request_duration_seconds", "Panel API latency", metrics.DefBuckets)
)

// New builds a server.
func New(db *store.DB, cfg Config, web http.Handler) *Server {
	return &Server{DB: db, Cfg: cfg, Fetcher: fetcher.New(fetcher.DefaultLimits), web: web}
}

// --- error handling ----------------------------------------------------------

// APIError is the single error shape returned by every endpoint.
type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Details   any    `json:"details,omitempty"`
	RequestID string `json:"request_id"`
	status    int
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func errorf(status int, code, format string, args ...any) *APIError {
	return &APIError{Code: code, Message: fmt.Sprintf(format, args...), status: status}
}

func badRequest(format string, args ...any) *APIError {
	return errorf(http.StatusBadRequest, "invalid_request", format, args...)
}

func notFound(what string) *APIError {
	return errorf(http.StatusNotFound, "not_found", "%s not found", what)
}

func conflictErr(format string, args ...any) *APIError {
	return errorf(http.StatusConflict, "conflict", format, args...)
}

func internal(err error) *APIError {
	return &APIError{Code: "internal_error", Message: "internal error", status: http.StatusInternalServerError, Details: nil}
}

type handler func(w http.ResponseWriter, r *http.Request) error

func (s *Server) wrap(name string, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rid := requestID(r)
		w.Header().Set("X-Request-Id", rid)
		err := h(w, r)
		status := http.StatusOK
		if err != nil {
			var ae *APIError
			if !errors.As(err, &ae) {
				switch {
				case errors.Is(err, store.ErrNotFound):
					ae = notFound("resource")
				case errors.Is(err, store.ErrConflict):
					ae = conflictErr("%s", err.Error())
				default:
					slog.Error("unhandled api error", "route", name, "request_id", rid, "err", err)
					ae = internal(err)
				}
			}
			ae.RequestID = rid
			status = ae.status
			if status == 0 {
				status = http.StatusInternalServerError
			}
			writeJSON(w, status, ae)
		}
		mHTTP.Inc("route", name, "class", strconv.Itoa(status/100)+"xx")
		mHTTPDur.Observe(time.Since(start).Seconds(), "route", name)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("malformed request body: %v", err)
	}
	return nil
}

func requestID(r *http.Request) string {
	if v := r.Header.Get("X-Request-Id"); v != "" && len(v) <= 64 {
		return v
	}
	return auth.RandomToken(9)
}

// --- middleware --------------------------------------------------------------

func securityHeaders(secure bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; "+
				"frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if secure {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		// CORS is denied by default: the UI is same-origin.
		if r.Header.Get("Origin") != "" && !sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, &APIError{Code: "cors_denied", Message: "cross-origin requests are not allowed"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	host := r.Host
	return strings.HasSuffix(o, "//"+host)
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		// Successful requests are debug-only on purpose: agents poll this API
		// every 15 s, so logging them at info buries real problems and fills
		// the disk. Mutations are recorded in the audit log with more detail,
		// and request counts live in the metrics.
		// Client IP and full paths of API calls are operational data, not user
		// DNS activity; query payloads never reach this log.
		level := slog.LevelDebug
		switch {
		case sw.status >= 500:
			level = slog.LevelError
		case sw.status >= 400:
			level = slog.LevelWarn
		}
		slog.Log(r.Context(), level, "http",
			"method", r.Method, "path", r.URL.Path, "status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", w.Header().Get("X-Request-Id"))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) { w.status = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- audit -------------------------------------------------------------------

func (s *Server) audit(ctx context.Context, r *http.Request, action, objType, objID string, before, after any) {
	u := userOf(ctx)
	actor := "system"
	var actorID *string
	if u != nil {
		actor = u.Email
		actorID = &u.ID
	}
	b, _ := json.Marshal(redact(before))
	a, _ := json.Marshal(redact(after))
	if before == nil {
		b = nil
	}
	if after == nil {
		a = nil
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO audit_events
		(actor, actor_id, action, object_type, object_id, request_id, ip, before_json, after_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		actor, actorID, action, objType, objID, r.Header.Get("X-Request-Id"), nullIP(clientIP(r)), b, a)
	if err != nil {
		slog.Error("audit write failed", "action", action, "err", err)
	}
}

func nullIP(s string) any {
	if net.ParseIP(s) == nil {
		return nil
	}
	return s
}

var secretKeys = map[string]bool{
	"password": true, "token": true, "secret": true, "totp_secret": true,
	"password_hash": true, "ciphertext": true, "recovery_codes": true,
	"private_key": true, "key": true, "authorization": true,
}

// redact removes secret-looking fields before anything is persisted or logged.
func redact(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return walkRedact(m)
}

func walkRedact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if secretKeys[strings.ToLower(k)] {
				t[k] = "***redacted***"
				continue
			}
			t[k] = walkRedact(val)
		}
		return t
	case []any:
		for i := range t {
			t[i] = walkRedact(t[i])
		}
		return t
	default:
		return v
	}
}

// event records an operator-facing timeline entry.
func (s *Server) event(ctx context.Context, level, component, code, msg string, nodeID *string, data any) {
	b, _ := json.Marshal(data)
	if data == nil {
		b = []byte("{}")
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO events (level, component, node_id, code, message, data)
		VALUES ($1,$2,$3,$4,$5,$6)`, level, component, nodeID, code, msg, b); err != nil {
		slog.Error("event write failed", "code", code, "err", err)
	}
}

// --- idempotency -------------------------------------------------------------

// idempotent replays a stored response for a repeated Idempotency-Key.
func (s *Server) idempotent(w http.ResponseWriter, r *http.Request, endpoint string, fn func() (int, any, error)) error {
	key := r.Header.Get("Idempotency-Key")
	ctx := r.Context()
	if key != "" {
		var status int
		var body []byte
		err := s.DB.QueryRow(ctx, `SELECT status, response FROM idempotency_keys WHERE key=$1 AND endpoint=$2`,
			key, endpoint).Scan(&status, &body)
		if err == nil {
			w.Header().Set("Idempotency-Replayed", "true")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return nil
		}
	}
	status, body, err := fn()
	if err != nil {
		return err
	}
	if key != "" {
		b, _ := json.Marshal(body)
		_, _ = s.DB.Exec(ctx, `INSERT INTO idempotency_keys (key, endpoint, response, status)
			VALUES ($1,$2,$3,$4) ON CONFLICT (key) DO NOTHING`, key, endpoint, b, status)
	}
	writeJSON(w, status, body)
	return nil
}

// --- optimistic locking ------------------------------------------------------

// ifMatch returns the expected version from the If-Match header, or 0.
func ifMatch(r *http.Request) (int64, error) {
	v := strings.Trim(r.Header.Get("If-Match"), `"`)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, badRequest("If-Match must be the numeric entity version")
	}
	return n, nil
}

func checkVersion(affected int64, expected int64) error {
	if affected == 0 && expected > 0 {
		return errorf(http.StatusPreconditionFailed, "version_conflict",
			"the object changed since you loaded it; reload and try again")
	}
	if affected == 0 {
		return notFound("object")
	}
	return nil
}
