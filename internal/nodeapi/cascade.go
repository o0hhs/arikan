package nodeapi

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"mikan/internal/nodetls"
)

// The cascade over the panel's tunnel: clients land on an entry node, their traffic leaves
// the internet through an exit node, and every connection between the two is opened by the
// side the country's filters let through — the exit dials the panel, and the entry's node
// opens the streams it carries over the connection the panel itself opened to it. Nothing
// here dials an exit's address: an exit that is not in the tunnel carries nothing, and the
// inbounds that named it fail instead of leaking out of the entry.
//
// The shapes of one connection, from the client inwards:
//
//	the core → 127.0.0.1 (CascadeListen), SOCKS5 with the exit's name as the user
//	the entry's bridge → a stream on the panel's bridge, asking for that exit
//	the panel → a stream on that exit's own connection, copying both ways
//	the exit → the core again, which routes and resolves as its own rules say
//
// A stream is handed over with CascadeStreamData, the exit's id, the panel's answer, and
// the target; the bytes after that are the client's own, and neither the panel nor any log
// keeps them.

const (
	// CascadePath is the panel's endpoint the exit nodes knock on: their certificate
	// signs the knock, and the connection becomes the streams of their data.
	CascadePath = "/node/cascade"
	// CascadeBridgePath is the node API path the panel opens an entry's bridge on: an
	// upgrade, then the node's streams.
	CascadeBridgePath = "/v1/cascade"
	// CascadeUpgrade is the token of both upgrades above.
	CascadeUpgrade = "mikan-cascade"
	// CascadeListen is the entry's loopback SOCKS5 the core's exits point at. Every node
	// may use the same address: it is its own loopback, and only its own core uses it.
	CascadeListen = "127.0.0.1:47105"
	// CascadeListener is the listening name rules see on the exit: IN-NAME,<name>,… of a
	// rule and the exit's own alternatives (WARP, another exit) may name it.
	CascadeListener = "mikan~cascade"
	// CascadeWarpListener is the same for the streams a route sent through the exit's WARP:
	// a protocol marked "through the exit's WARP" arrives under this name, so the exit's
	// rule sends exactly it to WARP while other nodes' traffic on the same exit goes on
	// by the exit's own way out.
	CascadeWarpListener = "mikan~cascade-warp"
	// CapCascadeWarp is what a node's health says when its code knows the two halves of
	// that route: the entry marks the stream, the exit reads the mark into its rule. The
	// panel builds such a route only when both ends say it.
	CapCascadeWarp = "cascade-warp"
	// CascadeStreamData is the first byte of a stream carrying one client's connection;
	// the panel pairs it with an exit, and any other kind is refused.
	CascadeStreamData = 1
	// CascadeHelloWindow is how far an exit's knock may be from the panel's clock.
	CascadeHelloWindow = 5 * time.Minute
	// CascadeMaxFrame bounds a knock and every frame a stream opens with: a target, an
	// answer. The bytes after them are the connection and are not framed.
	CascadeMaxFrame = 4 << 10
)

// Cascade is a node's part in the cascade; nil (or both halves nil) leaves it out.
type Cascade struct {
	// Entry is set on a node that sends its users' traffic through other nodes.
	Entry *CascadeEntry `json:"entry,omitempty"`
	// Exit is set on a node whose way to the internet other nodes' users take.
	Exit *CascadeExit `json:"exit,omitempty"`
}

// CascadeEntry is the entry node's side: the loopback SOCKS5 its core points its exits at,
// and what each exit name may use.
type CascadeEntry struct {
	// Listen is where the node runs the bridge ("127.0.0.1:47105").
	Listen string `json:"listen"`
	// Password is what the bridge asks of the core's connections; the username names the
	// exit. The core's own config carries both (the panel wrote them), so nothing else on
	// the server that finds the port can use it.
	Password string `json:"password"`
	// Routes are the exits of the node's inbounds and relay, one per name.
	Routes []CascadeRoute `json:"routes"`
}

