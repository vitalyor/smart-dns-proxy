package dnsfe

import (
	"bufio"
	"net"
	"strconv"
	"strings"
)

// WrapProxyProto makes a listener transparently accept an optional PROXY
// protocol v1 header. The sni-proxy prepends it when forwarding DoH so the real
// client IP survives the internal hop; direct clients send no header and are
// passed through untouched.
//
// ponytail: assumes the first ~6 bytes (PROXY header or TLS ClientHello) arrive
// promptly, which holds for both paths; a client that opens a socket and sends
// nothing simply reads as a direct connection.
func WrapProxyProto(l net.Listener) net.Listener { return &ppListener{l} }

type ppListener struct{ net.Listener }

func (l *ppListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return newPPConn(c), nil
}

type ppConn struct {
	net.Conn
	r    *bufio.Reader
	peer net.Addr
}

func newPPConn(c net.Conn) *ppConn {
	pc := &ppConn{Conn: c, r: bufio.NewReader(c), peer: c.RemoteAddr()}
	if hdr, _ := pc.r.Peek(6); string(hdr) == "PROXY " {
		if line, err := pc.r.ReadString('\n'); err == nil {
			// PROXY TCP4|TCP6 srcIP dstIP srcPort dstPort
			f := strings.Fields(strings.TrimSpace(line))
			if len(f) >= 6 {
				if ip := net.ParseIP(f[2]); ip != nil {
					port, _ := strconv.Atoi(f[4])
					pc.peer = &net.TCPAddr{IP: ip, Port: port}
				}
			}
		}
	}
	return pc
}

func (c *ppConn) Read(b []byte) (int, error) { return c.r.Read(b) }
func (c *ppConn) RemoteAddr() net.Addr       { return c.peer }

// ProxyHeaderV1 builds a PROXY protocol v1 header line for src→dst, or "" if the
// addresses are not usable TCP addresses.
func ProxyHeaderV1(src, dst net.Addr) string {
	s, ok1 := src.(*net.TCPAddr)
	d, ok2 := dst.(*net.TCPAddr)
	if !ok1 || !ok2 || s.IP == nil || d.IP == nil {
		return ""
	}
	fam := "TCP4"
	if s.IP.To4() == nil {
		fam = "TCP6"
	}
	return "PROXY " + fam + " " + s.IP.String() + " " + d.IP.String() + " " +
		strconv.Itoa(s.Port) + " " + strconv.Itoa(d.Port) + "\r\n"
}
