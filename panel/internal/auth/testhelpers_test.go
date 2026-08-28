package auth

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

func decode(t *testing.T, secret string) []byte {
	t.Helper()
	k, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func nowStep() int64 { return time.Now().Unix() / 30 }
