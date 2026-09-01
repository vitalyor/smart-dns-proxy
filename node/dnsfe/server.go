package dnsfe

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"

	"smartdns/shared/metrics"
	"smartdns/shared/model"
)

var (
	mQueries  = metrics.Counter("smartdns_dns_queries_total", "DNS queries by transport, qtype and outcome")
	mRcode    = metrics.Counter("smartdns_dns_responses_total", "DNS responses by rcode")
	mLatency  = metrics.Histogram("smartdns_dns_duration_seconds", "DNS handling latency", metrics.DefBuckets)
	mRejected = metrics.Counter("smartdns_dns_rejected_total", "Rejected DNS queries by reason")
	mUpstream = metrics.Counter("smartdns_dns_upstream_total", "Upstream (Unbound) exchanges by result")
	mSynth    = metrics.Counter("smartdns_dns_synthesized_total", "Synthesized answers by service and qtype")
	mInflight = metrics.Gauge("smartdns_dns_inflight", "In-flight DNS queries")
)

// Server serves DNS over UDP, TCP, TLS and HTTPS.
type Server struct {
	Router *Router
	// upstream is swapped by the reload goroutine while queries are in flight.
	upstream atomic.Pointer[string]

	limiter  *limiter
	client   *dns.Client
	clientTC *dns.Client
	inflight chan struct{}
	once     sync.Once
	// queryLog logs one line per query to stdout when SMARTDNS_QUERY_LOG=1 — a
	// noisy terminal diagnostic, off by default.
	queryLog bool
	// log is the always-on in-memory ring the panel reads for the live Logs view.
	log *ring
}

// New builds a server bound to a router.
func New(r *Router) *Server {
	c := r.Config()
	maxc := c.DNS.Access.MaxConcurrent
	if maxc <= 0 {
		maxc = 2048
	}
	s := &Server{
		Router:   r,
		limiter:  newLimiter(c.DNS.Access.RateLimitQPS, c.DNS.Access.RateLimitBurst),
		client:   &dns.Client{Net: "udp", Timeout: 4 * time.Second, UDPSize: 4096},
		clientTC: &dns.Client{Net: "tcp", Timeout: 5 * time.Second},
		inflight: make(chan struct{}, maxc),
		queryLog: os.Getenv("SMARTDNS_QUERY_LOG") == "1",
		log:      newRing(1000),
	}
	s.upstream.Store(&c.DNS.Upstream)
	if s.queryLog {
		slog.Info("query logging enabled (SMARTDNS_QUERY_LOG=1)")
	}
	return s
}

// Reload updates limits after a revision swap.
func (s *Server) Reload(c *model.NodeConfig) {
	s.upstream.Store(&c.DNS.Upstream)
	s.limiter.configure(c.DNS.Access.RateLimitQPS, c.DNS.Access.RateLimitBurst)
}

type dnsHandler struct {
	s     *Server
	proto string
}

func (h dnsHandler) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	ip := addrOf(w.RemoteAddr())
	resp := h.s.Handle(req, ip, h.proto, "")
	if resp == nil {
		_ = w.Close()
		return
	}
	if h.proto == "udp" {
		resp.Truncate(udpSize(req))
	}
	if err := w.WriteMsg(resp); err != nil {
		slog.Debug("write dns response", "err", err)
	}
}

// Handle produces the response for one query. Exported for tests.
func (s *Server) Handle(req *dns.Msg, client netip.Addr, proto, dohToken string) (resp *dns.Msg) {
	start := time.Now()
	mInflight.Add(1)
	decision, qname, qtype := "malformed", "", ""
	defer func() {
		mInflight.Add(-1)
		took := time.Since(start)
		mLatency.Observe(took.Seconds(), "proto", proto)
		rcode := "nil"
		if resp != nil {
			rcode = dns.RcodeToString[resp.Rcode]
		}
		s.log.add(LogEntry{
			TS: start.UnixMilli(), Proto: proto, Name: qname, Type: qtype,
			Decision: decision, Rcode: rcode, MS: took.Milliseconds(),
		})
		if s.queryLog {
			slog.Info("dnsq", "proto", proto, "client", client.String(),
				"name", qname, "type", qtype, "decision", decision,
				"rcode", rcode, "ms", took.Milliseconds())
		}
	}()

	if len(req.Question) != 1 || req.Opcode != dns.OpcodeQuery {
		mRejected.Inc("reason", "malformed")
		return refuse(req, dns.RcodeFormatError)
	}
	q := req.Question[0]
	qname = NormalizeQName(q.Name)
	qtype = dns.TypeToString[q.Qtype]

	if !s.Router.AllowClient(client, dohToken) {
		mRejected.Inc("reason", "acl")
		decision = "denied:acl"
		return refuse(req, dns.RcodeRefused)
	}
	if !s.limiter.allow(client) {
		mRejected.Inc("reason", "rate_limit")
		decision = "denied:ratelimit"
		return refuse(req, dns.RcodeRefused)
	}

	svc := s.Router.Lookup(qname)
	if svc == nil {
		mQueries.Inc("proto", proto, "qtype", qtype, "kind", "recursive")
		decision = "direct"
		return s.forward(req, proto)
	}
	mQueries.Inc("proto", proto, "qtype", qtype, "kind", "managed")
	decision = "managed:" + svc.Slug
	return s.synthesize(req, svc, q, proto)
}