// CascadeRoute is one exit as the core names it (the proxy's name, also the bridge's
// SOCKS5 username): the exit to use, then the reserves for when its tunnel is down.
type CascadeRoute struct {
	Name string `json:"name"`
	// Exits are node ids: the first is the exit the inbound named, the rest are reserves
	// the panel picked among its other exits. The core's proxy name stays the first's.
	Exits []int64 `json:"exits"`
	// Warp sends the streams of this route through the exit's own WARP: the bridge marks
	// each one, and the exit routes it by CascadeWarpListener instead of CascadeListener.
	Warp bool `json:"warp,omitempty"`
}

// CascadeExit is the exit node's side: the panel's endpoint its stream goes to.
type CascadeExit struct {
	URL string `json:"url"`
}

// CascadeStatus is a node's cascade as it is now, for the panel's card.
type CascadeStatus struct {
	Entry *CascadeEntryStatus `json:"entry,omitempty"`
	Exit  *CascadeExitStatus  `json:"exit,omitempty"`
}

// CascadeEntryStatus is the entry's bridge.
type CascadeEntryStatus struct {
	// Listening is the loopback SOCKS5; Error says why it is not up, in words.
	Listening bool   `json:"listening"`
	Error     string `json:"error,omitempty"`
	// Bridge: the panel holds the node's streams now; Since when it started to.
	Bridge bool      `json:"bridge"`
	Since  time.Time `json:"since,omitzero"`
}

// CascadeExitStatus is the exit's stream to the panel.
type CascadeExitStatus struct {
	Connected bool      `json:"connected"`
	Since     time.Time `json:"since,omitzero"`
	// Error is why the last attempt failed, in words for the admin (no secrets).
	Error string `json:"error,omitempty"`
}

// CascadeHello is an exit's knock on the panel: who it is, proven by the signature of the
// key of the certificate in it. The panel finds the node by the certificate's pin, the same
// one it pins when it dials the node.
type CascadeHello struct {
	Cert  string `json:"cert"`  // the node's certificate, PEM
	Time  int64  `json:"time"`  // unix seconds
	Nonce string `json:"nonce"` // 32 hex digits
	Sig   string `json:"sig"`   // base64 Ed25519 over CascadeHelloMessage
}

// CascadeHelloMessage is what an exit signs.
func CascadeHelloMessage(t int64, nonce string) []byte {
	return []byte("mikan-cascade/1\n" + fmt.Sprint(t) + "\n" + nonce)
}

// NewCascadeHello makes the knock of a node whose join key is key.
func NewCascadeHello(key nodetls.Key, now time.Time) (CascadeHello, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return CascadeHello{}, err
	}
	nonce := hex.EncodeToString(b[:])
	t := now.Unix()
	sig, err := nodetls.Sign(key.KeyPEM, CascadeHelloMessage(t, nonce))
	if err != nil {
		return CascadeHello{}, err
	}
	return CascadeHello{Cert: key.CertPEM, Time: t, Nonce: nonce, Sig: base64.StdEncoding.EncodeToString(sig)}, nil
}

// Errors of VerifyCascadeHello.
var (
	ErrCascadeHelloForm = errors.New("cascade hello: malformed")
	ErrCascadeHelloTime = errors.New("cascade hello: time out of the window")
	ErrCascadeHelloSig  = errors.New("cascade hello: bad signature")
)

// VerifyCascadeHello checks a knock's form, time and signature and returns the pin of the
// certificate that signed it. Whether that pin is one of the panel's exits is the caller's.
func VerifyCascadeHello(h CascadeHello, now time.Time) (string, error) {
	if len(h.Nonce) != 32 {
		return "", ErrCascadeHelloForm
	}
	if _, err := hex.DecodeString(h.Nonce); err != nil {
		return "", ErrCascadeHelloForm
	}
	sig, err := base64.StdEncoding.DecodeString(h.Sig)
	if err != nil {
		return "", ErrCascadeHelloForm
	}
	at := time.Unix(h.Time, 0)
	if now.Sub(at) > CascadeHelloWindow || at.Sub(now) > CascadeHelloWindow {
		return "", ErrCascadeHelloTime
	}
	pin, err := nodetls.Verify(h.Cert, CascadeHelloMessage(h.Time, h.Nonce), sig)
	if err != nil {
		return "", ErrCascadeHelloSig
	}
	return pin, nil
}

