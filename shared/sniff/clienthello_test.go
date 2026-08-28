package sniff

import (
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"testing"
	"time"
)

func TestPeekSNIFromRealClientHello(t *testing.T) {
	c1, c2 := net.Pipe()
	go func() {
		_ = tls.Client(c1, &tls.Config{ServerName: "api.example.com"}).Handshake()
	}()
	_ = c2.SetReadDeadline(time.Now().Add(3 * time.Second))
	name, raw, err := PeekSNI(c2, 16384)
	if err != nil {
		t.Fatal(err)
	}
	if name != "api.example.com" {
		t.Fatalf("got %q", name)
	}
	if len(raw) < 5 || raw[0] != 0x16 {
		t.Fatal("raw bytes must be replayable")
	}
	c1.Close()
	c2.Close()
}

func TestPeekSNIRejectsNonTLS(t *testing.T) {
	if _, _, err := PeekSNI(bytes.NewReader([]byte("GET / HTTP/1.1\r\n\r\n")), 4096); err != ErrNotTLS {
		t.Fatalf("want ErrNotTLS, got %v", err)
	}
}

func TestPeekSNIIncomplete(t *testing.T) {
	if _, _, err := PeekSNI(io.LimitReader(bytes.NewReader([]byte{0x16, 0x03, 0x01}), 3), 4096); err != ErrIncomplete {
		t.Fatalf("want ErrIncomplete, got %v", err)
	}
}
