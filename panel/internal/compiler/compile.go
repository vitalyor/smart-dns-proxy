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
	EgressMembers []EgressMember
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
	Country       string
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

	if err := detectCountryClash(in.Services, in.Nodes); err != nil {
		return nil, err
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

	// Адреса всех живых входных нод — они одинаковы для каждого сервиса.
	var ingressAddrs, ingressAddrs6 []string
	for _, n := range in.Nodes {
		if n.Role != "ingress" || !n.Eligible {
			continue
		}
		if n.PublicIPv4 != "" {
			ingressAddrs = append(ingressAddrs, n.PublicIPv4)
		}
		if n.PublicIPv6 != "" {
			ingressAddrs6 = append(ingressAddrs6, n.PublicIPv6)
		}
	}
	sort.Strings(ingressAddrs)
	sort.Strings(ingressAddrs6)
	if len(ingressAddrs) == 0 {
		return nil, fmt.Errorf("во флоте нет ни одной живой входной ноды с публичным IPv4: устройствам нечего отдавать в ответ DNS")
	}

	for _, s := range in.Services {
		if len(s.Entries) == 0 {
			out.Warnings = append(out.Warnings, fmt.Sprintf("service %q has an empty rule set and will match nothing", s.Slug))
		}
		// Входы не выбираются у сервиса: каждый сервис публикуется со всех
		// живых входных нод. Вход — это дверь, а не маршрут; страну решает
		// выход. Все адреса уходят в один ответ DNS и перемешиваются, поэтому
		// устройство само переходит на следующий вход, если один лёг.
		v4, v6 := ingressAddrs, ingressAddrs6

		policy := s.Policy
		// Режим ровно один: первая живая нода по порядку. Раздача по весам
		// перетасовывала ноды на каждое соединение и разносила один аккаунт по
		// нескольким странам — ради этого режимы и убраны.
		policy.Mode = "primary_fallback"
		policy.Targets = nil
		for _, m := range s.EgressMembers {
			n, ok := nodes[m.NodeID]
			if !ok || !n.Eligible {
				continue
			}
			// Входная нода в списке — это «выходить прямо отсюда». Туннеля нет,
			// значит нет ни адреса реле, ни записи в разрешённых у выхода:
			// сервис уже опознан по имени на самом входе.
			if n.Role == "ingress" {
				policy.Local = true
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
		if len(policy.Targets) == 0 && !policy.Local {
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
			// Каждая входная нода обслуживает все сервисы: она дверь, а не маршрут.
			cfg.Services = compiled
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
			// Про ноду без сервисов не предупреждаем: это не ошибка сборки, а
			// обычное состояние только что заведённой ноды, и три таких строки
			// в каждой ревизии приучают не читать предупреждения вовсе.
			// Какие ноды не задействованы, видно в списке нод.
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

// detectCountryClash не даёт собрать ревизию, если два сервиса делят домен, а
// выходят из разных стран.
//
// Домен принадлежит ровно одному сервису — по длине совпадения, — поэтому общий
// хост вроде challenges.cloudflare.com уйдёт в страну одного сервиса и для
// второго окажется чужим. Для проверки «вы не робот» или отпечатка устройства
// это значит, что сайт видит страницу из одной страны, а подтверждение из
// другой. Ломается это молча и через раз, поэтому ловим на сборке.
//
// Регулярные и отрицательные правила пропускаем: пересечение регулярного
// выражения с суффиксом достоверно не вычислить, а гадать здесь нельзя.
func detectCountryClash(services []ServiceInput, nodes []NodeInput) error {
	country := map[string]string{}
	for _, n := range nodes {
		// И входы тоже: сервис может выходить прямо со входа, и тогда страна
		// сервиса — страна входа. Без этого конфликт доменов между таким
		// сервисом и заграничным остался бы незамеченным.
		country[n.ID] = n.Country
	}
	// Страна сервиса. Ноды выхода одного сервиса уже обязаны быть из одной
	// страны, так что берём первую.
	svcCountry := map[string]string{}
	for _, s := range services {
		for _, m := range s.EgressMembers {
			if c, ok := country[m.NodeID]; ok {
				svcCountry[s.Slug] = c
				break
			}
		}
	}

	// Кто владеет каждым точным значением, и отдельно — какие суффиксы заявлены.
	owner := map[string]string{}
	suffixes := map[string]string{}
	for _, s := range services {
		for _, e := range s.Entries {
			if e.Kind != domainset.KindExact && e.Kind != domainset.KindSuffix {
				continue
			}
			if _, seen := owner[e.Value]; !seen {
				owner[e.Value] = s.Slug
			}
			if e.Kind == domainset.KindSuffix {
				if _, seen := suffixes[e.Value]; !seen {
					suffixes[e.Value] = s.Slug
				}
			}
		}
	}

	type clash struct{ domain, a, b, ca, cb string }
	var found []clash
	seen := map[string]bool{}
	note := func(domain, a, b string) {
		ca, cb := svcCountry[a], svcCountry[b]
		if a == b || ca == cb {
			return
		}
		key := domain + "|" + a + "|" + b
		if seen[key] {
			return
		}
		seen[key] = true
		found = append(found, clash{domain, a, b, orUnknown(ca), orUnknown(cb)})
	}

	for _, s := range services {
		for _, e := range s.Entries {
			if e.Kind != domainset.KindExact && e.Kind != domainset.KindSuffix {
				continue
			}
			// Тот же самый хост заявлен другим сервисом.
			if o, ok := owner[e.Value]; ok {
				note(e.Value, s.Slug, o)
			}
			// Хост попадает под суффикс другого сервиса: grok.x.com внутри x.com.
			for rest := e.Value; ; {
				i := strings.IndexByte(rest, '.')
				if i < 0 {
					break
				}
				rest = rest[i+1:]
				if o, ok := suffixes[rest]; ok {
					note(e.Value+" (внутри "+rest+")", s.Slug, o)
				}
			}
		}
	}
	if len(found) == 0 {
		return nil
	}
	sort.Slice(found, func(i, j int) bool { return found[i].domain < found[j].domain })
	var parts []string
	for i, c := range found {
		if i == 5 {
			parts = append(parts, fmt.Sprintf("… и ещё %d", len(found)-5))
			break
		}
		parts = append(parts, fmt.Sprintf("%s: %s (%s) и %s (%s)", c.domain, c.a, c.ca, c.b, c.cb))
	}
	return fmt.Errorf("общий домен у сервисов из разных стран — %s. "+
		"Общий хост уходит в страну одного сервиса, и для второго страница и подтверждение личности окажутся из разных стран. "+
		"Переведите эти сервисы в одну страну либо разведите домены", strings.Join(parts, "; "))
}

func orUnknown(c string) string {
	if c == "" {
		return "страна не указана"
	}
	return c
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
