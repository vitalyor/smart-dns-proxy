// Command sni-proxy is the ingress TCP:443 dispatcher. It sniffs the TLS SNI,
// checks it against the active revision and tunnels the untouched stream to an
// egress relay. It never terminates TLS and never proxies unmanaged names.
package main

import (
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"smartdns/node/nodecfg"
	"smartdns/node/proxy"
	"smartdns/shared/logging"
	"smartdns/shared/metrics"
	"smartdns/shared/model"
)

var version = "dev"

func main() {
	var (
		cfgPath  = flag.String("config", env("SMARTDNS_CONFIG", "/etc/smartdns/active/config.json"), "active node config artifact")
		identity = flag.String("identity", env("SMARTDNS_IDENTITY", "/var/lib/smartdns-agent/identity"), "node identity directory")
		listen   = flag.String("listen", env("PROXY_ADDR", ":443"), "TCP listen address for managed HTTPS")
		dohBack  = flag.String("doh-backend", env("DOH_BACKEND", "dns-frontend:8443"), "local DoH listener to forward the DoH hostname's SNI to (empty disables)")
		metrAddr = flag.String("metrics", env("METRICS_ADDR", "127.0.0.1:9102"), "Prometheus metrics address")
		showVer  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		os.Stdout.WriteString("sni-proxy " + version + "\n")
		return
	}
	level := logging.Setup("sni-proxy")

	loader, err := nodecfg.WaitFor(*cfgPath, 10*time.Minute)
	if err != nil {
		slog.Error("cannot load node configuration", "path", *cfgPath, "err", err)
		os.Exit(1)
	}
	level.Follow(loader.Get().LogLevel)
	loader.OnApply(func(c *model.NodeConfig) { level.Follow(c.LogLevel) })
	tlsCfg, err := nodecfg.DefaultIdentity(*identity).ClientTLS()
	if err != nil {
		slog.Error("cannot load node identity", "dir", *identity, "err", err)
		os.Exit(1)
	}

	p := proxy.New(loader.Get(), tlsCfg)
	p.SetDoHBackend(*dohBack)
	loader.OnApply(func(c *model.NodeConfig) { p.Apply(c) })
	go loader.Watch(2 * time.Second)
	p.StartProbes()

	l, err := net.Listen("tcp", *listen)
	if err != nil {
		slog.Error("listen failed", "addr", *listen, "err", err)
		os.Exit(1)
	}
	slog.Info("sni-proxy listening", "version", version, "addr", *listen, "revision", loader.Get().RevisionID)

	if *metrAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics.Handler())
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(p.Status()))
		})
		go func() {
			_ = (&http.Server{Addr: *metrAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe()
		}()
	}

	go func() {
		if err := p.Serve(l); err != nil {
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
