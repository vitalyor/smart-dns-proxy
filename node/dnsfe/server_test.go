package dnsfe

import (
	"sync"
	"testing"

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
