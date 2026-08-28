package compiler

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"smartdns/shared/domainset"
	"smartdns/shared/model"
)

func baseInput() Input {
	return Input{
		RevisionID: "rev-1", Sequence: 1,
		Nodes: []NodeInput{
			{ID: "in1", Name: "ingress-ru", Role: "ingress", PublicIPv4: "203.0.113.5", Eligible: true},
			{ID: "eg1", Name: "egress-de", Role: "egress", PublicIPv4: "198.51.100.9", RelayEndpoint: "198.51.100.9:8443", RelaySNI: "egress-de", Eligible: true},
		},
		Services: []ServiceInput{{
			ID: "s1", Slug: "gemini", Name: "Gemini", Priority: 100, TTL: 60,
			Entries:       []domainset.Entry{{Kind: domainset.KindSuffix, Value: "gemini.google.com"}},
			IngressNodes:  []string{"in1"},
			EgressMembers: []EgressMember{{NodeID: "eg1", Priority: 1, Weight: 1}},
		}},
		DNS: model.DNSConfig{Upstream: "127.0.0.1:5335", MinTTL: 30, MaxTTL: 300},
	}
}

func TestCompileProducesOneArtifactPerNode(t *testing.T) {
	out, err := Compile(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Artifacts) != 2 {
		t.Fatalf("expected 2 artifacts, got %d", len(out.Artifacts))
	}
	in := out.Configs["in1"]
	if len(in.Services) != 1 || in.Services[0].IngressV4[0] != "203.0.113.5" {
		t.Fatalf("ingress config wrong: %+v", in.Services)
	}
	if in.Services[0].Egress.Targets[0].Endpoint != "198.51.100.9:8443" {
		t.Fatal("egress target missing")
	}
	eg := out.Configs["eg1"]
	if len(eg.Egress.Allow.Suffix) != 1 || eg.Egress.Allow.Suffix[0] != "gemini.google.com" {
		t.Fatalf("egress allowlist must mirror the same rule set, got %+v", eg.Egress.Allow)
	}
	if len(eg.Egress.AllowedPorts) != 1 || eg.Egress.AllowedPorts[0] != 443 {
		t.Fatalf("ports wrong: %v", eg.Egress.AllowedPorts)
	}
}

func TestCompileIsDeterministic(t *testing.T) {
	a, err := Compile(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Compile(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if a.Manifest.ModelSHA256 != b.Manifest.ModelSHA256 {
		t.Fatal("compilation must be deterministic")
	}
}

func TestEqualPriorityConflictFailsCompilation(t *testing.T) {
	in := baseInput()
	in.Services = append(in.Services, ServiceInput{
		ID: "s2", Slug: "other", Name: "Other", Priority: 100, TTL: 60,
		Entries:       []domainset.Entry{{Kind: domainset.KindSuffix, Value: "gemini.google.com"}},
		IngressNodes:  []string{"in1"},
		EgressMembers: []EgressMember{{NodeID: "eg1", Priority: 1}},
	})
	_, err := Compile(in)
	if err == nil {
		t.Fatal("ambiguous ownership must fail compilation, not pick a random winner")
	}
	var ce *ConflictError
	if !strings.Contains(err.Error(), "gemini.google.com") {
		t.Fatalf("error should name the conflict: %v", err)
	}
	if ok := asConflict(err, &ce); !ok || len(ce.Conflicts) != 1 {
		t.Fatalf("expected a structured conflict, got %v", err)
	}
}

func asConflict(err error, target **ConflictError) bool {
	c, ok := err.(*ConflictError)
	if ok {
		*target = c
	}
	return ok
}

func TestDifferentPriorityResolvesOverlap(t *testing.T) {
	in := baseInput()
	in.Services = append(in.Services, ServiceInput{
		ID: "s2", Slug: "other", Name: "Other", Priority: 200, TTL: 60,
		Entries:       []domainset.Entry{{Kind: domainset.KindSuffix, Value: "gemini.google.com"}},
		IngressNodes:  []string{"in1"},
		EgressMembers: []EgressMember{{NodeID: "eg1", Priority: 1}},
	})
	if _, err := Compile(in); err != nil {
		t.Fatalf("explicit priority should resolve the overlap: %v", err)
	}
}

func TestServiceWithoutEgressFails(t *testing.T) {
	in := baseInput()
	in.Services[0].EgressMembers = nil
	if _, err := Compile(in); err == nil {
		t.Fatal("a service with no reachable egress must not compile")
	}
}

func TestAAAADisabledWhenIngressHasNoIPv6(t *testing.T) {
	in := baseInput()
	in.DNS.PublishAAAA = true
	out, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Configs["in1"].DNS.PublishAAAA {
		t.Fatal("AAAA must not be published from an ingress without IPv6")
	}
}

func TestManifestSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	in := baseInput()
	in.SigningKey = priv
	out, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifest(out.Manifest, pub); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	tampered := out.Manifest
	tampered.Sequence = 999
	if err := VerifyManifest(tampered, pub); err == nil {
		t.Fatal("tampered manifest must fail verification")
	}
}

func TestTTLIsClamped(t *testing.T) {
	in := baseInput()
	in.Services[0].TTL = 5
	out, _ := Compile(in)
	if out.Configs["in1"].Services[0].TTL != 30 {
		t.Fatal("TTL below the safe floor must be clamped")
	}
}
