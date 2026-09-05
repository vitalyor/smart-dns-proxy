package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"smartdns/panel/internal/auth"
	"smartdns/panel/internal/store"
)

const sessionCookie = "sdns_session"

type ctxKey string

const (
	ctxUser    ctxKey = "user"
	ctxAPIKey  ctxKey = "api_key"
	ctxSession ctxKey = "session"
)

// SessionUser is the authenticated principal attached to a request.
type SessionUser struct {
	ID        string
	Email     string
	Role      string
	SessionID string
	CSRF      string
}

func userOf(ctx context.Context) *SessionUser {
	u, _ := ctx.Value(ctxUser).(*SessionUser)
	return u
}

// authenticate resolves the session cookie; it never rejects on its own so
// that public endpoints keep working.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			// No cookie: this may be a machine caller with a scoped key.
			if p := s.resolveAPIKey(r); p != nil {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxAPIKey, p)))
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		type row struct {
			UserID string `db:"user_id"`
			Email  string `db:"email"`
			Role   string `db:"role"`
			SID    string `db:"sid"`
			CSRF   string `db:"csrf_token"`
		}
		v, err := store.One[row](r.Context(), s.DB, `
			SELECT s.id AS sid, s.csrf_token, u.id AS user_id, u.email, u.role
			FROM sessions s JOIN users u ON u.id = s.user_id
			WHERE s.token_hash = $1 AND s.expires_at > now() AND u.disabled_at IS NULL`,
			auth.HashToken(c.Value))
		if err != nil {
			clearSessionCookie(w, s.Cfg.SecureCookies)
			next.ServeHTTP(w, r)
			return
		}
		_, _ = s.DB.Exec(r.Context(), `UPDATE sessions SET last_seen_at = now() WHERE id = $1`, v.SID)
		ctx := context.WithValue(r.Context(), ctxUser, &SessionUser{
			ID: v.UserID, Email: v.Email, Role: v.Role, SessionID: v.SID, CSRF: v.CSRF,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAuth enforces a session, CSRF on mutating verbs, and a minimum role.
func (s *Server) requireAuth(minRole string, h handler) handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		u := userOf(r.Context())
		if u == nil {
			return errorf(http.StatusUnauthorized, "unauthenticated", "Войдите заново")
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !auth.ConstantTimeEqualString(r.Header.Get("X-CSRF-Token"), u.CSRF) {
				return errorf(http.StatusForbidden, "csrf_failed", "missing or invalid CSRF token")
			}
		}
		if !roleAtLeast(u.Role, minRole) {
			return errorf(http.StatusForbidden, "forbidden",
				"role %q is not allowed to perform this action (requires %q)", u.Role, minRole)
		}
		return h(w, r)
	}
}

var roleRank = map[string]int{"viewer": 1, "operator": 2, "owner": 3}

func roleAtLeast(have, need string) bool { return roleRank[have] >= roleRank[need] }

func setSessionCookie(w http.ResponseWriter, token string, ttl time.Duration, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(ttl),
	})
}

