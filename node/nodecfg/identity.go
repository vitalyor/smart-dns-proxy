package nodecfg

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
)

// Identity is the node keypair issued by the panel CA.
type Identity struct {
	Dir      string
	CertFile string
	KeyFile  string
	CAFile   string
}

// DefaultIdentity resolves the standard agent identity layout.
func DefaultIdentity(dir string) Identity {
	return Identity{
		Dir:      dir,
		CertFile: filepath.Join(dir, "node.crt"),
		KeyFile:  filepath.Join(dir, "node.key"),
		CAFile:   filepath.Join(dir, "ca.crt"),
	}
}

func (i Identity) pool() (*x509.CertPool, error) {
	b, err := os.ReadFile(i.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read CA: %w", err)
	}
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("CA file %s contains no certificate", i.CAFile)
	}
	return p, nil
}

// ClientTLS builds the mTLS config an ingress uses to reach an egress relay.
func (i Identity) ClientTLS() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(i.CertFile, i.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load node keypair: %w", err)
	}
	pool, err := i.pool()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"smartdns/1"},
	}, nil
}

// ServerTLS builds the mTLS config an egress relay presents to ingress peers.
func (i Identity) ServerTLS() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(i.CertFile, i.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load node keypair: %w", err)
	}
	pool, err := i.pool()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"smartdns/1"},
	}, nil
}
