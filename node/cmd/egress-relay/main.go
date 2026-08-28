// Command egress-relay terminates the ingress tunnel and dials the real origin.
// Only destinations present in the active revision allowlist are reachable.
package main

import (
	"crypto/tls"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"smartdns/node/nodecfg"
	"smartdns/node/relay"
	"smartdns/shared/logging"
	"smartdns/shared/metrics"
	"smartdns/shared/model"
)

var version = "dev"

func main() {
	var (
		cfgPath  = flag.String("config", env("SMARTDNS_CONFIG", "/etc/smartdns/active/config.json"), "active node config artifact")
		identity = flag.String("identity", env("SMARTDNS_IDENTITY", "/var/lib/smartdns-agent/identity"), "node identity directory")
		listen   = flag.String("listen", env("RELAY_ADDR", ":8443"), "tunnel listen address")
		metrAddr = flag.String("metrics", env("METRICS_ADDR", "127.0.0.1:9103"), "Prometheus metrics address")
		showVer  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		os.Stdout.WriteString("egress-relay " + version + "\n")
		return
	}
	level := logging.Setup("egress-relay")

	loader, err := nodecfg.WaitFor(*cfgPath, 10*time.Minute)
	if err != nil {
		slog.Error("cannot load node configuration", "path", *cfgPath, "err", err)
		os.Exit(1)
	}
	level.Follow(loader.Get().LogLevel)
	loader.OnApply(func(c *model.NodeConfig) { level.Follow(c.LogLevel) })
	tlsCfg, err := nodecfg.DefaultIdentity(*identity).ServerTLS()
	if err != nil {
		slog.Error("cannot load node identity", "dir", *identity, "err", err)
		os.Exit(1)
	}

	r := relay.New(loader.Get())
	loader.OnApply(func(c *model.NodeConfig) { r.Apply(c) })
	go loader.Watch(2 * time.Second)

	l, err := tls.Listen("tcp", *listen, tlsCfg)
	if err != nil {
		slog.Error("listen failed", "addr", *listen, "err", err)
		os.Exit(1)
	}
	slog.Info("egress-relay listening", "version", version, "addr", *listen, "revision", loader.Get().RevisionID)

	if *metrAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics.Handler())
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			c := loader.Get()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","revision":"` + c.RevisionID + `"}`))
		})
		go func() {
			_ = (&http.Server{Addr: *metrAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe()
		}()
	}

	go func() {
		if err := r.Serve(l); err != nil {
			slog.Error("serve stopped", "err", err)
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	_ = l.Close()
	slog.Info("shutting down")
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
