package api

import (
	"context"
	"net/http"
	"strings"

	"smartdns/panel/internal/auth"
	"smartdns/panel/internal/store"
)

// Scopes a machine key can hold. They are deliberately narrow: the subscription
// page service is publicly reachable, so a leak of its key must not hand over
// the panel. Remnawave's equivalent token is full-power; ours is not (ADR 0012).
const (
	ScopeSubRead         = "sub:read"         // read a subscriber and list their devices
	ScopeSubDevices      = "sub:devices"      // create and delete that subscriber's devices
	ScopeSubInstructions = "sub:instructions" // read the instruction bundle
)

var allScopes = []string{ScopeSubRead, ScopeSubDevices, ScopeSubInstructions}

// APIKeyPrincipal is the authenticated machine caller.
type APIKeyPrincipal struct {
	ID     string
	Name   string
	Scopes []string
}

func (p *APIKeyPrincipal) Has(scope string) bool {
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

func apiKeyOf(ctx context.Context) *APIKeyPrincipal {
	p, _ := ctx.Value(ctxAPIKey).(*APIKeyPrincipal)
	return p
}

// resolveAPIKey reads a Bearer key and returns the principal, or nil when the
// header is absent or the key is unknown/revoked.
func (s *Server) resolveAPIKey(r *http.Request) *APIKeyPrincipal {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return nil
	}
	key := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	if key == "" {
		return nil
	}
	k, err := store.One[store.APIKey](r.Context(), s.DB,
		`SELECT * FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL`, auth.HashToken(key))
	if err != nil {
		return nil
	}
	// Best-effort: a failed touch must not break the request.
	_, _ = s.DB.Exec(r.Context(), `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, k.ID)
	return &APIKeyPrincipal{ID: k.ID, Name: k.Name, Scopes: k.Scopes}
}

// requireScope guards a machine-only route. Browser sessions are not accepted:
// these endpoints exist for services. There is no CSRF check because there is no
// cookie to ride on.
func (s *Server) requireScope(scope string, h handler) handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		p := apiKeyOf(r.Context())
		if p == nil {
			return errorf(http.StatusUnauthorized, "unauthenticated", "требуется ключ доступа")
		}
		if !p.Has(scope) {
			return errorf(http.StatusForbidden, "forbidden", "ключу не выдано право %s", scope)
		}
		return h(w, r)
	}
}

// --- management (owner only) ------------------------------------------------

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) error {
	rows, err := store.Many[store.APIKey](r.Context(), s.DB,
		`SELECT * FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows, "scopes": allScopes})
	return nil
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.Name) == "" {
		return badRequest("укажите название ключа")
	}
	for _, sc := range req.Scopes {
		if !contains(allScopes, sc) {
			return badRequest("неизвестное право: %s", sc)
		}
	}
	if len(req.Scopes) == 0 {
		return badRequest("выберите хотя бы одно право")
	}
	key := auth.RandomToken(32)
	k, err := store.One[store.APIKey](r.Context(), s.DB, `
		INSERT INTO api_keys (name, key_hash, scopes) VALUES ($1,$2,$3)
		RETURNING *`, strings.TrimSpace(req.Name), auth.HashToken(key), req.Scopes)
	if err != nil {
		return err
	}
	s.audit(r.Context(), r, "api_key.created", "api_key", k.ID, nil,
		map[string]any{"name": k.Name, "scopes": k.Scopes})
	// Shown once: only the hash is stored.
	writeJSON(w, http.StatusCreated, map[string]any{"key": k, "secret": key})
	return nil
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	n, err := s.DB.ExecN(r.Context(), `UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("api_key")
	}
	s.audit(r.Context(), r, "api_key.revoked", "api_key", id, nil, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
	return nil
}