func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// --- endpoints ---------------------------------------------------------------

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TOTP     string `json:"totp,omitempty"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) error {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	ctx := r.Context()

	u, err := store.One[store.User](ctx, s.DB, `SELECT * FROM users WHERE email=$1`, req.Email)
	if err != nil {
		// Spend comparable time so a missing account is not distinguishable.
		auth.VerifyPassword(req.Password, "$argon2id$v=19$m=65536,t=3,p=4$YWJjZGVmZ2hpamtsbW5vcA$"+
			"Y2RlZmdoaWprbG1ub3BxcnN0dXZ3eHl6MDEyMzQ1Ng")
		return errorf(http.StatusUnauthorized, "invalid_credentials", "неверный email или пароль")
	}
	if u.DisabledAt != nil {
		return errorf(http.StatusForbidden, "account_disabled", "учётная запись отключена")
	}
	if u.LockedUntil != nil && u.LockedUntil.After(time.Now()) {
		return errorf(http.StatusTooManyRequests, "account_locked",
			"слишком много неудачных попыток; повторите после %s", u.LockedUntil.UTC().Format(time.RFC3339))
	}
	if !auth.VerifyPassword(req.Password, u.PasswordHash) {
		s.registerFailedLogin(ctx, u.ID)
		s.audit(ctx, r, "auth.login_failed", "user", u.ID, nil, map[string]any{"email": req.Email})
		return errorf(http.StatusUnauthorized, "invalid_credentials", "неверный email или пароль")
	}
	if u.TOTPEnabled {
		if req.TOTP == "" {
			return errorf(http.StatusUnauthorized, "totp_required", "введите код из приложения-аутентификатора")
		}
		secret, err := s.decryptSecret(u.TOTPSecret)
		if err != nil {
			return internal(err)
		}
		if !auth.VerifyTOTP(secret, req.TOTP) && !s.consumeRecoveryCode(ctx, u.ID, req.TOTP) {
			s.registerFailedLogin(ctx, u.ID)
			return errorf(http.StatusUnauthorized, "invalid_totp", "неверный код подтверждения")
		}
	}

	token := auth.RandomToken(32)
	csrf := auth.RandomToken(24)
	ttl := s.Cfg.SessionTTL
	if ttl == 0 {
		ttl = 12 * time.Hour
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO sessions (user_id, token_hash, csrf_token, expires_at, ip, user_agent)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		u.ID, auth.HashToken(token), csrf, time.Now().Add(ttl), nullIP(clientIP(r)), truncate(r.UserAgent(), 300)); err != nil {
		return err
	}
	_, _ = s.DB.Exec(ctx, `UPDATE users SET failed_logins=0, locked_until=NULL WHERE id=$1`, u.ID)
	setSessionCookie(w, token, ttl, s.Cfg.SecureCookies)
	s.audit(ctx, r, "auth.login", "user", u.ID, nil, map[string]any{"email": u.Email})

	writeJSON(w, http.StatusOK, map[string]any{
		"user":       map[string]any{"id": u.ID, "email": u.Email, "role": u.Role, "display_name": u.DisplayName, "totp_enabled": u.TOTPEnabled},
		"csrf_token": csrf,
		"expires_at": time.Now().Add(ttl).UTC(),
	})
	return nil
}

func (s *Server) registerFailedLogin(ctx context.Context, userID string) {
	_, _ = s.DB.Exec(ctx, `UPDATE users SET failed_logins = failed_logins + 1,
		locked_until = CASE WHEN failed_logins + 1 >= 10 THEN now() + interval '15 minutes' ELSE locked_until END
		WHERE id = $1`, userID)
}

func (s *Server) consumeRecoveryCode(ctx context.Context, userID, code string) bool {
	h := auth.HashToken(strings.TrimSpace(code))
	n, err := s.DB.ExecN(ctx, `UPDATE users SET recovery_codes_hash = array_remove(recovery_codes_hash, $2)
		WHERE id = $1 AND $2 = ANY(recovery_codes_hash)`, userID, h)
	return err == nil && n > 0
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) error {
	u := userOf(r.Context())
	if u != nil {
		_, _ = s.DB.Exec(r.Context(), `DELETE FROM sessions WHERE id=$1`, u.SessionID)
		s.audit(r.Context(), r, "auth.logout", "user", u.ID, nil, nil)
	}
	clearSessionCookie(w, s.Cfg.SecureCookies)
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed_out"})
	return nil
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) error {
	u := userOf(r.Context())
	if u == nil {
		return errorf(http.StatusUnauthorized, "unauthenticated", "Войдите заново")
	}
	row, err := store.One[store.User](r.Context(), s.DB, `SELECT * FROM users WHERE id=$1`, u.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id": row.ID, "email": row.Email, "role": row.Role,
			"display_name": row.DisplayName, "totp_enabled": row.TOTPEnabled,
		},
		"csrf_token": u.CSRF,
		"lab_mode":   s.Cfg.LabMode,
		"version":    s.Cfg.Version,
	})
	return nil
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) error {
	u := userOf(r.Context())
	type row struct {
		ID         string    `db:"id" json:"id"`
		IP         *string   `db:"ip" json:"ip"`
		UserAgent  string    `db:"user_agent" json:"user_agent"`
		LastSeenAt time.Time `db:"last_seen_at" json:"last_seen_at"`
		ExpiresAt  time.Time `db:"expires_at" json:"expires_at"`
		CreatedAt  time.Time `db:"created_at" json:"created_at"`
	}
	rows, err := store.Many[row](r.Context(), s.DB,
		`SELECT id, host(ip) AS ip, user_agent, last_seen_at, expires_at, created_at
		 FROM sessions WHERE user_id=$1 ORDER BY last_seen_at DESC`, u.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows, "current": u.SessionID})
	return nil
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) error {
	u := userOf(r.Context())
	id := r.PathValue("id")
	n, err := s.DB.ExecN(r.Context(), `DELETE FROM sessions WHERE id=$1 AND user_id=$2`, id, u.ID)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("session")
	}
	s.audit(r.Context(), r, "auth.session_revoked", "session", id, nil, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
	return nil
}

type changePasswordRequest struct {
	Current string `json:"current_password"`
	New     string `json:"new_password"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) error {
	u := userOf(r.Context())
	var req changePasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	row, err := store.One[store.User](r.Context(), s.DB, `SELECT * FROM users WHERE id=$1`, u.ID)
	if err != nil {
		return err
	}
	if !auth.VerifyPassword(req.Current, row.PasswordHash) {
		return errorf(http.StatusUnauthorized, "invalid_credentials", "текущий пароль неверен")
	}
	h, err := auth.HashPassword(req.New, auth.DefaultParams)
	if err != nil {
		return badRequest("%v", err)
	}
	if _, err := s.DB.Exec(r.Context(), `UPDATE users SET password_hash=$2, updated_at=now(), version=version+1 WHERE id=$1`, u.ID, h); err != nil {
		return err
	}
	// Every other session is invalidated after a password change.
	_, _ = s.DB.Exec(r.Context(), `DELETE FROM sessions WHERE user_id=$1 AND id<>$2`, u.ID, u.SessionID)
	s.audit(r.Context(), r, "auth.password_changed", "user", u.ID, nil, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
	return nil
}

func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) error {
	u := userOf(r.Context())
	secret := auth.NewTOTPSecret()
	enc, err := s.encryptSecret(secret)
	if err != nil {
		return internal(err)
	}
	if _, err := s.DB.Exec(r.Context(),
		`UPDATE users SET totp_secret_encrypted=$2, totp_enabled=false WHERE id=$1`, u.ID, enc); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"secret": secret,
		"uri":    auth.TOTPURI("SmartDNS", u.Email, secret),
	})
	return nil
}

