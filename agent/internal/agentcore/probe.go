package agentcore

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"smartdns/shared/model"
	"smartdns/shared/tlsutil"
)

// probe fills the DNS-meaningful health fields with cheap local checks. These
// are the signals that predict user-facing breakage — not CPU or RAM, which the
// operator already sees in their host view. The sneaky one is EgressReachable:
// DNS can answer perfectly while unblocking is silently broken because the
// tunnel to egress is down.
func (a *Agent) probe(h *model.Health) {
	cfg, err := a.ActiveConfig()
	if err != nil {
		return
	}
	// Управляющий сертификат есть у ноды любой роли: без него панель до неё не
	// достучится. Раньше его считала только точка входа, и у точки выхода в
	// панели всегда горело «0 дней» — тревога на пустом месте.
	h.CertDaysLeft = certDaysLeft(a.cfg.certPath())
	h.ObservedIPv4 = observedIPv4()

	switch cfg.Role {
	case "ingress":
		h.UpstreamOK = dialOK(cfg.DNS.Upstream, 1500*time.Millisecond)
		h.EgressReachable = ingressCanReachEgress(cfg)
		h.ResolverCertFP, h.ResolverCertDaysLeft = a.resolverCert()
	case "egress":
		// The relay's own resolver reachability stands in for resolve health.
		h.UpstreamOK = dialOK(cfg.Egress.Resolver, 1500*time.Millisecond)
	}
}

// ingressCanReachEgress returns true if at least one egress target of any
// service accepts a TCP connection. One reachable path is enough to unblock.
func ingressCanReachEgress(cfg *model.NodeConfig) bool {
	seen := map[string]bool{}
	any := false
	for _, s := range cfg.Services {
		for _, t := range s.Egress.Targets {
			if t.Endpoint == "" || seen[t.Endpoint] {
				continue
			}
			seen[t.Endpoint] = true
			any = true
			if dialOK(t.Endpoint, 2*time.Second) {
				return true
			}
		}
	}
	// No targets configured at all is not an egress-reachability failure.
	return !any
}

func dialOK(addr string, timeout time.Duration) bool {
	if addr == "" {
		return false
	}
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		// A bare host:port for UDP DNS (e.g. unbound:53) still answers a TCP
		// dial in our images; if not, treat unknown as not-ok.
		return false
	}
	_ = c.Close()
	return true
}

// resolverCert reports the public DoH/DoT certificate the listener actually
// serves. Файл на диске — только запасной ответ: если dns-frontend не смог
// перечитать продление, отпечаток слушателя останется старым, и панель это
// увидит вместо того, чтобы считать доставку успешной.
func (a *Agent) resolverCert() (fp string, daysLeft int) {
	if u := certInfoURL(a.cfg.DNSCountersURL); u != "" {
		if f, notAfter, ok := fetchCertInfo(u); ok {
			return f, int(time.Until(notAfter).Hours() / 24)
		}
	}
	path := filepath.Join(a.cfg.TLSDir, "fullchain.pem")
	if a.cfg.TLSDir == "" {
		return "", 0
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0
	}
	leaf, err := parseLeaf(b)
	if err != nil {
		return "", 0
	}
	return tlsutil.Fingerprint(leaf), int(time.Until(leaf.NotAfter).Hours() / 24)
}

// certInfoURL derives the sibling endpoint from the counters URL, so the node
// keeps exactly one internal address in its configuration.
func certInfoURL(countersURL string) string {
	if countersURL == "" {
		return ""
	}
	i := strings.LastIndex(countersURL, "/")
	if i < 0 {
		return ""
	}
	return countersURL[:i] + "/certinfo"
}

func fetchCertInfo(url string) (fp string, notAfter time.Time, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", time.Time{}, false
	}
	resp, err := dnsLogClient.Do(req)
	if err != nil {
		return "", time.Time{}, false
	}
	defer resp.Body.Close()
	var body struct {
		Available bool   `json:"available"`
		FP        string `json:"fingerprint"`
		NotAfter  string `json:"not_after"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body); err != nil || !body.Available {
		return "", time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, body.NotAfter)
	if err != nil {
		return "", time.Time{}, false
	}
	return body.FP, t, true
}

// certDaysLeft reports days until the earliest-expiring cert in the PEM file,
// or -1 if there is no readable cert. Used to warn before DoT/DoH silently dies.
func certDaysLeft(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	earliest := time.Time{}
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			continue
		}
		if earliest.IsZero() || c.NotAfter.Before(earliest) {
			earliest = c.NotAfter
		}
	}
	if earliest.IsZero() {
		return -1
	}
	return int(time.Until(earliest).Hours() / 24)
}

// observedIPv4 сообщает, какой адрес у ноды снаружи.
//
// Сначала смотрим на исходящий сокет: подключение UDP только фиксирует маршрут,
// пакет не уходит. На сервере с адресом прямо на интерфейсе этого достаточно.
// Но агент обычно живёт в контейнере на docker-мосту и видит там 172.x — тогда
// спрашиваем внешний отражатель. Ответ держим час: heartbeat уходит каждые
// несколько секунд, дёргать чужой сервис так часто незачем.
var (
	ipMu   sync.Mutex
	ipVal  string
	ipWhen time.Time
)

func observedIPv4() string {
	if ip := localOutboundIPv4(); ip != "" {
		return ip
	}
	ipMu.Lock()
	defer ipMu.Unlock()
	if ipVal != "" && time.Since(ipWhen) < time.Hour {
		return ipVal
	}
	if ip := reflectedIPv4(); ip != "" {
		ipVal, ipWhen = ip, time.Now()
	} else if ipVal != "" {
		// Отражатель недоступен — лучше повторить прежний ответ, чем стереть
		// адрес в панели из-за одной неудачной попытки.
		ipWhen = time.Now().Add(-55 * time.Minute)
	}
	return ipVal
}

func localOutboundIPv4() string {
	c, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	defer c.Close()
	a, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok || a.IP == nil || a.IP.IsPrivate() || a.IP.IsLoopback() || a.IP.IsLinkLocalUnicast() {
		return ""
	}
	return a.IP.String()
}

// reflectedIPv4 спрашивает адрес у внешнего отражателя. Два независимых, чтобы
// падение одного не оставляло панель без данных; ответ принимаем, только если
// это разумный публичный IPv4.
func reflectedIPv4() string {
	cl := &http.Client{Timeout: 4 * time.Second}
	for _, u := range []string{"https://api.ipify.org", "https://ifconfig.me/ip"} {
		resp, err := cl.Get(u)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		ip := net.ParseIP(strings.TrimSpace(string(b)))
		if ip == nil || ip.To4() == nil || ip.IsPrivate() || ip.IsLoopback() {
			continue
		}
		return ip.String()
	}
	return ""
}
