package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"smartdns/node/dnsfe"
	"smartdns/shared/domainset"
	"smartdns/shared/metrics"
	"smartdns/shared/model"
	"smartdns/shared/sniff"
	"smartdns/shared/tunnel"
)

var (
	mConn   = metrics.Counter("smartdns_ingress_connections_total", "Ingress connections by service and outcome")
	mReject = metrics.Counter("smartdns_ingress_rejected_total", "Rejected ingress connections by reason")
	mActive = metrics.Gauge("smartdns_ingress_active_sessions", "Active proxied sessions")
	mBytes  = metrics.Counter("smartdns_ingress_bytes_total", "Proxied bytes by service and direction")
)

type svcRoute struct {
	svc     *model.Service
	matcher *domainset.Matcher
	pool    *pool
}

// Proxy is the ingress TCP:443 SNI dispatcher.
type Proxy struct {
	mu         sync.RWMutex
	routes     []svcRoute
	cfg        *model.NodeConfig
	tlsCfg     *tls.Config
	active     atomic.Int64
	maxSess    int64
	dohHost    string // SNI of the local DoH server, forwarded to dohBackend on :443
	dohBackend string // where the DoH listener lives, e.g. "dns-frontend:8443"
	// debug включает журнал соединений. Идёт от панели (уровень логов ноды),
	// потому что содержимое журнала — метаданные о том, что человек смотрит.
	debug   bool
	connLog *connRing
}

// SetDoHBackend lets connections whose SNI equals the DoH hostname be forwarded
// to the local DoH listener, so DoH can share :443 with managed HTTPS on one IP.
func (p *Proxy) SetDoHBackend(addr string) {
	p.mu.Lock()
	p.dohBackend = addr
	p.mu.Unlock()
}

// New builds a proxy. tlsCfg must carry the node's client certificate so the
// egress relay can authenticate this ingress.
func New(c *model.NodeConfig, tlsCfg *tls.Config) *Proxy {
	p := &Proxy{tlsCfg: tlsCfg, connLog: newConnRing(1000)}
	p.Apply(c)
	return p
}

// Apply swaps in a new revision, preserving local egress health state.
func (p *Proxy) Apply(c *model.NodeConfig) {
	p.mu.Lock()
	prev := map[string]*pool{}
	for _, r := range p.routes {
		prev[r.svc.Slug] = r.pool
	}
	// Резолвер собираем из конфига напрямую: directResolver берёт замок на
	// чтение, а он здесь уже захвачен на запись.
	res := upstreamResolver(c.DNS.Upstream)
	routes := make([]svcRoute, 0, len(c.Services))
	for i := range c.Services {
		s := &c.Services[i]
		routes = append(routes, svcRoute{
			svc:     s,
			matcher: s.Match.Compile(),
			pool:    newPool(s.Egress, prev[s.Slug], res),
		})
	}
	p.routes, p.cfg = routes, c
	p.debug = strings.EqualFold(c.LogLevel, "debug")
	p.dohHost = stripPort(c.DNS.DoHHostname)
	p.maxSess = int64(c.Ingress.MaxSessions)
	if p.maxSess <= 0 {
		p.maxSess = 10000
	}
	p.mu.Unlock()
	slog.Info("ingress routes applied", "revision", c.RevisionID, "services", len(routes))
}

func (p *Proxy) lookup(host string) *svcRoute {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var best *svcRoute
	bestSpec, bestPrio := -1, -1
	for i := range p.routes {
		spec := p.routes[i].matcher.Specificity(host)
		if spec < 0 {
			continue
		}
		prio := p.routes[i].svc.Priority
		if spec > bestSpec || (spec == bestSpec && prio > bestPrio) {
			best, bestSpec, bestPrio = &p.routes[i], spec, prio
		}
	}
	return best
}

func (p *Proxy) config() *model.NodeConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg
}

