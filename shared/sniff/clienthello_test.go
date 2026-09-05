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

// splitRecords перекладывает один TLS-record в несколько: ровно то, что делают
// обходчики DPI, разрезая ClientHello, чтобы он не совпал с сигнатурой.
func splitRecords(rec []byte, at int) []byte {
	body := rec[5:]
	if at <= 0 || at >= len(body) {
		return rec
	}
	out := []byte{0x16, rec[1], rec[2], byte(at >> 8), byte(at)}
	out = append(out, body[:at]...)
	rest := body[at:]
	out = append(out, 0x16, rec[1], rec[2], byte(len(rest)>>8), byte(len(rest)))
	return append(out, rest...)
}

func realClientHello(t *testing.T, host string) []byte {
	t.Helper()
	c1, c2 := net.Pipe()
	go func() { _ = tls.Client(c1, &tls.Config{ServerName: host}).Handshake() }()
	_ = c2.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, raw, err := PeekSNI(c2, 16384)
	if err != nil {
		t.Fatal(err)
	}
	c1.Close()
	c2.Close()
	return raw
}

func TestPeekSNIAcrossSplitRecords(t *testing.T) {
	rec := realClientHello(t, "api.example.com")
	// режем в нескольких точках: и в середине, и почти в самом начале
	for _, at := range []int{1, 5, 20, len(rec) / 2, len(rec) - 6} {
		split := splitRecords(rec, at)
		name, raw, err := PeekSNI(bytes.NewReader(split), 16384)
		if err != nil {
			t.Fatalf("разрез на %d: %v", at, err)
		}
		if name != "api.example.com" {
			t.Fatalf("разрез на %d: имя %q", at, name)
		}
		// повтор должен отдавать ровно те байты, что пришли с провода
		if !bytes.Equal(raw, split) {
			t.Fatalf("разрез на %d: raw не совпадает с прочитанным", at)
		}
	}
}

func TestPeekSNITruncatedSecondRecord(t *testing.T) {
	rec := realClientHello(t, "api.example.com")
	split := splitRecords(rec, len(rec)/2)
	// обрываем поток посреди второй записи
	if _, _, err := PeekSNI(bytes.NewReader(split[:len(rec)/2+8]), 16384); err != ErrIncomplete {
		t.Fatalf("want ErrIncomplete, got %v", err)
	}
}

func TestPeekSNIRespectsByteBudget(t *testing.T) {
	rec := realClientHello(t, "api.example.com")
	if _, _, err := PeekSNI(bytes.NewReader(rec), 32); err != ErrTooLarge {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}
