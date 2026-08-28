package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"io"
)

// SecretKey is the AES-256-GCM key used for at-rest encryption of TOTP secrets
// and source tokens. It is derived from PANEL_SECRET_KEY and never logged.
type SecretKey [32]byte

// DeriveSecretKey stretches an operator-provided key string.
func DeriveSecretKey(raw string) SecretKey {
	return SecretKey(sha256.Sum256([]byte("smartdns/secret-key/v1|" + raw)))
}

var secretKey SecretKey
var secretKeySet bool

// SetSecretKey installs the process-wide encryption key.
func SetSecretKey(k SecretKey) { secretKey, secretKeySet = k, true }

func (s *Server) encryptSecret(plain string) ([]byte, error) {
	if !secretKeySet {
		return nil, errNoSecretKey
	}
	block, err := aes.NewCipher(secretKey[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (s *Server) decryptSecret(b []byte) (string, error) {
	if !secretKeySet {
		return "", errNoSecretKey
	}
	block, err := aes.NewCipher(secretKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(b) < gcm.NonceSize() {
		return "", errNoSecretKey
	}
	out, err := gcm.Open(nil, b[:gcm.NonceSize()], b[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
