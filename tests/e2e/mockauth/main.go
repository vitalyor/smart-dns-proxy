// Command mockauth is a tiny authoritative DNS server for the `.test` zone used
// by the container lab. It lets Unbound resolve ordinary names through a normal
// delegation path instead of static overrides.
package main

import (
	"log"
	"net"
	"os"
	"strings"

	"github.com/miekg/dns"
)

func main() {
	addr := env("AUTH_ADDR", ":53")
	records := map[string]string{}
	// RECORDS="origin.test=172.28.0.50,ordinary.test=203.0.113.10"
	for _, pair := range strings.Split(os.Getenv("RECORDS"), ",") {
		if kv := strings.SplitN(strings.TrimSpace(pair), "=", 2); len(kv) == 2 {
			records[strings.ToLower(kv[0])+"."] = kv[1]
		}
	}
	h := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Authoritative = true
		if len(r.Question) == 1 {
			q := r.Question[0]
			ip, ok := records[strings.ToLower(q.Name)]
			switch {
			case !ok:
				m.Rcode = dns.RcodeNameError
			case q.Qtype == dns.TypeA && net.ParseIP(ip).To4() != nil:
				m.Answer = append(m.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A:   net.ParseIP(ip).To4(),
				})
			}
			if len(m.Answer) == 0 && m.Rcode == dns.RcodeSuccess {
				m.Ns = append(m.Ns, soa())
			}
		}
		_ = w.WriteMsg(m)
	})
	go func() { log.Fatal((&dns.Server{Addr: addr, Net: "tcp", Handler: h}).ListenAndServe()) }()
	log.Printf("mock authoritative DNS listening on %s with %d records", addr, len(records))
	log.Fatal((&dns.Server{Addr: addr, Net: "udp", Handler: h}).ListenAndServe())
}

func soa() dns.RR {
	return &dns.SOA{
		Hdr:    dns.RR_Header{Name: "test.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 60},
		Ns:     "ns.test.",
		Mbox:   "hostmaster.test.",
		Serial: 1, Refresh: 3600, Retry: 600, Expire: 86400, Minttl: 60,
	}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
