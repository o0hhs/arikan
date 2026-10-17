package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

// OpenTest is storetest.Open for this package's own tests, which cannot import storetest
// (it imports store): a fresh database, cloned from the server's template, dropped when the
// store closes. The template — a database the embedded migrations were applied to once — is
// the same one storetest prepares, so the lock and the name below must match its own.
func OpenTest(ctx context.Context, _ string) (*Store, error) {
	dsn := os.Getenv("MIKAN_TEST_DATABASE_URL")
	if dsn == "" {
		return nil, errors.New("tests require MIKAN_TEST_DATABASE_URL pointing to a disposable PostgreSQL database")
	}
	var token [12]byte
	rand.Read(token[:])
	name := "mikan_test_" + hex.EncodeToString(token[:])
	tpl, err := testTemplate(ctx, dsn)
	if err != nil {
		return nil, err
	}
	exec := func(ctx context.Context, sql string) error {
		return withConn(ctx, dsn, func(c *pgx.Conn) error { _, err := c.Exec(ctx, sql); return err })
	}
	if err := exec(ctx, "CREATE DATABASE "+quote(name)+" TEMPLATE "+quote(tpl)); err != nil {
		return nil, err
	}
	return OpenMigrated(ctx, testDSN(dsn, name), func() error {
		return exec(context.Background(), "DROP DATABASE IF EXISTS "+quote(name)+" WITH (FORCE)")
	})
}

// testTemplateLock is storetest's templateLock: one template database per server, prepared
// once even when this package's tests and the others' run at the same time.
const testTemplateLock int64 = 0x6d696b616e01

// testTemplate migrates the database every fixture clones and returns its name. The name
// carries the migrations' digest, so changed migrations mean a template of their own.
func testTemplate(ctx context.Context, dsn string) (string, error) {
	set, err := MigrationSet()
	if err != nil {
		return "", err
	}
	name := "mikan_test_template_" + set
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return "", err
	}
	defer c.Close(context.WithoutCancel(ctx))
	// The lock lives on this connection and goes when it closes.
	if _, err := c.Exec(ctx, "SELECT pg_advisory_lock($1)", testTemplateLock); err != nil {
		return "", err
	}
	var exists bool
	if err := c.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)", name).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return name, nil
	}
	if _, err := c.Exec(ctx, "CREATE DATABASE "+quote(name)); err != nil {
		return "", err
	}
	st, err := OpenMigrated(ctx, testDSN(dsn, name), nil)
	if err != nil {
		// A half-migrated template would be cloned by every fixture after it.
		_, _ = c.Exec(context.WithoutCancel(ctx), "DROP DATABASE IF EXISTS "+quote(name)+" WITH (FORCE)")
		return "", err
	}
	if err := st.Close(); err != nil {
		return "", err
	}
	return name, nil
}

// testDSN is dsn pointed at another database on the same server.
func testDSN(dsn, name string) string {
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
