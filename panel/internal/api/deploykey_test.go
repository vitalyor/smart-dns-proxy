package api

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// The generated key must be a real, parseable OpenSSH keypair — a broken key
// would let a node fail to clone with no obvious cause.
func TestGenDeployKey(t *testing.T) {
	priv, pub, err := genDeployKey("smartdns-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParsePrivateKey(priv); err != nil {
		t.Fatalf("private key not parseable: %v", err)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub)); err != nil {
		t.Fatalf("public key not valid authorized_keys: %v", err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Fatalf("expected ssh-ed25519 public key, got %q", pub)
	}
}

func TestDeployInstallCommand(t *testing.T) {
	priv, _, err := genDeployKey("x")
	if err != nil {
		t.Fatal(err)
	}
	cmd := deployInstallCommand("vitalyor/smart-dns-proxy", "ingress", "BUNDLE123", string(priv))
	for _, want := range []string{
		"git clone --depth 1 git@github.com:vitalyor/smart-dns-proxy.git",
		"--role ingress --bundle BUNDLE123",
		"BEGIN OPENSSH PRIVATE KEY",
		"chmod 600 /etc/smartdns/deploy_key",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("install command missing %q", want)
		}
	}
	if !strings.Contains(cmd, "\nSMARTDNS_KEY\n") {
		t.Fatal("heredoc terminator must sit on its own line")
	}
	// The body must run in a child shell so set -e / cd cannot kill the operator's
	// session when pasted into a terminal.
	if !strings.HasPrefix(cmd, "sudo bash -s <<'SMARTDNS_INSTALL'\n") {
		t.Fatalf("command must be wrapped in a child shell, got prefix %.40q", cmd)
	}
	if !strings.HasSuffix(cmd, "\nSMARTDNS_INSTALL") {
		t.Fatal("command must close the wrapper heredoc")
	}
	if strings.HasPrefix(cmd, "set -e") {
		t.Fatal("set -e must live inside the child shell, not at the top level")
	}
}
