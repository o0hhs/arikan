package node

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mikan/internal/fsutil"
	"mikan/internal/nodeapi"
)

// The node's own page: a small HTTPS server serving the files of <data>/www (and 404.html for
// what is not there), with the node's certificate. It is what a browser reaching the node's
// domain over https gets, and what a REALITY inbound's dest may name on loopback: the
// handshake of every client then mirrors a real page on the node's own domain instead of a
// third party's site. What a stranger sees is an ordinary site, not a panel.
const (
	// SiteDefaultListen is where the page is served unless MIKAN_SITE_LISTEN says otherwise:
	// port 443, where a site is expected. An inbound usually holds it, so a node falls back
	// to SiteFallbackListen and says so.
	SiteDefaultListen = ":443"
	// SiteFallbackListen is a loopback port nothing else uses: the page keeps working as a
	// REALITY destination (which is dialed on loopback) when 443 belongs to an inbound.
	SiteFallbackListen = "127.0.0.1:18443"
	// SiteDir is the directory of the page's files, under the node's data directory.
	SiteDir = "www"
	// hsts is sent while the node's certificate is one a browser trusts, as the panel sends
	// it; a self-signed one (no public certificate yet) would lock a visitor out of the page
	// for a year with no way past the warning.
	hsts = "max-age=31536000"
)

// Site serves the node's own page. Its lifetime is the node's: it is started once, and the
// certificate of the last applied state is what it presents.
type Site struct {
	listen string
	dir    string
	log    *slog.Logger

	// listenFunc is net.Listen, unless a test swaps it.
	listenFunc func(network, addr string) (net.Listener, error)
	now        func() time.Time

	mu      sync.Mutex
	started bool
	cert    *tls.Certificate
	srv     *http.Server
	ln      net.Listener
	status  nodeapi.SiteStatus
}

func newSite(dir, listen string, log *slog.Logger) *Site {
	return &Site{dir: filepath.Join(dir, SiteDir), listen: listen, log: log, listenFunc: net.Listen, now: time.Now}
}

// Start serves the page; nothing happens without the files of the page or with no listen
// address (MIKAN_SITE_LISTEN=off). The port of SiteDefaultListen held by another program (an
// inbound on 443, most often) sends the page to SiteFallbackListen.
func (s *Site) Start() {
	if s == nil || s.listen == "" || strings.EqualFold(s.listen, "off") {
		return
	}
	s.mu.Lock()
	already := s.started
	s.started = true
	s.mu.Unlock()
	if already {
		return
	}
	if err := s.ensurePage(); err != nil {
		s.status = nodeapi.SiteStatus{Listen: s.listen, Error: err.Error()}
		s.log.Warn("the node's page does not run", "dir", s.dir, "err", err)
		return
	}
	ln, err := s.listenFunc("tcp", s.listen)
	if err != nil && s.listen == SiteDefaultListen {
		s.log.Info("the node's page moves to a loopback port", "taken", s.listen, "err", err)
		ln, err = s.listenFunc("tcp", SiteFallbackListen)
	}
	if err != nil {
		s.status = nodeapi.SiteStatus{Listen: s.listen, Error: err.Error()}
		s.log.Warn("the node's page does not run", "listen", s.listen, "err", err)
		return
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(s.serve),
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"h2", "http/1.1"},
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				s.mu.Lock()
				defer s.mu.Unlock()
				if s.cert == nil {
					// Before the panel sends its certificate the page has nothing to present:
					// the handshake fails, which is what a client of a node without a
					// certificate sees everywhere else too.
					return nil, errors.New("no node certificate yet")
				}
				return s.cert, nil
			},
		},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	s.mu.Lock()
	s.srv, s.ln = srv, ln
	s.status = nodeapi.SiteStatus{Listen: ln.Addr().String(), Port: portOf(ln.Addr()), OK: true}
	s.mu.Unlock()
	s.writeStatus()
	go func() {
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Warn("the node's page stopped", "err", err)
		}
	}()
	s.log.Info("the node's page runs", "listen", ln.Addr().String(), "dir", s.dir)
}

