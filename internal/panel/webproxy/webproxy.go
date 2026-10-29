// Package webproxy is the panel's listener behind the node's shared 443: the node's SNI
// router hands it the TLS connections whose name is the panel's or the subscription
// domain's, with a PROXY protocol v2 header naming the client in front. The listener is on
// loopback alone, takes the header from a loopback peer only, and reports the client's
// address as the connection's, so the panel records the client and not 127.0.0.1.
package webproxy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"time"
)

// headerWait bounds the wait for the header: the node writes it at once, with the
// ClientHello after it.
const headerWait = 5 * time.Second

// maxHeader bounds the header's payload (addresses and TLVs): our router sends 12 or 36
// bytes, the protocol allows 65535, and nothing legitimate needs kilobytes.
const maxHeader = 512

var (
	// ErrNoHeader means the stream does not start with a PROXY protocol v2 signature.
	ErrNoHeader = errors.New("webproxy: no PROXY v2 header")
	// ErrMalformed means the header is there but not one this listener may go by.
	ErrMalformed = errors.New("webproxy: malformed PROXY v2 header")
)

// signature is the 12 bytes every v2 header starts with.
var signature = []byte("\r\n\r\n\x00\r\nQUIT\n")

// ReadV2 reads one PROXY protocol v2 header (PROXY command, TCP over IPv4 or IPv6) from r
// and returns the client's address. A version 1 line, a LOCAL command, another family
// (UNIX, UDP), a truncated or empty header are all refused: the only sender here is the
// node's router, and a connection it did not mark is not one it forwarded. r must have a
// read deadline: ReadV2 has none of its own.
func ReadV2(r io.Reader) (netip.AddrPort, error) {
	var sig [12]byte
	if _, err := io.ReadFull(r, sig[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return netip.AddrPort{}, fmt.Errorf("%w: %v", ErrNoHeader, err)
		}
		return netip.AddrPort{}, err
	}
	if string(sig[:]) != string(signature) {
		return netip.AddrPort{}, ErrNoHeader
	}
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return netip.AddrPort{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	// Version 2, the PROXY command: LOCAL carries no client address and is no forward.
	if hdr[0] != 0x21 {
		return netip.AddrPort{}, fmt.Errorf("%w: version and command %#x", ErrMalformed, hdr[0])
	}
	n := int(binary.BigEndian.Uint16(hdr[2:]))
	var need int
	switch hdr[1] {
	case 0x11: // TCP over IPv4
		need = 12
	case 0x21: // TCP over IPv6
		need = 36
	default:
		return netip.AddrPort{}, fmt.Errorf("%w: family and protocol %#x", ErrMalformed, hdr[1])
	}
	if n < need || n > maxHeader {
		return netip.AddrPort{}, fmt.Errorf("%w: payload of %d bytes", ErrMalformed, n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return netip.AddrPort{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	srcPort := binary.BigEndian.Uint16(body[need-4:])
	// The destination (the node's own address) is not used: only who the client is matters.
	var addr netip.Addr
	if need == 12 {
		addr = netip.AddrFrom4([4]byte(body[:4]))
	} else {
		addr = netip.AddrFrom16([16]byte(body[:16]))
	}
	return netip.AddrPortFrom(addr.Unmap(), srcPort), nil
}

// Listener accepts connections from loopback alone, each starting with a PROXY protocol v2
// header, and returns them with the client's address as the connection's own. A peer that
// is not loopback, or a header that is absent or no v2 PROXY one, is refused and closed.
type Listener struct {
	net.Listener
	log *slog.Logger
}

// Listen binds a loopback address, "127.0.0.1:0" for a port of the system's choosing.
func Listen(addr string, log *slog.Logger) (*Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Listener{Listener: ln, log: log}, nil
}

// Port is the port the listener holds.
func (l *Listener) Port() int {
	if t, ok := l.Addr().(*net.TCPAddr); ok {
		return t.Port
	}
	return 0
}

// Accept takes the next connection whose peer is loopback and whose header is a v2 PROXY
// one; anything else is closed and the next connection taken.
func (l *Listener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if src, ok := l.admit(c); ok {
			return &conn{Conn: c, src: src}, nil
		}
	}
}

// admit reads the header off c and says who the client is; false closes c.
func (l *Listener) admit(c net.Conn) (netip.AddrPort, bool) {
	peer, ok := addrPort(c.RemoteAddr())
	if !ok || !peer.Addr().IsLoopback() {
		// Only the node on this host forwards here. Anybody else on loopback could name a
		// client too — that is what PROXY is — but a peer from off the host never may.
		l.warn("web listener: a connection from off loopback", "peer", c.RemoteAddr())
		_ = c.Close()
		return netip.AddrPort{}, false
	}
	_ = c.SetReadDeadline(time.Now().Add(headerWait))
	src, err := ReadV2(c)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		l.debug("web listener: the connection carries no PROXY v2 header", "peer", c.RemoteAddr(), "err", err)
		_ = c.Close()
		return netip.AddrPort{}, false
	}
	return src, true
}

func (l *Listener) warn(msg string, args ...any) {
	if l.log != nil {
		l.log.Warn(msg, args...)
	}
}

func (l *Listener) debug(msg string, args ...any) {
	if l.log != nil {
		l.log.Debug(msg, args...)
	}
}

// conn is a forwarded connection: reads and writes as the socket's, its remote address the
// client's the PROXY header named.
type conn struct {
	net.Conn
	src netip.AddrPort
}

func (c *conn) RemoteAddr() net.Addr { return net.TCPAddrFromAddrPort(c.src) }

func addrPort(a net.Addr) (netip.AddrPort, bool) {
	if t, ok := a.(*net.TCPAddr); ok {
		return t.AddrPort(), true
	}
	ap, err := netip.ParseAddrPort(a.String())
	return ap, err == nil
}
