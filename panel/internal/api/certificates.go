package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"smartdns/panel/internal/acmedns"
	"smartdns/panel/internal/pusher"
	"smartdns/panel/internal/store"
)

// The resolver certificate covers the bare hostname and the wildcard beneath it.
// The wildcard is what gives Android a personal DoT address — Private DNS takes
// a hostname and nothing else, so the device token has to live in the name.
const resolverCertName = "resolver"

const (
	secretCFToken = "cloudflare_api_token"
	secretACMEKey = "acme_account_key"
)

func (s *Server) getSecret(ctx contextT, key string) (string, error) {
	var b []byte
	if err := s.DB.QueryRow(ctx, `SELECT value FROM panel_secrets WHERE key=$1`, key).Scan(&b); err != nil {
		return "", err
	}
	return s.decryptSecret(b)
}

func (s *Server) putSecret(ctx contextT, key, plain string) error {
	enc, err := s.encryptSecret(plain)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `
		INSERT INTO panel_secrets (key, value) VALUES ($1,$2)
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, key, enc)
	return err
}

// resolverDomains derives what the certificate must cover from the configured
// resolver names, so the operator never types a domain list twice.
func (s *Server) resolverDomains(ctx contextT) []string {
	set := map[string]bool{}
	for _, k := range []string{"doh_hostname", "dot_hostname"} {
		if h := strings.TrimSpace(getSetting(ctx, s.DB, k, "")); h != "" {
			set[h] = true
			set["*."+h] = true
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func (s *Server) certificatesStatus(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	type row struct {
		Domains   []string  `db:"domains"`
		NotAfter  time.Time `db:"not_after"`
		Staging   bool      `db:"staging"`
		UpdatedAt time.Time `db:"updated_at"`
	}
	out := map[string]any{
		"domains_needed":   s.resolverDomains(ctx),
		"cloudflare_ready": false,
	}
	if _, err := s.getSecret(ctx, secretCFToken); err == nil {
		out["cloudflare_ready"] = true
	}
	if c, err := store.One[row](ctx, s.DB,
		`SELECT domains, not_after, staging, updated_at FROM certificates WHERE name=$1`, resolverCertName); err == nil {
		out["certificate"] = map[string]any{
			"domains": c.Domains, "not_after": c.NotAfter, "staging": c.Staging,
			"updated_at": c.UpdatedAt, "days_left": int(time.Until(c.NotAfter).Hours() / 24),
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// putCloudflareToken stores the token after checking it, so a wrong or
// under-privileged token is rejected at the moment it is entered rather than
// during an issuance three weeks later.
func (s *Server) putCloudflareToken(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		return badRequest("вставьте токен Cloudflare")
	}
	ctx := r.Context()
	cf := acmedns.NewCloudflare(req.Token)
	vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := cf.Verify(vctx); err != nil {
		return badRequest("%s", err.Error())
	}
	// Zone access is checked too: Zone:Read is what turns a domain name into the
	// zone id, and its absence is the usual reason DNS-01 fails.
	domains := s.resolverDomains(ctx)
	if len(domains) > 0 {
		if _, _, err := cf.ZoneID(vctx, strings.TrimPrefix(domains[0], "*.")); err != nil {
			return badRequest("%s", err.Error())
		}
	}
	if err := s.putSecret(ctx, secretCFToken, req.Token); err != nil {
		return err
	}
	s.audit(ctx, r, "certificates.token.saved", "secret", secretCFToken, nil, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	return nil
}

// acmeAccountKey loads the panel's ACME account key, creating it once.
func (s *Server) acmeAccountKey(ctx contextT) (*ecdsa.PrivateKey, error) {
	if pemStr, err := s.getSecret(ctx, secretACMEKey); err == nil && pemStr != "" {
		if block, _ := pem.Decode([]byte(pemStr)); block != nil {
			return x509.ParseECPrivateKey(block.Bytes)
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
	if err := s.putSecret(ctx, secretACMEKey,
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))); err != nil {
		return nil, err
	}
	return k, nil
}

func (s *Server) issueResolverCert(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Email   string `json:"email"`
		Staging bool   `json:"staging"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	res, err := s.runIssue(ctx, req.Email, req.Staging)
	if err != nil {
		s.event(ctx, "warn", "panel", "cert_failed", "Не удалось выпустить сертификат резолвера: "+err.Error(), nil, nil)
		return badRequest("%s", err.Error())
	}
	pushed := s.distributeCert(ctx, res.CertPEM, res.KeyPEM)
	s.audit(ctx, r, "certificates.issued", "certificate", resolverCertName, nil,
		map[string]any{"domains": res.Domains, "staging": req.Staging, "pushed": pushed})
	s.event(ctx, "info", "panel", "cert_issued",
		"Сертификат резолвера выпущен до "+res.NotAfter.Format("2006-01-02"), nil, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"domains": res.Domains, "not_after": res.NotAfter, "nodes_updated": pushed,
	})
	return nil
}

