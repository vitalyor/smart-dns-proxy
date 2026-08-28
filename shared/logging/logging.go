// Package logging gives every binary the same log level handling: one level
// read from LOG_LEVEL at startup, changeable at runtime without a restart, and
// a throttle that keeps a repeating condition from filling the disk.
package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// Window is how long a repeated message stays suppressed. Debug is never
// throttled: turning debug on means you want to see everything.
const Window = time.Minute

// maxKeys bounds the throttle map. Keys are (level, message) pairs, so the
// bound is the number of call sites; the reset below is a safety net, not a
// working eviction policy.
const maxKeys = 1024

// Parse maps a configuration string onto a level. Empty means info.
func Parse(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("unknown log level %q (debug|info|warn|error)", s)
}

// Level is the process log level. It carries whether the value came from the
// local environment, because a locally set LOG_LEVEL must survive whatever the
// control plane pushes: an operator debugging one node should not be
// overridden by a panel-wide setting.
type Level struct {
	v     *slog.LevelVar
	local bool
}

// Setup installs the process logger and returns the level handle.
func Setup(component string) *Level {
	env, ok := os.LookupEnv("LOG_LEVEL")
	level, err := Parse(env)

	l := &Level{v: new(slog.LevelVar), local: ok && strings.TrimSpace(env) != ""}
	l.v.Set(level)

	var h slog.Handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l.v})
	if os.Getenv("LOG_THROTTLE") != "off" {
		h = NewThrottle(h, Window)
	}
	slog.SetDefault(slog.New(h).With("component", component))

	if err != nil {
		l.local = false
		slog.Warn("ignoring LOG_LEVEL, falling back to info", "err", err)
	}
	return l
}

// String reports the active level.
func (l *Level) String() string { return strings.ToLower(l.v.Level().String()) }

// Local reports whether the level is pinned by the local environment.
func (l *Level) Local() bool { return l.local }

// Set changes the level at runtime. An unparseable value is refused rather
// than silently reset, so a typo never turns logging off.
func (l *Level) Set(s string) error {
	level, err := Parse(s)
	if err != nil {
		return err
	}
	old := l.v.Level()
	if old == level {
		return nil
	}
	// Announce on whichever side of the switch can still print it: raising
	// verbosity is announced after, lowering it before. Either way the change
	// leaves a record instead of the logs silently going quiet or loud.
	say := func() {
		slog.Info("log level changed", "from", strings.ToLower(old.String()), "to", strings.ToLower(level.String()))
	}
	if level < old {
		l.v.Set(level)
		say()
	} else {
		say()
		l.v.Set(level)
	}
	return nil
}

// Follow applies a level pushed by the control plane. An empty value means the
// panel has no opinion; a local LOG_LEVEL always wins.
func (l *Level) Follow(s string) {
	if s == "" || l.local {
		return
	}
	if err := l.Set(s); err != nil {
		slog.Warn("ignoring log level from the control plane", "err", err)
	}
}

type throttle struct {
	h      slog.Handler
	window time.Duration

	mu   *sync.Mutex
	seen map[string]*counter
}

type counter struct {
	first   time.Time
	dropped int
}

// NewThrottle collapses repeats of the same message inside window into one
// record carrying a "suppressed" count.
func NewThrottle(h slog.Handler, window time.Duration) slog.Handler {
	return &throttle{h: h, window: window, mu: new(sync.Mutex), seen: map[string]*counter{}}
}

func (t *throttle) Enabled(ctx context.Context, l slog.Level) bool { return t.h.Enabled(ctx, l) }

func (t *throttle) WithAttrs(as []slog.Attr) slog.Handler {
	return &throttle{h: t.h.WithAttrs(as), window: t.window, mu: t.mu, seen: t.seen}
}

func (t *throttle) WithGroup(name string) slog.Handler {
	return &throttle{h: t.h.WithGroup(name), window: t.window, mu: t.mu, seen: t.seen}
}

func (t *throttle) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < slog.LevelInfo {
		return t.h.Handle(ctx, r)
	}
	key := r.Level.String() + "\x00" + r.Message

	t.mu.Lock()
	c, ok := t.seen[key]
	if ok && r.Time.Sub(c.first) < t.window {
		c.dropped++
		t.mu.Unlock()
		return nil
	}
	dropped := 0
	if ok {
		dropped = c.dropped
	}
	if len(t.seen) > maxKeys {
		t.seen = make(map[string]*counter, maxKeys)
	}
	t.seen[key] = &counter{first: r.Time}
	t.mu.Unlock()

	if dropped > 0 {
		r.AddAttrs(slog.Int("suppressed", dropped))
	}
	return t.h.Handle(ctx, r)
}
