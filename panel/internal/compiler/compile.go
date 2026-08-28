// Package compiler turns the panel database state into immutable, per-node
// artifacts. One normalized rule set drives DNS rewrite, ingress routing and
// the egress allowlist inside the same revision: they can never diverge.
package compiler

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"smartdns/shared/domainset"
	"smartdns/shared/model"
)

// Input is the full desired state handed to the compiler.
type Input struct {
	RevisionID   string
	Sequence     int64
	Services     []ServiceInput
	Nodes        []NodeInput
	DNS          model.DNSConfig
	Ingress      model.IngressConfig
	EgressTuning model.EgressConfig
	MinAgentVer  string
	LabMode      bool
	// LogLevel is pushed to every node in this revision. Empty leaves each
	// node on its own default.
	LogLevel   string
	SigningKey ed25519.PrivateKey
}

// ServiceInput is one enabled service with its resolved dependencies.
type ServiceInput struct {
	ID            string
	Slug          string
	Name          string
	Priority      int
	TTL           uint32
	AllowedPorts  []int
	UDPMode       string
	Entries       []domainset.Entry
	RuleSetHash   string
	IngressNodes  []string // node IDs, already filtered to eligible members
	IngressMode   string
	EgressMembers []EgressMember
	EgressMode    string
	Policy        model.EgressPolicy
}

// EgressMember is a candidate egress node for a service.
type EgressMember struct {
	NodeID   string
	Priority int
	Weight   int
}

// NodeInput describes a registered node.
type NodeInput struct {
	ID            string
	Name          string
	Role          string
	PublicIPv4    string
	PublicIPv6    string
	RelayEndpoint string
	RelaySNI      string
	Eligible      bool
}

// Output is a compiled revision.
type Output struct {
	Manifest  model.Manifest
	Artifacts map[string][]byte // node ID -> canonical JSON
	Configs   map[string]*model.NodeConfig
	Warnings  []string
	Summary   Summary
}

// Summary is the operator-facing digest of a revision.
type Summary struct {
	Services       int            `json:"services"`
	IngressNodes   int            `json:"ingress_nodes"`
	EgressNodes    int            `json:"egress_nodes"`
	TotalRules     int            `json:"total_rules"`
	RulesByService map[string]int `json:"rules_by_service"`
	LabMode        bool           `json:"lab_mode"`
}

// Conflict reports a domain claimed by two services at the same priority.
type Conflict struct {
	Value    string   `json:"value"`
	Kind     string   `json:"kind"`
	Services []string `json:"services"`
	Priority int      `json:"priority"`
}

// ConflictError is returned when the desired state is ambiguous.
type ConflictError struct{ Conflicts []Conflict }

func (e *ConflictError) Error() string {
	names := make([]string, 0, len(e.Conflicts))
	for i, c := range e.Conflicts {
		if i == 5 {
			names = append(names, fmt.Sprintf("… and %d more", len(e.Conflicts)-5))
			break
		}
		names = append(names, fmt.Sprintf("%s (%s) claimed by %s", c.Value, c.Kind, strings.Join(c.Services, ", ")))
	}
	return "rule conflicts at equal priority: " + strings.Join(names, "; ")
}

