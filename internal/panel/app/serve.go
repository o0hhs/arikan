package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/http2"

	"mikan/internal/logbuf"
	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/acme"
	"mikan/internal/panel/autotune"
	"mikan/internal/panel/config"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/ipgeo"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/reach"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/support"
	"mikan/internal/panel/tlscert"
	"mikan/internal/panel/updates"
	"mikan/internal/release"
)

// workerStopTimeout is how long Serve waits for the workers before it closes the database.
const workerStopTimeout = 20 * time.Second

func Serve(ctx context.Context, cfg config.Config, version string, web fs.FS) error {
	// The last lines of the log, secrets cut out, for the Diagnostics page.
	logs := logbuf.New(logbuf.DefaultLines)
	logger := slog.New(logs.Handler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel})))
	dsn, err := store.DatabaseURL()
	if err != nil {
		return err
	}
	// Held from before the store opens (it migrates) until the panel is down: database
	// migrate and restore refuse to run under a live panel.
	unlock, err := store.LockPanel(ctx, dsn)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := store.OpenPostgres(ctx, cfg.DataDir, dsn)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := domain.Seed(ctx, st, time.Now()); err != nil {
		return fmt.Errorf("seed: %w", err)
	}

	ep, err := settings.New(st.Q).Endpoint(ctx)
	if err != nil {
		return err
	}
	host := ep.Host
	if host == "" {
		host = "localhost"
	}
	// The self-signed certificate is long-lived and pinned in Hysteria2/TUIC links where
	// the local node has no public one.
	tlsDir := filepath.Join(cfg.DataDir, "tls")
	self, err := tlscert.LoadOrCreateSelfSigned(tlsDir, host, time.Now())
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	holder := &tlscert.Holder{}
	holder.Set(self)

	opts := Options{Version: version, Web: web, TrustProxy: cfg.TrustProxy, SupportOff: cfg.SupportOff, Log: logger, Now: time.Now,
		Autotune: autotune.DefaultOptions().Scaled(cfg.AutotuneScale), TelegramAPI: cfg.TelegramAPI,
		DataDir: cfg.DataDir, Dev: cfg.Dev, Releases: updates.Fetch(release.IndexURL, release.LatestURL), DNS: dnscheck.New(), Reach: reach.New("", nil, time.Now), Geo: ipgeo.New(nil, time.Now), Logs: logs}
	botURL := cfg.SupportBotURL
	if botURL == "" {
		botURL = support.DefaultBotURL
	}
	bot, err := support.NewHTTPBot(botURL)
	if err != nil {
		return fmt.Errorf("MIKAN_SUPPORT_BOT_URL: %w", err)
	}
	opts.SupportBot = bot
	nodesDir := filepath.Join(tlsDir, "nodes")
	nodeCerts := tlscert.NewNodeStore(filepath.Join(tlsDir, "custom-nodes"), time.Now)
	opts.NodeCerts = nodeCerts
	// The nodes' reverse tunnels: a node the panel cannot reach connects out to it and serves
	// its API over that connection (reverse.go). This is the way the nodes keep working where
	// the ISP cuts what arrives at them while what leaves passes.
	tunnels := newTunnels(st, logger, time.Now)
	set := settings.New(st.Q)
	var (
		certs     *acme.Manager
		subCerts  *acme.Manager
		nodesACME *acme.Nodes
	)
	// The subscription domain gets its own certificate; clients that name another get the
	// panel's.
	names := &tlscert.BySNI{Panel: holder, Sub: &tlscert.Holder{}}
	if d, err := set.String(ctx, settings.KeySubDomain); err == nil {
		names.SetName(d)
	}
	if !cfg.Dev {
		certs = acme.New(cfg.DataDir, holder, self, set, logger, time.Now)
		subCerts = acme.NewSub(cfg.DataDir, names.Sub, set, certs.Challenge(), logger, time.Now)
		subCerts.Load(ctx)
		opts.SubCerts = subCerts
		// The certificate already on disk is served before the nodes are first synced: links
		// drop the pin once it is public, so the local node must start with it too.
		certs.Load(ctx)
		opts.Certs = certs
		opts.HSTS = certs.Trusted
		// Each remote node's public certificate, the challenge answered on its port 80.
		nodesACME = certs.Nodes(func(ctx context.Context) ([]acme.NodeTarget, error) {
			nodes, err := st.Q.ListNodes(ctx)
			if err != nil {
				return nil, err
			}
			var out []acme.NodeTarget
			for _, n := range nodes {
				if n.Address == "" {
					continue
				}
				t := acme.NodeTarget{ID: n.ID, Host: domain.NodeHost(n)}
				if ip, err := netip.ParseAddr(n.PublicHost); err == nil {
					t.Own = []netip.Addr{ip.Unmap()}
				}
				if own, _, _ := nodeCerts.Get(n.ID, t.Host); own != nil {
					t.HasOwn = true
				}
				if c, err := nodeClient(cfg, n, tunnels); err == nil {
					t.Client = c
				}
				out = append(out, t)
			}
			return out, nil
		})
		opts.NodesACME = nodesACME
	}
	opts.TLS = NewNodeTLS(cfg.DataDir, nodeCerts, certs, nodesACME, set, logger, time.Now)
	opts.PanelCert = func() (nodetls.Pair, error) { return nodetls.LoadOrCreate(nodesDir, time.Now()) }
	opts.Tunnel = tunnels.serve
	opts.Tunnels = tunnels
	opts.Connect = func(n db.Node) (nodesync.Target, error) {
		c, err := nodeClient(cfg, n, tunnels)
		if err != nil {
			return nodesync.Target{}, err
		}
		return nodesync.Target{Node: c, TLS: opts.TLS.Source(n), Local: n.Address == "", Address: n.Address,
			Tunnel: func() (nodesync.TunnelView, bool) { return tunnels.View(n.ID) }}, nil
	}
	var panelTLS *tls.Config
	if !cfg.Dev {
		panelTLS = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: names.Get, NextProtos: []string{"h2", "http/1.1"}}
		// A node's reverse tunnel asks for its own ALPN, and only then is a client certificate
		// requested: a browser is never prompted for one. The certificate is checked against the
		// nodes' pins in the route itself, where the database is.
		// The node pins the panel's certificate from its join key (opts.PanelCert, the pair the
		// keys carry), not the panel's public one: the tunnel's TLS presents that pair, so the
		// pin matches even when the panel's own certificate is a bare-IP self-signed one.
		var tunnelCert []tls.Certificate
		if panelPair, perr := opts.PanelCert(); perr == nil {
			if c, cerr := tls.X509KeyPair([]byte(panelPair.CertPEM), []byte(panelPair.KeyPEM)); cerr == nil {
				tunnelCert = []tls.Certificate{c}
			}
		}
		tunnelTLS := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: tunnelCert,
			NextProtos: []string{nodeapi.TunnelALPN}, ClientAuth: tls.RequireAnyClientCert}
		panelTLS.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			for _, p := range hello.SupportedProtos {
				if p == nodeapi.TunnelALPN {
					return tunnelTLS, nil
				}
			}
			return nil, nil
		}
	}
	listenHost, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %q: %w", cfg.Listen, err)
	}
	subPort := NewSubPort(listenHost, panelTLS, logger)
	defer subPort.Close()
	opts.SubPort, opts.SubPortError = subPort.Set, subPort.Error
	opts.SubDomain = func(name string) {
		names.SetName(name)
		if subCerts != nil {
			subCerts.Renew()
		}
	}
	p, err := NewPanel(st, opts)
	if err != nil {
		return err
	}
	// A certificate issued, renewed or uploaded later goes to the nodes at once: they get it
	// only with their state, which is sent again on a change.
	if certs != nil && p.Nodes != nil {
		certs.OnChange(p.Nodes.SlotsChanged)
		nodesACME.OnChange(func(id int64) {
			if s, ok := p.Nodes.Syncer(id); ok {
				s.SlotsChanged()
			}
		})
	}
	paths, err := p.Apply(ctx)
	if err != nil {
		return err
	}
	if paths.Admin == "" {
		logger.Warn("panel is not initialized yet: run `mikan admin bootstrap`")
	}
	subPort.SetHandler(p.SubOnly())
	if port, _, err := settings.Get[int](ctx, settings.New(st.Q), settings.KeySubPort); err == nil {
		subPort.Start(port)
	}
	// The workers (node sync, billing, the bot, certificates) use the database: they are
	// stopped, and waited for, before it is closed, however Serve returns. The deferred
	// close of the store runs after this one.
	workers, stopWorkers := context.WithCancel(ctx)
	var running sync.WaitGroup
	defer func() {
		stopWorkers()
		done := make(chan struct{})
		go func() { running.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(workerStopTimeout):
			logger.Warn("workers did not stop in time; closing the database under them")
		}
	}()
	running.Go(func() { p.Run(workers) })
	if certs != nil {
		running.Go(func() { certs.Run(workers) })
		running.Go(func() { subCerts.Run(workers) })
		running.Go(func() { nodesACME.Run(workers) })
		// kill -HUP (an external tool's renewal hook) serves a new custom certificate now.
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		defer signal.Stop(hup)
		running.Go(func() {
			for {
				select {
				case <-workers.Done():
					return
				case <-hup:
					certs.Renew()
					subCerts.Renew()
				}
			}
		})
	}

	httpSrv := httpServer(subDomainRouter{names: names, sub: p.SubOnly(), all: p.Handler})
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	if panelTLS != nil {
		ln = tls.NewListener(ln, panelTLS)
	}
	logger.Info("panel started", "listen", cfg.Listen, "tls", !cfg.Dev, "version", version)

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