// ensurePage makes the page's directory and, only when it is empty, a neutral page to
// replace: what the admin puts in www/ is never touched.
func (s *Site) ensurePage() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	writeIfMissing := func(name, text string) error {
		p := filepath.Join(s.dir, name)
		if _, err := os.Stat(p); err == nil {
			return nil
		}
		return fsutil.WriteFileAtomic(p, []byte(text), 0o644)
	}
	if err := writeIfMissing("index.html", defaultPage); err != nil {
		return err
	}
	return writeIfMissing("404.html", defaultPage404)
}

// defaultPage: an ordinary page and nothing that says who runs the server. The admin replaces
// it with their own (the same files, data/node/www on the host).
const defaultPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Welcome</title>
</head>
<body>
<p>Nothing here yet.</p>
</body>
</html>
`

const defaultPage404 = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Not found</title>
</head>
<body>
<p>Not found.</p>
</body>
</html>
`

// SetCert is the certificate of the state just applied: what the page presents from now on.
func (s *Site) SetCert(certPath, keyPath string) {
	if s == nil {
		return
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		s.log.Warn("the node's page keeps its old certificate", "err", err)
		return
	}
	s.mu.Lock()
	s.cert = &cert
	s.mu.Unlock()
}

// Status is the page as the node reports it in its health; nil while there never was one
// (no state with a certificate yet, or MIKAN_SITE_LISTEN=off).
func (s *Site) Status() *nodeapi.SiteStatus {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return nil
	}
	st := s.status
	return &st
}

// StatusFile is where the page's state is written for `arikan doctor` on the node's server,
// as hello.json is for the installer.
const StatusFile = "site.json"

// writeStatus keeps the file in step; a failure is not worth more than a log line.
func (s *Site) writeStatus() {
	raw, err := json.Marshal(s.Status())
	if err != nil {
		return
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(filepath.Dir(s.dir), StatusFile), append(raw, '\n'), 0o644); err != nil {
		s.log.Warn("write "+StatusFile, "err", err)
	}
}

func (s *Site) serve(w http.ResponseWriter, r *http.Request) {
	root, err := os.OpenRoot(s.dir) // no way out of dir, symlinks included
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	fsys := root.FS()
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if !servable(fsys, name) {
		if !servable(fsys, "404.html") {
			http.NotFound(w, r)
			return
		}
		page, err := fs.ReadFile(fsys, "404.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.plain(w.Header())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(page)
		return
	}
	s.plain(w.Header())
	http.FileServerFS(fsys).ServeHTTP(w, r)
}

// servable: a file, or a directory with index.html; never a hidden one (.git, .env), as the
// panel's own site serves its files.
func servable(fsys fs.FS, name string) bool {
	if name == "" {
		name = "."
	}
	for seg := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(seg, ".") && seg != "." {
			return false
		}
	}
	st, err := fs.Stat(fsys, name)
	if err != nil {
		return false
	}
	if st.IsDir() {
		st, err = fs.Stat(fsys, path.Join(name, "index.html"))
		return err == nil && !st.IsDir()
	}
	return true
}

// plain drops what would tell a panel from an ordinary site: the panel's own policy headers,
// and the Server line with mihomo's or Go's name in it. HSTS goes out only while the node's
// certificate is one browsers trust (the same rule as the panel's).
func (s *Site) plain(h http.Header) {
	for _, k := range []string{"Server", "X-Powered-By"} {
		h.Del(k)
	}
	if s.trusted() {
		h.Set("Strict-Transport-Security", hsts)
	} else {
		h.Del("Strict-Transport-Security")
	}
}

// trusted says whether the certificate the page presents looks like one a browser accepts:
// a public one the panel ordered, not the self-signed pin a node starts with. A chain to a
// real CA is not walked here (the node has no roots of its own to judge by): a certificate
// that is signed by itself is the one that would lock a visitor out.
func (s *Site) trusted() bool {
	s.mu.Lock()
	cert := s.cert
	s.mu.Unlock()
	if cert == nil || len(cert.Certificate) == 0 {
		return false
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return false
	}
	return leaf.Issuer.String() != leaf.Subject.String()
}

// portOf: the port of a listener's address, 0 when it is not a TCP one to name in a REALITY
// dest (a loopback port is what the panel may allow).
func portOf(addr net.Addr) int {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.IP == nil || !tcp.IP.IsLoopback() {
		return 0
	}
	return tcp.Port
}
