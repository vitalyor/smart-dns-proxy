package agentcore

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"smartdns/shared/model"
)

// The DoH token set arrives on its own channel and is stored beside the config
// artifact, never inside it: access changes far more often than configuration,
// and a subscriber adding a device must not trigger a config rollout (ADR 0012).
func (c Config) accessPath() string { return filepath.Join(c.StateDir, "access.json") }

// handleAccess stores the full token set. Full set rather than a delta, so the
// operation is idempotent: a push that never arrived is repaired by the next one
// instead of leaving the node subtly wrong.
func (a *Agent) handleAccess(w http.ResponseWriter, r *http.Request) error {
	raw, err := readAll(r, 1<<20)
	if err != nil {
		return err
	}
	var set model.AccessSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return fmt.Errorf("access set is not valid JSON: %w", err)
	}
	b, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		return err
	}
	// 0644: the DNS frontend mounts the state directory read-only and reads it.
	if err := writeFileAtomic(a.cfg.accessPath(), b, 0o644); err != nil {
		return err
	}
	h := model.AccessHash(set.Tokens)
	slog.Info("access set stored", "tokens", len(set.Tokens), "hash", h[:12])
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "tokens": len(set.Tokens), "hash": h})
	return nil
}

// accessHash reports what this node currently holds, so the panel can spot drift
// during its ordinary health poll and re-push without any separate mechanism.
func (a *Agent) accessHash() string {
	b, err := os.ReadFile(a.cfg.accessPath())
	if err != nil {
		return model.AccessHash(nil)
	}
	var set model.AccessSet
	if err := json.Unmarshal(b, &set); err != nil {
		return model.AccessHash(nil)
	}
	return model.AccessHash(set.Tokens)
}
