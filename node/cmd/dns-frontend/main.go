// Command dns-frontend serves plain DNS, DoT and DoH on an ingress node.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/miekg/dns"

	"smartdns/node/dnsfe"
	"smartdns/node/nodecfg"
	"smartdns/shared/logging"
	"smartdns/shared/metrics"
	"smartdns/shared/model"
	"smartdns/shared/tlsutil"
)

var version = "dev"

func main() {
	var (
		cfgPath    = flag.String("config", env("SMARTDNS_CONFIG", "/etc/smartdns/active/config.json"), "path to the active node config artifact")
		udpAddr    = flag.String("udp", env("DNS_UDP_ADDR", ":53"), "plain DNS UDP listen address (empty disables)")
		tcpAddr    = flag.String("tcp", env("DNS_TCP_ADDR", ":53"), "plain DNS TCP listen address (empty disables)")
		dotAddr    = flag.String("dot", env("DNS_DOT_ADDR", ":853"), "DoT listen address (empty disables)")
		dohAddr    = flag.String("doh", env("DNS_DOH_ADDR", ":8443"), "DoH HTTPS listen address (empty disables)")
		dohPlain   = flag.String("doh-plain", env("DNS_DOH_PLAIN_ADDR", ""), "DoH plain HTTP listen address, for use behind a trusted TLS terminator")
		dohPath    = flag.String("doh-path", env("DNS_DOH_PATH", "/dns-query"), "DoH base path")
		certFile   = flag.String("cert", env("TLS_CERT_FILE", ""), "TLS certificate for DoT/DoH")
		keyFile    = flag.String("key", env("TLS_KEY_FILE", ""), "TLS key for DoT/DoH")
		metrAddr   = flag.String("metrics", env("METRICS_ADDR", "127.0.0.1:9101"), "Prometheus metrics listen address")
		logAddr    = flag.String("log-addr", env("DNS_LOG_ADDR", ":9053"), "internal HTTP address serving the live query log (reached by the node-agent only)")
		accessPath = flag.String("access", env("SMARTDNS_ACCESS", "/var/lib/smartdns-agent/access.json"), "path to the DoH token set written by the agent")
		showVer    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		os.Stdout.WriteString("dns-frontend " + version + "\n")
		return
	}
	level := logging.Setup("dns-frontend")

	loader, err := nodecfg.WaitFor(*cfgPath, 10*time.Minute)
	if err != nil {
		slog.Error("cannot load node configuration", "path", *cfgPath, "err", err)
		os.Exit(1)
	}
	cfg := loader.Get()
	router := dnsfe.NewRouter(cfg)
	srv := dnsfe.New(router)
	level.Follow(cfg.LogLevel)
	loader.OnApply(func(c *model.NodeConfig) {
		level.Follow(c.LogLevel)
		router.Apply(c)
		srv.Reload(c)
	})
	go loader.Watch(2 * time.Second)

	// Access travels on its own channel: the agent writes this file, we pick it
	// up here, and a configuration rollout never touches it.
	accessCtx, stopAccess := context.WithCancel(context.Background())
	defer stopAccess()
	dnsfe.WatchAccess(accessCtx, *accessPath, 2*time.Second, srv.ApplyTokens)

	slog.Info("dns-frontend starting",
		"version", version, "revision", cfg.RevisionID, "services", len(cfg.Services),
		"upstream", cfg.DNS.Upstream, "access_mode", cfg.DNS.Access.Mode)

	var servers []*dns.Server
	start := func(s *dns.Server, name string) {
		servers = append(servers, s)
		go func() {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, net.ErrClosed) {
				slog.Error("listener stopped", "listener", name, "err", err)
				os.Exit(1)
			}
		}()
		slog.Info("listening", "listener", name, "addr", s.Addr)
	}

	if *udpAddr != "" {
		start(srv.ListenUDP(*udpAddr), "dns-udp")
	}
	if *tcpAddr != "" {
		start(srv.ListenTCP(*tcpAddr), "dns-tcp")
	}

	var tlsCfg *tls.Config
	if *dotAddr != "" || *dohAddr != "" {
		if *certFile != "" && *keyFile != "" {
			tlsCfg, err = tlsutil.ReloadingServerConfig(*certFile, *keyFile)
		} else {
			// No cert files: only self-signed lab mode can produce one.
			names := []string{cfg.DNS.DoTHostname, cfg.DNS.DoHHostname}
			tlsCfg, err = tlsutil.ServerConfig(*certFile, *keyFile, names)
		}
		if err != nil {
			// Missing/broken TLS must not take down plain DNS on :53. DoT/DoH are
			// simply skipped until a certificate is provided.
			slog.Warn("DoT/DoH disabled: TLS not configured; plain DNS on :53 stays up",
				"err", err, "hint", "set TLS_CERT_FILE/TLS_KEY_FILE (or ALLOW_SELF_SIGNED_TLS=1 for lab)")
			tlsCfg = nil
		}
	}
	if *dotAddr != "" && tlsCfg != nil {
		c := tlsCfg.Clone()
		c.NextProtos = []string{"dot"}
		start(srv.ListenTLS(*dotAddr, c), "dot")
	}

	mux := http.NewServeMux()
	mux.Handle(strings.TrimRight(*dohPath, "/")+"/", srv.DoHHandler(*dohPath))
	mux.Handle(*dohPath, srv.DoHHandler(*dohPath))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		c := loader.Get()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","revision":"` + c.RevisionID + `","services":` + itoa(len(c.Services)) + `}`))
	})

	if *dohAddr != "" && tlsCfg != nil {
		h := tlsCfg.Clone()
		h.NextProtos = []string{"h2", "http/1.1"}
		srv := &http.Server{Addr: *dohAddr, Handler: mux, TLSConfig: h, ReadHeaderTimeout: 5 * time.Second}
		// The sni-proxy forwards DoH with a PROXY-protocol header so the real
		// client IP survives; direct clients on :8443 send none and pass through.
		ln, err := net.Listen("tcp", *dohAddr)
		if err != nil {
			slog.Error("cannot listen for DoH", "addr", *dohAddr, "err", err)
			os.Exit(1)
		}
		go func() {
			slog.Info("listening", "listener", "doh", "addr", *dohAddr)
			if err := srv.ServeTLS(dnsfe.WrapProxyProto(ln), "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("listener stopped", "listener", "doh", "err", err)
				os.Exit(1)
			}
		}()
	}
	if *dohPlain != "" {
		go serveHTTP(&http.Server{Addr: *dohPlain, Handler: mux,
			ReadHeaderTimeout: 5 * time.Second}, false, "doh-plain")
	}
	if *metrAddr != "" {
		mm := http.NewServeMux()
		mm.Handle("/metrics", metrics.Handler())
		go serveHTTP(&http.Server{Addr: *metrAddr, Handler: mm, ReadHeaderTimeout: 5 * time.Second}, false, "metrics")
	}
	if *logAddr != "" {
		lm := http.NewServeMux()
		lm.HandleFunc("/log", srv.LogHandler())
		lm.HandleFunc("/counters", srv.CountersHandler())
		// Отпечаток именно загруженного сертификата, а не файла на диске:
		// панель по нему видит, доехало ли продление до слушателя.
		lm.HandleFunc("/certinfo", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			leaf, err := tlsutil.LeafInfo(tlsCfg)
			if err != nil {
				w.Write([]byte(`{"available":false}`))
				return
			}
			w.Write([]byte(`{"available":true,"fingerprint":"` + tlsutil.Fingerprint(leaf) +
				`","not_after":"` + leaf.NotAfter.UTC().Format(time.RFC3339) + `"}`))
		})
		go serveHTTP(&http.Server{Addr: *logAddr, Handler: lm, ReadHeaderTimeout: 5 * time.Second}, false, "query-log")
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	slog.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		dnsfe.Shutdown(ctx, s)
	}
}

func serveHTTP(s *http.Server, useTLS bool, name string) {
	slog.Info("listening", "listener", name, "addr", s.Addr)
	var err error
	if useTLS {
		err = s.ListenAndServeTLS("", "")
	} else {
		err = s.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("listener stopped", "listener", name, "err", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
