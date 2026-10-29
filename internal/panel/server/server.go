package server

import (
	"net"
	"net/http"
	"path"
	"strings"
	"sync/atomic"

	"mikan/internal/frontpage"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/settings"
)

// Server routes by the first path segment: the secret admin path, the subscription
// path, the monitoring page's own path, or nothing. Everything else gets the same bare 404,
// so a scanner cannot tell a panel from any other HTTPS endpoint.
type Server struct {
	paths   atomic.Pointer[settings.Paths]
	admin   http.Handler
	sub     http.Handler
	legacy  http.Handler // the old panel's links (settings.Paths.Legacy); nil: none
	site    http.Handler // every other path (OwnSite); nil: NotFound
	monitor http.Handler // the public monitoring page; nil: none
	// monitorPath is the page's own secret segment (settings.KeyMonitor); "" while the
	// admin has not turned it on, and the path is then served like any other.
	monitorPath atomic.Pointer[string]
	// agent is the advertising agents' panel (its page and its API) under its own secret
	// path; nil until the panel wires it in.
	agent http.Handler
	hsts  atomic.Pointer[func() bool]
	// subHost is the subscription domain's host when one is set: that host serves
	// subscriptions alone, at the root, and nothing of the panel.
	subHost atomic.Pointer[string]
	// hello answers a node's hello and says so; whatever it does not answer is served
	// like any unknown path (see nodehello). nil: none.
	hello func(w http.ResponseWriter, r *http.Request) bool
	// tunnel answers a node's request to open a reverse tunnel and says so (see the panel's
	// app); whatever it does not answer is served like any unknown path. nil: none.
	tunnel func(w http.ResponseWriter, r *http.Request) bool
}

// SetSubDomain points the host-only routing at the subscription domain's host ("" when
// there is none). Requests addressed to it never reach the panel, whatever their path.
func (s *Server) SetSubDomain(host string) {
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "."))
	if host == "" {
		s.subHost.Store(nil)
		return
	}
	s.subHost.Store(&host)
}

// hostOnly is the Host header's host part, without the port (addresses arrive as
// "name:port", IPv6 literals as "[::1]:port" or bare "[::1]").
func hostOnly(hostname string) string { //nolint:gocritic // a name, not an address
	if h, _, err := net.SplitHostPort(hostname); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(hostname, "[]")
}

// SetMonitor takes the handler of the public monitoring page.
func (s *Server) SetMonitor(h http.Handler) { s.monitor = h }

// SetMonitorPath points the page at the secret segment it answers under ("" when the admin
// has not turned it on, or has no password for it: the path is then served like any other).
func (s *Server) SetMonitorPath(path string) { s.monitorPath.Store(&path) }

// SetHello takes the handler of the nodes' hellos.
func (s *Server) SetHello(h func(w http.ResponseWriter, r *http.Request) bool) { s.hello = h }

// SetTunnel takes the handler of the nodes' reverse tunnels.
func (s *Server) SetTunnel(h func(w http.ResponseWriter, r *http.Request) bool) { s.tunnel = h }

// SetLegacy takes the handler of the old panel's subscription links.
func (s *Server) SetLegacy(h http.Handler) { s.legacy = h }

// SetSite takes the handler of the paths the panel does not own (OwnSite).
func (s *Server) SetSite(h http.Handler) { s.site = h }

func New(admin, sub http.Handler) *Server {
	s := &Server{admin: admin, sub: sub}
	s.paths.Store(&settings.Paths{})
	empty := ""
	s.monitorPath.Store(&empty)
	return s
}

func (s *Server) SetPaths(p settings.Paths) { s.paths.Store(&p) }

// SetAgent takes the handler of the advertising agents' panel.
func (s *Server) SetAgent(h http.Handler) { s.agent = h }

// SetHSTS makes the answers tell browsers to use HTTPS for this host from now on, while on
// says so. Only for a panel that serves TLS itself, and only while its certificate is one
// browsers trust: a browser that remembers HSTS offers no way past a certificate warning,
// so a panel that falls back to its self-signed certificate (a renewal that failed, a
// custom one that expired) would lock its admin out. nil: never.
func (s *Server) SetHSTS(on func() bool) {
	if on == nil {
		s.hsts.Store(nil)
		return
	}
	s.hsts.Store(&on)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.serve(w, r, true) }

