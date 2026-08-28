// Package pki implements the small internal CA used for agent mTLS and for
// the ingress→egress tunnel. Node private keys never leave the node.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// OrgName marks every certificate issued by this panel.
const OrgName = "smartdns"

// GenerateKey returns a fresh P-256 key encoded as PEM.
func GenerateKey() (*ecdsa.PrivateKey, []byte, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, nil, err
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// NewCA creates a self-signed CA valid for ten years.
func NewCA(commonName string) (certPEM, keyPEM []byte, err error) {
	key, keyPEM, err := GenerateKey()
	if err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{OrgName}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM, nil
}

// CSRRequest describes a certificate the panel is asked to issue.
type CSRRequest struct {
	CommonName string
	Role       string // ingress|egress|agent
	DNSNames   []string
	IPs        []string
	TTL        time.Duration
}

// LoadCA parses a CA keypair.
func LoadCA(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, nil, fmt.Errorf("invalid CA PEM")
	}
	crt, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return crt, key, nil
}

// Sign issues a leaf certificate for the given CSR DER using the CA.
func Sign(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, csrDER []byte, req CSRRequest) ([]byte, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, fmt.Errorf("parse csr: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("csr signature: %w", err)
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = 90 * 24 * time.Hour
	}
	tpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject: pkix.Name{
			CommonName:         req.CommonName,
			Organization:       []string{OrgName},
			OrganizationalUnit: []string{req.Role},
		},
		NotBefore:   time.Now().Add(-5 * time.Minute),
		NotAfter:    time.Now().Add(ttl),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		DNSNames:    req.DNSNames,
	}
	for _, s := range req.IPs {
		if ip := net.ParseIP(s); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, csr.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// NewCSR builds a CSR for a node identity.
func NewCSR(key *ecdsa.PrivateKey, commonName string) ([]byte, error) {
	tpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: commonName}}
	return x509.CreateCertificateRequest(rand.Reader, tpl, key)
}

// Fingerprint returns the SHA-256 fingerprint of a PEM certificate.
func Fingerprint(certPEM []byte) (string, error) {
	b, _ := pem.Decode(certPEM)
	if b == nil {
		return "", fmt.Errorf("not a PEM certificate")
	}
	s := sha256.Sum256(b.Bytes)
	return hex.EncodeToString(s[:]), nil
}

// SerialOf returns the decimal serial of a PEM certificate.
func SerialOf(certPEM []byte) (string, time.Time, time.Time, error) {
	b, _ := pem.Decode(certPEM)
	if b == nil {
		return "", time.Time{}, time.Time{}, fmt.Errorf("not a PEM certificate")
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return "", time.Time{}, time.Time{}, err
	}
	return c.SerialNumber.String(), c.NotBefore, c.NotAfter, nil
}

func fingerprintDER(der []byte) string {
	s := sha256.Sum256(der)
	return hex.EncodeToString(s[:])
}

func serial() *big.Int {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	n, _ := rand.Int(rand.Reader, max)
	return n
}
