package agentcore

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"smartdns/shared/model"
)

func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	a, err := New(Config{StateDir: dir, Role: "egress"})
	if err != nil {
		t.Fatal(err)
	}
	a.state.NodeID = "node-1"
	a.state.Role = "egress"
	return a
}

func egressConfig(rev string, seq int64) []byte {
	c := model.NodeConfig{
		SchemaVersion: 1, RevisionID: rev, Sequence: seq,
		NodeID: "node-1", Role: "egress",
		Egress: model.EgressConfig{AllowedPorts: []int{443}},
	}
	b, _ := json.Marshal(c)
	return b
}

// A pushed config must be staged, activated via the symlink the data plane
// reads, and recorded in state — the heart of the push apply path.
func TestHandleConfigApplies(t *testing.T) {
	a := newTestAgent(t)
	req := httptest.NewRequest("POST", "/v1/config", bytes.NewReader(egressConfig("rev-1", 1)))
	rw := httptest.NewRecorder()
	if err := a.handleConfig(rw, req); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if a.State().AppliedRevisionID != "rev-1" || a.State().AppliedSequence != 1 {
		t.Fatalf("state not updated: %+v", a.State())
	}
	// The active symlink must point at a dir holding the pushed config.
	got, err := os.ReadFile(filepath.Join(a.cfg.activeLink(), "config.json"))
	if err != nil {
		t.Fatalf("active config unreadable: %v", err)
	}
	var c model.NodeConfig
	_ = json.Unmarshal(got, &c)
	if c.RevisionID != "rev-1" {
		t.Fatalf("active config is wrong revision: %s", c.RevisionID)
	}
}

// A config addressed to another node must be refused — the node validates
// ownership even though the transport already authenticated the panel.
func TestHandleConfigRejectsForeignNode(t *testing.T) {
	a := newTestAgent(t)
	c := model.NodeConfig{SchemaVersion: 1, RevisionID: "r", NodeID: "someone-else", Role: "egress",
		Egress: model.EgressConfig{AllowedPorts: []int{443}}}
	b, _ := json.Marshal(c)
	req := httptest.NewRequest("POST", "/v1/config", bytes.NewReader(b))
	if err := a.handleConfig(httptest.NewRecorder(), req); err == nil {
		t.Fatal("config for another node must be rejected")
	}
	if a.State().AppliedRevisionID != "" {
		t.Fatal("a rejected config must not become active")
	}
}
