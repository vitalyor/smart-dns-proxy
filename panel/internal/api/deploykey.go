package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Per-node deploy keys. When the panel is configured with a GitHub token, every
// node gets its OWN read-only SSH key: the panel generates the pair, registers
// the public half on the repository via the REST API (no `gh` CLI, so the panel
// can run anywhere), and bakes the private half into the one-time install
// command. Deleting the node revokes the key. The privileged GitHub token never
// leaves the panel; nodes only ever hold a read-only key scoped to this one repo.

var githubClient = &http.Client{Timeout: 15 * time.Second}

// genDeployKey creates a fresh ed25519 keypair: the private key in OpenSSH PEM
// form, the public key in authorized_keys form.
func genDeployKey(comment string) (privPEM []byte, pubAuthorized string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	blk, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, "", err
	}
	return pem.EncodeToMemory(blk), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))), nil
}

// provisionDeployKey mints and registers a read-only deploy key for a node.
// Returns ok=false when GitHub is not configured or registration fails, so the
// caller falls back to the manual install command without failing node creation.
func (s *Server) provisionDeployKey(ctx context.Context, nodeName string) (privPEM string, keyID int64, ok bool) {
	if s.Cfg.GitHubToken == "" || s.Cfg.GitHubRepo == "" {
		return "", 0, false
	}
	title := "smartdns-" + nodeName
	priv, pub, err := genDeployKey(title)
	if err != nil {
		slog.Error("deploy key: keygen failed", "err", err, "node", nodeName)
		return "", 0, false
	}
	id, err := githubAddDeployKey(ctx, s.Cfg.GitHubToken, s.Cfg.GitHubRepo, title, pub)
	if err != nil {
		slog.Error("deploy key: registration failed — falling back to manual install", "err", err, "node", nodeName)
		return "", 0, false
	}
	slog.Info("deploy key registered", "node", nodeName, "key_id", id)
	return string(priv), id, true
}

// deployInstallCommand is the self-contained command shown in the UI: write the
// read-only key, clone the private repo with it, then run the installer.
//
// The whole body runs inside `sudo bash -s <<'…'`, i.e. a child shell, so the
// `set -e` and `cd` inside can never touch the operator's interactive session —
// pasting the command straight into a terminal is safe. Prompts (Docker, sudo)
// still work because the installer reads them from /dev/tty, not stdin.
func deployInstallCommand(repo, role, bundle, privPEM string) string {
	return fmt.Sprintf(`sudo bash -s <<'SMARTDNS_INSTALL'
set -e
umask 077
install -d -m 700 /etc/smartdns
cat > /etc/smartdns/deploy_key <<'SMARTDNS_KEY'
%sSMARTDNS_KEY
chmod 600 /etc/smartdns/deploy_key
GIT_SSH_COMMAND='ssh -i /etc/smartdns/deploy_key -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new' \
  git clone --depth 1 git@github.com:%s.git /opt/smartdns-src
cd /opt/smartdns-src
bash install.sh --role %s --bundle %s
SMARTDNS_INSTALL`, privPEM, repo, role, bundle)
}

func githubAddDeployKey(ctx context.Context, token, repo, title, pubKey string) (int64, error) {
	body, _ := json.Marshal(map[string]any{"title": title, "key": pubKey, "read_only": true})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.github.com/repos/"+repo+"/keys", bytes.NewReader(body))
	githubHeaders(req, token)
	res, err := githubClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return 0, fmt.Errorf("github deploy key %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// githubDeleteDeployKey revokes a key. A missing key counts as already gone.
func githubDeleteDeployKey(ctx context.Context, token, repo string, id int64) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete,
		fmt.Sprintf("https://api.github.com/repos/%s/keys/%d", repo, id), nil)
	githubHeaders(req, token)
	res, err := githubClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("github delete key %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func githubHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
}
