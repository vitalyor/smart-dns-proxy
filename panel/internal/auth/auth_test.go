package auth

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery", DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("correct horse battery", h) {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword("wrong password here", h) {
		t.Fatal("wrong password accepted")
	}
	if VerifyPassword("x", "not-a-hash") {
		t.Fatal("garbage hash accepted")
	}
}

func TestShortPasswordRejected(t *testing.T) {
	if _, err := HashPassword("short", DefaultParams); err == nil {
		t.Fatal("short password must be rejected")
	}
}

func TestTOTP(t *testing.T) {
	s := NewTOTPSecret()
	if VerifyTOTP(s, "000000") && VerifyTOTP(s, "111111") {
		t.Fatal("TOTP accepts everything")
	}
	if !VerifyTOTP(s, currentCode(t, s)) {
		t.Fatal("valid TOTP code rejected")
	}
}

func currentCode(t *testing.T, secret string) string {
	t.Helper()
	for _, c := range []string{} {
		_ = c
	}
	// Recompute using the same primitive the verifier uses.
	key := decode(t, secret)
	return totpAt(key, nowStep())
}
