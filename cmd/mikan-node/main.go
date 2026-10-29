// mikan-node runs the VPN data plane: the Citrus core (a fork of mihomo) embedded as a
// library, controlled by the panel over a unix socket, or over pinned TLS when the node runs
// on another server. It links the core and is therefore distributed under GPL-3.0.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mikan/internal/acmechallenge"
	"mikan/internal/logbuf"
	"mikan/internal/node"
	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/config"
)

var version = "dev"

func main() {
	// The installer checks a join key before it sets the node up and learns the API port
	// from it (key-port), and the node's address as the panel has it, for its advice (key-host;
	// empty for keys of older panels); the key comes in the environment so it does not show in
	// the process list.
	if len(os.Args) > 1 && (os.Args[1] == "key-port" || os.Args[1] == "key-host") {
		printJoinKey(os.Args[1])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "hello" {
		helloOnce()
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mikan-node:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The last lines of the log, secrets cut out, for the panel's Diagnostics page.
	logs := logbuf.New(logbuf.DefaultLines)
	log := slog.New(logs.Handler(slog.NewTextHandler(os.Stderr, nil)))
	dataDir := envOr("MIKAN_DATA_DIR", config.DefaultDataDir)
	sock := envOr("MIKAN_NODE_SOCKET", config.DefaultNodeSocket)

	release, err := time.ParseDuration(envOr("MIKAN_DEVICE_RELEASE", "60s"))
	if err != nil {
		return fmt.Errorf("MIKAN_DEVICE_RELEASE: %w", err)
	}
	allowPrivate, err := strconv.ParseBool(envOr("MIKAN_ALLOW_PRIVATE", "false"))
	if err != nil {
		return fmt.Errorf("MIKAN_ALLOW_PRIVATE: %w", err)
	}
	// The panel's HTTP-01 challenges for this node's certificate: port 80, unless a web
	// server of the admin's holds it and passes /.well-known/acme-challenge/ on.
	acmeListen, err := acmechallenge.ParseListen(os.Getenv("MIKAN_ACME_LISTEN"))
	if err != nil {
		return fmt.Errorf("MIKAN_ACME_LISTEN: %w", err)
	}
	// Port 80: the panel's HTTP-01 challenges, and a 301 to HTTPS for everything else (a
	// browser reaching the node's domain over http lands on the node's own page). A node of
	// its own server (it has a join key) takes the port by default; the panel's own node
	// leaves it alone, the panel answers port 80 there. MIKAN_HTTP_REDIRECT says it outright.
	defRedirect := os.Getenv("MIKAN_NODE_JOIN") != ""
	redirect, err := strconv.ParseBool(envOr("MIKAN_HTTP_REDIRECT", strconv.FormatBool(defRedirect)))
	if err != nil {
		return fmt.Errorf("MIKAN_HTTP_REDIRECT: %w", err)
	}
	// The join key is the node's identity to its panel: the API's certificate on its server,
	// and the certificate it signs the cascade's stream with (nodeapi.Cascade). The panel's
	// own node has no key.
	raw, err := config.Secret("MIKAN_NODE_JOIN")
	if err != nil {
		return err
	}
	var join nodetls.Key
	if raw != "" {
		if join, err = nodetls.DecodeKey(raw); err != nil {
			return fmt.Errorf("MIKAN_NODE_JOIN: %w", err)
		}
	}
	eng, err := node.Start(node.Options{DataDir: dataDir, Version: version, Log: log, DeviceRelease: release, AllowPrivate: allowPrivate, ACMEListen: acmeListen, Logs: logs, Join: join})
	if err != nil {
		return err
	}
	if redirect {
		eng.Challenge.SetRedirect(true)
		// A web server of the admin's may hold the port; the challenges then come through it
		// (MIKAN_ACME_LISTEN) and there is nothing to redirect on this side.
		if err := eng.Challenge.Start(); err != nil {
			log.Warn("no HTTP redirect on port 80", "listen", eng.Challenge.Addr(), "err", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		return err
	}
	srv := &http.Server{Handler: node.Handler(eng, log), ReadHeaderTimeout: 10 * time.Second}
	serve := func(ln net.Listener) {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("node api", "err", err)
			stop()
		}
	}
	go serve(ln)
	log.Info("node started", "version", version, "socket", sock)
	// The state from before the restart comes back behind the API, which answers already.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("node: the restore panicked", "panic", r)
			}
		}()
		eng.Restore()
	}()
	// A remote node also serves the Node API to its panel (see nodetls): on its own port, and
	// over the tunnel it opens out to the panel.
	var inbound *node.InboundAPI
	if raw != "" {
		cfg, err := join.ServerConfig()
		if err != nil {
			return fmt.Errorf("MIKAN_NODE_JOIN: %w", err)
		}
		// The node's own port for the panel. The panel may tell the node to keep it closed and
		// reach it over the reverse tunnel alone (DesiredState.InboundAPI), and the node keeps
		// that answer across restarts; its server's admin can keep the port closed whatever the
		// panel says (MIKAN_NODE_INBOUND_API=off, or MIKAN_NODE_API_LISTEN=off).
		addr := net.JoinHostPort(envOr("MIKAN_NODE_API_LISTEN", ""), strconv.Itoa(join.Port))
		inbound = node.NewInboundAPI(addr, cfg, node.Handler(eng, log), eng.APIListener, log)
		eng.UseInboundAPI(inbound)
		if inboundAPIAllowed() && node.SavedInboundAPI(dataDir) {
			inbound.Set(true)
		} else {
			log.Info("the API port stays closed: the reverse tunnel is the panel's only way in", "port", join.Port)
		}
		log.Info("node api for the panel", "port", join.Port)
		// The tunnel the node opens out to its panel: the way round that works when the panel
		// cannot reach the node at all. It needs the panel's address from the join key.
		if join.PanelURL != "" {
			if pair, err := tls.X509KeyPair([]byte(join.CertPEM), []byte(join.KeyPEM)); err != nil {
				log.Error("reverse tunnel: the node's certificate is unreadable", "err", err)
			} else {
				go func() {
					defer func() {
						if r := recover(); r != nil {
							log.Error("node: the reverse tunnel panicked", "panic", r)
						}
					}()
					node.RunReverseTunnel(ctx, node.ReverseTunnel{
						URL: join.PanelURL,
						// The node's own certificate as a client certificate: the panel accepts it
						// by the pin it holds, and only a client that asks for the tunnel's ALPN is
						// asked for a certificate at all.
						Client:  &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}, NextProtos: []string{nodeapi.TunnelALPN}},
						Server:  cfg,
						Handler: node.Handler(eng, log),
						Log:     log,
					})
				}()
			}
		} else {
			log.Info("no reverse tunnel: the join key comes from a panel before them")
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("node: the hello loop panicked", "panic", r)
				}
			}()
			helloLoop(ctx, join, dataDir, log)
		}()
	}

	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := eng.PersistCounters(); err != nil {
				log.Error("persist counters", "err", err)
			}
			eng.TickAPILock()
		case <-ctx.Done():
			if inbound != nil {
				inbound.Close()
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			return eng.PersistCounters()
		}
	}
}

// inboundAPIAllowed says whether the node's own API port may be opened at all: the server's
// admin keeps it closed whatever the panel says (MIKAN_NODE_INBOUND_API=off, or
// MIKAN_NODE_API_LISTEN=off), and the panel then has the reverse tunnel alone.
func inboundAPIAllowed() bool {
	return !strings.EqualFold(os.Getenv("MIKAN_NODE_INBOUND_API"), "off") &&
		!strings.EqualFold(os.Getenv("MIKAN_NODE_API_LISTEN"), "off")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// printJoinKey prints one field of the join key in MIKAN_NODE_JOIN for the installer:
// the port of the node's API for "key-port", its address for "key-host". A key that does
// not decode stops the installer with the reason.
func printJoinKey(field string) {
	key, err := nodetls.DecodeKey(os.Getenv("MIKAN_NODE_JOIN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "mikan-node:", err)
		os.Exit(1)
	}
	if field == "key-port" {
		fmt.Println(key.Port)
		return
	}
	fmt.Println(key.Host)
}
