package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writePair(t *testing.T, certFile, keyFile, cn string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{cn},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cb := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kd, _ := x509.MarshalECPrivateKey(key)
	kb := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd})
	if err := os.WriteFile(certFile, cb, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, kb, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReloadingServerConfigMissing(t *testing.T) {
	if _, err := ReloadingServerConfig("/no/such/cert.pem", "/no/such/key.pem"); err == nil {
		t.Fatal("expected an error when the certificate file is absent")
	}
}

func TestReloadingServerConfigReloadsOnChange(t *testing.T) {
	dir := t.TempDir()
	cf := filepath.Join(dir, "c.pem")
	kf := filepath.Join(dir, "k.pem")
	writePair(t, cf, kf, "first.example")

	cfg, err := ReloadingServerConfig(cf, kf)
	if err != nil {
		t.Fatal(err)
	}
	leafCN := func() string {
		c, err := cfg.GetCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		return leaf.Subject.CommonName
	}
	if got := leafCN(); got != "first.example" {
		t.Fatalf("initial cert CN = %q, want first.example", got)
	}

	// Rotate the file and bump its mtime; the next handshake must serve the new cert.
	writePair(t, cf, kf, "second.example")
	_ = os.Chtimes(cf, time.Now().Add(time.Second), time.Now().Add(time.Second))
	if got := leafCN(); got != "second.example" {
		t.Fatalf("after rotation cert CN = %q, want second.example", got)
	}
}
