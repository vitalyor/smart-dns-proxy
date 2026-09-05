// Command panel-api is the control plane: HTTP API, web UI, compiler,
// scheduler and the mutually authenticated agent gateway. It is never in the
// path of user traffic.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"smartdns/panel/internal/api"
	"smartdns/panel/internal/auth"
	"smartdns/panel/internal/fetcher"
	"smartdns/panel/internal/jobs"
	"smartdns/panel/internal/pusher"
	"smartdns/panel/internal/store"
	"smartdns/panel/web"
	"smartdns/shared/logging"
	"smartdns/shared/pki"
)

var version = "dev"

func main() {
	var (
		addr     = flag.String("addr", env("PANEL_ADDR", ":8080"), "UI/API listen address")
		dsn      = flag.String("dsn", env("DATABASE_URL", ""), "PostgreSQL connection string")
		stateDir = flag.String("state", env("PANEL_STATE_DIR", "/var/lib/smartdns-panel"), "directory for CA and signing keys")
		showVer  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println("panel-api", version)
		return
	}
	level := logging.Setup("panel-api")

	if *dsn == "" {
		fatal("DATABASE_URL is required")
	}
	secret := os.Getenv("PANEL_SECRET_KEY")
	if len(secret) < 16 {
		fatal("PANEL_SECRET_KEY must be set to at least 16 characters (used to encrypt TOTP secrets at rest)")
	}
	api.SetSecretKey(api.DeriveSecretKey(secret))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, *dsn)
	if err != nil {
		fatal("database: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		fatal("migrations: %v", err)
	}
	slog.Info("database ready")

	caCert, caKey, err := loadOrCreateCA(*stateDir)
	if err != nil {
		fatal("certificate authority: %v", err)
	}
	signPub, signPriv, err := loadOrCreateSigningKey(*stateDir)
	if err != nil {
		fatal("signing key: %v", err)
	}
	clientCertPEM, clientKeyPEM, clientFP, err := loadOrCreatePanelClientCert(*stateDir, caCert, caKey)
	if err != nil {
		fatal("panel client certificate: %v", err)
	}
	push, err := pusher.New(clientCertPEM, clientKeyPEM, caCert)
	if err != nil {
		fatal("pusher: %v", err)
	}

	publicURL := env("PANEL_PUBLIC_URL", "http://localhost:8080")
	cfg := api.Config{
		PublicURL:      publicURL,
		AgentPublicURL: env("AGENT_PUBLIC_URL", strings.Replace(publicURL, ":8080", ":8443", 1)),
		SecureCookies:  strings.HasPrefix(publicURL, "https://"),
		SessionTTL:     durationEnv("SESSION_TTL", 12*time.Hour),
		LabMode:        os.Getenv("LAB_MODE") == "1",
		SigningKey:     signPriv,
		SigningPub:     signPub,
		CACertPEM:      caCert,
		CAKeyPEM:       caKey,
		Version:        version,
		Level:          level,
		PanelClientFP:  clientFP,
		Pusher:         push,
		StateDir:       *stateDir,
		DatabaseURL:    *dsn,
		GitHubRepo:     os.Getenv("GITHUB_REPO"),
	}
	srv := api.New(db, cfg, web.Handler())
	if err := srv.SeedSettings(ctx); err != nil {
		fatal("seed settings: %v", err)
	}
	srv.ApplyStoredLogLevel(ctx)
	if err := bootstrapAdmin(ctx, db); err != nil {
		fatal("bootstrap admin: %v", err)
	}

	runner := &jobs.Runner{
		DB: db, Owner: hostname(), Fetch: fetcher.New(fetcher.DefaultLimits), LabMode: cfg.LabMode,
	}
	runner.Start(ctx)

	uiServer := &http.Server{
		Addr: *addr, Handler: srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second,
	}
	// The panel connects out to nodes on a timer: polls health and re-pushes
	// to any node that has drifted. Nodes never dial in.
	go srv.PollNodes(ctx, durationEnv("POLL_INTERVAL", 10*time.Second))
	// Usage needs coarser resolution than health, and each tick costs one
	// request per ingress node — so it runs on its own, slower ticker.
	go srv.PollCounters(ctx, durationEnv("COUNTERS_INTERVAL", 60*time.Second))
	// Renewal lives in one place on a schedule instead of on every node.
	go srv.RenewCerts(ctx, durationEnv("CERT_RENEW_INTERVAL", 12*time.Hour))

	go func() {
		slog.Info("panel UI/API listening", "addr", *addr, "public_url", publicURL, "version", version, "lab_mode", cfg.LabMode)
		if err := uiServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal("ui listener: %v", err)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = uiServer.Shutdown(sctx)
}

// loadOrCreatePanelClientCert mints the panel's client certificate — the single
// identity every node pins in its bundle. It travels in the state volume, so a
// panel restored from backup on a new host presents the same identity and every
// node still trusts it: that is what makes the panel movable.
func loadOrCreatePanelClientCert(dir string, caCertPEM, caKeyPEM []byte) (certPEM, keyPEM []byte, fp string, err error) {
	certPath := filepath.Join(dir, "panel-client.crt")
	keyPath := filepath.Join(dir, "panel-client.key")
	if fileExists(certPath) && fileExists(keyPath) {
		certPEM, err = os.ReadFile(certPath)
		if err != nil {
			return nil, nil, "", err
		}
		keyPEM, err = os.ReadFile(keyPath)
		if err != nil {
			return nil, nil, "", err
		}
		fp, _ = pki.Fingerprint(certPEM)
		return certPEM, keyPEM, fp, nil
	}
	caCert, caKey, err := pki.LoadCA(caCertPEM, caKeyPEM)
	if err != nil {
		return nil, nil, "", err
	}
	certPEM, keyPEM, err = pki.Issue(caCert, caKey, pki.CSRRequest{
		CommonName: "panel-client", Role: "panel", TTL: 3650 * 24 * time.Hour,
	})
	if err != nil {
		return nil, nil, "", err
	}
	if err := writeFile(certPath, certPEM, 0o644); err != nil {
		return nil, nil, "", err
	}
	if err := writeFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, nil, "", err
	}
	fp, _ = pki.Fingerprint(certPEM)
	slog.Info("created the panel client certificate", "path", certPath, "fingerprint", fp)
	return certPEM, keyPEM, fp, nil
}

