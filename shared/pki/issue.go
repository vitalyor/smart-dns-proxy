package pki

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"time"
)

// Issue mints a fresh keypair and a leaf certificate signed by the CA. Unlike
// Sign (which takes a CSR from a node that keeps its own key), the panel uses
// Issue to generate the node's whole identity for the bundle, and to mint its
// own client certificate for pushing.
func Issue(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, req CSRRequest) (certPEM, keyPEM []byte, err error) {
	key, keyPEM, err := GenerateKey()
	if err != nil {
		return nil, nil, err
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = 397 * 24 * time.Hour
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
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM, nil
}

// FingerprintCert returns the SHA-256 fingerprint of a parsed certificate's DER.
// Matches Fingerprint() applied to the same cert in PEM form.
func FingerprintCert(der []byte) string {
	return fingerprintDER(der)
}
