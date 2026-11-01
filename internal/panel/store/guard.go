package store

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// The panel guard keeps database migrate and restore off a running panel: the panel holds
// it shared for as long as it runs, they take it exclusively or refuse. A backup takes no
// part in it: pg_dump reads a consistent snapshot beside the running panel.
const panelGuard = "panel"

// guardCheck is how often the panel looks at the session that holds its guard. A restart
// of PostgreSQL drops that session and the lock with it; until the panel takes the lock
// again, migrate and restore would run on the database it is using.
const guardCheck = 2 * time.Second

// guardHold is the panel's shared advisory lock, taken again when its session dies.
type guardHold struct {
	dsn  string
	mu   sync.Mutex
	conn *sql.DB
	c    *sql.Conn
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// LockPanel holds the panel guard shared on a connection of its own until release. A
// migrate or restore in progress makes it fail rather than wait: the panel would start on
// a schema in the middle of a change. The lock is taken again if that connection dies
// (PostgreSQL restarted), so the panel does not have to be restarted to keep the guard.
func LockPanel(ctx context.Context, dsn string) (release func() error, err error) {
	g := &guardHold{dsn: dsn, stop: make(chan struct{}), done: make(chan struct{})}
	if err := g.acquire(ctx); err != nil {
		return nil, err
	}
	go g.watch()
	return g.release, nil
}

// acquire opens a session and takes the shared guard on it. The session already held, if
// it is still there, is unlocked only after the new one has the lock, so the guard is
// never given up in between.
func (g *guardHold) acquire(ctx context.Context) error {
	conn, err := connectPostgres(ctx, g.dsn, runtimeSession)
	if err != nil {
		return err
	}
	conn.SetMaxOpenConns(1)
	c, err := conn.Conn(ctx)
	if err != nil {
		conn.Close()
		return err
	}
	_, id, err := schemaLock(ctx, c, panelGuard)
	var locked bool
	if err == nil {
		err = c.QueryRowContext(ctx, "SELECT pg_try_advisory_lock_shared($1)", id).Scan(&locked)
	}
	if err == nil && !locked {
		err = errors.New("a database migrate or restore is running; start the panel after it ends")
	}
	if err != nil {
		_ = errors.Join(c.Close(), conn.Close())
		return err
	}
	g.mu.Lock()
	select {
	case <-g.stop:
		g.mu.Unlock()
		unlockConn(c)
		_ = errors.Join(c.Close(), conn.Close())
		if err := ctx.Err(); err != nil {
			return err
		}
		return errors.New("panel guard stopped")
	default:
	}
	oldC, oldConn := g.c, g.conn
	g.c, g.conn = c, conn
	g.mu.Unlock()
	// The new session already holds the guard. The previous one is released only now, so
	// there is no moment with neither.
	dropGuard(oldC, oldConn)
	return nil
}

func (g *guardHold) watch() {
	defer close(g.done)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-g.stop
		cancel()
	}()
	tick := time.NewTicker(guardCheck)
	defer tick.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if g.healthy() {
				continue
			}
			err := g.acquire(ctx)
			if err != nil && ctx.Err() == nil {
				fails++
				if fails == 1 || fails%5 == 0 {
					slog.Warn("panel guard: the database lock is not held", "err", err)
				}
				continue
			}
			if err == nil && fails > 0 {
				slog.Info("panel guard: the database lock is held again")
				fails = 0
			}
		}
	}
}

func (g *guardHold) healthy() bool {
	g.mu.Lock()
	c := g.c
	g.mu.Unlock()
	if c == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return c.PingContext(ctx) == nil
}

func (g *guardHold) release() error {
	g.once.Do(func() { close(g.stop) })
	<-g.done
	g.mu.Lock()
	defer g.mu.Unlock()
	c, conn := g.c, g.conn
	g.c, g.conn = nil, nil
	// Closing the pool ends the session, and the session's locks with it, but the server
	// lets them go only after the client is gone: unlock first, so a migrate started right
	// after release does not still find the guard taken.
	err := unlockConn(c)
	if c != nil {
		err = errors.Join(err, c.Close())
	}
	if conn != nil {
		err = errors.Join(err, conn.Close())
	}
	return err
}

// unlockConn gives back the shared guard held on c. A dead session has nothing to give
// back; closing it is the caller's.
func unlockConn(c *sql.Conn) error {
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, id, err := schemaLock(ctx, c, panelGuard)
	if err != nil {
		return err
	}
	_, err = c.ExecContext(ctx, "SELECT pg_advisory_unlock_shared($1)", id)
	return err
}

func dropGuard(c *sql.Conn, conn *sql.DB) {
	_ = unlockConn(c)
	if c != nil {
		_ = c.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
}

// lockOffline takes the panel guard exclusively on c's session; unlock gives it back.
func lockOffline(ctx context.Context, c *sql.Conn) (unlock func(), err error) {
	_, id, err := schemaLock(ctx, c, panelGuard)
	if err != nil {
		return nil, err
	}
	var locked bool
	if err := c.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", id).Scan(&locked); err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("the panel is running (or another database migrate or restore is): stop it first")
	}
	return func() { c.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", id) }, nil
}
