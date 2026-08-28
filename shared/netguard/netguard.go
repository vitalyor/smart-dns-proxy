// Package netguard classifies IP addresses that must never be reachable
// through the rule fetcher (SSRF) or the egress relay (open proxy / rebinding).
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

var blocked = func() []netip.Prefix {
	raw := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
		"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
		"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "100::/64",
		"2001::/23", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10",
		"ff00::/8",
	}
	out := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// IsBlocked reports whether addr is loopback, private, link-local, multicast,
// CGNAT, documentation space or a cloud metadata endpoint.
func IsBlocked(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	a := addr.Unmap()
	for _, p := range blocked {
		if p.Contains(a) || p.Contains(addr) {
			return true
		}
	}
	return false
}

// CheckAddrs returns an error if any resolved address is blocked.
func CheckAddrs(addrs []netip.Addr) error {
	if len(addrs) == 0 {
		return fmt.Errorf("no addresses resolved")
	}
	for _, a := range addrs {
		if IsBlocked(a) {
			return fmt.Errorf("destination %s is in a blocked range", a)
		}
	}
	return nil
}

// SafeDialer returns a dialer that re-checks the concrete IP after DNS
// resolution and immediately before connect, defeating DNS rebinding.
func SafeDialer() *net.Dialer { return SafeDialerPolicy(false) }

// SafeDialerPolicy is SafeDialer with an explicit lab escape hatch.
// allowPrivate MUST stay false in production: it disables the open-proxy and
// SSRF guard. It exists so integration tests and single-host lab stacks can
// reach mock origins on RFC1918 / loopback addresses.
func SafeDialerPolicy(allowPrivate bool) *net.Dialer {
	d := &net.Dialer{}
	if allowPrivate {
		return d
	}
	d.Control = func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			return fmt.Errorf("control: %w", err)
		}
		if IsBlocked(ip) {
			return fmt.Errorf("blocked destination %s", ip)
		}
		return nil
	}
	return d
}

// ResolvePublic resolves host and returns only public addresses.
func ResolvePublic(ctx context.Context, r *net.Resolver, host string) ([]netip.Addr, error) {
	return Resolve(ctx, r, host, false)
}

// Resolve resolves host, filtering blocked ranges unless allowPrivate is set.
func Resolve(ctx context.Context, r *net.Resolver, host string, allowPrivate bool) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !allowPrivate && IsBlocked(ip) {
			return nil, fmt.Errorf("literal %s is blocked", ip)
		}
		return []netip.Addr{ip}, nil
	}
	if r == nil {
		r = net.DefaultResolver
	}
	addrs, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if allowPrivate {
		return addrs, nil
	}
	out := addrs[:0]
	for _, a := range addrs {
		if !IsBlocked(a) {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("all addresses for %s are blocked", host)
	}
	return out, nil
}
