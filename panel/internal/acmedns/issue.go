package acmedns

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/crypto/acme"
)

const (
	DirLetsEncrypt = "https://acme-v02.api.letsencrypt.org/directory"
	DirStaging     = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// Result is an issued certificate and the key it belongs to.
type Result struct {
	CertPEM  []byte
	KeyPEM   []byte
	NotAfter time.Time
	Domains  []string
}

// Issue obtains one certificate covering every domain via DNS-01. A wildcard and
// its bare name each get their own challenge value at the same record name, so
// the records are added rather than replaced.
func Issue(ctx context.Context, accountKey *ecdsa.PrivateKey, cf *Cloudflare, domains []string, email string, staging bool) (*Result, error) {
	if len(domains) == 0 {
		return nil, errors.New("не указан ни один домен")
	}
	if err := cf.Verify(ctx); err != nil {
		return nil, err
	}
	zoneID, zoneName, err := cf.ZoneID(ctx, strings.TrimPrefix(domains[0], "*."))
	if err != nil {
		return nil, err
	}
	slog.Info("acme: zone resolved", "zone", zoneName, "domains", domains)

	dir := DirLetsEncrypt
	if staging {
		dir = DirStaging
	}
	client := &acme.Client{Key: accountKey, DirectoryURL: dir}
	acct := &acme.Account{}
	if email != "" {
		acct.Contact = []string{"mailto:" + email}
	}
	if _, err := client.Register(ctx, acct, acme.AcceptTOS); err != nil &&
		!errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, fmt.Errorf("регистрация в Let's Encrypt: %w", err)
	}

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(domains...))
	if err != nil {
		return nil, fmt.Errorf("создание заказа: %w", err)
	}

	// Cleanup runs whatever happens: a leftover TXT is harmless but untidy, and
	// it would accumulate on every retry.
	var created []string
	defer func() {
		for _, id := range created {
			if err := cf.DeleteTXT(context.WithoutCancel(ctx), zoneID, id); err != nil {
				slog.Warn("acme: cannot delete challenge record", "err", err)
			}
		}
	}()

	type pending struct {
		chal    *acme.Challenge
		authURL string
	}
	var accept []pending
	waitFor := map[string][]string{} // имя записи -> ожидаемые значения

	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return nil, fmt.Errorf("получение авторизации: %w", err)
		}
		if authz.Status == acme.StatusValid {
			continue
		}
		var chal *acme.Challenge
		for _, c := range authz.Challenges {
			if c.Type == "dns-01" {
				chal = c
				break
			}
		}
		if chal == nil {
			return nil, fmt.Errorf("для %s не предложен dns-01", authz.Identifier.Value)
		}
		val, err := client.DNS01ChallengeRecord(chal.Token)
		if err != nil {
			return nil, err
		}
		name := "_acme-challenge." + authz.Identifier.Value
		id, err := cf.AddTXT(ctx, zoneID, name, val)
		if err != nil {
			return nil, fmt.Errorf("создание TXT-записи: %w", err)
		}
		created = append(created, id)
		waitFor[name] = append(waitFor[name], val)
		accept = append(accept, pending{chal: chal, authURL: authzURL})
	}

	if len(accept) > 0 {
		if err := waitForTXT(ctx, waitFor, 3*time.Minute); err != nil {
			return nil, err
		}
		for _, p := range accept {
			if _, err := client.Accept(ctx, p.chal); err != nil {
				return nil, fmt.Errorf("подтверждение проверки: %w", err)
			}
		}
		for _, p := range accept {
			if _, err := client.WaitAuthorization(ctx, p.authURL); err != nil {
				return nil, fmt.Errorf("проверка домена не прошла: %w", err)
			}
		}
	}

	if _, err := client.WaitOrder(ctx, order.URI); err != nil {
		return nil, fmt.Errorf("ожидание заказа: %w", err)
	}

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{DNSNames: domains}, certKey)
	if err != nil {
		return nil, err
	}
	der, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, fmt.Errorf("выпуск сертификата: %w", err)
	}
	if len(der) == 0 {
		return nil, errors.New("Let's Encrypt не вернул сертификат")
	}
	leaf, err := x509.ParseCertificate(der[0])
	if err != nil {
		return nil, err
	}
	var chain []byte
	for _, b := range der {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b})...)
	}
	keyDER, err := x509.MarshalECPrivateKey(certKey)
	if err != nil {
		return nil, err
	}
	return &Result{
		CertPEM:  chain,
		KeyPEM:   pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		NotAfter: leaf.NotAfter,
		Domains:  domains,
	}, nil
}

// waitForTXT blocks until every expected challenge value is visible, so we do
// not hand Let's Encrypt a name it will fail to resolve and burn an attempt.
func waitForTXT(ctx context.Context, want map[string][]string, timeout time.Duration) error {
	c := &dns.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(timeout)
	for {
		missing := ""
		for name, values := range want {
			seen, err := txtValues(c, name)
			if err != nil {
				missing = name
				break
			}
			for _, v := range values {
				if !containsStr(seen, v) {
					missing = name
					break
				}
			}
			if missing != "" {
				break
			}
		}
		if missing == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("TXT-запись %s не разошлась за отведённое время", missing)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func txtValues(c *dns.Client, name string) ([]string, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeTXT)
	// Cloudflare's own resolver sees Cloudflare-hosted zones without waiting for
	// a cache to expire, which is exactly what we need here.
	resp, _, err := c.Exchange(m, "1.1.1.1:53")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, rr := range resp.Answer {
		if t, ok := rr.(*dns.TXT); ok {
			out = append(out, strings.Join(t.Txt, ""))
		}
	}
	return out, nil
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
