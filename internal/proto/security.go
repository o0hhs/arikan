package proto

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"mikan/internal/netaddr"
)

// Options relax checks for one installation.
type Options struct {
	// SelfStealPort allows REALITY dest 127.0.0.1:<port>: the panel's own HTTPS with a
	// real certificate for the server's domain, so the SNI matches the IP.
	SelfStealPort int
	// SitePort allows REALITY dest 127.0.0.1:<port> too: the node's own page
	// (data/node/www, internal/node/site.go) with the certificate the panel ordered for the
	// node. The handshake of every client then mirrors a real page on the node's own domain
	// and depends on nobody else's site.
	SitePort int
	// AnyDest skips the dest check (rendering clients of an already accepted template).
	AnyDest bool
}

func validateReality(r map[string]any, o Options) error {
	dest, _ := r["dest"].(string)
	host, port, err := net.SplitHostPort(dest)
	if err != nil || host == "" {
		return fail("reality_dest", "reality-config.dest")
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fail("reality_dest", "reality-config.dest")
	}
	// The node dials dest for every probe of the port, so an internal address here would
	// publish that service to the internet.
	if !o.AnyDest && !netaddr.PublicHost(host) && !loopbackDest(host, port, o) {
		return fail("reality_dest_private", "reality-config.dest")
	}
	if _, err := RealityPublicKey(str(r["private-key"])); err != nil {
		return fail("reality_key", "reality-config.private-key")
	}
	names := strings1(r["server-names"])
	if len(names) == 0 {
		return fail("reality_sni", "reality-config.server-names")
	}
	sids := strings1(r["short-id"])
	if len(sids) == 0 {
		return fail("reality_sid", "reality-config.short-id")
	}
	for _, s := range sids {
		if len(s) > 16 || len(s)%2 != 0 {
			return fail("reality_sid", "reality-config.short-id")
		}
		if _, err := hex.DecodeString(s); err != nil {
			return fail("reality_sid", "reality-config.short-id")
		}
	}
	if _, ok := r["proxy"]; ok {
		return fail("config_key", "reality-config.proxy")
	}
	return nil
}

// loopbackDest says whether a dest of this installation may name a loopback address: the
// panel's own HTTPS (self-steal), or the node's own page (site), each on the one port the
// installation says it answers on. Any other loopback dest would publish an internal
// service to the internet: the node dials dest for every probe of the port.
func loopbackDest(host, port string, o Options) bool {
	if host != "127.0.0.1" && host != "localhost" {
		return false
	}
	return (o.SelfStealPort > 0 && port == strconv.Itoa(o.SelfStealPort)) || (o.SitePort > 0 && port == strconv.Itoa(o.SitePort))
}

// realityKeys remembers the public key of each private key asked about (a panel has a
// handful): deriving it is an x25519 scalar multiplication, and every subscription fetch
// and every validation of a REALITY template does it.
var realityKeys sync.Map

// RealityPublicKey derives the client's public key from the server's private key.
func RealityPublicKey(private string) (string, error) {
	if pub, ok := realityKeys.Load(private); ok {
		return pub.(string), nil
	}
	pub, err := realityPublicKey(private)
	if err == nil {
		realityKeys.Store(private, pub)
	}
	return pub, err
}

func realityPublicKey(private string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(private, "="))
	if err != nil {
		return "", err
	}
	k, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

func publicHTTPS(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.User == nil && netaddr.PublicHost(u.Hostname())
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// strings1 accepts a YAML list or a single string.
func strings1(v any) []string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	}
	return nil
}