// httpServer is the panel's HTTP server, on its own port and on the subscription port.
func httpServer(h http.Handler) *http.Server {
	srv := &http.Server{
		// A node's reverse tunnel negotiates its own ALPN, and net/http serves nothing on such
		// a connection: the hook serves HTTP/1.1 on it, and the panel's routes take it from
		// there (see reverse.go).
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){
			nodeapi.TunnelALPN: serveTunnelProto,
		},
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// A reader slower than this does not hold a connection: the longest answers (a
		// certificate ordered on the spot, 90 s; a node's speed test, 100 s) fit well within.
		WriteTimeout:   3 * time.Minute,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 64 << 10,
		ErrorLog:       log.New(dropHandshakeNoise{}, "", log.LstdFlags),
	}
	// A non-nil TLSNextProto turns net/http's automatic HTTP/2 off, while the panel's TLS
	// config still advertises h2: a browser would pick h2, be read as HTTP/1.1 and get its
	// connection dropped ("the server unexpectedly disconnected"). The h2 handler goes in
	// beside the tunnel's own protocol.
	if err := http2.ConfigureServer(srv, &http2.Server{}); err != nil {
		srv.ErrorLog.Printf("http2: %v", err)
	}
	return srv
}

// NodeClient reaches a node's API as the running panel does: the local node over its
// socket, a remote one over TLS with the panel's client certificate and the node's pin.
// The CLI uses it too, from inside the panel's container.
func NodeClient(cfg config.Config, n db.Node) (*nodeapi.Client, error) {
	return nodeClient(cfg, n, nil)
}

