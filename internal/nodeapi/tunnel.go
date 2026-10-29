package nodeapi

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

// The first byte of every stream says what it carries: the stream type, then the payload. The
// control stream carries the node's API (and nothing else); the data type is left for the
// user traffic a later round may bring through the same connection, so it needs no new
// endpoint or handshake.
const (
	// StreamControl is the node's API: HTTP over TLS, as on its own port.
	StreamControl byte = 1
	// StreamData is user traffic, for a later round; a node closes such a stream unserved.
	StreamData byte = 2
)