func (p *Proxy) settings() model.IngressConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s := p.cfg.Ingress
	if s.ClientHelloTimeoutMs <= 0 {
		s.ClientHelloTimeoutMs = 3000
	}
	if s.MaxPreReadBytes <= 0 {
		s.MaxPreReadBytes = 16384
	}
	if s.DialTimeoutMs <= 0 {
		s.DialTimeoutMs = 8000
	}
	if s.IdleTimeoutSec <= 0 {
		s.IdleTimeoutSec = 300
	}
	return s
}

// StartProbes keeps local egress health fresh without user traffic.
func (p *Proxy) StartProbes() {
	go func() {
		for {
			p.mu.RLock()
			routes := append([]svcRoute(nil), p.routes...)
			p.mu.RUnlock()
			interval := 30 * time.Second
			for _, r := range routes {
				if r.pool.policy.ProbeInterval > 0 {
					interval = time.Duration(r.pool.policy.ProbeInterval) * time.Second
				}
				r.pool.probe(p.tlsCfg, 5*time.Second)
			}
			time.Sleep(interval)
		}
	}()
}

// Serve accepts connections until l is closed.
func (p *Proxy) Serve(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go p.handle(c)
	}
}

func (p *Proxy) handle(c net.Conn) {
	defer c.Close()
	if n := p.active.Add(1); n > p.maxSess {
		p.active.Add(-1)
		mReject.Inc("reason", "max_sessions")
		return
	}
	defer func() { mActive.Set(p.active.Add(-1)) }()
	mActive.Set(p.active.Load())

	st := p.settings()
	start := time.Now()
	client := clientIP(c)
	_ = c.SetReadDeadline(time.Now().Add(time.Duration(st.ClientHelloTimeoutMs) * time.Millisecond))
	sni, raw, err := sniff.PeekSNI(c, st.MaxPreReadBytes)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		reason := reasonOf(err)
		mReject.Inc("reason", reason)
		p.note(ConnEntry{TS: start.UnixMilli(), Client: client, SNI: sni,
			Result: reason, MS: time.Since(start).Milliseconds()})
		return
	}
	host, err := domainset.NormalizeHost(sni)
	if err != nil {
		// IP literals and malformed names are never proxied.
		mReject.Inc("reason", "invalid_sni")
		p.note(ConnEntry{TS: start.UnixMilli(), Client: client, SNI: sni,
			Result: "invalid_sni", MS: time.Since(start).Milliseconds()})
		return
	}
	route := p.lookup(host)
	if route == nil {
		// DoH shares :443 with managed HTTPS on a single IP: a ClientHello for
		// the DoH hostname is forwarded verbatim to the local DoH listener.
		if p.dohForward(c, host, raw) {
			p.note(ConnEntry{TS: start.UnixMilli(), Client: client, SNI: host,
				Service: "_doh", Result: "ok", MS: time.Since(start).Milliseconds()})
			return
		}
		// This is the guard that stops the ingress being an open TCP proxy.
		mReject.Inc("reason", "sni_not_managed")
		p.note(ConnEntry{TS: start.UnixMilli(), Client: client, SNI: host,
			Result: "sni_not_managed", MS: time.Since(start).Milliseconds()})
		// The server name appears only at debug level: it is metadata about
		// what a user is browsing, so it must not reach steady-state logs.
		slog.Debug("rejected unmanaged SNI", "sni", host)
		return
	}
	port := 443
	if la, ok := c.LocalAddr().(*net.TCPAddr); ok && la.Port != 0 {
		port = la.Port
	}
	if !portAllowed(route.svc.AllowedPorts, port) {
		mReject.Inc("reason", "port_not_allowed")
		p.note(ConnEntry{TS: start.UnixMilli(), Client: client, SNI: host, Port: port,
			Service: route.svc.Slug, Result: "port_not_allowed", MS: time.Since(start).Milliseconds()})
		return
	}

	timeout := time.Duration(st.DialTimeoutMs) * time.Millisecond
	var up net.Conn
	var tgt *target
	// Прямой выход с самой входной ноды. Нужен там, где провайдер входа сайт не
	// режет: туннель до заграничного выхода добавил бы только задержку. Если
	// прямой выход не удался, а запасные ноды у сервиса есть, идём в туннель.
	if route.svc.Egress.Local {
		up, err = dialDirect(p.directResolver(), host, port, timeout)
		if err != nil {
			mConn.Inc("service", route.svc.Slug, "result", "local_failed")
			slog.Warn("direct exit failed", "service", route.svc.Slug, "err", err)
		}
	}
	if up == nil {
		up, tgt, err = route.pool.dial(p.tlsCfg, host, port, timeout)
		if err != nil {
			mConn.Inc("service", route.svc.Slug, "result", "no_egress")
			slog.Warn("no usable egress", "service", route.svc.Slug, "err", err)
			p.note(ConnEntry{TS: start.UnixMilli(), Client: client, SNI: host, Port: port,
				Service: route.svc.Slug, Result: "no_egress", MS: time.Since(start).Milliseconds()})
			return
		}
	}
	// Replay the ClientHello bytes verbatim: TLS is never terminated here.
	if _, err := up.Write(raw); err != nil {
		_ = up.Close()
		mConn.Inc("service", route.svc.Slug, "result", "write_failed")
		p.note(ConnEntry{TS: start.UnixMilli(), Client: client, SNI: host, Port: port,
			Service: route.svc.Slug, Egress: exitLabel(tgt), Result: "write_failed",
			MS: time.Since(start).Milliseconds()})
		return
	}
	mConn.Inc("service", route.svc.Slug, "result", "ok")
	slog.Debug("proxying", "service", route.svc.Slug, "sni", host, "port", port, "egress", exitLabel(tgt))
	p.note(ConnEntry{TS: start.UnixMilli(), Client: client, SNI: host, Port: port,
		Service: route.svc.Slug, Egress: exitLabel(tgt), Result: "ok",
		MS: time.Since(start).Milliseconds()})
	a2b, b2a := tunnel.Splice(c, up, time.Duration(st.IdleTimeoutSec)*time.Second)
	mBytes.Add(a2b+int64(len(raw)), "service", route.svc.Slug, "direction", "up")
	mBytes.Add(b2a, "service", route.svc.Slug, "direction", "down")
}