// WriteCascadeAsk writes the opening of a stream an entry node opens: the kind and the exit
// the panel is to pair it with.
func WriteCascadeAsk(w io.Writer, kind byte, exit int64) error {
	var b [9]byte
	b[0] = kind
	binary.BigEndian.PutUint64(b[1:], uint64(exit))
	_, err := w.Write(b[:])
	return err
}

// ReadCascadeAsk reads what WriteCascadeAsk wrote.
func ReadCascadeAsk(r io.Reader) (kind byte, exit int64, err error) {
	var b [9]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, 0, err
	}
	id := binary.BigEndian.Uint64(b[1:])
	if id > math.MaxInt64 {
		return 0, 0, errors.New("cascade: bad exit id")
	}
	return b[0], int64(id), nil
}

// WriteCascadeAnswer answers an ask: "" pairs the stream, anything else is the panel's
// reason, which the entry's bridge turns into the client's SOCKS5 refusal.
func WriteCascadeAnswer(w io.Writer, reason string) error {
	if reason == "" {
		_, err := w.Write([]byte{0})
		return err
	}
	if len(reason) > CascadeMaxFrame {
		reason = reason[:CascadeMaxFrame]
	}
	b := make([]byte, 3+len(reason))
	b[0] = 1
	binary.BigEndian.PutUint16(b[1:], uint16(len(reason)))
	copy(b[3:], reason)
	_, err := w.Write(b)
	return err
}

// CascadeRefused is the panel's answer: it did not pair the stream.
type CascadeRefused struct{ Reason string }

func (e *CascadeRefused) Error() string { return e.Reason }

// ReadCascadeAnswer reads what WriteCascadeAnswer wrote; nil means the stream is paired.
func ReadCascadeAnswer(r io.Reader) error {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return err
	}
	if b[0] == 0 {
		return nil
	}
	var n [2]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return err
	}
	raw := make([]byte, binary.BigEndian.Uint16(n[:]))
	if _, err := io.ReadFull(r, raw); err != nil {
		return err
	}
	return &CascadeRefused{Reason: string(raw)}
}

// CascadeTarget is what an entry sends an exit before the bytes of one connection: the
// address the client asked for, as the client's own client named it (a name stays a name,
// and the exit's resolver — ExitDNS included — resolves it).
type CascadeTarget struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// Warp: the route this connection came by leaves through the exit's WARP. An exit that
	// does not know the field (older than it) leaves the stream direct, which is why the
	// panel builds such a route only for a node whose health says CapCascadeWarp.
	Warp bool `json:"warp,omitempty"`
}

// WriteCascadeTarget writes the target as one frame: its length, then the address. The
// length matters: the client's own bytes follow at once, and a decoder left to read ahead
// would take some of them.
func WriteCascadeTarget(w io.Writer, t CascadeTarget) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > CascadeMaxFrame {
		return errors.New("cascade: the target does not fit a frame")
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(raw)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(raw)
	return err
}

// ReadCascadeTarget reads what WriteCascadeTarget wrote, and not one byte more: the
// client's connection starts right after the frame.
func ReadCascadeTarget(r io.Reader) (CascadeTarget, error) {
	var t CascadeTarget
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return t, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n == 0 || n > CascadeMaxFrame {
		return t, errors.New("cascade: bad target frame")
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(r, raw); err != nil {
		return t, err
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return t, err
	}
	if t.Host == "" || t.Port < 1 || t.Port > 65535 {
		return t, errors.New("cascade: bad target")
	}
	if len(t.Host) > 255 {
		return t, errors.New("cascade: target name too long")
	}
	return t, nil
}
