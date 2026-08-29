// Command node-agent runs on every ingress and egress node. Under the push
// model it is a small mutual-TLS server: the panel connects to it to push
// config and poll health. It never dials the panel and never needs the Docker
// socket — the data plane reloads the active artifact on its own.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"smartdns/agent/internal/agentcore"
	"smartdns/shared/logging"
	"smartdns/shared/metrics"
)

var version = "2.0.1"

func main() {
	agentcore.Version = version
	var (
		bundle     = flag.String("bundle", env("NODE_BUNDLE", ""), "provisioning bundle from the panel (base64); needed only until the identity is on disk")
		bundleFile = flag.String("bundle-file", env("NODE_BUNDLE_FILE", ""), "path to read the bundle from, waited for if missing (lab/orchestration use)")
		role       = flag.String("role", env("NODE_ROLE", ""), "ingress or egress (must match the bundle)")
		stateDir   = flag.String("state", env("AGENT_STATE_DIR", "/var/lib/smartdns-agent"), "state directory")
		listen     = flag.String("listen", env("MGMT_ADDR", ":3333"), "management listen address the panel connects to")
		metrAddr   = flag.String("metrics", env("METRICS_ADDR", "127.0.0.1:9104"), "Prometheus metrics address")
		showVer    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println("node-agent", version)
		return
	}
	level := logging.Setup("node-agent")

	a, err := agentcore.New(agentcore.Config{
		StateDir: *stateDir, ListenAddr: *listen, Role: *role,
		KeepRevisions: 3, Level: level,
	})
	if err != nil {
		fatal("agent init: %v", err)
	}

	// The bundle provisions the identity once; after that it is ignored, so it
	// is harmless to leave it set across restarts.
	if !a.Provisioned() {
		b := *bundle
		if b == "" && *bundleFile != "" {
			b = waitForBundle(*bundleFile)
		}
		if b == "" {
			fatal("this node is not provisioned: provide the bundle from the panel via --bundle, NODE_BUNDLE or NODE_BUNDLE_FILE")
		}
		if err := a.LoadBundle(b); err != nil {
			fatal("bundle rejected: %v", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *metrAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics.Handler())
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			st := a.State()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"ok","node_id":%q,"applied_revision":%q}`, st.NodeID, st.AppliedRevisionID)
		})
		go func() {
			_ = (&http.Server{Addr: *metrAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe()
		}()
	}

	if err := a.Serve(ctx); err != nil {
		fatal("%v", err)
	}
}

// waitForBundle blocks until the bundle file appears and is non-empty. A data
// plane orchestrator may start the agent before the panel has minted the node.
func waitForBundle(path string) string {
	for i := 0; i < 300; i++ {
		if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			return strings.TrimSpace(string(b))
		}
		if i == 0 {
			slog.Info("waiting for the provisioning bundle", "path", path)
		}
		time.Sleep(2 * time.Second)
	}
	return ""
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fatal(format string, args ...any) {
	slog.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}
