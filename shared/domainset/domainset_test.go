package domainset

import "testing"

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"Example.COM.":    "example.com",
		" api.Test.Org ":  "api.test.org",
		"пример.рф":       "xn--e1afmkfd.xn--p1ai",
		"XN--E1AFMKFD.РФ": "xn--e1afmkfd.xn--p1ai",
	}
	for in, want := range cases {
		got, err := NormalizeHost(in)
		if err != nil || got != want {
			t.Fatalf("NormalizeHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", ".", "localhost", "a..b.com", "-bad.com", "a b.com"} {
		if _, err := NormalizeHost(bad); err == nil {
			t.Fatalf("NormalizeHost(%q) should fail", bad)
		}
	}
}

func TestSuffixLabelBoundary(t *testing.T) {
	m := Set{Suffix: []string{"example.com"}}.Compile()
	for _, h := range []string{"example.com", "api.example.com", "a.b.example.com"} {
		if !m.Match(h) {
			t.Fatalf("%s should match", h)
		}
	}
	for _, h := range []string{"notexample.com", "example.com.evil.net", "com"} {
		if m.Match(h) {
			t.Fatalf("%s must not match", h)
		}
	}
}

func TestParseLines(t *testing.T) {
	body := "\ufeff# comment\n\ndomain:gemini.google.com\nfull:api.openai.com\n*.claude.ai\n0.0.0.0 ads.example.com\ndomain-keyword:bad\n!adblock\n"
	res, err := ParseLines(body, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 4 {
		t.Fatalf("got %d entries: %v", len(res.Entries), res.Entries)
	}
	if res.Skipped != 1 {
		t.Fatalf("expected 1 skipped, got %d", res.Skipped)
	}
	if Hash(res.Entries) != Hash(res.Entries) {
		t.Fatal("hash not stable")
	}
}

func TestBareDomainIsSuffix(t *testing.T) {
	res, _ := ParseLines("openai.com\nfull:api.openai.com", ParseOptions{})
	if len(res.Entries) != 2 {
		t.Fatalf("got %d entries: %v", len(res.Entries), res.Entries)
	}
	kind := map[string]Kind{}
	for _, e := range res.Entries {
		kind[e.Value] = e.Kind
	}
	if kind["openai.com"] != KindSuffix {
		t.Fatalf("bare domain must be suffix, got %v", kind["openai.com"])
	}
	if kind["api.openai.com"] != KindExact {
		t.Fatalf("full: must be exact, got %v", kind["api.openai.com"])
	}
}

func TestParseDeterministic(t *testing.T) {
	a, _ := ParseLines("b.com\na.com\nc.com", ParseOptions{})
	b, _ := ParseLines("c.com\na.com\nb.com", ParseOptions{})
	if Hash(a.Entries) != Hash(b.Entries) {
		t.Fatal("order must not affect hash")
	}
}

func TestMergeExcludes(t *testing.T) {
	inc := []Entry{{KindExact, "a.example.com"}, {KindSuffix, "example.com"}, {KindExact, "keep.org"}}
	exc := []Entry{{KindSuffix, "example.com"}}
	out := Merge(inc, exc)
	if len(out) != 1 || out[0].Value != "keep.org" {
		t.Fatalf("merge got %v", out)
	}
}

func TestRegexDisabledByDefault(t *testing.T) {
	res, _ := ParseLines("regexp:^a.*\\.com$", ParseOptions{})
	if len(res.Entries) != 0 {
		t.Fatal("regex must be off by default")
	}
	res, err := ParseLines("regexp:^a.*\\.com$", ParseOptions{AllowRegex: true})
	if err != nil || len(res.Entries) != 1 {
		t.Fatalf("regex enabled: %v %v", res.Entries, err)
	}
}

func TestDiff(t *testing.T) {
	old := []Entry{{KindExact, "a.com"}, {KindExact, "b.com"}}
	new := []Entry{{KindExact, "b.com"}, {KindExact, "c.com"}}
	add, rem := Diff(old, new)
	if len(add) != 1 || add[0].Value != "c.com" || len(rem) != 1 || rem[0].Value != "a.com" {
		t.Fatalf("diff wrong: %v %v", add, rem)
	}
}
