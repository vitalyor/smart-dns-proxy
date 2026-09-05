// Package subpage serves the public subscription page. It is a thin, stateless
// front for the panel: it holds no database, stores no device tokens, and keeps
// only a short in-memory cache so a panel blip does not take the page down
// (ADR 0012).
package subpage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config is everything the service needs. Nothing here is persisted.
type Config struct {
	PanelURL string        // where the panel lives, e.g. https://panel.example
	APIKey   string        // scoped Bearer key issued by the panel
	CacheTTL time.Duration // how long a status answer stays fresh
	StaleFor time.Duration // how long a stale answer may cover a panel outage
	Version  string
}

type cached struct {
	body      []byte
	status    int
	fetchedAt time.Time
}

// Server is the public HTTP front.
type Server struct {
	cfg   Config
	http  *http.Client
	mu    sync.Mutex
	cache map[string]cached
	rl    *limiter
}

func New(cfg Config) *Server {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 5 * time.Second
	}
	if cfg.StaleFor <= 0 {
		cfg.StaleFor = 5 * time.Minute
	}
	return &Server{
		cfg:   cfg,
		http:  &http.Client{Timeout: 10 * time.Second},
		cache: map[string]cached{},
		rl:    newLimiter(120, time.Minute),
	}
}

// shortID is the first path segment. Anything that is not a plausible id is
// rejected before it ever reaches the panel.
func shortID(path string) string {
	p := strings.Trim(path, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		p = p[:i]
	}
	if len(p) < 8 || len(p) > 64 {
		return ""
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ""
		}
	}
	return p
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.cfg.Version})
	})
	mux.HandleFunc("GET /{short}/api/status", s.status)
	mux.HandleFunc("POST /{short}/api/devices", s.addDevice)
	mux.HandleFunc("DELETE /{short}/api/devices/{id}", s.deleteDevice)
	mux.HandleFunc("GET /{short}/api/devices/{id}/config", s.deviceConfig)
	mux.HandleFunc("GET /{short}", s.page)
	mux.HandleFunc("GET /", s.root)
	return s.middleware(mux)
}

// middleware applies a per-IP rate limit and the security headers a public page
// should carry. The limit exists because the page is addressed by a secret in
// the URL: 96 bits is far beyond guessing, but there is no reason to let anyone
// try at speed.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.rl.allow(clientIP(r)) {
			http.Error(w, "слишком много запросов", http.StatusTooManyRequests)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; img-src data:; form-action 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	// No index: without a link there is nothing to show, and listing anything
	// would defeat the point of an unguessable address.
	http.Error(w, "Нужна персональная ссылка", http.StatusNotFound)
}

// panelCall forwards to the panel with the service key attached.
func (s *Server) panelCall(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.cfg.PanelURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return s.http.Do(req)
}

// status serves the subscriber view, with a short cache in front. On a panel
// outage a stale answer is served rather than an error page: the person can
// still see their devices even while the panel is down.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	id := shortID(r.URL.Path)
	if id == "" {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	c, ok := s.cache[id]
	s.mu.Unlock()
	if ok && time.Since(c.fetchedAt) < s.cfg.CacheTTL {
		writeCached(w, c)
		return
	}
	resp, err := s.panelCall(r.Context(), http.MethodGet, "/api/v1/sub/"+id, nil, "")
	if err != nil {
		if ok && time.Since(c.fetchedAt) < s.cfg.StaleFor {
			w.Header().Set("X-Stale", "1")
			writeCached(w, c)
			return
		}
		slog.Warn("panel unreachable", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "панель недоступна, попробуйте позже"})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusOK {
		s.mu.Lock()
		s.cache[id] = cached{body: body, status: resp.StatusCode, fetchedAt: time.Now()}
		s.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func writeCached(w http.ResponseWriter, c cached) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(c.status)
	_, _ = w.Write(c.body)
}

// invalidate drops the cached view so a change the person just made shows up at
// once instead of after the TTL.
func (s *Server) invalidate(id string) {
	s.mu.Lock()
	delete(s.cache, id)
	s.mu.Unlock()
}

func (s *Server) addDevice(w http.ResponseWriter, r *http.Request) {
	id := shortID(r.URL.Path)
	if id == "" {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	resp, err := s.panelCall(r.Context(), http.MethodPost, "/api/v1/sub/"+id+"/devices",
		strings.NewReader(string(body)), "application/json")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "панель недоступна, попробуйте позже"})
		return
	}
	defer resp.Body.Close()
	s.invalidate(id)
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(out)
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := shortID(r.URL.Path)
	if id == "" {
		http.NotFound(w, r)
		return
	}
	resp, err := s.panelCall(r.Context(), http.MethodDelete,
		"/api/v1/sub/"+id+"/devices/"+r.PathValue("id"), nil, "")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "панель недоступна, попробуйте позже"})
		return
	}
	defer resp.Body.Close()
	s.invalidate(id)
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(out)
}

// deviceConfig streams the setup file through. It is never cached: it carries
// the person's token, and this service keeps no secrets on disk or in memory
// beyond the request that needs them.
func (s *Server) deviceConfig(w http.ResponseWriter, r *http.Request) {
	id := shortID(r.URL.Path)
	if id == "" {
		http.NotFound(w, r)
		return
	}
	resp, err := s.panelCall(r.Context(), http.MethodGet,
		"/api/v1/sub/"+id+"/devices/"+r.PathValue("id")+"/config", nil, "")
	if err != nil {
		http.Error(w, "панель недоступна, попробуйте позже", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Content-Disposition"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

var errNoKey = errors.New("PANEL_API_KEY is required")

// Validate fails fast on a misconfiguration rather than serving a broken page.
func (c Config) Validate() error {
	if strings.TrimSpace(c.APIKey) == "" {
		return errNoKey
	}
	if strings.TrimSpace(c.PanelURL) == "" {
		return errors.New("PANEL_URL is required")
	}
	return nil
}
