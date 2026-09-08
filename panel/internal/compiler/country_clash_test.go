package compiler

import (
	"strings"
	"testing"

	"smartdns/shared/domainset"
)

// Общий домен у сервисов из разных стран должен останавливать сборку, а из
// одной — проходить. Проверяем оба пересечения: точное совпадение и вложенность
// вида grok.x.com внутри x.com.
func TestDetectCountryClash(t *testing.T) {
	nodes := []NodeInput{
		{ID: "de", Role: "egress", Country: "DE"},
		{ID: "us", Role: "egress", Country: "US"},
	}
	svc := func(slug, node string, entries ...domainset.Entry) ServiceInput {
		return ServiceInput{Slug: slug, EgressMembers: []EgressMember{{NodeID: node}}, Entries: entries}
	}
	ex := func(v string) domainset.Entry { return domainset.Entry{Kind: domainset.KindExact, Value: v} }
	su := func(v string) domainset.Entry { return domainset.Entry{Kind: domainset.KindSuffix, Value: v} }

	cases := []struct {
		name     string
		services []ServiceInput
		wantErr  string
	}{
		{
			name: "один и тот же хост в разных странах",
			services: []ServiceInput{
				svc("claude", "us", ex("challenges.cloudflare.com")),
				svc("gemini", "de", ex("challenges.cloudflare.com")),
			},
			wantErr: "challenges.cloudflare.com",
		},
		{
			name: "вложенность в разных странах",
			services: []ServiceInput{
				svc("grok", "us", su("x.com")),
				svc("prochie", "de", ex("grok.x.com")),
			},
			wantErr: "grok.x.com",
		},
		{
			name: "тот же хост, но одна страна — не ошибка",
			services: []ServiceInput{
				svc("claude", "us", ex("challenges.cloudflare.com")),
				svc("gemini", "us", ex("challenges.cloudflare.com")),
			},
		},
		{
			name: "разные страны без пересечений — не ошибка",
			services: []ServiceInput{
				svc("claude", "us", ex("claude.ai")),
				svc("gemini", "de", ex("gemini.google.com")),
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := detectCountryClash(c.services, nodes)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("ожидалась удачная сборка, получено: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ожидалась ошибка про %s, сборка прошла", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("в ошибке нет %q: %v", c.wantErr, err)
			}
		})
	}
}
