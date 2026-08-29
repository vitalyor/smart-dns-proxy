// Package agentcore implements the node agent under the push model: the panel
// connects to it, not the other way round. The agent holds the node's TLS
// server identity (minted by the panel and pasted in as a bundle), listens for
// config pushes and health polls over mutual TLS, and applies each config by
// swapping the active symlink the data plane reloads on its own. It never dials
// the panel and never needs the Docker socket.
package agentcore

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"smartdns/shared/logging"
	"smartdns/shared/model"
)

// Version is the agent build version, reported to the panel in health.
var Version = "2.0.1"

// Config controls the agent runtime.
type Config struct {
	StateDir      string
	ListenAddr    string // where the panel reaches us, default :3333
	Role          string
	KeepRevisions int
	Level         *logging.Level
}

// Agent is the running node agent (an HTTPS server).
type Agent struct {
	cfg     Config
	started time.Time

	mu       sync.Mutex
	state    State
	panelFP  string // pinned panel client-cert fingerprint from the bundle
	degraded bool
	lastErr  string
}

// State is persisted between restarts.
type State struct {
	NodeID            string    `json:"node_id"`
	Name              string    `json:"name"`
	Role              string    `json:"role"`
	AppliedRevisionID string    `json:"applied_revision_id"`
	AppliedSequence   int64     `json:"applied_sequence"`
	AppliedSHA256     string    `json:"applied_sha256"`
	PreviousRevision  string    `json:"previous_revision_id"`
	LastAppliedAt     time.Time `json:"last_applied_at"`
}

// New prepares the directory layout and loads any persisted state.
func New(cfg Config) (*Agent, error) {
	if cfg.KeepRevisions <= 0 {
		cfg.KeepRevisions = 3
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":3333"
	}
	for _, d := range []string{cfg.StateDir, cfg.identityDir(), cfg.revisionsDir()} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, err
		}
	}
	a := &Agent{cfg: cfg, started: time.Now()}
	a.loadState()
	return a, nil
}

func (c Config) identityDir() string  { return filepath.Join(c.StateDir, "identity") }
func (c Config) revisionsDir() string { return filepath.Join(c.StateDir, "revisions") }
func (c Config) activeLink() string   { return filepath.Join(c.StateDir, "active") }
func (c Config) previousLink() string { return filepath.Join(c.StateDir, "previous") }
func (c Config) statePath() string    { return filepath.Join(c.StateDir, "state.json") }
func (c Config) certPath() string     { return filepath.Join(c.identityDir(), "node.crt") }
func (c Config) keyPath() string      { return filepath.Join(c.identityDir(), "node.key") }
func (c Config) caPath() string       { return filepath.Join(c.identityDir(), "ca.crt") }
func (c Config) panelFPPath() string  { return filepath.Join(c.identityDir(), "panel.fp") }

// Provisioned reports whether the node already holds an identity.
func (a *Agent) Provisioned() bool {
	return fileExists(a.cfg.certPath()) && fileExists(a.cfg.keyPath()) &&
		fileExists(a.cfg.caPath()) && fileExists(a.cfg.panelFPPath())
}

// LoadBundle writes the identity from a pasted bundle. Idempotent: an already
// provisioned node keeps its identity so a restart with the bundle still set is
// harmless. Re-provisioning with a different node id is refused.
func (a *Agent) LoadBundle(encoded string) error {
	b, err := model.DecodeBundle(encoded)
	if err != nil {
		return err
	}
	if a.Provisioned() {
		if a.state.NodeID != "" && a.state.NodeID != b.NodeID {
			return fmt.Errorf("this node is already provisioned as %s; remove %s to re-provision",
				a.state.NodeID, a.cfg.identityDir())
		}
		return nil
	}
	if a.cfg.Role != "" && b.Role != a.cfg.Role {
		return fmt.Errorf("bundle role %q does not match this agent's role %q", b.Role, a.cfg.Role)
	}
	if err := writeFile(a.cfg.keyPath(), []byte(b.NodeKeyPEM), 0o600); err != nil {
		return err
	}
	if err := writeFile(a.cfg.certPath(), []byte(b.NodeCertPEM), 0o644); err != nil {
		return err
	}
	if err := writeFile(a.cfg.caPath(), []byte(b.CACertPEM), 0o644); err != nil {
		return err
	}
	if err := writeFile(a.cfg.panelFPPath(), []byte(b.PanelClientFP), 0o644); err != nil {
		return err
	}
	a.mu.Lock()
	a.state.NodeID, a.state.Name, a.state.Role = b.NodeID, b.Name, b.Role
	a.mu.Unlock()
	_ = a.saveState()
	slog.Info("node provisioned from bundle", "node_id", b.NodeID, "name", b.Name, "role", b.Role)
	return nil
}

