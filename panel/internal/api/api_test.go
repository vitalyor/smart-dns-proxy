package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRedactRemovesSecrets(t *testing.T) {
	in := map[string]any{
		"email":    "user@example.net",
		"password": "hunter2hunter2",
		"nested":   map[string]any{"token": "abc", "keep": 1},
		"list":     []any{map[string]any{"secret": "s3cr3t"}},
	}
	out, _ := json.Marshal(redact(in))
	s := string(out)
	for _, leaked := range []string{"hunter2hunter2", "abc", "s3cr3t"} {
		if hasSub(s, leaked) {
			t.Fatalf("secret %q survived redaction: %s", leaked, s)
		}
	}
	if !hasSub(s, "user@example.net") {
		t.Fatal("non-secret fields must be preserved for the audit trail")
	}
}

func hasSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestRoleRanking(t *testing.T) {
	cases := []struct {
		have, need string
		want       bool
	}{
		{"owner", "operator", true},
		{"operator", "operator", true},
		{"viewer", "operator", false},
		{"operator", "owner", false},
		{"", "viewer", false},
	}
	for _, c := range cases {
		if got := roleAtLeast(c.have, c.need); got != c.want {
			t.Fatalf("roleAtLeast(%q,%q) = %v, want %v", c.have, c.need, got, c.want)
		}
	}
}

func TestSecurityHeadersAndCORSDenial(t *testing.T) {
	h := securityHeaders(true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://panel.local/api/v1/nodes", nil))
	for _, k := range []string{"Content-Security-Policy", "X-Frame-Options", "Strict-Transport-Security"} {
		if rec.Header().Get(k) == "" {
			t.Fatalf("%s header missing", k)
		}
	}

	rec = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://panel.local/api/v1/nodes", nil)
	r.Header.Set("Origin", "https://evil.example")
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin request should be denied, got %d", rec.Code)
	}
}

func TestIfMatchParsing(t *testing.T) {
	r := httptest.NewRequest("PATCH", "/x", nil)
	if v, err := ifMatch(r); v != 0 || err != nil {
		t.Fatal("absent If-Match means no optimistic locking")
	}
	r.Header.Set("If-Match", `"7"`)
	if v, _ := ifMatch(r); v != 7 {
		t.Fatal("quoted version should parse")
	}
	r.Header.Set("If-Match", "not-a-number")
	if _, err := ifMatch(r); err == nil {
		t.Fatal("garbage If-Match must be rejected")
	}
	if err := checkVersion(0, 7); err == nil {
		t.Fatal("a stale version must produce a precondition failure")
	}
	if err := checkVersion(1, 7); err != nil {
		t.Fatal("a matching version must succeed")
	}
}

func TestSecretEncryptionRoundTrip(t *testing.T) {
	SetSecretKey(DeriveSecretKey("unit-test-key-material"))
	s := &Server{}
	enc, err := s.encryptSecret("TOTPSECRET")
	if err != nil {
		t.Fatal(err)
	}
	if hasSub(string(enc), "TOTPSECRET") {
		t.Fatal("ciphertext must not contain the plaintext")
	}
	got, err := s.decryptSecret(enc)
	if err != nil || got != "TOTPSECRET" {
		t.Fatalf("round trip failed: %q %v", got, err)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Gemini":         "gemini",
		"ChatGPT Plus!":  "chatgpt-plus",
		"  Claude  AI  ": "claude-ai",
		"Джемини":        "dzhemini", // Cyrillic transliterated, not dropped
		"Мой Сервис":     "moi-servis",
		"2ip.io":         "2ip-io",
		"!!!":            "", // nothing latinizable → empty, caller rejects
	} {
		if got := slugify(in); got != want {
			t.Fatalf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
	// Long Cyrillic name must stay within slugRe's 40-char cap and still match.
	long := slugify("Очень Длинное Название Сервиса Для Проверки Обрезки")
	if len(long) > 40 || !slugRe.MatchString(long) {
		t.Fatalf("capped slug invalid: %d %q", len(long), long)
	}
}
