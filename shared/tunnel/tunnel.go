// Package tunnel implements the ingress→egress transport profile: TLS 1.3 with
// mutual certificate authentication issued by the panel CA, followed by a
// fixed-size CONNECT frame carrying the sniffed destination.
//
// Chosen over VLESS/Reality for v1 (see docs/adr/0001-transport.md): it is a
// single pinned, testable profile with no external data-plane binary, and the
// authorization decision stays inside our own allowlist code.
package tunnel

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	Version      byte = 1
	CmdConnect   byte = 1
	StatusOK     byte = 0
	StatusDenied byte = 1
	StatusDNS    byte = 2
	StatusDial   byte = 3
	StatusProto  byte = 4

	maxHostLen  = 253
	handshakeTO = 10 * time.Second
)

// ALPN is negotiated on the tunnel TLS session.
const ALPN = "smartdns/1"

var ErrProtocol = errors.New("tunnel protocol error")

// WriteConnect sends a CONNECT frame and waits for the status byte.
func WriteConnect(c net.Conn, host string, port int) error {
	if len(host) == 0 || len(host) > maxHostLen {
		return fmt.Errorf("%w: host length", ErrProtocol)
	}
	_ = c.SetDeadline(time.Now().Add(handshakeTO))
	defer c.SetDeadline(time.Time{})

	buf := make([]byte, 0, 5+len(host))
	buf = append(buf, Version, CmdConnect, byte(len(host)))
	buf = append(buf, host...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(port))
	if _, err := c.Write(buf); err != nil {
		return err
	}
	var st [1]byte
	if _, err := io.ReadFull(c, st[:]); err != nil {
		return err
	}
	if st[0] != StatusOK {
		return fmt.Errorf("egress refused: %s", StatusText(st[0]))
	}
	return nil
}

// ReadConnect parses a CONNECT frame on the egress side.
func ReadConnect(c net.Conn) (host string, port int, err error) {
	_ = c.SetDeadline(time.Now().Add(handshakeTO))
	defer c.SetDeadline(time.Time{})

	var hdr [3]byte
	if _, err = io.ReadFull(c, hdr[:]); err != nil {
		return "", 0, err
	}
	if hdr[0] != Version || hdr[1] != CmdConnect {
		return "", 0, ErrProtocol
	}
	n := int(hdr[2])
	if n == 0 || n > maxHostLen {
		return "", 0, ErrProtocol
	}
	b := make([]byte, n+2)
	if _, err = io.ReadFull(c, b); err != nil {
		return "", 0, err
	}
	return string(b[:n]), int(binary.BigEndian.Uint16(b[n:])), nil
}

// WriteStatus answers a CONNECT frame.
func WriteStatus(c net.Conn, status byte) error {
	_ = c.SetWriteDeadline(time.Now().Add(handshakeTO))
	defer c.SetWriteDeadline(time.Time{})
	_, err := c.Write([]byte{status})
	return err
}

// StatusText renders a status byte.
func StatusText(s byte) string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusDenied:
		return "destination not allowed"
	case StatusDNS:
		return "resolution failed or blocked"
	case StatusDial:
		return "dial failed"
	default:
		return "protocol error"
	}
}

// Splice copies bytes in both directions until either side closes.
// It never inspects or records the payload.
func Splice(a, b net.Conn, idle time.Duration) (aToB, bToA int64) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn, n *int64) {
		buf := make([]byte, 32*1024)
		for {
			if idle > 0 {
				_ = src.SetReadDeadline(time.Now().Add(idle))
			}
			nr, er := src.Read(buf)
			if nr > 0 {
				if idle > 0 {
					_ = dst.SetWriteDeadline(time.Now().Add(idle))
				}
				nw, ew := dst.Write(buf[:nr])
				*n += int64(nw)
				if ew != nil {
					break
				}
			}
			if er != nil {
				break
			}
		}
		if tc, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go cp(b, a, &aToB)
	go cp(a, b, &bToA)
	<-done
	<-done
	_ = a.Close()
	_ = b.Close()
	return
}