// runIssue obtains the certificate and stores it. Kept separate from the handler
// so the renewal loop can call exactly the same path.
func (s *Server) runIssue(ctx contextT, email string, staging bool) (*acmedns.Result, error) {
	domains := s.resolverDomains(ctx)
	if len(domains) == 0 {
		return nil, errNoResolverName
	}
	token, err := s.getSecret(ctx, secretCFToken)
	if err != nil || token == "" {
		return nil, errNoCFToken
	}
	key, err := s.acmeAccountKey(ctx)
	if err != nil {
		return nil, err
	}
	ictx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	res, err := acmedns.Issue(ictx, key, acmedns.NewCloudflare(token), domains, email, staging)
	if err != nil {
		return nil, err
	}
	if _, err := s.DB.Exec(ctx, `
		INSERT INTO certificates (name, domains, cert_pem, key_pem, not_after, staging, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,now())
		ON CONFLICT (name) DO UPDATE SET domains=EXCLUDED.domains, cert_pem=EXCLUDED.cert_pem,
			key_pem=EXCLUDED.key_pem, not_after=EXCLUDED.not_after, staging=EXCLUDED.staging,
			updated_at=now()`,
		resolverCertName, res.Domains, string(res.CertPEM), string(res.KeyPEM), res.NotAfter, staging); err != nil {
		return nil, err
	}
	return res, nil
}

// distributeCert hands the certificate to every ingress node. One wildcard
// serves the whole fleet, so this replaces per-node HTTP-01 issuance and the
// open :80 it required.
func (s *Server) distributeCert(ctx contextT, certPEM, keyPEM []byte) int {
	nodes, err := store.Many[pollRow](ctx, s.DB, `
		SELECT n.id::text, n.name, n.mgmt_address, i.fingerprint, n.desired_revision_id::text, n.role
		FROM nodes n JOIN node_identities i ON i.node_id = n.id
		WHERE n.role='ingress' AND n.mgmt_address <> '' AND i.revoked_at IS NULL AND n.status <> 'disabled'`)
	if err != nil {
		return 0
	}
	ok := 0
	for _, n := range nodes {
		t := pusher.Target{NodeID: n.ID, Name: n.Name, MgmtAddress: n.Mgmt, NodeCertFP: n.FP}
		if err := s.Cfg.Pusher.PushCert(ctx, t, certPEM, keyPEM); err != nil {
			slog.Warn("cannot install certificate", "node", n.Name, "err", err)
			continue
		}
		ok++
	}
	return ok
}

// RenewCerts keeps the wildcard fresh. Renewal lives in one place on a schedule
// rather than on every node, which is the other reason issuance moved here.
func (s *Server) RenewCerts(ctx context.Context, every time.Duration) {
	if s.Cfg.Pusher == nil {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.renewOnce(ctx)
		}
	}
}

func (s *Server) renewOnce(ctx context.Context) {
	type row struct {
		NotAfter time.Time `db:"not_after"`
		Staging  bool      `db:"staging"`
	}
	c, err := store.One[row](ctx, s.DB,
		`SELECT not_after, staging FROM certificates WHERE name=$1`, resolverCertName)
	if err != nil {
		return // ещё не выпускали — обновлять нечего
	}
	if time.Until(c.NotAfter) > 30*24*time.Hour {
		return
	}
	slog.Info("renewing resolver certificate", "not_after", c.NotAfter)
	res, err := s.runIssue(ctx, "", c.Staging)
	if err != nil {
		slog.Warn("renewal failed", "err", err)
		s.event(ctx, "warn", "panel", "cert_renew_failed", "Не удалось продлить сертификат: "+err.Error(), nil, nil)
		return
	}
	n := s.distributeCert(ctx, res.CertPEM, res.KeyPEM)
	s.event(ctx, "info", "panel", "cert_renewed",
		"Сертификат продлён до "+res.NotAfter.Format("2006-01-02"), nil, nil)
	slog.Info("resolver certificate renewed", "not_after", res.NotAfter, "nodes", n)
}

var (
	errNoResolverName = &APIError{Code: "no_resolver_name", Message: "Сначала задайте имя DoH/DoT в Настройках", status: http.StatusBadRequest}
	errNoCFToken      = &APIError{Code: "no_cf_token", Message: "Сначала сохраните токен Cloudflare", status: http.StatusBadRequest}
)
