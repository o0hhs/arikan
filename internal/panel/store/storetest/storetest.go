// Package storetest opens stores for tests, each in a PostgreSQL database of its own on
// the disposable server MIKAN_TEST_DATABASE_URL names. It stays out of the panel binary.
//
// A fixture does not migrate. The first store opened on a server prepares one template
// database — the embedded migrations, once — and every fixture after it is a clone of that
// template: CREATE DATABASE ... TEMPLATE costs a file copy where the migrations cost
// seconds, and the panel's tests open a store in almost every case. Their own databases
// also make a fixture a little server of its own: nothing shares a schema, and a test that
// opens a second connection (a CLI, a node, a pg_dump of the installation) reaches the
// fixture's data by its database name alone.
//
// The template stays behind, one database per migration set: that is what makes the next
// run fast, and the server it lives on is a disposable one.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"

	"mikan/internal/panel/store"
)

// templateLock serializes preparing the template database, so the parallel test binaries
// of one `go test` share the first one's work instead of migrating each for themselves.
const templateLock int64 = 0x6d696b616e01 // "mikan" + 1

// Open gives each fixture its own database, dropped when the store closes. Reopening the
// same directory opens the same database again, like the old schema-backed fixtures did.
func Open(ctx context.Context, dataDir string) (*store.Store, error) {
	dsn, err := serverDSN()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, ".test-postgres-database")
	name, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		name = []byte(newDatabaseName())
		if err := createFromTemplate(ctx, dsn, string(name)); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, name, 0o600); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		// The directory was used before: its database may have been dropped when that
		// store closed, and a name alone would meet "database does not exist".
		exists, err := databaseExists(ctx, dsn, string(name))
		if err != nil {
			return nil, err
		}
		if !exists {
			if err := createFromTemplate(ctx, dsn, string(name)); err != nil {
				return nil, err
			}
		}
	}
	return openDatabase(ctx, dsn, string(name))
}

// serverDSN is the disposable server every fixture lives on.
func serverDSN() (string, error) {
	dsn := os.Getenv("MIKAN_TEST_DATABASE_URL")
	if dsn == "" {
		return "", errors.New("tests require MIKAN_TEST_DATABASE_URL pointing to a disposable PostgreSQL database")
	}
	return dsn, nil
}

// newDatabaseName is a fresh name for a fixture's database.
func newDatabaseName() string {
	var token [12]byte
	rand.Read(token[:])
	return "mikan_test_" + hex.EncodeToString(token[:])
}

// openDatabase opens the fixture's database; it is dropped when the store closes.
func openDatabase(ctx context.Context, dsn, name string) (*store.Store, error) {
	return store.OpenMigrated(ctx, withDatabase(dsn, name), func() error {
		return exec(context.Background(), dsn, "DROP DATABASE IF EXISTS "+quote(name)+" WITH (FORCE)")
	})
}

// createFromTemplate clones the server's template database into name.
func createFromTemplate(ctx context.Context, dsn, name string) error {
	tpl, err := template(ctx, dsn)
	if err != nil {
		return err
	}
	return exec(ctx, dsn, "CREATE DATABASE "+quote(name)+" TEMPLATE "+quote(tpl))
}

// template migrates the database every fixture clones, once per server. Its name carries
// the migrations' digest, so a checkout whose migrations changed prepares a template of its
// own instead of cloning a stale one, and it is prepared under an advisory lock the other
// test binaries wait on.
func template(ctx context.Context, dsn string) (string, error) {
	set, err := store.MigrationSet()
	if err != nil {
		return "", err
	}
	name := "mikan_test_template_" + set
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return "", err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	// The lock lives on this connection and goes when it closes.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", templateLock); err != nil {
		return "", err
	}
	exists, err := databaseExistsOn(ctx, conn, name)
	if err != nil {
		return "", err
	}
	if exists {
		return name, nil
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+quote(name)); err != nil {
		return "", err
	}
	st, err := store.OpenMigrated(ctx, withDatabase(dsn, name), nil)
	if err != nil {
		// A half-migrated template would be cloned by every fixture after it.
		_ = exec(context.WithoutCancel(ctx), dsn, "DROP DATABASE IF EXISTS "+quote(name)+" WITH (FORCE)")
		return "", err
	}
	if err := st.Close(); err != nil {
		return "", err
	}
	return name, nil
}

// databaseExists says whether a database of that name is on the server.
func databaseExists(ctx context.Context, dsn, name string) (bool, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return false, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	return databaseExistsOn(ctx, conn, name)
}

func databaseExistsOn(ctx context.Context, conn *pgx.Conn, name string) (bool, error) {
	var exists bool
	err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)", name).Scan(&exists)
	return exists, err
}

// withDatabase is dsn pointed at another database on the same server: the name replaces
// the one in the path, everything else (host, credentials, options) stays.
func withDatabase(dsn, name string) string {
	if u, err := url.Parse(dsn); err == nil && strings.Contains(dsn, "://") {
		u.Path = "/" + name
		return u.String()
	}
	kept := make([]string, 0, 4)
	for _, field := range strings.Fields(dsn) {
		if strings.HasPrefix(field, "dbname=") {
			continue
		}
		kept = append(kept, field)
	}
	return strings.Join(append(kept, "dbname="+name), " ")
}

// quote is a database name as SQL.
func quote(name string) string { return pgx.Identifier{name}.Sanitize() }

// exec runs one statement on the server itself.
func exec(ctx context.Context, dsn, sql string) error {
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer c.Close(context.Background())
	_, err = c.Exec(ctx, sql)
	return err
}
