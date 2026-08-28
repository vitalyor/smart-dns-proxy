// Command mockorigin stands in for a real service during e2e tests. It has its
// own certificate authority, so a test only passes if TLS was never terminated
// on the way, and it reports the source address it observed.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	host := env("ORIGIN_HOST", "origin.test")
	addr := env("ORIGIN_ADDR", ":443")
	certOut := os.Getenv("ORIGIN_CERT_OUT")

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host, Organization: []string{"mock-origin"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{host},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kd, _ := x509.MarshalECPrivateKey(key)
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}))
	if err != nil {
		log.Fatal(err)
	}
	if certOut != "" {
		if err := os.WriteFile(certOut, certPEM, 0o644); err != nil {
			log.Printf("cannot publish CA to %s: %v", certOut, err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		w.Header().Set("X-Observed-Source", ip)
		fmt.Fprintf(w, "origin-ok host=%s source=%s\n", r.Host, ip)
	})
	srv := &http.Server{
		Addr:      addr,
		Handler:   mux,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12},
	}
	log.Printf("mock origin for %s listening on %s", host, addr)
	log.Fatal(srv.ListenAndServeTLS("", ""))
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
