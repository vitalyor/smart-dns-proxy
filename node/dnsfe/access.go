package dnsfe

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"smartdns/shared/model"
)

// The DoH token set lives in its own file, written by the agent, and never
// inside the config artifact: access changes far more often than configuration,
// and applying a config must not clobber a newer set (ADR 0012).

// LoadAccess reads the token set. A missing file is not an error — a freshly
// provisioned node simply holds no tokens until the panel pushes them.
func LoadAccess(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var f model.AccessSet
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Tokens, nil
}

// WatchAccess applies the set once and then whenever the file changes, until ctx
// is done. Polling the digest keeps it dependency-free and survives the atomic
// rename the agent uses to publish a new set.
func WatchAccess(ctx context.Context, path string, every time.Duration, apply func([]string)) {
	last := ""
	load := func() {
		tokens, err := LoadAccess(path)
		if err != nil {
			slog.Warn("cannot read access set", "path", path, "err", err)
			return
		}
		h := model.AccessHash(tokens)
		if h == last {
			return
		}
		last = h
		apply(tokens)
		slog.Info("access set applied", "tokens", len(tokens), "hash", h[:12])
	}
	load()
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				load()
			}
		}
	}()
}
