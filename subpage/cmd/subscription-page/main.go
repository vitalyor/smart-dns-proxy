// Command subscription-page serves the public page where a subscriber manages
// their own devices. It is deployed separately from the panel and holds no state.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"smartdns/shared/logging"
	"smartdns/subpage/internal/subpage"
)

var version = "dev"

func main() {
	var (
		addr     = flag.String("addr", env("APP_ADDR", ":8080"), "listen address")
		panelURL = flag.String("panel", env("PANEL_URL", ""), "panel base URL, e.g. https://panel.example")
		apiKey   = flag.String("key", env("PANEL_API_KEY", ""), "scoped API key issued by the panel")
		cacheTTL = flag.Duration("cache", durEnv("CACHE_TTL", 5*time.Second), "how long a status answer stays fresh")
		staleFor = flag.Duration("stale", durEnv("STALE_FOR", 5*time.Minute), "how long a stale answer may cover a panel outage")
		showVer  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		os.Stdout.WriteString("subscription-page " + version + "\n")
		return
	}
	logging.Setup("subscription-page")

	cfg := subpage.Config{PanelURL: *panelURL, APIKey: *apiKey, CacheTTL: *cacheTTL, StaleFor: *staleFor, Version: version}
	if err := cfg.Validate(); err != nil {
		slog.Error("bad configuration", "err", err)
		os.Exit(1)
	}
	srv := &http.Server{Addr: *addr, Handler: subpage.New(cfg).Routes(), ReadHeaderTimeout: 5 * time.Second}

	go func() {
		slog.Info("subscription page listening", "addr", *addr, "panel", *panelURL, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listener stopped", "err", err)
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func durEnv(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