// SubOnly serves the subscription path alone, for the subscription port: the admin panel
// is not there, and its path gets the same 404 as any other.
func (s *Server) SubOnly() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.serve(w, r, false) })
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request, admin bool) {
	SecurityHeaders(w.Header())
	if on := s.hsts.Load(); on != nil && (*on)() {
		w.Header().Set("Strict-Transport-Security", HSTSValue)
	}
	// The subscription domain's host: subscriptions alone, at the root — the panel's
	// paths are a 404 there, like on the subscription port.
	if sh := s.subHost.Load(); sh != nil && strings.EqualFold(hostOnly(r.Host), *sh) {
		s.sub.ServeHTTP(w, r)
		return
	}
	p := r.URL.Path
	// Reject non-canonical paths ("//", "/./", "/../") instead of guessing what they mean.
	if p == "" || p[0] != '/' || (path.Clean(p) != p && path.Clean(p)+"/" != p) {
		s.other(w, r)
		return
	}
	seg, rest, _ := strings.Cut(p[1:], "/")
	paths := s.paths.Load()
	monPath := *s.monitorPath.Load()
	switch {
	case admin && seg != "" && paths.Admin != "" && secure.Equal(seg, paths.Admin):
		s.forward(w, r, s.admin, seg, rest, p)
	case seg != "" && paths.Sub != "" && secure.Equal(seg, paths.Sub):
		s.forward(w, r, s.sub, seg, rest, p)
	case admin && s.monitor != nil && seg != "" && monPath != "" && secure.Equal(seg, monPath):
		// The monitoring page: its own address, its own password, no admin session.
		s.forward(w, r, s.monitor, seg, rest, p)
	case s.agent != nil && admin && seg != "" && paths.Agent != "" && secure.Equal(seg, paths.Agent):
		s.forward(w, r, s.agent, seg, rest, p)
	case s.legacy != nil && paths.Legacy != "" && strings.HasPrefix(p, "/"+paths.Legacy+"/"):
		// The old panel's links: on every port, as the old panel had them on its own.
		r2 := r.Clone(r.Context())
		r2.URL.Path = p[len(paths.Legacy)+1:]
		r2.URL.RawPath = ""
		s.legacy.ServeHTTP(w, r2)
	case s.hello != nil && s.hello(w, r):
	case s.tunnel != nil && s.tunnel(w, r):
	default:
		s.other(w, r)
	}
}

func (s *Server) other(w http.ResponseWriter, r *http.Request) {
	if s.site == nil {
		NotFound(w)
		return
	}
	s.site.ServeHTTP(w, r)
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, h http.Handler, seg, rest, full string) {
	if !strings.HasPrefix(full[1+len(seg):], "/") {
		// "/<secret>" without the trailing slash: relative asset URLs need the slash.
		http.Redirect(w, r, "/"+seg+"/", http.StatusFound)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + rest
	r2.URL.RawPath = ""
	h.ServeHTTP(w, r2)
}

// HSTSValue: a year, for this host only. includeSubDomains and preload are left out: the
// panel does not know what else the host's domain serves.
const HSTSValue = "max-age=31536000"

func SecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
}

// FrameCSP is the policy of an addon's admin pages (docs/pipeline/addons.md, section 9). The
// sandbox gives the frame an opaque origin even when it is opened directly: no cookies, no
// storage, no admin API, no parent, no navigation of the top window.
const FrameCSP = "sandbox allow-scripts; default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'; object-src 'none'"

// FrameHeaders replace SecurityHeaders' framing rules for an addon's pages: the admin panel
// may frame them, and CORP is cross-origin because the opaque frame could not load its
// own scripts with same-origin. nosniff and no-referrer stay.
func FrameHeaders(h http.Header) {
	h.Set("Content-Security-Policy", FrameCSP)
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
}

// NotFound is the plain 404 (frontpage.NotFound).
func NotFound(w http.ResponseWriter) { frontpage.NotFound(w) }
