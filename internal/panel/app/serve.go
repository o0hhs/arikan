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
	"strconv"
	"sync"
	"syscall"
	"time"

	"mikan/internal/logbuf"
	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/acme"
	"mikan/internal/panel/autotune"
	"mikan/internal/panel/checkhost"
	"mikan/internal/panel/config"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/ipgeo"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/pgdump"
	"mikan/internal/panel/reach"
	"mikan/internal/panel/s3backup"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/support"
	"mikan/internal/panel/tlscert"
	"mikan/internal/panel/updates"
	"mikan/internal/panel/webproxy"
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
	// An S3 restore prepared by the previous run is loaded before the panel guard is taken
	// and before the store opens: pg_restore needs the database to itself.
	restored := restorePending(ctx, dsn, cfg.DataDir, logger)
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
		DataDir: cfg.DataDir, Dev: cfg.Dev, Releases: updates.Fetch(release.IndexURL, release.LatestURL), DNS: dnscheck.New(), CheckHost: checkhost.New("", nil, time.Now), Reach: reach.New("", nil, time.Now), Geo: ipgeo.New(nil, time.Now), Logs: logs}
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
	set := settings.New(st.Q)
	if restored {
		if err := s3backup.MarkRestored(ctx, set, time.Now()); err != nil {
			logger.Warn("pending restore: the restore state was not updated", "err", err)
		}
	}
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
				if c, err := NodeClient(cfg, n); err == nil {
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
	opts.Connect = func(n db.Node) (nodesync.Target, error) {
		c, err := NodeClient(cfg, n)
		if err != nil {
			return nodesync.Target{}, err
		}
		return nodesync.Target{Node: c, TLS: opts.TLS.Source(n), SubsTLS: subsTLSSource(opts.SubCerts), Local: n.Address == "", Address: n.Address}, nil
	}
	var panelTLS *tls.Config
	if !cfg.Dev {
		panelTLS = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: names.Get, NextProtos: []string{"h2", "http/1.1"}}
	}
	listenHost, listenPortText, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %q: %w", cfg.Listen, err)
	}
	// The listener is this server's own (PANEL_PORT in config.json); the panel_port setting
	// is what every link, the admin path and each node's address are built from. A database
	// restored from another server (a .dump, the Telegram backup) brings that server's
	// panel_port, while the listen port stays the one this server was installed with: the
	// panel would advertise a port nothing answers on, and every node would be given the
	// wrong address. The panel cannot move itself onto the setting, so the setting follows
	// the listener.
	if !cfg.Dev {
		if listenPort, err := strconv.Atoi(listenPortText); err == nil {
			set := settings.New(st.Q)
			if port, _, err := settings.Get[int](ctx, set, settings.KeyPanelPort); err == nil && port != 0 && port != listenPort {
				logger.Warn("the panel port setting does not match the listener; correcting the setting", "setting", port, "listen", listenPort)
				if err := settings.Set(ctx, set, settings.KeyPanelPort, listenPort); err != nil {
					logger.Error("correct the panel port setting", "err", err)
				}
			}
		}
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
	// The panel behind the node's shared 443: a listener on loopback the node's SNI router
	// hands the panel's own names to, a PROXY v2 header with the client's address in front.
	// Without TLS (development) there is nothing a node could hand it.
	var webLn *webproxy.Listener
	if panelTLS != nil {
		if webLn, err = webproxy.Listen("127.0.0.1:0", logger); err != nil {
			logger.Error("web listener for the node's router: the panel's names stay on its own port", "err", err)
		} else {
			defer webLn.Close()
			opts.WebPort = webLn.Port()
		}
	}
	p, err := NewPanel(st, opts)
	if err != nil {
		return err
	}
	// A certificate issued, renewed or uploaded later goes to the nodes at once: they get it
	// only with their state, which is sent again on a change. The subscription domain's
	// certificate is the nodes' answers' own, so it moves their state too.
	if certs != nil && p.Nodes != nil {
		certs.OnChange(p.Nodes.SlotsChanged)
		nodesACME.OnChange(func(id int64) {
			if s, ok := p.Nodes.Syncer(id); ok {
				s.SlotsChanged()
			}
		})
		if subCerts != nil {
			subCerts.OnChange(p.Nodes.SlotsChanged)
		}
	}
	paths, err := p.Apply(ctx)
	if err != nil {
		return err
	}
	if paths.Admin == "" {
		logger.Warn("panel is not initialized yet: run `arikan admin bootstrap`")
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

	handler := subDomainRouter{names: names, sub: p.SubOnly(), all: p.Handler}
	httpSrv := httpServer(handler)
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	if panelTLS != nil {
		ln = tls.NewListener(ln, panelTLS)
	}
	// The loopback server behind the node's router: the same handler, the same certificate
	// by name. Its connections carry the client the node named in the PROXY header.
	var webSrv *http.Server
	if webLn != nil {
		webSrv = httpServer(handler)
		go func() {
			err := webSrv.Serve(tls.NewListener(webLn, panelTLS))
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				logger.Error("web listener", "err", err)
			}
		}()
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
	if webSrv != nil {
		_ = webSrv.Shutdown(shutdownCtx)
	}
	return httpSrv.Shutdown(shutdownCtx)
}

// restorePending loads data/restore/pending.dump, written by s3backup after a pre-restore
// backup and a check. A broken archive is moved aside so the panel still starts; a failed
// restore is reverted by store.RestorePostgres.
func restorePending(ctx context.Context, dsn, dataDir string, logger *slog.Logger) bool {
	if dataDir == "" {
		return false
	}
	path := s3backup.PendingRestorePath(dataDir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false
	} else if err != nil {
		logger.Error("pending restore", "err", err)
		return false
	}
	fail := func(err error) {
		failed := path + ".failed"
		_ = os.Remove(failed)
		if renameErr := os.Rename(path, failed); renameErr != nil {
			logger.Error("pending restore: could not move the dump aside", "err", errors.Join(err, renameErr))
			return
		}
		logger.Error("pending restore failed; the dump is kept aside", "path", failed, "err", err)
	}
	if err := pgdump.Check(ctx, path); err != nil {
		fail(err)
		return false
	}
	err := store.RestorePostgres(ctx, dsn, dataDir, func(ctx context.Context) error {
		return pgdump.Tool(ctx, dsn, nil, "pg_restore", "--dbname=", "--clean", "--if-exists", "--single-transaction", "--exit-on-error", "--no-owner", "--no-acl", path)
	})
	var leftover *store.LeftoverSchemaError
	if err != nil && !errors.As(err, &leftover) {
		fail(err)
		return false
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("pending restore: the dump was not removed", "err", err)
	}
	logger.Info("database restored from an S3 backup")
	return true
}

// httpServer is the panel's HTTP server, on its own port and on the subscription port.
func httpServer(h http.Handler) *http.Server {
	return &http.Server{
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
}

// NodeClient reaches a node's API as the running panel does: the local node over its
// socket, a remote one over TLS with the panel's client certificate and the node's pin.
// The CLI uses it too, from inside the panel's container.
func NodeClient(cfg config.Config, n db.Node) (*nodeapi.Client, error) {
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
	return nodeapi.NewTLSClient(n.Address, tc), nil
}

// subsTLSSource is the certificate the nodes that answer subscription links present: the
// subscription domain's own (acme.NewSub), which is not the node's. nil in development, and
// while the domain's certificate is not issued yet: the node then presents its own.
func subsTLSSource(m *acme.Manager) nodesync.SubsTLSSource {
	if m == nil {
		return nil
	}
	return func() (*nodeapi.TLSFiles, error) {
		cert := m.Public()
		if cert == nil {
			return nil, nil
		}
		certPEM, keyPEM, err := tlscert.CustomPEM(cert)
		if err != nil {
			return nil, err
		}
		return &nodeapi.TLSFiles{CertPEM: certPEM, KeyPEM: keyPEM}, nil
	}
}

// Internet scanners produce a constant stream of failed TLS handshakes; they are not actionable.
type dropHandshakeNoise struct{}

func (dropHandshakeNoise) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("TLS handshake error")) {
		return len(p), nil
	}
	return os.Stderr.Write(p)
}
