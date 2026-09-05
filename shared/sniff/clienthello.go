// Package sniff extracts the SNI from a TLS ClientHello without terminating
// TLS. The bytes are forwarded untouched, so the client keeps its original
// end-to-end TLS session with the real origin.
package sniff

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

var (
	ErrNotTLS     = errors.New("not a TLS ClientHello")
	ErrNoSNI      = errors.New("no SNI extension")
	ErrTooLarge   = errors.New("ClientHello exceeds pre-read limit")
	ErrIncomplete = errors.New("incomplete ClientHello")
)

// maxRecords bounds how many TLS records one ClientHello may be spread over.
// A handshake needs one or two; anything beyond that is a peer dripping bytes,
// and the read deadline the caller sets is the other half of that defence.
const maxRecords = 8

// PeekSNI reads the ClientHello from r into a buffer, returns the server name
// and the consumed bytes so the caller can replay them.
//
// The handshake may be split across several TLS records — RFC 8446 §5.1 allows
// it, and DPI-evasion tools (zapret, GoodbyeDPI и подобные) split it on purpose.
// Reading only the first record dropped exactly those clients, so records are
// concatenated until the handshake parses or the byte budget runs out.
func PeekSNI(r io.Reader, maxBytes int) (serverName string, raw []byte, err error) {
	if maxBytes <= 0 {
		maxBytes = 16 * 1024
	}
	var handshake []byte
	for i := 0; i < maxRecords; i++ {
		hdr := make([]byte, 5)
		if _, err = io.ReadFull(r, hdr); err != nil {
			return "", raw, ErrIncomplete
		}
		if hdr[0] != 0x16 || hdr[1] != 0x03 {
			return "", append(raw, hdr...), ErrNotTLS
		}
		length := int(binary.BigEndian.Uint16(hdr[3:5]))
		if length <= 0 || len(raw)+5+length > maxBytes {
			return "", append(raw, hdr...), ErrTooLarge
		}
		body := make([]byte, length)
		if _, err = io.ReadFull(r, body); err != nil {
			return "", append(raw, hdr...), ErrIncomplete
		}
		raw = append(append(raw, hdr...), body...)
		handshake = append(handshake, body...)

		name, perr := parseClientHello(handshake)
		if perr == ErrIncomplete {
			continue // ждём следующую запись: сообщение продолжается в ней
		}
		return name, raw, perr
	}
	return "", raw, ErrIncomplete
}

func parseClientHello(b []byte) (string, error) {
	// handshake header: type(1) length(3). Слишком мало байт — это «данных пока
	// не хватает», а не «не TLS»: остаток приедет следующей записью.
	if len(b) > 0 && b[0] != 0x01 {
		return "", ErrNotTLS
	}
	if len(b) < 4 {
		return "", ErrIncomplete
	}
	hl := int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	b = b[4:]
	if hl > len(b) {
		return "", ErrIncomplete
	}
	b = b[:hl]
	// version(2) random(32)
	if len(b) < 34 {
		return "", ErrIncomplete
	}
	b = b[34:]
	// session id
	var ok bool
	if b, ok = skipU8(b); !ok {
		return "", ErrIncomplete
	}
	// cipher suites
	if b, ok = skipU16(b); !ok {
		return "", ErrIncomplete
	}
	// compression methods
	if b, ok = skipU8(b); !ok {
		return "", ErrIncomplete
	}
	if len(b) < 2 {
		return "", ErrNoSNI
	}
	extLen := int(binary.BigEndian.Uint16(b))
	b = b[2:]
	if extLen > len(b) {
		return "", ErrIncomplete
	}
	b = b[:extLen]
	for len(b) >= 4 {
		typ := binary.BigEndian.Uint16(b)
		l := int(binary.BigEndian.Uint16(b[2:]))
		b = b[4:]
		if l > len(b) {
			return "", ErrIncomplete
		}
		if typ == 0x0000 { // server_name
			return parseSNIExtension(b[:l])
		}
		b = b[l:]
	}
	return "", ErrNoSNI
}

func parseSNIExtension(b []byte) (string, error) {
	if len(b) < 2 {
		return "", ErrIncomplete
	}
	listLen := int(binary.BigEndian.Uint16(b))
	b = b[2:]
	if listLen > len(b) {
		return "", ErrIncomplete
	}
	b = b[:listLen]
	for len(b) >= 3 {
		nameType := b[0]
		l := int(binary.BigEndian.Uint16(b[1:]))
		b = b[3:]
		if l > len(b) {
			return "", ErrIncomplete
		}
		if nameType == 0 {
			return strings.ToLower(string(b[:l])), nil
		}
		b = b[l:]
	}
	return "", ErrNoSNI
}

func skipU8(b []byte) ([]byte, bool) {
	if len(b) < 1 {
		return nil, false
	}
	n := int(b[0])
	if 1+n > len(b) {
		return nil, false
	}
	return b[1+n:], true
}

func skipU16(b []byte) ([]byte, bool) {
	if len(b) < 2 {
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(b))
	if 2+n > len(b) {
		return nil, false
	}
	return b[2+n:], true
}
