package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/component/sniffer"

	"mikan/internal/nodeapi"
)

// The SNI router: the node's TCP 443 shared by several inbounds (REALITY, VLESS and Trojan
// over TLS, AnyTLS), each listening on loopback with PROXY protocol. It reads the client's
// TLS ClientHello, picks the inbound by the name it asks for and hands the connection over
// with a PROXY v2 header carrying the client's address, so devices, limits and filters see
// the client and not 127.0.0.1. A name no route has goes to the default inbound (a REALITY
// one passes it to its target, as any stranger's). A web route (the panel's listener on
// loopback) gets the client the same way: with the header in front, so the panel sees the
// client's address too.

const (
	// helloWait bounds the wait for the ClientHello; helloMax what is read of it (a
	// record is 16 KiB, a ClientHello with post-quantum keys fits).
	helloWait = 5 * time.Second
	helloMax  = 16<<10 + 5
)

// sniRouter is the running router; nil while the node has none.
type sniRouter struct {
	ln     net.Listener
	log    *slog.Logger
	mu     sync.RWMutex
	routes map[string]string // name (lowercase) → backend address
	web    map[string]string // name (lowercase) → the panel's listener on loopback
	def    string            // backend for any other name; "": closed
	stub   map[string]bool   // names that get the front page instead ("": no name)
	front  *frontPage        // the node's front page; nil: none
	wg     sync.WaitGroup
	err    error
}

// nameOf is a ClientHello name as the maps keep it.
func nameOf(name string) string { return strings.ToLower(strings.TrimSuffix(name, ".")) }

// warn logs a route the router had to drop; a router built by a test may have no logger.
func (r *sniRouter) warn(msg string, args ...any) {
	if r.log != nil {
		r.log.Warn(msg, args...)
	}
}

// target is where a name goes: an inbound behind the router, or a web server on loopback
// (web=true, the panel and the subscriptions); otherwise the default inbound.
func (r *sniRouter) target(name string) (to string, web bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := strings.ToLower(strings.TrimSuffix(name, "."))
	if to, ok := r.routes[n]; ok {
		return to, false
	}
	if to, ok := r.web[n]; ok {
		return to, true
	}
	return r.def, false
}

// stubbed says whether name gets the front page: a name of the stub's, or with no default
// inbound any name no route has. The page must be on.
func (r *sniRouter) stubbed(name string) bool {
	if !r.front.on() {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := nameOf(name)
	if _, ok := r.routes[n]; ok {
		return false
	}
	if _, ok := r.web[n]; ok {
		return false
	}
	return r.stub[n] || r.def == "" && len(r.stub) > 0
}

// set replaces the routes: names to inbound ports on loopback, by the ports the inbounds run,
// and names to a web server on loopback (the panel's listener). A web name an inbound route
// already takes is no web route: the inbound wins, and the whole route is dropped with a log
// line rather than guessed at.
func (r *sniRouter) set(rt *nodeapi.SNIRouter, ports map[string]string) {
	routes := map[string]string{}
	for _, x := range rt.Routes {
		p, ok := ports[x.Inbound]
		if !ok {
			continue
		}
		for _, n := range x.Names {
			routes[nameOf(n)] = net.JoinHostPort("127.0.0.1", p)
		}
	}
	web := map[string]string{}
	for _, w := range rt.Web {
		if w.Port <= 0 || w.Port > 65535 {
			r.warn("sni router: web route with a bad port", "port", w.Port)
			continue
		}
		conflict := ""
		for _, n := range w.Names {
			if _, ok := routes[nameOf(n)]; ok {
				conflict = nameOf(n)
				break
			}
		}
		if conflict != "" {
			r.warn("sni router: web route rejected, an inbound takes its name", "name", conflict, "port", w.Port)
			continue
		}
		to := net.JoinHostPort("127.0.0.1", strconv.Itoa(w.Port))
		for _, n := range w.Names {
			web[nameOf(n)] = to
		}
	}
	stub := map[string]bool{}
	for _, n := range rt.Stub {
		stub[nameOf(n)] = true
	}
	def := ""
	if p, ok := ports[rt.Default]; ok {
		def = net.JoinHostPort("127.0.0.1", p)
	}
	r.mu.Lock()
	r.routes, r.def = routes, def
	r.web, r.stub = web, stub
	r.mu.Unlock()
}

func (r *sniRouter) serve(admit func(netip.Addr) bool) {
	for {
		c, err := r.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.handle(c, admit)
		}()
	}
}