func (s *Server) synthesize(req *dns.Msg, svc *model.Service, q dns.Question, proto string) *dns.Msg {
	cfg := s.Router.Config()
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true
	m.RecursionAvailable = true
	// A synthesized answer cannot carry the origin's DNSSEC signature.
	// Never claim authenticated data.
	m.AuthenticatedData = false
	copyEDNS(req, m)

	ttl := svc.TTL
	if ttl < cfg.DNS.MinTTL {
		ttl = cfg.DNS.MinTTL
	}
	if cfg.DNS.MaxTTL > 0 && ttl > cfg.DNS.MaxTTL {
		ttl = cfg.DNS.MaxTTL
	}
	if ttl == 0 {
		ttl = 60
	}

	switch q.Qtype {
	case dns.TypeA:
		for _, a := range svc.IngressV4 {
			if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
				m.Answer = append(m.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl},
					A:   ip.To4(),
				})
			}
		}
		rotate(m.Answer)
		mSynth.Inc("service", svc.Slug, "qtype", "A")
	case dns.TypeAAAA:
		// Happy Eyeballs prefers IPv6: publish AAAA only when the whole IPv6
		// data path has passed e2e health, otherwise answer NODATA.
		if cfg.DNS.PublishAAAA {
			for _, a := range svc.IngressV6 {
				if ip := net.ParseIP(a); ip != nil && ip.To4() == nil {
					m.Answer = append(m.Answer, &dns.AAAA{
						Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: ttl},
						AAAA: ip,
					})
				}
			}
			rotate(m.Answer)
		}
		mSynth.Inc("service", svc.Slug, "qtype", "AAAA")
	case dns.TypeHTTPS, dns.TypeSVCB:
		// Origin hints (ipv4hint/ech) would let the client bypass the ingress.
		mSynth.Inc("service", svc.Slug, "qtype", dns.TypeToString[q.Qtype])
		if !cfg.DNS.BlockHTTPSRR {
			return s.forward(req, proto)
		}
	case dns.TypeCNAME, dns.TypeNS, dns.TypeSOA:
		// Deliberately NODATA: a CNAME would send the client off-ingress.
	default:
		// Non-address metadata (MX/TXT/…) is safe to resolve normally.
		return s.forward(req, proto)
	}

	if len(m.Answer) == 0 {
		m.Ns = append(m.Ns, soaFor(q.Name, ttl))
	}
	mRcode.Inc("rcode", dns.RcodeToString[m.Rcode], "kind", "synthesized")
	return m
}

func (s *Server) forward(req *dns.Msg, proto string) *dns.Msg {
	up := *s.upstream.Load()
	if up == "" {
		up = "127.0.0.1:5335"
	}
	select {
	case s.inflight <- struct{}{}:
		defer func() { <-s.inflight }()
	default:
		mRejected.Inc("reason", "concurrency")
		return refuse(req, dns.RcodeServerFailure)
	}

	out := req.Copy()
	out.Id = dns.Id()
	resp, _, err := s.client.Exchange(out, up)
	if err == nil && resp != nil && resp.Truncated && proto != "udp" {
		resp, _, err = s.clientTC.Exchange(out, up)
	}
	if err != nil || resp == nil {
		mUpstream.Inc("result", "error")
		// Never invent an answer when the resolver is down.
		return refuse(req, dns.RcodeServerFailure)
	}
	mUpstream.Inc("result", "ok")
	resp.Id = req.Id
	resp.Question = req.Question
	mRcode.Inc("rcode", dns.RcodeToString[resp.Rcode], "kind", "recursive")
	return resp
}

func refuse(req *dns.Msg, rcode int) *dns.Msg {
	m := new(dns.Msg)
	m.SetRcode(req, rcode)
	m.RecursionAvailable = true
	copyEDNS(req, m)
	mRcode.Inc("rcode", dns.RcodeToString[rcode], "kind", "rejected")
	return m
}

func copyEDNS(req, resp *dns.Msg) {
	if o := req.IsEdns0(); o != nil {
		size := o.UDPSize()
		if size < 512 {
			size = 512
		}
		if size > 4096 {
			size = 4096
		}
		resp.SetEdns0(size, false)
	}
}

