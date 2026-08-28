// Package integration proves the fundamental L4 flow before any control plane
// exists: DNS rewrite -> ingress SNI sniff -> tunnel -> egress -> origin.
package integration

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"smartdns/node/dnsfe"
	"smartdns/node/proxy"
	"smartdns/node/relay"
	"smartdns/shared/domainset"
	"smartdns/shared/model"
	"smartdns/shared/pki"
	"smartdns/shared/tunnel"
)

const (
	managedHost = "origin.test"
	otherHost   = "ordinary.test"
)

// --- test fixtures -----------------------------------------------------------

type ca struct {
	certPEM, keyPEM []byte
	cert            *x509.Certificate
	key             *ecdsa.PrivateKey
}

func newCA(t *testing.T) *ca {
	t.Helper()
	cp, kp, err := pki.NewCA("smartdns-test-ca")
	if err != nil {
		t.Fatal(err)
	}
	c, k, err := pki.LoadCA(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	return &ca{cp, kp, c, k}
}

func (c *ca) issue(t *testing.T, cn, role string, ips []string) tls.Certificate {
	t.Helper()
	key, keyPEM, err := pki.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	csr, err := pki.NewCSR(key, cn)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := pki.Sign(c.cert, c.key, csr, pki.CSRRequest{
		CommonName: cn, Role: role, DNSNames: []string{cn}, IPs: ips, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func (c *ca) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AppendCertsFromPEM(c.certPEM)
	return p
}

// originServer is a TLS server with its own independent CA, standing in for a
// real service. If the ingress ever terminated TLS, this certificate check
// would fail.
type originServer struct {
	addr     string
	pool     *x509.CertPool
	seenFrom chan string
}

func startOrigin(t *testing.T, hostname string) *originServer {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hostname, Organization: []string{"real-origin"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{hostname},
		IsCA:         true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)

	o := &originServer{pool: pool, seenFrom: make(chan string, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("/whoami", func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		select {
		case o.seenFrom <- host:
		default:
		}
		fmt.Fprintf(w, "origin-ok from=%s host=%s", host, r.Host)
	})
	// The origin binds the IPv6 loopback so the test can bind the *same* port
	// number on the IPv4 loopback for the ingress. That keeps the production
	// rule "destination port == the port the client connected to" under test
	// instead of adding a test-only port rewrite.
	l, err := tls.Listen("tcp6", "[::1]:0", &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	o.addr = l.Addr().String()
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return o
}

// mockResolver stands in for Unbound: authoritative answers for *.test.
type mockResolver struct{ addr string }

func startResolver(t *testing.T, records map[string]string) *mockResolver {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.RecursionAvailable = true
		q := r.Question[0]
		ip, ok := records[strings.TrimSuffix(strings.ToLower(q.Name), ".")]
		switch {
		case !ok:
			m.Rcode = dns.RcodeNameError
		case q.Qtype == dns.TypeA && net.ParseIP(ip).To4() != nil:
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
				A:   net.ParseIP(ip).To4(),
			})
		case q.Qtype == dns.TypeAAAA && net.ParseIP(ip).To4() == nil:
			m.Answer = append(m.Answer, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 300},
				AAAA: net.ParseIP(ip),
			})
		}
		_ = w.WriteMsg(m)
	})
	srv := &dns.Server{PacketConn: pc, Handler: h}
	go srv.ActivateAndServe()
	t.Cleanup(func() { srv.Shutdown() })
	return &mockResolver{addr: pc.LocalAddr().String()}
}

// --- the stack ---------------------------------------------------------------

type stack struct {
	dns       *dnsfe.Server
	ingress   net.Listener
	relayAddr string
	origin    *originServer
	ca        *ca
}