// Compile validates the input and produces per-node artifacts.
func Compile(in Input) (*Output, error) {
	if in.RevisionID == "" {
		return nil, errors.New("revision id is required")
	}
	nodes := map[string]NodeInput{}
	for _, n := range in.Nodes {
		nodes[n.ID] = n
	}

	if err := detectConflicts(in.Services); err != nil {
		return nil, err
	}

	out := &Output{
		Artifacts: map[string][]byte{},
		Configs:   map[string]*model.NodeConfig{},
		Summary:   Summary{RulesByService: map[string]int{}, LabMode: in.LabMode},
	}
	now := time.Now().UTC()

	// Per-service compiled shape shared by ingress nodes.
	compiled := make([]model.Service, 0, len(in.Services))
	// Egress node -> union of allowed domains and ports.
	egressAllow := map[string][]domainset.Entry{}
	egressPorts := map[string]map[int]bool{}

	for _, s := range in.Services {
		if len(s.Entries) == 0 {
			out.Warnings = append(out.Warnings, fmt.Sprintf("service %q has an empty rule set and will match nothing", s.Slug))
		}
		var v4, v6 []string
		for _, id := range s.IngressNodes {
			n, ok := nodes[id]
			if !ok || !n.Eligible {
				continue
			}
			if n.PublicIPv4 != "" {
				v4 = append(v4, n.PublicIPv4)
			}
			if n.PublicIPv6 != "" {
				v6 = append(v6, n.PublicIPv6)
			}
		}
		if len(v4) == 0 {
			out.Warnings = append(out.Warnings, fmt.Sprintf("service %q has no healthy ingress node with a public IPv4 address", s.Slug))
		}
		sort.Strings(v4)
		sort.Strings(v6)

		policy := s.Policy
		policy.Mode = orDefault(s.EgressMode, "primary_fallback")
		policy.Targets = nil
		for _, m := range s.EgressMembers {
			n, ok := nodes[m.NodeID]
			if !ok || !n.Eligible {
				continue
			}
			ep := n.RelayEndpoint
			if ep == "" && n.PublicIPv4 != "" {
				ep = n.PublicIPv4 + ":8443"
			}
			if ep == "" {
				out.Warnings = append(out.Warnings, fmt.Sprintf("egress node %q has no relay endpoint and was skipped", n.Name))
				continue
			}
			policy.Targets = append(policy.Targets, model.EgressTarget{
				NodeID: n.ID, Name: n.Name, Endpoint: ep,
				SNI: orDefault(n.RelaySNI, n.Name), Priority: m.Priority, Weight: m.Weight,
			})
			egressAllow[n.ID] = append(egressAllow[n.ID], s.Entries...)
			if egressPorts[n.ID] == nil {
				egressPorts[n.ID] = map[int]bool{}
			}
			for _, p := range portsOrDefault(s.AllowedPorts) {
				egressPorts[n.ID][p] = true
			}
		}
		sort.SliceStable(policy.Targets, func(i, j int) bool { return policy.Targets[i].Priority < policy.Targets[j].Priority })
		if len(policy.Targets) == 0 {
			return nil, fmt.Errorf("service %q has no usable egress node: pick another egress group or bring a node back", s.Slug)
		}
		if policy.FailThreshold == 0 {
			policy.FailThreshold = 3
		}
		if policy.RiseThreshold == 0 {
			policy.RiseThreshold = 2
		}
		if policy.ProbeInterval == 0 {
			policy.ProbeInterval = 30
		}

		cs := model.Service{
			Slug: s.Slug, Name: s.Name, Priority: s.Priority, TTL: clampTTL(s.TTL),
			AllowedPorts: portsOrDefault(s.AllowedPorts),
			UDPMode:      orDefault(s.UDPMode, "disabled_fallback"),
			Match:        domainset.NewSet(s.Entries),
			RuleSetHash:  s.RuleSetHash,
			IngressV4:    v4, IngressV6: v6,
			Egress: policy,
		}
		compiled = append(compiled, cs)
		out.Summary.RulesByService[s.Slug] = len(s.Entries)
		out.Summary.TotalRules += len(s.Entries)
	}
	sort.Slice(compiled, func(i, j int) bool { return compiled[i].Slug < compiled[j].Slug })
	out.Summary.Services = len(compiled)

	for _, n := range in.Nodes {
		cfg := &model.NodeConfig{
			SchemaVersion: 1, RevisionID: in.RevisionID, Sequence: in.Sequence,
			NodeID: n.ID, NodeName: n.Name, Role: n.Role,
			LogLevel: in.LogLevel,
		}
		switch n.Role {
		case "ingress":
			cfg.Services = servicesForIngress(compiled, n.ID, in.Services)
			cfg.DNS = in.DNS
			cfg.Ingress = in.Ingress
			if cfg.DNS.PublishAAAA && n.PublicIPv6 == "" {
				cfg.DNS.PublishAAAA = false
				cfg.Notes = append(cfg.Notes, "AAAA publication disabled: this ingress has no IPv6 address")
			}
			out.Summary.IngressNodes++
		case "egress":
			entries := domainset.Merge(egressAllow[n.ID], nil)
			ports := make([]int, 0, len(egressPorts[n.ID]))
			for p := range egressPorts[n.ID] {
				ports = append(ports, p)
			}
			sort.Ints(ports)
			eg := in.EgressTuning
			eg.Allow = domainset.NewSet(entries)
			eg.AllowedPorts = ports
			eg.AllowPrivateDestinations = in.LabMode
			cfg.Egress = eg
			out.Summary.EgressNodes++
			if len(entries) == 0 {
				out.Warnings = append(out.Warnings, fmt.Sprintf("egress node %q has an empty allowlist and will refuse every destination", n.Name))
			}
		default:
			return nil, fmt.Errorf("node %q has unknown role %q", n.Name, n.Role)
		}
		if in.LabMode {
			cfg.Notes = append(cfg.Notes, "LAB MODE: private and loopback destinations are reachable; never use this configuration in production")
		}
		b, err := cfg.Canonical()
		if err != nil {
			return nil, fmt.Errorf("render artifact for %s: %w", n.Name, err)
		}
		out.Artifacts[n.ID] = b
		out.Configs[n.ID] = cfg
	}

	man := model.Manifest{
		RevisionID: in.RevisionID, Sequence: in.Sequence, CreatedAt: now,
		CompilerVersion: model.CompilerVersion,
		MinAgentVersion: orDefault(in.MinAgentVer, "1.0.0"),
	}
	ids := make([]string, 0, len(out.Artifacts))
	for id := range out.Artifacts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := ""
	for _, id := range ids {
		b := out.Artifacts[id]
		sum := model.SHA256Hex(b)
		man.Artifacts = append(man.Artifacts, model.ArtifactRef{
			NodeID: id, Kind: model.ArtifactKind, Path: "config.json", SHA256: sum, Size: int64(len(b)),
		})
		h += id + ":" + sum + "\n"
	}
	man.ModelSHA256 = model.SHA256Hex([]byte(h))
	if len(in.SigningKey) == ed25519.PrivateKeySize {
		man.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(in.SigningKey, man.SigningPayload()))
	}
	out.Manifest = man
	return out, nil
}