func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) error {
	u := userOf(r.Context())
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	row, err := store.One[store.User](r.Context(), s.DB, `SELECT * FROM users WHERE id=$1`, u.ID)
	if err != nil {
		return err
	}
	if len(row.TOTPSecret) == 0 {
		return badRequest("сначала запросите секрет через /auth/totp/setup")
	}
	secret, err := s.decryptSecret(row.TOTPSecret)
	if err != nil {
		return internal(err)
	}
	if !auth.VerifyTOTP(secret, req.Code) {
		return errorf(http.StatusBadRequest, "invalid_totp", "код не совпадает; проверьте время на устройстве")
	}
	plain, hashes := auth.NewRecoveryCodes(10)
	if _, err := s.DB.Exec(r.Context(),
		`UPDATE users SET totp_enabled=true, recovery_codes_hash=$2 WHERE id=$1`, u.ID, hashes); err != nil {
		return err
	}
	s.audit(r.Context(), r, "auth.totp_enabled", "user", u.ID, nil, nil)
	writeJSON(w, http.StatusOK, map[string]any{"status": "enabled", "recovery_codes": plain})
	return nil
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) error {
	u := userOf(r.Context())
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	row, err := store.One[store.User](r.Context(), s.DB, `SELECT * FROM users WHERE id=$1`, u.ID)
	if err != nil {
		return err
	}
	if !auth.VerifyPassword(req.Password, row.PasswordHash) {
		return errorf(http.StatusUnauthorized, "invalid_credentials", "пароль неверен")
	}
	if _, err := s.DB.Exec(r.Context(),
		`UPDATE users SET totp_enabled=false, totp_secret_encrypted=NULL, recovery_codes_hash='{}' WHERE id=$1`, u.ID); err != nil {
		return err
	}
	s.audit(r.Context(), r, "auth.totp_disabled", "user", u.ID, nil, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "disabled"})
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

var errNoSecretKey = errors.New("secret encryption key is not configured")
