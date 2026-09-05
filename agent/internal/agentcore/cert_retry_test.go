package agentcore

import (
	"errors"
	"testing"
)

func TestIsRetryableACME(t *testing.T) {
	cases := map[string]bool{
		"finalize: 404 urn:ietf:params:acme:error:malformed: Certificate not found": true,
		"wait order: context deadline exceeded":                                     true,
		"finalize: connection reset by peer":                                        true,
		// A hard validation failure must NOT retry — it would burn LE's per-hour budget.
		"domain validation failed (is :80 reachable and the DNS A-record pointed here?): NXDOMAIN": false,
		"no http-01 challenge offered for dns.example.com":                                         false,
		"acme register: some account problem":                                                      false,
	}
	for msg, want := range cases {
		if got := isRetryableACME(errors.New(msg)); got != want {
			t.Errorf("isRetryableACME(%q) = %v, want %v", msg, got, want)
		}
	}
}
