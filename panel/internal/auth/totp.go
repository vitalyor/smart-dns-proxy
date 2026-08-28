package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// NewTOTPSecret returns a base32 secret suitable for authenticator apps.
func NewTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(b), "=")
}

// TOTPURI builds the otpauth:// provisioning URI for a QR code.
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// VerifyTOTP checks a 6-digit code, accepting one step of clock skew each way.
func VerifyTOTP(secret, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return false
	}
	step := time.Now().Unix() / 30
	for _, s := range []int64{step - 1, step, step + 1} {
		if ConstantTimeEqualString(totpAt(key, s), code) {
			return true
		}
	}
	return false
}

func totpAt(key []byte, counter int64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter))
	m := hmac.New(sha1.New, key)
	m.Write(buf[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000)
}

// NewRecoveryCodes returns n single-use codes plus their stored hashes.
func NewRecoveryCodes(n int) (plain []string, hashes []string) {
	for i := 0; i < n; i++ {
		c := RandomToken(8)
		plain = append(plain, c)
		hashes = append(hashes, HashToken(c))
	}
	return
}