func loadOrCreateCA(dir string) (certPEM, keyPEM []byte, err error) {
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	if fileExists(certPath) && fileExists(keyPath) {
		c, err := os.ReadFile(certPath)
		if err != nil {
			return nil, nil, err
		}
		k, err := os.ReadFile(keyPath)
		return c, k, err
	}
	certPEM, keyPEM, err = pki.NewCA("smartdns-panel-ca")
	if err != nil {
		return nil, nil, err
	}
	if err := writeFile(certPath, certPEM, 0o644); err != nil {
		return nil, nil, err
	}
	if err := writeFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, nil, err
	}
	slog.Info("created a new panel certificate authority", "path", certPath)
	return certPEM, keyPEM, nil
}

func loadOrCreateSigningKey(dir string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	path := filepath.Join(dir, "manifest-signing.key")
	if b, err := os.ReadFile(path); err == nil {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(raw) != ed25519.PrivateKeySize {
			return nil, nil, errors.New("manifest signing key is corrupt; restore it from backup")
		}
		priv := ed25519.PrivateKey(raw)
		return priv.Public().(ed25519.PublicKey), priv, nil
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, nil, err
	}
	if err := writeFile(path, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600); err != nil {
		return nil, nil, err
	}
	slog.Info("created a new manifest signing key", "path", path)
	return pub, priv, nil
}

// bootstrapAdmin creates the first owner account from the environment, or
// prints a generated password once so the operator can sign in.
func bootstrapAdmin(ctx context.Context, db *store.DB) error {
	n, err := store.Value[int](ctx, db, `SELECT count(*)::int FROM users`)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	email := strings.ToLower(strings.TrimSpace(env("ADMIN_EMAIL", "admin@localhost")))
	password := os.Getenv("ADMIN_PASSWORD")
	generated := false
	if len(password) < 12 {
		password = auth.RandomToken(15)
		generated = true
	}
	hash, err := auth.HashPassword(password, auth.DefaultParams)
	if err != nil {
		return err
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO users (email, password_hash, role, display_name) VALUES ($1,$2,'owner','Владелец')`,
		email, hash); err != nil {
		return err
	}
	if generated {
		fmt.Printf("\n=== Учётная запись владельца создана ===\n  email:  %s\n  пароль: %s\n"+
			"  Смените пароль после первого входа (Настройки → Безопасность).\n\n", email, password)
	}
	slog.Info("owner account created", "email", email)
	return nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func writeFile(p string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	return os.WriteFile(p, b, mode)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func durationEnv(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return def
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "panel-" + auth.RandomToken(4)
	}
	return h
}

func fatal(format string, args ...any) {
	slog.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}