// exitLabel называет путь наружу для журнала. При прямом выходе ноды-цели нет,
// а аргументы slog вычисляются всегда, даже когда уровень отладки выключен:
// tgt.Name на пустой цели ронял процесс на каждом таком соединении.
func exitLabel(t *target) string {
	if t == nil {
		return "прямой выход"
	}
	return t.Name
}

// dialDirect выходит в интернет прямо с входной ноды, без туннеля.
//
// Единственная тонкость — петля. Имя сервиса на этой же ноде разрешается в её
// собственный адрес, и соединение вернулось бы в этот же прокси, а тот снова
// набрал бы сам себя — до конца сокетов. Поэтому сверяем, куда попали.
func dialDirect(res *net.Resolver, host string, port int, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ips, err := res.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: timeout}
	var lastErr error
	for _, ip := range ips {
		c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip.IP.String(), strconv.Itoa(port)))
		if err != nil {
			lastErr = err
			continue
		}
		ra, aok := c.RemoteAddr().(*net.TCPAddr)
		la, bok := c.LocalAddr().(*net.TCPAddr)
		if aok && bok && ra.IP.Equal(la.IP) {
			_ = c.Close()
			lastErr = fmt.Errorf("прямой выход ведёт на саму ноду (%s): имя разрешается в её адрес", ra.IP)
			continue
		}
		return c, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("имя %s не разрешилось ни в один адрес", host)
	}
	return nil, lastErr
}

// directResolver — чем прокси разрешает имена при прямом выходе.
//
// Берём тот же рекурсивный резолвер, что обслуживает DNS-часть ноды. Резолвер
// провайдера, прописанный в системе, отвечал «server misbehaving» на живые
// имена, и прямой выход падал на ровном месте. Свой unbound рядом, на петле.
func (p *Proxy) directResolver() *net.Resolver {
	up := ""
	if cfg := p.config(); cfg != nil {
		up = cfg.DNS.Upstream
	}
	return upstreamResolver(up)
}