// nodeClient is NodeClient, and takes the node's reverse tunnel when it is connected: the
// node opened the connection out to the panel, and the panel asks over that. The TLS inside
// the stream is the same as on a direct connection — the node's certificate pinned by the
// panel, the panel's by the node — so a node that listens on no port at all is reached just
// the same.
func nodeClient(cfg config.Config, n db.Node, tn *tunnels) (*nodeapi.Client, error) {
	if n.Address == "" {
		if cfg.NodeSocket == "" {
			return nil, nodesync.ErrNoNode
		}
		return nodeapi.NewUnixClient(cfg.NodeSocket), nil
	}
	panel, err := nodetls.LoadOrCreate(filepath.Join(cfg.DataDir, "tls", "nodes"), time.Now())
	if err != nil {
		return nil, err
	}
	tc, err := nodetls.ClientConfig(panel, n.CertSha256)
	if err != nil {
		return nil, err
	}
	if tn == nil {
		return nodeapi.NewTLSClient(n.Address, tc), nil
	}
	dial := func(ctx context.Context) (net.Conn, error) {
		if stream, ok := tn.Open(ctx, n.ID); ok {
			return stream, nil
		}
		// No tunnel now (or it cannot take another stream): the direct path, which is the only
		// one a node that listens on its API port has.
		var d net.Dialer
		return d.DialContext(ctx, "tcp", n.Address)
	}
	return nodeapi.NewDialClient(n.Address, tc, dial), nil
}

// Internet scanners produce a constant stream of failed TLS handshakes; they are not actionable.
type dropHandshakeNoise struct{}

func (dropHandshakeNoise) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("TLS handshake error")) {
		return len(p), nil
	}
	return os.Stderr.Write(p)
}
