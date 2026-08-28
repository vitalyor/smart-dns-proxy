package api

import (
	"testing"

	"smartdns/panel/internal/rules"
)

func TestCleanLines(t *testing.T) {
	got := cleanLines([]string{"a.com\n# comment\n b.com \n", "a.com", "", "c.com"})
	want := []string{"a.com", "b.com", "c.com"} // deduped, trimmed, comments dropped
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestCountDomains(t *testing.T) {
	if n := countDomains("# c\ndomain:a\n\ndomain:b\n"); n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}
}

// Every catalog entry must point at a preset that actually ships, or the
// wizard's "create from catalog" would 400 at runtime.
func TestCatalogPresetsExist(t *testing.T) {
	presets := rules.Presets()
	for _, c := range catalog {
		if _, ok := presets[c.Preset]; !ok {
			t.Errorf("catalog %q references missing preset %q", c.Slug, c.Preset)
		}
	}
}