// upstreamResolver направляет поиск имён в наш собственный unbound вместо
// системного резолвера хоста. У хостера он один, нередко служебный и не
// публичный, а на нём висит всё: имя ноды выхода разрешается заново на каждое
// новое соединение и на каждую проверку здоровья. Стоило ему поперхнуться —
// и разом умирали все проксируемые сервисы, потому что вход переставал
// находить, куда идти. Свой unbound ещё и кеширует, так что наружу уходит
// один запрос на TTL, а не тысячи.
func upstreamResolver(up string) *net.Resolver {
	if up == "" {
		return net.DefaultResolver
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", up)
		},
	}
}

// dohForward tunnels a DoH-hostname ClientHello to the local DoH listener. It
// returns true when the SNI matched DoH (whether or not the backend answered),
// so the caller does not also count it as an unmanaged rejection.
// dohMatch принимает и само имя резолвера, и личные имена под ним.
//
// Личное имя вида <токен>.dns.example.net нужно Android: в Private DNS вводится
// только имя хоста, поля для токена там нет. Wildcard-сертификат покрывает
// ровно эту форму, поэтому на 853 такие имена работали всегда — а на 443
// сравнение шло точным, и DoH по личному имени молча отбивался как чужой SNI.
//
// Глубже одного уровня не принимаем: токен — одна метка, и расширять шаблон
// значило бы принимать любое имя в зоне.
func dohMatch(host, dohHost string) bool {
	if host == dohHost {
		return true
	}
	rest, ok := strings.CutSuffix(host, "."+dohHost)
	return ok && rest != "" && !strings.Contains(rest, ".")
}

func (p *Proxy) dohForward(c net.Conn, host string, raw []byte) bool {
	p.mu.RLock()
	dohHost, backend := p.dohHost, p.dohBackend
	idle := time.Duration(p.cfg.Ingress.IdleTimeoutSec) * time.Second
	p.mu.RUnlock()
	if dohHost == "" || backend == "" || !dohMatch(host, dohHost) {
		return false
	}
	up, err := net.DialTimeout("tcp", backend, 5*time.Second)
	if err != nil {
		mReject.Inc("reason", "doh_backend_unreachable")
		slog.Warn("DoH backend unreachable", "backend", backend, "err", err)
		return true
	}
	// Carry the real client address to dns-frontend via a PROXY-protocol header,
	// so the live query log shows the source device, not this proxy.
	if hdr := dnsfe.ProxyHeaderV1(c.RemoteAddr(), up.RemoteAddr()); hdr != "" {
		if _, err := up.Write([]byte(hdr)); err != nil {
			_ = up.Close()
			return true
		}
	}
	if _, err := up.Write(raw); err != nil {
		_ = up.Close()
		return true
	}
	mConn.Inc("service", "_doh", "result", "ok")
	if idle <= 0 {
		idle = 300 * time.Second
	}
	tunnel.Splice(c, up, idle)
	return true
}

// stripPort returns the host part of "host" or "host:port".
func stripPort(s string) string {
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

func reasonOf(err error) string {
	switch {
	case errors.Is(err, sniff.ErrNotTLS):
		return "not_tls"
	case errors.Is(err, sniff.ErrNoSNI):
		return "no_sni"
	case errors.Is(err, sniff.ErrTooLarge):
		return "too_large"
	case errors.Is(err, io.EOF), errors.Is(err, sniff.ErrIncomplete):
		return "incomplete"
	default:
		return "read_error"
	}
}

func portAllowed(allowed []int, port int) bool {
	if len(allowed) == 0 {
		return port == 443
	}
	for _, p := range allowed {
		if p == port {
			return true
		}
	}
	return false
}

// Status renders a short human-readable health line per service.
func (p *Proxy) Status() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var b strings.Builder
	for _, r := range p.routes {
		fmt.Fprintf(&b, "%s: rules=%d", r.svc.Slug, r.matcher.Size())
		for _, t := range r.pool.targets {
			h, l := t.snapshot()
			fmt.Fprintf(&b, " [%s healthy=%v %.1fms]", t.Name, h, l)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
