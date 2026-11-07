package nodeapi

import (
	"encoding/binary"
	"io"
)

// The reverse tunnel: a node that its panel cannot reach opens the connection itself (that way
// round always works when the panel's own does not) and serves its API over it. The panel asks
// for the tunnel on its own HTTPS listener, the node answers and both ends multiplex the
// connection with yamux: the panel opens streams, the node serves each of them with the API.
//
// Authentication is the material both ends already have: the node presents its own certificate
// as a client certificate and the panel accepts it only if its SHA-256 is the pin the node was
// given (nodes.cert_sha256); the node's API, served inside each stream over TLS, pins the
// panel's certificate as it does on a direct connection. No token, nothing secret in a URL.
const (
	// TunnelPath is the panel's route for the tunnels, the same on every panel.
	TunnelPath = "/node/tunnel"
	// TunnelALPN names the tunnel in the TLS handshake. The panel asks a client certificate
	// only of a client that offers it, so a browser is never prompted for one on any other
	// connection to the panel.
	TunnelALPN = "mikan-node-tunnel/1"
	// TunnelUpgrade is the Upgrade value of the request that turns the connection into one.
	TunnelUpgrade = "mikan-tunnel"
)

// The first byte of every stream says what it carries, then the payload: the control stream is
// the node's API, a data stream is one client's connection carried to the node. The data type
// is what lets the panel reach a node's protocols when the filters cut every port of the node
// from Russia: the client connects to the panel, and the panel hands the connection to the node
// over the tunnel the node itself opened.
const (
	// StreamControl is the node's API: HTTP over TLS, as on its own port.
	StreamControl byte = 1
	// StreamData is one client's connection: the port of the node's protocol it was meant for
	// (a big-endian uint16), then the client's bytes both ways, untouched.
	StreamData byte = 2
)

// TunnelTrafficPath is the panel's route the client's traffic lands on: the panel asks for a
// node's tunnel for it and carries the connection to the node's protocol on the stream. It is
// served on the panel's own listener alone, as the tunnel itself is.
const TunnelTrafficPath = "/node/traffic"

// MaxTunnelPort is the highest port a node takes a data stream for: the protocol's own port,
// where the node listens on loopback for the tunnel (see the panel's inbound state).
const MaxTunnelPort = 65535

// WriteStreamPort writes a data stream's first bytes: its type, then the node's port.
func WriteStreamPort(w io.Writer, port uint16) error {
	var b [3]byte
	b[0] = StreamData
	binary.BigEndian.PutUint16(b[1:], port)
	_, err := w.Write(b[:])
	return err
}

// ReadStreamPort reads what a data stream opens with: its type and the node's port.
func ReadStreamPort(r io.Reader) (kind byte, port uint16, err error) {
	var b [3]byte
	if _, err = io.ReadFull(r, b[:]); err != nil {
		return 0, 0, err
	}
	return b[0], binary.BigEndian.Uint16(b[1:]), nil
}
