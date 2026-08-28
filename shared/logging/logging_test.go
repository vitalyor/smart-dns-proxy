package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"": slog.LevelInfo, "info": slog.LevelInfo, "DEBUG": slog.LevelDebug,
		" warn ": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError,
	} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Fatalf("Parse(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := Parse("verbose"); err == nil {
		t.Fatal("Parse should reject an unknown level")
	}
}

// A repeating warning must not be able to fill the disk, and the operator must
// still learn how often it happened.
func TestThrottleCollapsesRepeats(t *testing.T) {
	var buf bytes.Buffer
	h := NewThrottle(slog.NewJSONHandler(&buf, nil), time.Minute)
	base := time.Now()

	emit := func(at time.Time, msg string) {
		r := slog.NewRecord(at, slog.LevelWarn, msg, 0)
		if err := h.Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < 500; i++ {
		emit(base.Add(time.Duration(i)*time.Millisecond), "destination refused")
	}
	emit(base, "something else")

	if n := strings.Count(buf.String(), "destination refused"); n != 1 {
		t.Fatalf("500 repeats produced %d lines, want 1", n)
	}
	if !strings.Contains(buf.String(), "something else") {
		t.Fatal("a distinct message must not be swallowed")
	}

	// After the window the message is reported again, with the drop count.
	buf.Reset()
	emit(base.Add(2*time.Minute), "destination refused")
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["suppressed"] != float64(499) {
		t.Fatalf("suppressed = %v, want 499", rec["suppressed"])
	}
}

// Debug is the escape hatch: when it is on, nothing is collapsed.
func TestThrottleLeavesDebugAlone(t *testing.T) {
	var buf bytes.Buffer
	h := NewThrottle(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}), time.Minute)
	for i := 0; i < 10; i++ {
		r := slog.NewRecord(time.Now(), slog.LevelDebug, "per-connection detail", 0)
		_ = h.Handle(context.Background(), r)
	}
	if n := strings.Count(buf.String(), "per-connection detail"); n != 10 {
		t.Fatalf("debug records collapsed: got %d, want 10", n)
	}
}

func TestSetRefusesGarbage(t *testing.T) {
	l := &Level{v: new(slog.LevelVar)}
	l.v.Set(slog.LevelInfo)
	if err := l.Set("loud"); err == nil {
		t.Fatal("Set should reject an unknown level")
	}
	if l.String() != "info" {
		t.Fatalf("level changed despite the error: %v", l.String())
	}
	if err := l.Set("debug"); err != nil || l.String() != "debug" {
		t.Fatalf("Set(debug) = %v, level %v", err, l.String())
	}
}

// An operator debugging one node must not be overridden by the panel-wide
// setting pushed in the next revision.
func TestLocalLevelWinsOverControlPlane(t *testing.T) {
	local := &Level{v: new(slog.LevelVar), local: true}
	local.v.Set(slog.LevelDebug)
	local.Follow("error")
	if local.String() != "debug" {
		t.Fatalf("control plane overrode a local LOG_LEVEL: %v", local.String())
	}

	managed := &Level{v: new(slog.LevelVar)}
	managed.v.Set(slog.LevelInfo)
	managed.Follow("error")
	if managed.String() != "error" {
		t.Fatalf("managed level did not follow the control plane: %v", managed.String())
	}
	managed.Follow("")
	if managed.String() != "error" {
		t.Fatal("an empty push must leave the level alone")
	}
}

// Turning debug on is the common case, and it must leave a record: the
// announcement has to land on whichever side of the switch can still print it.
func TestLevelChangeIsAlwaysAnnounced(t *testing.T) {
	for _, tc := range []struct{ from, to string }{
		{"warn", "debug"}, {"debug", "warn"}, {"info", "debug"}, {"debug", "info"},
	} {
		var buf bytes.Buffer
		l := &Level{v: new(slog.LevelVar)}
		start, _ := Parse(tc.from)
		l.v.Set(start)
		slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: l.v})))

		if err := l.Set(tc.to); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), "log level changed") {
			t.Fatalf("%s -> %s produced no record: %q", tc.from, tc.to, buf.String())
		}
	}
}
