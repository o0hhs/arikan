package node

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mikan/internal/nodeapi"
)

// The subscription server: HTTPS answers for the links the panel rendered. It is reached
// two ways, both on the node's public 443 — the SNI router hands a ClientHello of the
// subscription domain over (take), or the node's own page holds the port and dispatches
// (Handle). Nothing here renders, looks a subscriber up or logs a path: a request is a
// record the panel left, or a 404.

// expiredSub is what a link past its record's expiry gets: 410, so the app can tell "this
// link is done" from "this link never existed" and shows the subscriber what to do.
const expiredSub = `This subscription link has expired.
Get a new link in the service: open the bot or the subscription page.
`

// subServer serves the records. It has no port of its own: the connections come from the
// router's hand-over or from the page's handler.
type subServer struct {
	store *subStore
	log   *slog.Logger
	ln    *handover
	srv   *http.Server

	mu    sync.RWMutex
	names map[string]bool // lowercased SNI names it answers for

	cert atomic.Pointer[tls.Certificate]
	// view is how the answers are reached (router, page) and what went wrong; nil while the
	// feature is off.
	view atomic.Pointer[subsView]
}

// subsView is the part of the status that changes with the state rather than with the data.
type subsView struct {
	ok     bool
	err    string
	listen string
}

func newSubServer(store *subStore, logger *slog.Logger) *subServer {
	s := &subServer{store: store, log: logger, names: map[string]bool{}, ln: newHandover()}
	s.srv = &http.Server{
		Handler:           http.HandlerFunc(s.serve),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		// A stranger's broken handshake is no news, and a line with a path in it never goes
		// out: the errors are dropped whole.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			if c := s.cert.Load(); c != nil {
				return c, nil
			}
			return nil, errors.New("no subscription certificate yet")
		},
	}
	go func() { _ = s.srv.Serve(tls.NewListener(s.ln, cfg)) }()
	return s
}

// apply takes the state's part of the feature: the names it answers for and the certificate
// it presents. A nil subs turns the names off (the records stay on the disk: the panel may
// turn it back on, and a node restarted must not lose them).
func (s *subServer) apply(subs *nodeapi.SubsServer, nodeTLS *nodeapi.TLSFiles) {
	if s == nil {
		return
	}
	names := map[string]bool{}
	var cert *tls.Certificate
	var tlsFiles *nodeapi.TLSFiles
	if subs != nil {
		for _, n := range subs.Names {
			if n = strings.ToLower(strings.TrimSuffix(n, ".")); n != "" {
				names[n] = true
			}
		}
		tlsFiles = subs.TLS
		if tlsFiles == nil || tlsFiles.CertPEM == "" {
			tlsFiles = nodeTLS
		}
	}
	if tlsFiles != nil && tlsFiles.CertPEM != "" {
		if c, err := tls.X509KeyPair([]byte(tlsFiles.CertPEM), []byte(tlsFiles.KeyPEM)); err == nil {
			cert = &c
		}
	}
	s.mu.Lock()
	s.names = names
	s.mu.Unlock()
	if cert != nil {
		s.cert.Store(cert)
	}
}

// place says how the answers are reached, and why they are not when they cannot be: the
// node's public 443 comes from the router or from the page that holds it. "" turns the
// feature off in Health.
func (s *subServer) place(listen, why string) {
	if s == nil {
		return
	}
	s.mu.RLock()
	off := len(s.names) == 0
	s.mu.RUnlock()
	if off {
		s.view.Store(nil)
		return
	}
	s.view.Store(&subsView{ok: listen != "" && why == "", err: why, listen: listen})
}

// handles says whether name (a ClientHello's SNI) is the subscription domain's.
func (s *subServer) handles(name string) bool {
	if s == nil {
		return false
	}
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.names[name]
}

// namesFor is the router's view of what it hands over; nil while nothing is served.
func (s *subServer) namesFor() map[string]bool {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.names) == 0 {
		return nil
	}
	out := make(map[string]bool, len(s.names))
	for n := range s.names {
		out[n] = true
	}
	return out
}

