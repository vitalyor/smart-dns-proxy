// Package nodecfg loads the active revision artifact and hot-reloads it when
// the agent atomically swaps the `active` symlink. No container restart and no
// Docker socket are involved in applying a revision.
package nodecfg

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"smartdns/shared/model"
)

// Loader watches a config path and publishes the latest valid NodeConfig.
type Loader struct {
	path    string
	cur     atomic.Pointer[model.NodeConfig]
	stamp   string
	onApply []func(*model.NodeConfig)
}

// New reads the config once; it fails if the first read is invalid.
func New(path string) (*Loader, error) {
	l := &Loader{path: path}
	if err := l.reload(); err != nil {
		return nil, err
	}
	return l, nil
}

// WaitFor blocks until the agent has published a first valid configuration.
// A data plane container may legitimately start before the node has enrolled
// or before the first revision has been applied.
func WaitFor(path string, timeout time.Duration) (*Loader, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		l, err := New(path)
		if err == nil {
			return l, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, lastErr
		}
		slog.Info("waiting for the agent to publish a configuration", "path", path)
		time.Sleep(3 * time.Second)
	}
}

// OnApply registers a callback fired on every successful (re)load.
func (l *Loader) OnApply(f func(*model.NodeConfig)) { l.onApply = append(l.onApply, f) }

// Get returns the current config. Never nil after New succeeds.
func (l *Loader) Get() *model.NodeConfig { return l.cur.Load() }

// Watch polls for changes. Poll beats inotify here: the agent replaces a
// symlink, and a poll loop cannot miss or double-fire on the swap.
func (l *Loader) Watch(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		st, err := os.Stat(l.path)
		if err != nil {
			continue
		}
		if stampOf(st) == l.stamp {
			continue
		}
		if err := l.reload(); err != nil {
			slog.Error("reload failed, keeping previous config", "err", err)
			continue
		}
		c := l.Get()
		slog.Info("configuration reloaded", "revision", c.RevisionID, "sequence", c.Sequence)
	}
}

func (l *Loader) reload() error {
	b, err := os.ReadFile(l.path)
	if err != nil {
		return err
	}
	var c model.NodeConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return err
	}
	l.cur.Store(&c)
	if st, err := os.Stat(l.path); err == nil {
		l.stamp = stampOf(st)
	}
	for _, f := range l.onApply {
		f(&c)
	}
	return nil
}

func stampOf(st os.FileInfo) string {
	return st.ModTime().UTC().Format(time.RFC3339Nano) + "/" + itoa(st.Size())
}

func itoa(n int64) string {
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
