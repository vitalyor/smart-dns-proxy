package agentcore

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

func TestACMEAccountKeyPersists(t *testing.T) {
	a := &Agent{cfg: Config{StateDir: t.TempDir()}}
	k1, err := a.acmeAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	k2, err := a.acmeAccountKey() // second call must load the same key, not mint a new one
	if err != nil {
		t.Fatal(err)
	}
	if !k1.Equal(k2) {
		t.Fatal("account key was regenerated instead of reloaded")
	}
}

func TestCurrentCertNotAfter(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{cfg: Config{TLSDir: dir}}
	if _, ok := a.currentCertNotAfter(); ok {
		t.Fatal("reported a cert when none exists")
	}
	want := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	writeTestCert(t, filepath.Join(dir, "fullchain.pem"), want)
	got, ok := a.currentCertNotAfter()
	if !ok {
		t.Fatal("cert not found after writing")
	}
	if !got.Equal(want) {
		t.Fatalf("NotAfter = %v, want %v", got, want)
	}
}

func writeTestCert(t *testing.T, path string, notAfter time.Time) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o644); err != nil {
		t.Fatal(err)
	}
}