// Serve runs the mTLS management server until ctx is cancelled. The data plane
// keeps serving the last-known-good config regardless of whether the panel ever
// connects — fail-open is simply the active symlink persisting on disk.
func (a *Agent) Serve(ctx context.Context) error {
	if !a.Provisioned() {
		return errors.New("node is not provisioned: paste the bundle from the panel into NODE_BUNDLE")
	}
	fp, err := os.ReadFile(a.cfg.panelFPPath())
	if err != nil {
		return err
	}
	a.panelFP = string(fp)

	cert, err := tls.LoadX509KeyPair(a.cfg.certPath(), a.cfg.keyPath())
	if err != nil {
		return err
	}
	caPEM, err := os.ReadFile(a.cfg.caPath())
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return errors.New("bundle CA is not a valid certificate")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/config", a.wrap(a.handleConfig))
	mux.HandleFunc("GET /v1/health", a.wrap(a.handleHealth))

	srv := &http.Server{
		Addr:    a.cfg.ListenAddr,
		Handler: mux,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientCAs:    pool,
			ClientAuth:   tls.RequireAndVerifyClientCert,
			MinVersion:   tls.VersionTLS12,
			// The CA vouches for any client it signed; the pin narrows that to
			// the one panel whose fingerprint is in our bundle.
			VerifyConnection: a.pinPanel,
		},
		ReadHeaderTimeout: 10 * time.Second,
	}

	slog.Info("agent listening", "version", Version, "addr", a.cfg.ListenAddr,
		"node_id", a.state.NodeID, "role", a.state.Role, "applied_revision", a.state.AppliedRevisionID)

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServeTLS("", "") }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// pinPanel rejects any client whose leaf fingerprint is not the pinned panel.
func (a *Agent) pinPanel(cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("no client certificate")
	}
	sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
	got := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(got), []byte(a.panelFP)) != 1 {
		return errors.New("client certificate is not the pinned panel")
	}
	return nil
}

func (a *Agent) wrap(h func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			slog.Warn("management request failed", "path", r.URL.Path, "err", err)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
	}
}

// handleConfig applies a config the panel pushes. The staging/symlink swap is
// identical to before; only the source changed from a pull to this POST body.
func (a *Agent) handleConfig(w http.ResponseWriter, r *http.Request) error {
	raw, err := readAll(r, 64<<20)
	if err != nil {
		return err
	}
	var cfg model.NodeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		a.setErr("invalid_json")
		return fmt.Errorf("config is not valid JSON: %w", err)
	}
	if err := validate(&cfg, a.state.NodeID, a.state.Role); err != nil {
		a.setErr("validation_failed")
		return err
	}
	sum := sha256.Sum256(raw)
	got := hex.EncodeToString(sum[:])

	if cfg.RevisionID == a.state.AppliedRevisionID && got == a.state.AppliedSHA256 {
		// Idempotent re-push (the panel reconciles drift by re-sending): ack
		// without touching disk.
		return a.ackConfig(w)
	}

	dir := filepath.Join(a.cfg.revisionsDir(), cfg.RevisionID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "config.json"), raw, 0o640); err != nil {
		return err
	}
	prevTarget, _ := os.Readlink(a.cfg.activeLink())
	if err := swapSymlink(a.cfg.activeLink(), dir); err != nil {
		return err
	}
	if prevTarget != "" {
		_ = swapSymlink(a.cfg.previousLink(), prevTarget)
	}

	a.mu.Lock()
	a.state.PreviousRevision = a.state.AppliedRevisionID
	a.state.AppliedRevisionID = cfg.RevisionID
	a.state.AppliedSequence = cfg.Sequence
	a.state.AppliedSHA256 = got
	a.state.LastAppliedAt = time.Now().UTC()
	a.degraded, a.lastErr = false, ""
	a.mu.Unlock()
	_ = a.saveState()
	a.pruneRevisions()
	if a.cfg.Level != nil {
		a.cfg.Level.Follow(cfg.LogLevel)
	}
	slog.Info("config applied", "revision", cfg.RevisionID, "sequence", cfg.Sequence, "services", len(cfg.Services))
	return a.ackConfig(w)
}

