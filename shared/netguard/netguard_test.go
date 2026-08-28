package netguard

import (
	"net/netip"
	"testing"
)

func TestIsBlocked(t *testing.T) {
	bad := []string{
		"127.0.0.1", "10.1.2.3", "192.168.1.1", "172.16.0.1", "169.254.169.254",
		"100.64.0.1", "0.0.0.0", "224.0.0.1", "::1", "fe80::1", "fc00::1",
		"::ffff:127.0.0.1", "2001:db8::1",
	}
	for _, s := range bad {
		if !IsBlocked(netip.MustParseAddr(s)) {
			t.Fatalf("%s must be blocked", s)
		}
	}
	good := []string{"8.8.8.8", "1.1.1.1", "142.250.74.110", "2606:4700::1111"}
	for _, s := range good {
		if IsBlocked(netip.MustParseAddr(s)) {
			t.Fatalf("%s must be allowed", s)
		}
	}
}
