package agentcore

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
)

const (
	acmeDirLE      = "https://acme-v02.api.letsencrypt.org/directory"
	acmeDirStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

type certRequest struct {
	Domain  string `json:"domain"`
	Email   string `json:"email"`
	Force   bool   `json:"force"`   // reissue even if the current cert is still valid
	Staging bool   `json:"staging"` // use Let's Encrypt staging (no rate limits, untrusted)
}

type certResult struct {
	OK       bool   `json:"ok"`
	Domain   string `json:"domain,omitempty"`
	NotAfter string `json:"not_after,omitempty"`
	Error    string `json:"error,omitempty"`
}

// handleCert issues a DoT/DoH certificate for the node via ACME HTTP-01. The
// challenge server binds :80 only for the duration of the order and is closed
// again immediately, so the node exposes nothing on :80 the rest of the time.
// Issuance failures come back as {ok:false, error} with HTTP 200 so the panel
// can show the reason instead of a bare 400.
func (a *Agent) handleCert(w http.ResponseWriter, r *http.Request) error {
	raw, err := readAll(r, 1<<16)
	if err != nil {
		return err
	}
	var req certRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return fmt.Errorf("cert request is not valid JSON: %w", err)
	}
	req.Domain = strings.TrimSpace(req.Domain)
	req.Email = strings.TrimSpace(req.Email)
	if req.Domain == "" {
		return errors.New("domain is required")
	}
	if a.cfg.Role != "" && a.cfg.Role != "ingress" {
		return errors.New("certificates are only issued on ingress nodes")
	}
	if a.cfg.TLSDir == "" {
		return errors.New("no TLS directory configured on this node (set TLS_DIR)")
	}

	// Serialise: two issuances would fight over :80 and the ACME account.
	a.certMu.Lock()
	defer a.certMu.Unlock()

	if !req.Force {
		if na, ok := a.currentCertNotAfter(); ok && time.Until(na) > 30*24*time.Hour {
			writeJSON(w, http.StatusOK, certResult{OK: true, Domain: req.Domain, NotAfter: na.UTC().Format(time.RFC3339)})
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	res, err := a.issueCert(ctx, req)
	if err != nil {
		slog.Warn("certificate issuance failed", "domain", req.Domain, "err", err)
		writeJSON(w, http.StatusOK, certResult{OK: false, Domain: req.Domain, Error: err.Error()})
		return nil
	}
	slog.Info("certificate issued", "domain", req.Domain, "not_after", res.NotAfter)
	writeJSON(w, http.StatusOK, *res)
	return nil
}

// issueCert runs an ACME HTTP-01 order and writes fullchain.pem/privkey.pem into
// the TLS dir the dns-frontend hot-reloads. Let's Encrypt occasionally leaves an
// order in a state where finalize returns a transient 404 ("certificate not
// found") that a brand-new order clears — so the whole order is retried a few
// times. Each attempt uses a fresh key and order (a previously finalized order's
// cert can't be paired with a new key, so reusing it is not an option).
func (a *Agent) issueCert(ctx context.Context, req certRequest) (*certResult, error) {
	dir := acmeDirLE
	if req.Staging {
		dir = acmeDirStaging
	}
	accountKey, err := a.acmeAccountKey()
	if err != nil {
		return nil, err
	}
	client := &acme.Client{Key: accountKey, DirectoryURL: dir}

	acct := &acme.Account{}
	if req.Email != "" {
		acct.Contact = []string{"mailto:" + req.Email}
	}
	if _, err := client.Register(ctx, acct, acme.AcceptTOS); err != nil &&
		!errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, fmt.Errorf("acme register: %w", err)
	}

	const attempts = 3
	var chain, keyPEM []byte
	var notAfter time.Time
	for attempt := 1; ; attempt++ {
		chain, keyPEM, notAfter, err = a.runACMEOrder(ctx, client, req)
		if err == nil {
			break
		}
		if attempt >= attempts || !isRetryableACME(err) {
			return nil, err
		}
		slog.Warn("cert order failed, retrying with a fresh order",
			"domain", req.Domain, "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt) * 3 * time.Second):
		}
	}

	// Write the key first (0600), then the chain, so dns-frontend never reloads a
	// chain whose key has not landed yet.
	if err := writeFileAtomic(filepath.Join(a.cfg.TLSDir, "privkey.pem"), keyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(a.cfg.TLSDir, "fullchain.pem"), chain, 0o644); err != nil {
		return nil, err
	}
	return &certResult{OK: true, Domain: req.Domain, NotAfter: notAfter.UTC().Format(time.RFC3339)}, nil
}

// runACMEOrder performs one full HTTP-01 order and returns the PEM chain, the PEM
// private key it was issued for, and the leaf's expiry. It does not touch disk,
// so a failed attempt leaves the live certificate untouched.
func (a *Agent) runACMEOrder(ctx context.Context, client *acme.Client, req certRequest) ([]byte, []byte, time.Time, error) {
	fail := func(err error) ([]byte, []byte, time.Time, error) { return nil, nil, time.Time{}, err }

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(req.Domain))
	if err != nil {
		return fail(fmt.Errorf("authorize order: %w", err))
	}

	// Collect the HTTP-01 challenge responses before opening :80.
	responses := map[string]string{}
	var accept []*acme.Challenge
	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return fail(fmt.Errorf("get authorization: %w", err))
		}
		if authz.Status == acme.StatusValid {
			continue
		}
		var chal *acme.Challenge
		for _, c := range authz.Challenges {
			if c.Type == "http-01" {
				chal = c
				break
			}
		}
		if chal == nil {
			return fail(fmt.Errorf("no http-01 challenge offered for %s", authz.Identifier.Value))
		}
		resp, err := client.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return fail(err)
		}
		responses[client.HTTP01ChallengePath(chal.Token)] = resp
		accept = append(accept, chal)
	}

	if len(accept) > 0 {
		stop, err := a.serveChallenges(responses)
		if err != nil {
			return fail(err)
		}
		defer stop()
		for _, chal := range accept {
			if _, err := client.Accept(ctx, chal); err != nil {
				return fail(fmt.Errorf("accept challenge: %w", err))
			}
		}
		for _, authzURL := range order.AuthzURLs {
			if _, err := client.WaitAuthorization(ctx, authzURL); err != nil {
				return fail(fmt.Errorf("domain validation failed (is :80 reachable and the DNS A-record pointed here?): %w", err))
			}
		}
	}

	if _, err := client.WaitOrder(ctx, order.URI); err != nil {
		return fail(fmt.Errorf("wait order: %w", err))
	}

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fail(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{DNSNames: []string{req.Domain}}, certKey)
	if err != nil {
		return fail(err)
	}
	der, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return fail(fmt.Errorf("finalize: %w", err))
	}
	if len(der) == 0 {
		return fail(errors.New("acme returned no certificate"))
	}
	leaf, err := x509.ParseCertificate(der[0])
	if err != nil {
		return fail(err)
	}

	var chain []byte
	for _, block := range der {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block})...)
	}
	keyDER, err := x509.MarshalECPrivateKey(certKey)
	if err != nil {
		return fail(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return chain, keyPEM, leaf.NotAfter, nil
}

// isRetryableACME reports whether an order failure is worth a fresh attempt.
// Transient finalize/order/network hiccups are; a domain-validation failure is
// not — retrying it in a tight loop only burns Let's Encrypt's per-hour budget.
func isRetryableACME(err error) bool {
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "domain validation failed") {
		return false
	}
	for _, sub := range []string{
		"finalize", "certificate not found", "wait order", "order is not ready",
		"timeout", "timed out", "connection reset", "unexpected eof",
	} {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// serveChallenges binds the ACME HTTP-01 address and serves the challenge
// responses. The returned stop closes the listener again.
func (a *Agent) serveChallenges(responses map[string]string) (func(), error) {
	addr := a.cfg.ACMEHTTPAddr
	if addr == "" {
		addr = ":80"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("bind %s for the HTTP-01 challenge: %w", addr, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if resp, ok := responses[r.URL.Path]; ok {
			_, _ = w.Write([]byte(resp))
			return
		}
		http.NotFound(w, r)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return func() { _ = srv.Close() }, nil
}

// acmeAccountKey loads the persisted ACME account key or creates one.
func (a *Agent) acmeAccountKey() (*ecdsa.PrivateKey, error) {
	p := filepath.Join(a.cfg.StateDir, "acme", "account.key")
	if b, err := os.ReadFile(p); err == nil {
		if blk, _ := pem.Decode(b); blk != nil {
			if k, err := x509.ParseECPrivateKey(blk.Bytes); err == nil {
				return k, nil
			}
		}
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	if err := writeFile(p, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// currentCertNotAfter reads the expiry of the cert already in the TLS dir.
func (a *Agent) currentCertNotAfter() (time.Time, bool) {
	b, err := os.ReadFile(filepath.Join(a.cfg.TLSDir, "fullchain.pem"))
	if err != nil {
		return time.Time{}, false
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return time.Time{}, false
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return time.Time{}, false
	}
	return leaf.NotAfter, true
}
