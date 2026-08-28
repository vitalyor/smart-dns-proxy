package api

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"
)

// The tar pack/unpack path carries the panel's key material during a move, so
// it round-trips exactly and never writes outside the target directory.
func TestBackupTarRoundTrip(t *testing.T) {
	src := t.TempDir()
	files := map[string]string{"ca.crt": "CERT", "ca.key": "KEY", "manifest-signing.key": "SIGN"}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(src, n), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tarball := filepath.Join(t.TempDir(), "panelstate.tar")
	if err := tarDir(tarball, src); err != nil {
		t.Fatalf("tarDir: %v", err)
	}
	dst := t.TempDir()
	if err := untar(tarball, dst); err != nil {
		t.Fatalf("untar: %v", err)
	}
	for n, want := range files {
		got, err := os.ReadFile(filepath.Join(dst, n))
		if err != nil {
			t.Fatalf("missing %s: %v", n, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", n, got, want)
		}
	}

	// clearDir empties but keeps the mount point.
	if err := clearDir(dst); err != nil {
		t.Fatalf("clearDir: %v", err)
	}
	if e, _ := os.ReadDir(dst); len(e) != 0 {
		t.Fatalf("clearDir left %d entries", len(e))
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("clearDir removed the directory itself: %v", err)
	}
}

// A malicious archive must not escape the destination directory.
func TestUntarRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	arch := filepath.Join(dir, "evil.tar")
	f, err := os.Create(arch)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	payload := []byte("pwned")
	_ = tw.WriteHeader(&tar.Header{Name: "../../escape", Typeflag: tar.TypeReg, Size: int64(len(payload)), Mode: 0o600})
	_, _ = tw.Write(payload)
	tw.Close()
	f.Close()

	out := t.TempDir()
	if err := untar(arch, out); err != nil {
		t.Fatalf("untar: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(out), "escape")); err == nil {
		t.Fatal("path traversal: file written outside target directory")
	}
	if _, err := os.Stat(filepath.Join(out, "escape")); err != nil {
		t.Fatalf("expected flattened file inside target: %v", err)
	}
}
