package dnsfe

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"smartdns/shared/model"
)

func TestTokenFromSNI(t *testing.T) {
	r := &Router{}
	r.Apply(&model.NodeConfig{DNS: model.DNSConfig{DoTHostname: "dns.nolim.online"}})

	want := func(label string) string {
		sum := sha256.Sum256([]byte(label))
		return hex.EncodeToString(sum[:])
	}

	cases := map[string]string{
		"abc123.dns.nolim.online":  want("abc123"),
		"ABC123.DNS.NOLIM.ONLINE":  want("abc123"), // имя регистронезависимо
		"abc123.dns.nolim.online.": want("abc123"), // с корневой точкой
		"dns.nolim.online":         "",             // голое имя резолвера — токена нет
		"":                         "",
		"evil.example.com":         "",             // чужой домен
		"a.b.dns.nolim.online":     "",             // только один уровень считается токеном
	}
	for sni, exp := range cases {
		if got := r.TokenFromSNI(sni); got != exp {
			t.Errorf("TokenFromSNI(%q) = %q, want %q", sni, got, exp)
		}
	}
}

// Без настроенного имени DoT ничего не считается токеном — иначе на свежей ноде
// произвольный SNI мог бы притвориться устройством.
func TestTokenFromSNIWithoutHostname(t *testing.T) {
	r := &Router{}
	r.Apply(&model.NodeConfig{})
	if got := r.TokenFromSNI("abc.dns.nolim.online"); got != "" {
		t.Errorf("expected no token without a configured DoT hostname, got %q", got)
	}
}
