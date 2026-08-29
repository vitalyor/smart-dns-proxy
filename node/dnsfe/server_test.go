package dnsfe

import (
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// rotate is called from every concurrent query; run under `go test -race` to
// guard the shared counter against regressing to an unsynchronized bump.
func TestRotateConcurrent(t *testing.T) {
	mk := func() []dns.RR {
		return []dns.RR{
			&dns.A{Hdr: dns.RR_Header{Name: "a."}},
			&dns.A{Hdr: dns.RR_Header{Name: "b."}},
			&dns.A{Hdr: dns.RR_Header{Name: "c."}},
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); rotate(mk()) }()
	}
	wg.Wait()
}

// A phone keeps one DoT connection open all day; the miekg/dns defaults would
// close it after 128 queries (then iOS fails open to plain DNS and the unblock
// breaks). Guard that DoT/TCP lift the cap and keep a long idle window.
func TestTCPConnPolicy(t *testing.T) {
	s := &Server{}
	for name, srv := range map[string]*dns.Server{"tcp": s.ListenTCP(":0"), "dot": s.ListenTLS(":0", nil)} {
		if srv.MaxTCPQueries != -1 {
			t.Errorf("%s: MaxTCPQueries=%d, want -1 (unlimited)", name, srv.MaxTCPQueries)
		}
		if srv.IdleTimeout == nil || srv.IdleTimeout() < 60*time.Second {
			t.Errorf("%s: idle timeout too short for a phone", name)
		}
	}
}