// status is the whole picture for Health and GET /v1/subs.
func (s *subServer) status() *nodeapi.SubsStatus {
	if s == nil {
		return &nodeapi.SubsStatus{Error: nodeapi.CodeSubsOff}
	}
	st := s.store.status()
	v := s.view.Load()
	if v == nil {
		st.OK, st.Error = false, nodeapi.CodeSubsOff
		return &st
	}
	st.OK, st.Error, st.Listen = v.ok, v.err, v.listen
	return &st
}

// subsMux mounts the paths the panel fills the store with (api.go). A record the store
// refuses (too big, too many, a path that is no token) is skipped and counted: one broken
// profile must not stop the rest of a sync, and the generation prune would otherwise never
// run.
func subsMux(mux *http.ServeMux, s *subServer) {
	mux.HandleFunc("PUT /v1/subs", func(w http.ResponseWriter, r *http.Request) {
		var req nodeapi.SubsRequest
		if !decode(w, r, &req) {
			return
		}
		res := nodeapi.SubsResult{}
		for _, rec := range req.Records {
			if err := s.store.put(req.Generation, rec); err != nil {
				res.Rejected++
				if res.Reason == "" {
					res.Reason = err.Error()
				}
				continue
			}
			res.Stored++
		}
		res.Bytes = s.store.status().Bytes
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("POST /v1/subs/remove", func(w http.ResponseWriter, r *http.Request) {
		var req nodeapi.SubsRemoveRequest
		if !decode(w, r, &req) {
			return
		}
		writeJSON(w, http.StatusOK, nodeapi.SubsResult{Removed: s.store.remove(req.Paths), Bytes: s.store.status().Bytes})
	})
	mux.HandleFunc("POST /v1/subs/prune", func(w http.ResponseWriter, r *http.Request) {
		var req nodeapi.SubsPruneRequest
		if !decode(w, r, &req) {
			return
		}
		writeJSON(w, http.StatusOK, nodeapi.SubsResult{Removed: s.store.prune(req.Generation), Bytes: s.store.status().Bytes})
	})
	mux.HandleFunc("GET /v1/subs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.status())
	})
}

// take serves c, which sent hello already, and returns when c is done with: the router's
// way in, like the front page's.
func (s *subServer) take(c net.Conn, hello []byte) {
	if s == nil {
		_ = c.Close()
		return
	}
	rc := &replayConn{Conn: c, r: io.MultiReader(bytes.NewReader(hello), c), done: make(chan struct{})}
	if s.ln.push(rc) {
		<-rc.done
	}
}

// Handle answers a request the node's own page passed on because its first segment is a
// record's path. false: not a subscription at all, the page's files are the caller's.
func (s *subServer) Handle(w http.ResponseWriter, r *http.Request) bool {
	token := firstSegment(r.URL.Path)
	if !validSubPath(token) {
		return false
	}
	if _, ok := s.store.lookup(token); !ok {
		return false
	}
	s.serve(w, r)
	return true
}

// warn answers a request the record's path is not: the same bytes for a token that never
// existed and for one that was dropped.
func subNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "Not found.\n")
}

func firstSegment(p string) string {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	seg, _, _ := strings.Cut(p, "/")
	return seg
}

func (s *subServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		subNotFound(w)
		return
	}
	token := firstSegment(r.URL.Path)
	if !validSubPath(token) {
		subNotFound(w)
		return
	}
	lk, ok := s.store.lookup(token)
	if !ok {
		subNotFound(w)
		return
	}
	if lk.expired {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusGone)
		_, _ = io.WriteString(w, expiredSub)
		return
	}
	body, ok := nodeapi.PickSubBody(lk.rec.Bodies, r.URL.Query().Get("format"), r.UserAgent(), r.Header.Get("Accept"))
	if !ok {
		subNotFound(w)
		return
	}
	hd := w.Header()
	for k, v := range lk.rec.Headers {
		hd.Set(k, v)
	}
	hd.Set("Cache-Control", "no-store")
	typ := body.Type
	if typ == "" {
		typ = "text/plain; charset=utf-8"
	}
	hd.Set("Content-Type", typ)
	if body.Name != "" {
		hd.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(body.Name))
	}
	hd.Set("Content-Length", strconv.Itoa(len(body.Body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body.Body)
}
