// Package acmedns issues certificates through ACME DNS-01 with Cloudflare.
//
// DNS-01 is the only way to get a wildcard, and a wildcard is what lets Android
// use a personal DoT address (<token>.dns.example): Private DNS accepts a
// hostname and nothing else, so the token has to ride in the name.
//
// The Cloudflare token lives in the panel, never on a node: it can rewrite the
// whole zone, and a node is a public server in the most exposed position we have
// (ADR 0012).
package acmedns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const cfAPI = "https://api.cloudflare.com/client/v4"

// Cloudflare is the DNS-01 provider.
type Cloudflare struct {
	Token string
	HTTP  *http.Client
}

func NewCloudflare(token string) *Cloudflare {
	return &Cloudflare{Token: token, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

type cfEnvelope struct {
	Success bool              `json:"success"`
	Errors  []cfError         `json:"errors"`
	Result  json.RawMessage   `json:"result"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e cfError) String() string { return fmt.Sprintf("%d %s", e.Code, e.Message) }

func (c *Cloudflare) call(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, cfAPI+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env cfEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("Cloudflare вернул неожиданный ответ (HTTP %d)", resp.StatusCode)
	}
	if !env.Success {
		if len(env.Errors) > 0 {
			return fmt.Errorf("Cloudflare: %s", env.Errors[0])
		}
		return fmt.Errorf("Cloudflare отклонил запрос (HTTP %d)", resp.StatusCode)
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// Verify checks the token can actually be used. It deliberately does NOT rely on
// /user/tokens/verify: account-scoped tokens (the newer cfat_ format) return
// success:false there while still being perfectly usable for zone operations.
// Listing zones is the real capability we need, so that is what we test.
func (c *Cloudflare) Verify(ctx context.Context) error {
	var zones []struct {
		ID string `json:"id"`
	}
	if err := c.call(ctx, http.MethodGet, "/zones?per_page=1", nil, &zones); err != nil {
		return err
	}
	return nil
}

// ZoneID finds the zone that owns a name by walking up its labels, so callers
// can pass dns.example.com without knowing the zone is example.com.
func (c *Cloudflare) ZoneID(ctx context.Context, name string) (string, string, error) {
	labels := strings.Split(strings.Trim(name, "."), ".")
	for i := 0; i+1 < len(labels); i++ {
		cand := strings.Join(labels[i:], ".")
		var zones []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := c.call(ctx, http.MethodGet, "/zones?name="+cand, nil, &zones); err != nil {
			return "", "", err
		}
		if len(zones) == 1 {
			return zones[0].ID, zones[0].Name, nil
		}
	}
	return "", "", fmt.Errorf("в Cloudflare нет зоны для %s (проверьте, что домен добавлен и токену выдан доступ к нему)", name)
}

// AddTXT creates a record. It never overwrites: a wildcard order and its bare
// name produce two different challenge values at the same record name, and both
// must be present at once.
func (c *Cloudflare) AddTXT(ctx context.Context, zoneID, name, value string) (string, error) {
	var res struct {
		ID string `json:"id"`
	}
	body := map[string]any{"type": "TXT", "name": name, "content": value, "ttl": 60}
	if err := c.call(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", body, &res); err != nil {
		return "", err
	}
	return res.ID, nil
}

// DeleteTXT removes a challenge record. Cleanup is best-effort: a leftover TXT
// is harmless, and failing the issuance over it would be worse.
func (c *Cloudflare) DeleteTXT(ctx context.Context, zoneID, recordID string) error {
	return c.call(ctx, http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+recordID, nil, nil)
}