func (a *Agent) ackConfig(w http.ResponseWriter) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"applied_revision_id": a.state.AppliedRevisionID,
		"applied_sequence":    a.state.AppliedSequence,
		"status":              a.statusLocked(),
	})
	return nil
}

func (a *Agent) handleHealth(w http.ResponseWriter, r *http.Request) error {
	a.mu.Lock()
	h := model.Health{
		AppliedRevisionID: a.state.AppliedRevisionID,
		AppliedSequence:   a.state.AppliedSequence,
		Status:            a.statusLocked(),
		Role:              a.state.Role,
		Version:           Version,
		UptimeS:           int64(time.Since(a.started).Seconds()),
		LastErr:           a.lastErr,
	}
	a.mu.Unlock()
	a.probe(&h)
	writeJSON(w, http.StatusOK, h)
	return nil
}

func (a *Agent) statusLocked() string {
	if a.degraded {
		return "degraded"
	}
	return "healthy"
}

func (a *Agent) setErr(code string) {
	a.mu.Lock()
	a.degraded, a.lastErr = true, code
	a.mu.Unlock()
}

func validate(c *model.NodeConfig, nodeID, role string) error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("unsupported artifact schema version %d", c.SchemaVersion)
	}
	if nodeID != "" && c.NodeID != nodeID {
		return fmt.Errorf("config belongs to node %s, not to this node", c.NodeID)
	}
	if role != "" && c.Role != role {
		return fmt.Errorf("config role %q does not match this node's role %q", c.Role, role)
	}
	switch c.Role {
	case "ingress":
		if len(c.Services) == 0 {
			return errors.New("ingress config contains no services")
		}
		for _, s := range c.Services {
			if len(s.Egress.Targets) == 0 {
				return fmt.Errorf("service %q has no egress target", s.Slug)
			}
			if len(s.IngressV4) == 0 && len(s.IngressV6) == 0 {
				return fmt.Errorf("service %q has no ingress address to publish", s.Slug)
			}
		}
	case "egress":
		if len(c.Egress.AllowedPorts) == 0 {
			return errors.New("egress config has an empty port allowlist")
		}
	default:
		return fmt.Errorf("unknown role %q", c.Role)
	}
	return nil
}

// pruneRevisions keeps the newest N revisions and never removes the active or
// previous one, even when the disk is nearly full.
func (a *Agent) pruneRevisions() {
	entries, err := os.ReadDir(a.cfg.revisionsDir())
	if err != nil {
		return
	}
	protected := map[string]bool{a.state.AppliedRevisionID: true, a.state.PreviousRevision: true}
	type item struct {
		name string
		mod  time.Time
	}
	var items []item
	for _, e := range entries {
		if !e.IsDir() || protected[e.Name()] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{e.Name(), info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	keep := a.cfg.KeepRevisions - len(protected)
	if keep < 0 {
		keep = 0
	}
	for i, it := range items {
		if i < keep {
			continue
		}
		_ = os.RemoveAll(filepath.Join(a.cfg.revisionsDir(), it.name))
	}
}

// ActiveConfig reads the currently active artifact, if any.
func (a *Agent) ActiveConfig() (*model.NodeConfig, error) {
	b, err := os.ReadFile(filepath.Join(a.cfg.activeLink(), "config.json"))
	if err != nil {
		return nil, err
	}
	var c model.NodeConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (a *Agent) loadState() {
	b, err := os.ReadFile(a.cfg.statePath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &a.state)
}

func (a *Agent) saveState() error {
	a.mu.Lock()
	b, _ := json.MarshalIndent(a.state, "", "  ")
	a.mu.Unlock()
	return writeFileAtomic(a.cfg.statePath(), b, 0o640)
}

// State returns a copy of the persisted state.
func (a *Agent) State() State { a.mu.Lock(); defer a.mu.Unlock(); return a.state }

func swapSymlink(link, target string) error {
	tmp := link + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

func writeFile(p string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	return os.WriteFile(p, b, mode)
}

func writeFileAtomic(p string, b []byte, mode os.FileMode) error {
	tmp := p + ".tmp"
	if err := writeFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