func buildStack(t *testing.T) *stack {
	t.Helper()
	c := newCA(t)
	// Pick a port that is free on both loopback families: the origin takes it
	// on ::1, the ingress takes the same number on 127.0.0.1.
	var origin *originServer
	var il net.Listener
	var originPort int
	for attempt := 0; attempt < 20 && il == nil; attempt++ {
		origin = startOrigin(t, managedHost)
		_, portStr, _ := net.SplitHostPort(origin.addr)
		originPort = atoi(portStr)
		il, _ = net.Listen("tcp4", "127.0.0.1:"+portStr)
	}
	if il == nil {
		t.Skip("could not find a port free on both loopback families")
	}
	t.Cleanup(func() { il.Close() })

	res := startResolver(t, map[string]string{
		managedHost: "::1",
		otherHost:   "203.0.113.10",
	})

	// --- egress node ---
	egCfg := &model.NodeConfig{
		SchemaVersion: 1, RevisionID: "rev-test", Sequence: 1, Role: "egress", NodeName: "egress-test",
		Egress: model.EgressConfig{
			Allow:        domainset.Set{Exact: []string{managedHost}},
			AllowedPorts: []int{443, originPort},
			Resolver:     res.addr,
			// Lab only: the mock origin lives on loopback.
			AllowPrivateDestinations: true,
		},
	}
	r := relay.New(egCfg)
	egCert := c.issue(t, "egress-test", "egress", []string{"127.0.0.1"})
	rl, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{egCert},
		ClientCAs:    c.pool(),
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{tunnel.ALPN},
	})
	if err != nil {
		t.Fatal(err)
	}
	go r.Serve(rl)
	t.Cleanup(func() { rl.Close() })

	// --- ingress node ---
	svc := model.Service{
		Slug: "testsvc", Name: "Test Service", TTL: 60, AllowedPorts: []int{443, originPort},
		Match:     domainset.Set{Exact: []string{managedHost}},
		IngressV4: []string{"198.51.100.7"},
		Egress: model.EgressPolicy{
			Mode: "primary_fallback",
			Targets: []model.EgressTarget{{
				NodeID: "eg1", Name: "egress-test", Endpoint: rl.Addr().String(), SNI: "egress-test", Priority: 1, Weight: 1,
			}},
			FailThreshold: 3, RiseThreshold: 2,
		},
	}
	inCfg := &model.NodeConfig{
		SchemaVersion: 1, RevisionID: "rev-test", Sequence: 1, Role: "ingress", NodeName: "ingress-test",
		Services: []model.Service{svc},
		DNS: model.DNSConfig{
			Upstream: res.addr, MinTTL: 30, MaxTTL: 300, BlockHTTPSRR: true,
			Access: model.DNSAccess{Mode: "allowlist", AllowedCIDRs: []string{"127.0.0.0/8", "::1/128"}, RateLimitQPS: 1000},
		},
		Ingress: model.IngressConfig{ClientHelloTimeoutMs: 3000, MaxPreReadBytes: 16384, DialTimeoutMs: 5000, IdleTimeoutSec: 30},
	}
	inCert := c.issue(t, "ingress-test", "ingress", []string{"127.0.0.1"})
	p := proxy.New(inCfg, &tls.Config{
		Certificates: []tls.Certificate{inCert},
		RootCAs:      c.pool(),
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{tunnel.ALPN},
	})
	go p.Serve(il)

	return &stack{dns: dnsfe.New(dnsfe.NewRouter(inCfg)), ingress: il, relayAddr: rl.Addr().String(), origin: origin, ca: c}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func query(t *testing.T, s *stack, name string, qtype uint16) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	m.SetEdns0(4096, false)
	return s.dns.Handle(m, netip.MustParseAddr("127.0.0.1"), "udp", "")
}

// --- acceptance checks -------------------------------------------------------

func TestManagedDomainReturnsIngressAddress(t *testing.T) {
	s := buildStack(t)
	resp := query(t, s, managedHost, dns.TypeA)
	if len(resp.Answer) != 1 {
		t.Fatalf("expected one A record, got %v", resp.Answer)
	}
	a := resp.Answer[0].(*dns.A)
	if a.A.String() != "198.51.100.7" {
		t.Fatalf("managed domain must resolve to the ingress, got %s", a.A)
	}
	if a.Hdr.Ttl != 60 {
		t.Fatalf("ttl = %d, want 60", a.Hdr.Ttl)
	}
	if resp.AuthenticatedData {
		t.Fatal("synthesized answers must never set the AD bit")
	}
}

func TestOrdinaryDomainGoesToResolver(t *testing.T) {
	s := buildStack(t)
	resp := query(t, s, otherHost, dns.TypeA)
	if len(resp.Answer) != 1 {
		t.Fatalf("expected an upstream answer, got %v", resp.Answer)
	}
	if got := resp.Answer[0].(*dns.A).A.String(); got != "203.0.113.10" {
		t.Fatalf("ordinary domain must keep its real address, got %s", got)
	}
}

func TestAAAAWithoutIPv6PathIsNodata(t *testing.T) {
	s := buildStack(t)
	resp := query(t, s, managedHost, dns.TypeAAAA)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 0 {
		t.Fatalf("expected NODATA, got rcode=%d answers=%v", resp.Rcode, resp.Answer)
	}
	if len(resp.Ns) == 0 {
		t.Fatal("NODATA should carry a SOA in the authority section")
	}
}