// VerifyManifest checks the panel signature on the agent side.
func VerifyManifest(m model.Manifest, pub ed25519.PublicKey) error { return m.Verify(pub) }

func servicesForIngress(all []model.Service, nodeID string, inputs []ServiceInput) []model.Service {
	member := map[string]bool{}
	for _, s := range inputs {
		for _, id := range s.IngressNodes {
			if id == nodeID {
				member[s.Slug] = true
			}
		}
	}
	out := make([]model.Service, 0, len(all))
	for _, s := range all {
		if member[s.Slug] {
			out = append(out, s)
		}
	}
	return out
}

func detectConflicts(services []ServiceInput) error {
	type owner struct {
		slug     string
		priority int
	}
	claims := map[domainset.Entry][]owner{}
	for _, s := range services {
		for _, e := range s.Entries {
			claims[e] = append(claims[e], owner{s.Slug, s.Priority})
		}
	}
	var conflicts []Conflict
	for e, os := range claims {
		if len(os) < 2 {
			continue
		}
		top := os[0].priority
		var tied []string
		for _, o := range os {
			if o.priority > top {
				top = o.priority
			}
		}
		for _, o := range os {
			if o.priority == top {
				tied = append(tied, o.slug)
			}
		}
		if len(tied) > 1 {
			sort.Strings(tied)
			conflicts = append(conflicts, Conflict{Value: e.Value, Kind: string(e.Kind), Services: tied, Priority: top})
		}
	}
	if len(conflicts) > 0 {
		sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Value < conflicts[j].Value })
		return &ConflictError{Conflicts: conflicts}
	}
	return nil
}

func clampTTL(t uint32) uint32 {
	if t < 30 {
		return 30
	}
	if t > 300 {
		return 300
	}
	return t
}

func portsOrDefault(p []int) []int {
	if len(p) == 0 {
		return []int{443}
	}
	out := append([]int(nil), p...)
	sort.Ints(out)
	return out
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
