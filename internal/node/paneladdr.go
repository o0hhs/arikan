package node

import (
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"mikan/internal/fsutil"
)

// PanelAddrFile is where a node remembers the addresses its panel reaches it from, in the
// node's data directory next to hello.json. The installer on the node's server reads it and
// puts the node's API port behind those addresses instead of leaving it open to everyone.
const PanelAddrFile = "panel-addr"

// panelAddrEvery is how often the file is written while the panel keeps connecting from an
// address already in it: the panel asks the node for its health every few seconds, and the
// installer watches the timestamp to see that a connection got through after a rule change.
const panelAddrEvery = 10 * time.Second

// PanelAddrMax is how many addresses the file keeps. A panel may reach its node over IPv4
// and IPv6, or from a new address after a move (another hoster, another NAT); the installer
// allows them all, so learning the new address does not depend on the old rule being gone.
const PanelAddrMax = 4

// PanelAddr is one address the panel connected from and when it last did.
type PanelAddr struct {
	IP   string    `json:"ip"`
	Last time.Time `json:"last"`
}

// PanelAddrs is what the file holds, most recent first.
type PanelAddrs struct {
	IPs []PanelAddr `json:"ips"`
}

// WatchPanelAddr returns ln wrapped so that the address of every connection carrying
// traffic is remembered in path. The node API over TCP is TLS with the panel's certificate
// pinned (nodetls), so a connection that completes its handshake and sends a request is the
// panel's: anybody else fails the handshake and is never written down.
func WatchPanelAddr(ln net.Listener, path string, log *slog.Logger) net.Listener {
	addrs, err := ReadPanelAddrs(path)
	if err != nil && !os.IsNotExist(err) {
		log.Warn("read "+PanelAddrFile, "err", err)
	}
	return &panelWatch{Listener: ln, path: path, log: log, addrs: addrs.IPs}
}

// ReadPanelAddrs reads what the file holds; a missing file is an empty set, not an error.
func ReadPanelAddrs(path string) (PanelAddrs, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return PanelAddrs{}, err
	}
	var out PanelAddrs
	if err := json.Unmarshal(raw, &out); err != nil {
		return PanelAddrs{}, err
	}
	return out, nil
}

type panelWatch struct {
	net.Listener
	path string
	log  *slog.Logger

	mu      sync.Mutex
	addrs   []PanelAddr // most recent first, at most PanelAddrMax
	written time.Time
}

type panelConn struct {
	net.Conn
	watch *panelWatch
	once  sync.Once
}

// Read: the first bytes that arrive mean the TLS handshake (the panel's, or nobody's) is
// over, so this very connection identifies the panel.
func (c *panelConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.once.Do(func() { c.watch.record(c.Conn.RemoteAddr()) })
	}
	return n, err
}

func (w *panelWatch) Accept() (net.Conn, error) {
	conn, err := w.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &panelConn{Conn: conn, watch: w}, nil
}

func (w *panelWatch) record(remote net.Addr) {
	ip := remoteIP(remote)
	if ip == "" {
		return
	}
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	addrs, isNew := addPanelAddr(w.addrs, ip, now)
	w.addrs = addrs
	if !isNew && now.Sub(w.written) < panelAddrEvery {
		return
	}
	if err := writePanelAddrs(w.path, addrs); err != nil {
		w.log.Warn("write "+PanelAddrFile, "err", err)
		return
	}
	w.written = now
}

// addPanelAddr puts ip at the front, drops an older entry of the same address and keeps at
// most PanelAddrMax entries. isNew says whether this is the first time it is seen.
func addPanelAddr(addrs []PanelAddr, ip string, at time.Time) (out []PanelAddr, isNew bool) {
	isNew = true
	out = append(out[:0:0], PanelAddr{IP: ip, Last: at})
	for _, a := range addrs {
		if a.IP == ip {
			isNew = false
			continue
		}
		if len(out) < PanelAddrMax {
			out = append(out, a)
		}
	}
	return out, isNew
}

func writePanelAddrs(path string, addrs []PanelAddr) error {
	raw, err := json.Marshal(PanelAddrs{IPs: addrs})
	if err != nil {
		return err
	}
	// 0644: the installer on the host reads it as root, and the address of the panel is no
	// secret (the .env holds much more).
	return fsutil.WriteFileAtomic(path, append(raw, '\n'), 0o644)
}

// remoteIP is the address of a connection without its port; IPv4 written as IPv6
// (::ffff:10.0.0.1, a dual-stack listener) comes out as IPv4, the way ufw rules name it.
func remoteIP(remote net.Addr) string {
	host, _, err := net.SplitHostPort(remote.String())
	if err != nil {
		return ""
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}