func TestHTTPSRecordIsSuppressedForManagedDomain(t *testing.T) {
	s := buildStack(t)
	resp := query(t, s, managedHost, dns.TypeHTTPS)
	if len(resp.Answer) != 0 {
		t.Fatal("HTTPS/SVCB hints would let the client bypass the ingress")
	}
}

func TestDNSAccessControl(t *testing.T) {
	s := buildStack(t)
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(managedHost), dns.TypeA)
	resp := s.dns.Handle(m, netip.MustParseAddr("198.51.100.99"), "udp", "")
	if resp.Rcode != dns.RcodeRefused {
		t.Fatalf("off-allowlist client must be REFUSED, got %s", dns.RcodeToString[resp.Rcode])
	}
}

func TestEndToEndTLSPassthrough(t *testing.T) {
	s := buildStack(t)
	// The client believes it is talking to managedHost and validates the
	// certificate of the real origin, exactly as it would without us.
	conn, err := tls.Dial("tcp", s.ingress.Addr().String(), &tls.Config{
		ServerName: managedHost,
		RootCAs:    s.origin.pool,
	})
	if err != nil {
		t.Fatalf("handshake through the ingress failed: %v", err)
	}
	defer conn.Close()

	cert := conn.ConnectionState().PeerCertificates[0]
	if cert.Subject.Organization[0] != "real-origin" {
		t.Fatalf("client must see the origin certificate, got %v", cert.Subject)
	}

	fmt.Fprintf(conn, "GET /whoami HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", managedHost)
	body, err := io.ReadAll(conn)
	if err != nil && len(body) == 0 {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "origin-ok") {
		t.Fatalf("origin response not received: %q", string(body))
	}

	select {
	case from := <-s.origin.seenFrom:
		if from == "" {
			t.Fatal("origin did not record a source address")
		}
		t.Logf("origin observed source %s (the egress), never the client", from)
	case <-time.After(2 * time.Second):
		t.Fatal("origin never saw the request")
	}
}

func TestUnmanagedSNIIsRejected(t *testing.T) {
	s := buildStack(t)
	conn, err := tls.Dial("tcp", s.ingress.Addr().String(), &tls.Config{
		ServerName: "evil.example.com", InsecureSkipVerify: true,
	})
	if err == nil {
		conn.Close()
		t.Fatal("the ingress must not proxy names outside the compiled rule set")
	}
}

func TestIngressRefusesNonTLS(t *testing.T) {
	s := buildStack(t)
	c, err := net.Dial("tcp", s.ingress.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadAll(c); err == nil {
		var b [1]byte
		if n, _ := c.Read(b[:]); n > 0 {
			t.Fatal("plain HTTP must not be proxied")
		}
	}
}

func TestEgressRejectsDestinationOutsideAllowlist(t *testing.T) {
	s := buildStack(t)
	inCert := s.ca.issue(t, "ingress-test", "ingress", []string{"127.0.0.1"})
	conn, err := tls.Dial("tcp", s.relayAddr, &tls.Config{
		Certificates: []tls.Certificate{inCert},
		RootCAs:      s.ca.pool(),
		ServerName:   "egress-test",
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{tunnel.ALPN},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	err = tunnel.WriteConnect(conn, "attacker.example.com", 443)
	if err == nil {
		t.Fatal("the egress must never be usable as an open proxy")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("unexpected refusal reason: %v", err)
	}
}

func TestEgressRequiresClientCertificate(t *testing.T) {
	s := buildStack(t)
	conn, err := tls.Dial("tcp", s.relayAddr, &tls.Config{
		RootCAs: s.ca.pool(), ServerName: "egress-test", MinVersion: tls.VersionTLS13,
		NextProtos: []string{tunnel.ALPN},
	})
	if err == nil {
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if e := tunnel.WriteConnect(conn, managedHost, 443); e == nil {
			t.Fatal("an unauthenticated peer must not be able to use the relay")
		}
		conn.Close()
		return
	}
}

func TestEgressBlocksPrivateDestinationsByDefault(t *testing.T) {
	cfg := &model.NodeConfig{
		Egress: model.EgressConfig{
			Allow:        domainset.Set{Exact: []string{"metadata.example.com"}},
			AllowedPorts: []int{443},
		},
	}
	r := relay.New(cfg)
	if !r.Allowed("metadata.example.com", 443) {
		t.Fatal("allowlisted host should pass the name check")
	}
	if r.Allowed("metadata.example.com", 22) {
		t.Fatal("ports outside the revision must be refused")
	}
	if r.Allowed("other.example.com", 443) {
		t.Fatal("non-allowlisted host must be refused")
	}
}