func (r *sniRouter) handle(c net.Conn, admit func(netip.Addr) bool) {
	defer c.Close()
	src, ok := addrPort(c.RemoteAddr())
	if !ok || admit != nil && !admit(src.Addr()) {
		return
	}
	hello, name := readHello(c)
	if r.stubbed(name) {
		r.front.take(c, hello)
		return
	}
	to, _ := r.target(name)
	if to == "" {
		return
	}
	up, err := net.DialTimeout("tcp", to, 5*time.Second)
	if err != nil {
		return
	}
	defer up.Close()
	dst, _ := addrPort(c.LocalAddr())
	if _, err := up.Write(append(proxyV2(src, dst), hello...)); err != nil {
		return
	}
	relay(c, up)
}

// readHello reads the ClientHello (all that came of it) and the name it asks for; "" when
// it is no TLS or has no SNI.
func readHello(c net.Conn) ([]byte, string) {
	_ = c.SetReadDeadline(time.Now().Add(helloWait))
	defer c.SetReadDeadline(time.Time{})
	buf := make([]byte, 0, 2048)
	tmp := make([]byte, 4096)
	for len(buf) < helloMax {
		n, err := c.Read(tmp)
		buf = append(buf, tmp[:n]...)
		// A TLS record starts with a handshake: anything else is no ClientHello at all.
		if len(buf) > 0 && buf[0] != 0x16 {
			return buf, ""
		}
		if name, serr := sniffer.SniffTLS(buf); serr == nil && name != nil {
			return buf, *name
		}
		// A whole ClientHello with no name in it is done with at once, not waited on for
		// the rest of helloWait.
		if err != nil || helloWhole(buf) {
			return buf, ""
		}
	}
	return buf, ""
}

// helloWhole says whether buf holds a whole ClientHello in its first record: the record's
// length and the handshake message's both fit.
func helloWhole(buf []byte) bool {
	if len(buf) < 9 || buf[0] != 0x16 || buf[5] != 0x01 {
		return false
	}
	rec := int(buf[3])<<8 | int(buf[4])
	msg := int(buf[6])<<16 | int(buf[7])<<8 | int(buf[8])
	return 4+msg <= rec && len(buf) >= 5+rec
}

// proxyV2 is a PROXY protocol v2 header for a TCP connection from src to dst.
func proxyV2(src, dst netip.AddrPort) []byte {
	sig := []byte("\r\n\r\n\x00\r\nQUIT\n")
	var b bytes.Buffer
	b.Write(sig)
	b.WriteByte(0x21) // version 2, PROXY
	s, d := src.Addr().Unmap(), dst.Addr().Unmap()
	if s.Is4() && d.Is4() {
		b.WriteByte(0x11) // TCP over IPv4
		_ = binary.Write(&b, binary.BigEndian, uint16(12))
		b.Write(s.AsSlice())
		b.Write(d.AsSlice())
	} else {
		b.WriteByte(0x21) // TCP over IPv6
		_ = binary.Write(&b, binary.BigEndian, uint16(36))
		s16, d16 := s.As16(), d.As16()
		b.Write(s16[:])
		b.Write(d16[:])
	}
	_ = binary.Write(&b, binary.BigEndian, src.Port())
	_ = binary.Write(&b, binary.BigEndian, dst.Port())
	return b.Bytes()
}

func addrPort(a net.Addr) (netip.AddrPort, bool) {
	if t, ok := a.(*net.TCPAddr); ok {
		return t.AddrPort(), true
	}
	ap, err := netip.ParseAddrPort(a.String())
	return ap, err == nil
}

// relay copies both ways until one side is done, then closes the other's write side.
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}

// close stops accepting and waits for nothing: the relays end with their connections.
func (r *sniRouter) close() {
	_ = r.ln.Close()
}

// setRouter starts, changes or stops the router as the state says. ports are the inbounds'
// ports as rendered. An error to bind is kept for Health and tried again at the next Apply.
func (e *Engine) setRouter(rt *nodeapi.SNIRouter, ports map[string]string) {
	e.routerMu.Lock()
	defer e.routerMu.Unlock()
	if rt == nil {
		if e.router != nil {
			e.router.close()
			e.router = nil
		}
		e.routerStatus.Store(nil)
		return
	}
	listen := rt.Listen
	if listen == "" {
		listen = ":443"
	}
	if e.router != nil && e.router.ln.Addr().String() != "" && e.routerAddr == listen {
		e.router.set(rt, ports)
		return
	}
	if e.router != nil {
		e.router.close()
		e.router = nil
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", listen)
	if err != nil {
		e.routerStatus.Store(&nodeapi.PortStatus{Error: err.Error()})
		e.log.Warn("sni router", "listen", listen, "err", err)
		return
	}
	r := &sniRouter{ln: ln, log: e.log, front: e.front}
	r.set(rt, ports)
	e.router, e.routerAddr = r, listen
	e.routerStatus.Store(&nodeapi.PortStatus{OK: true})
	go r.serve(e.Reg.admitFrom)
}
