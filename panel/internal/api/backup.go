package api

import (
	"archive/tar"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Backup and restore of the whole control plane from the UI. The artifact is
// byte-for-byte the same shape scripts/backup.sh and the automatic backup
// container produce — a tar holding db-<ts>.dump (pg_dump -Fc) plus
// panelstate-<ts>.tar (CA, signing key, panel client cert) — so a file made
// here restores with the CLI and vice versa. Restore replaces everything and
// restarts the process so the moved panel comes up with the same identity and
// every node still trusts it. Owner-only; the artifact contains private keys.

// handleBackup streams a fresh backup as a download. Optional {"passphrase"}
// encrypts it exactly like the CLI (openssl AES-256-CBC, PBKDF2, 200k iters).
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Passphrase string `json:"passphrase"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // body is optional

	ctx := r.Context()
	ts := time.Now().UTC().Format("20060102T150405Z")
	tmp, err := os.MkdirTemp("", "sdbackup")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	dbName := "db-" + ts + ".dump"
	if out, err := runCmd(ctx, "", "pg_dump", "-Fc", "-d", s.Cfg.DatabaseURL, "-f", filepath.Join(tmp, dbName)); err != nil {
		slog.Error("backup pg_dump failed", "err", err, "out", out)
		return errorf(http.StatusInternalServerError, "backup_failed", "не удалось сделать дамп базы")
	}

	stateName := "panelstate-" + ts + ".tar"
	if err := tarDir(filepath.Join(tmp, stateName), s.Cfg.StateDir); err != nil {
		slog.Error("backup state tar failed", "err", err)
		return errorf(http.StatusInternalServerError, "backup_failed", "не удалось упаковать ключи панели")
	}

	combined := filepath.Join(tmp, "smartdns-"+ts+".tar")
	if err := tarFiles(combined, tmp, dbName, stateName); err != nil {
		return err
	}

	out, name := combined, "smartdns-"+ts+".tar"
	if body.Passphrase != "" {
		enc := combined + ".enc"
		c := exec.CommandContext(ctx, "openssl", "enc", "-aes-256-cbc", "-pbkdf2", "-iter", "200000",
			"-salt", "-in", combined, "-out", enc, "-pass", "env:SDBK_PASS")
		c.Env = append(os.Environ(), "SDBK_PASS="+body.Passphrase)
		if b, err := c.CombinedOutput(); err != nil {
			slog.Error("backup encrypt failed", "err", err, "out", string(b))
			return errorf(http.StatusInternalServerError, "backup_failed", "не удалось зашифровать копию")
		}
		out, name = enc, name+".enc"
	}

	f, err := os.Open(out)
	if err != nil {
		return err
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if st != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	}
	_, _ = io.Copy(w, f)
	s.audit(ctx, r, "backup.created", "panel", "backup", nil, map[string]any{"encrypted": body.Passphrase != ""})
	return nil
}

// handleRestore accepts an uploaded backup (multipart: file + optional
// passphrase), replaces the database and key material, then restarts so the
// process reloads the restored CA and certificates.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseMultipartForm(128 << 20); err != nil {
		return badRequest("не удалось прочитать загрузку")
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		return badRequest("прикрепите файл резервной копии")
	}
	defer file.Close()
	pass := r.FormValue("passphrase")

	ctx := r.Context()
	tmp, err := os.MkdirTemp("", "sdrestore")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	upload := filepath.Join(tmp, "upload")
	dst, err := os.Create(upload)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, io.LimitReader(file, 512<<20)); err != nil {
		dst.Close()
		return err
	}
	dst.Close()

	combined := upload
	if strings.HasSuffix(hdr.Filename, ".enc") || isEncrypted(upload) {
		if pass == "" {
			return badRequest("файл зашифрован — укажите пароль резервной копии")
		}
		dec := filepath.Join(tmp, "decrypted.tar")
		c := exec.CommandContext(ctx, "openssl", "enc", "-d", "-aes-256-cbc", "-pbkdf2", "-iter", "200000",
			"-in", upload, "-out", dec, "-pass", "env:SDBK_PASS")
		c.Env = append(os.Environ(), "SDBK_PASS="+pass)
		if b, err := c.CombinedOutput(); err != nil {
			slog.Warn("restore decrypt failed", "out", string(b))
			return badRequest("не удалось расшифровать — неверный пароль?")
		}
		combined = dec
	}

	extractDir := filepath.Join(tmp, "x")
	if err := os.MkdirAll(extractDir, 0o700); err != nil {
		return err
	}
	if err := untar(combined, extractDir); err != nil {
		return badRequest("файл не похож на резервную копию SmartDNS")
	}
	dbDump := firstMatch(extractDir, "db-*.dump")
	stateTar := firstMatch(extractDir, "panelstate-*.tar")
	if dbDump == "" || stateTar == "" {
		return badRequest("в копии нет дампа базы или ключей панели")
	}

	// Restore the database. --clean --if-exists drops and recreates every object;
	// the process restart right after rebuilds the connection pool.
	// ponytail: pg_restore prints warnings to stderr and can exit non-zero on
	// benign notices; we log the output and rely on the restart + boot migrations.
	if out, err := runCmd(ctx, "", "pg_restore", "--clean", "--if-exists", "--no-owner",
		"-d", s.Cfg.DatabaseURL, dbDump); err != nil {
		slog.Error("restore pg_restore failed", "err", err, "out", out)
		return errorf(http.StatusInternalServerError, "restore_failed", "не удалось восстановить базу")
	}

	// Replace key material in place (never remove the mounted directory itself).
	if err := clearDir(s.Cfg.StateDir); err != nil {
		return err
	}
	if err := untar(stateTar, s.Cfg.StateDir); err != nil {
		slog.Error("restore state untar failed", "err", err)
		return errorf(http.StatusInternalServerError, "restore_failed", "не удалось восстановить ключи панели")
	}

	s.audit(ctx, r, "backup.restored", "panel", "restore", nil, map[string]any{"file": hdr.Filename})
	slog.Warn("control plane restored from backup — restarting to reload identity")
	writeJSON(w, http.StatusOK, map[string]any{"status": "restored", "restart": true})

	// Restart after the response is flushed. SIGTERM triggers the graceful
	// shutdown in main; Docker's restart policy brings the process back, and it
	// boots from the restored database and key material.
	go func() {
		time.Sleep(800 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
	return nil
}

// --- helpers ----------------------------------------------------------------

func runCmd(ctx context.Context, dir, name string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		c.Dir = dir
	}
	b, err := c.CombinedOutput()
	return string(b), err
}

// isEncrypted reports whether a file carries OpenSSL's salted magic.
func isEncrypted(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8)
	n, _ := io.ReadFull(f, buf)
	return n == 8 && string(buf) == "Salted__"
}

func firstMatch(dir, pattern string) string {
	m, _ := filepath.Glob(filepath.Join(dir, pattern))
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

// tarDir packs every regular file directly under dir (panelstate is flat).
func tarDir(dst, dir string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := tarAddFile(tw, filepath.Join(dir, e.Name()), e.Name()); err != nil {
			return err
		}
	}
	return nil
}

// tarFiles packs the named files from base into a single archive.
func tarFiles(dst, base string, names ...string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()
	for _, n := range names {
		if err := tarAddFile(tw, filepath.Join(base, n), n); err != nil {
			return err
		}
	}
	return nil
}

func tarAddFile(tw *tar.Writer, path, name string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(st, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	_, err = io.Copy(tw, src)
	return err
}

// untar extracts regular files into dir, refusing paths that escape it.
func untar(src, dir string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(hdr.Name) // flatten; both archives are flat
		if name == "." || name == ".." || name == "" {
			continue
		}
		out := filepath.Join(dir, name)
		w, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, tr); err != nil {
			w.Close()
			return err
		}
		w.Close()
	}
}

// clearDir removes the contents of dir but keeps the directory (a mount point).
func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
