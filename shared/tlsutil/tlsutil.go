// Package tlsutil builds TLS configurations for the node listeners.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"sync"
	"time"
)

// ReloadingServerConfig returns a tls.Config whose certificate is re-read from
// disk when the file changes, at handshake time — so a renewed certificate is
// picked up without restarting the process. It fails only if the certificate
// cannot be loaded even once.
func ReloadingServerConfig(certFile, keyFile string) (*tls.Config, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if _, err := r.reload(); err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		MinVersion:       tls.VersionTLS12,
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256},
		GetCertificate:   func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return r.get() },
	}
	return cfg, nil
}

type certReloader struct {
	certFile, keyFile string
	mu                sync.RWMutex
	cert              *tls.Certificate
	mtime             time.Time
}

// reload loads the keypair if the cert file's mtime changed since last load.
func (r *certReloader) reload() (*tls.Certificate, error) {
	fi, err := os.Stat(r.certFile)
	if err != nil {
		return nil, err
	}
	r.mu.RLock()
	cur, mt := r.cert, r.mtime
	r.mu.RUnlock()
	if cur != nil && fi.ModTime().Equal(mt) {
		return cur, nil
	}
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.cert, r.mtime = &cert, fi.ModTime()
	r.mu.Unlock()
	slog.Info("TLS certificate loaded", "file", r.certFile)
	return &cert, nil
}

// get returns the current certificate, falling back to the last good one if a
// transient reload error occurs (e.g. the file is mid-rotation).
func (r *certReloader) get() (*tls.Certificate, error) {
	if c, err := r.reload(); err == nil {
		return c, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.cert != nil {
		return r.cert, nil
	}
	return nil, fmt.Errorf("no usable TLS certificate")
}

// ServerConfig loads a certificate pair, or generates a short-lived
// self-signed certificate when no files are configured. Self-signed mode is
// only acceptable for lab and integration testing; the panel warns about it.
func ServerConfig(certFile, keyFile string, names []string) (*tls.Config, error) {
	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load keypair: %w", err)
		}
		return base(cert), nil
	}
	if os.Getenv("ALLOW_SELF_SIGNED_TLS") != "1" {
		return nil, fmt.Errorf("no TLS certificate configured; set TLS_CERT_FILE/TLS_KEY_FILE or ALLOW_SELF_SIGNED_TLS=1 for lab use")
	}
	cert, err := selfSigned(names)
	if err != nil {
		return nil, err
	}
	slog.Warn("using a self-signed TLS certificate; DoT/DoH clients will not validate it", "names", names)
	return base(cert), nil
}

func base(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates:     []tls.Certificate{cert},
		MinVersion:       tls.VersionTLS12,
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256},
	}
}

func selfSigned(names []string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	var dns []string
	for _, n := range names {
		if n != "" {
			dns = append(dns, n)
		}
	}
	if len(dns) == 0 {
		dns = []string{"localhost"}
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: dns[0], Organization: []string{"smartdns-selfsigned"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(0, 3, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dns,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	kd, _ := x509.MarshalECPrivateKey(key)
	return tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}),
	)
}