func udpSize(req *dns.Msg) int {
	if o := req.IsEdns0(); o != nil {
		if n := int(o.UDPSize()); n >= 512 {
			if n > 4096 {
				return 4096
			}
			return n
		}
	}
	return dns.MinMsgSize
}

func soaFor(name string, ttl uint32) dns.RR {
	labels := dns.SplitDomainName(name)
	zone := name
	if len(labels) > 2 {
		zone = strings.Join(labels[len(labels)-2:], ".") + "."
	}
	return &dns.SOA{
		Hdr:     dns.RR_Header{Name: zone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: ttl},
		Ns:      "ingress." + zone,
		Mbox:    "hostmaster." + zone,
		Serial:  uint32(time.Now().Unix() / 60),
		Refresh: 3600, Retry: 600, Expire: 86400, Minttl: ttl,
	}
}

var rotSeed uint32

// rotate cycles the answer order so clients spread across ingress members.
// Called concurrently from every query, so the counter is bumped atomically.
func rotate(rr []dns.RR) {
	if len(rr) < 2 {
		return
	}
	n := int(atomic.AddUint32(&rotSeed, 1)) % len(rr)
	if n == 0 {
		return
	}
	out := append(append([]dns.RR{}, rr[n:]...), rr[:n]...)
	copy(rr, out)
}

func addrOf(a net.Addr) netip.Addr {
	switch v := a.(type) {
	case *net.UDPAddr:
		ip, _ := netip.AddrFromSlice(v.IP)
		return ip.Unmap()
	case *net.TCPAddr:
		ip, _ := netip.AddrFromSlice(v.IP)
		return ip.Unmap()
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return netip.Addr{}
	}
	ip, _ := netip.ParseAddr(host)
	return ip.Unmap()
}

// ListenUDP/ListenTCP/ListenTLS start the classic transports.
func (s *Server) ListenUDP(addr string) *dns.Server {
	return &dns.Server{Addr: addr, Net: "udp", Handler: dnsHandler{s, "udp"}, UDPSize: 4096}
}

// tcpConnPolicy keeps a phone's persistent DoT/TCP connection alive. miekg/dns
// otherwise defaults to MaxTCPQueries=128 (closes the connection after 128
// queries) and an 8s idle timeout — a phone reaches 128 lookups in minutes, and
// on the close iOS fails open to the network's plain DNS, so managed domains
// resolve directly and stop being routed through the node. -1 removes the query
// cap; the long idle keeps a briefly-quiet connection open.
func tcpConnPolicy(s *dns.Server) *dns.Server {
	s.MaxTCPQueries = -1
	s.IdleTimeout = func() time.Duration { return 120 * time.Second }
	return s
}

func (s *Server) ListenTCP(addr string) *dns.Server {
	return tcpConnPolicy(&dns.Server{Addr: addr, Net: "tcp", Handler: dnsHandler{s, "tcp"}})
}

func (s *Server) ListenTLS(addr string, tc *tls.Config) *dns.Server {
	return tcpConnPolicy(&dns.Server{Addr: addr, Net: "tcp-tls", TLSConfig: tc, Handler: dnsHandler{s, "dot"}})
}

// DoHHandler implements RFC 8484 GET and POST.
func (s *Server) DoHHandler(basePath string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.Trim(strings.TrimPrefix(r.URL.Path, basePath), "/")
		if token != "" {
			sum := sha256.Sum256([]byte(token))
			token = hex.EncodeToString(sum[:])
		}
		var body []byte
		var err error
		switch r.Method {
		case http.MethodGet:
			q := r.URL.Query().Get("dns")
			if q == "" {
				http.Error(w, "missing dns parameter", http.StatusBadRequest)
				return
			}
			body, err = base64.RawURLEncoding.DecodeString(q)
		case http.MethodPost:
			if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/dns-message") {
				http.Error(w, "unsupported content type", http.StatusUnsupportedMediaType)
				return
			}
			body, err = io.ReadAll(io.LimitReader(r.Body, 8192))
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err != nil || len(body) == 0 {
			http.Error(w, "bad dns message", http.StatusBadRequest)
			return
		}
		req := new(dns.Msg)
		if err := req.Unpack(body); err != nil {
			http.Error(w, "bad dns message", http.StatusBadRequest)
			return
		}
		resp := s.Handle(req, clientIPOf(r), "doh", token)
		out, err := resp.Pack()
		if err != nil {
			http.Error(w, "pack failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Header().Set("Cache-Control", "max-age=0")
		_, _ = w.Write(out)
	})
}

func clientIPOf(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, _ := netip.ParseAddr(host)
	return ip.Unmap()
}

// Shutdown gracefully stops a dns.Server.
func Shutdown(ctx context.Context, srv *dns.Server) { _ = srv.ShutdownContext(ctx) }
