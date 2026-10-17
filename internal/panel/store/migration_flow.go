package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"strings"
)

// MigrationSet is a digest of the embedded PostgreSQL migrations — their names and their
// contents. Fixtures that prepare a database once and clone it put it in the template
// database's name, so a migration added or edited anywhere means a new template instead of
// a clone of a stale one.
func MigrationSet() (string, error) {
	entries, err := fs.ReadDir(postgresFS, ".")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		data, err := fs.ReadFile(postgresFS, e.Name())
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", e.Name(), len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// MigrateResult is what Migrate did: the schema versions before and after it.
type MigrateResult struct {
	Before, After int64
}

// Migrate applies the PostgreSQL migrations; it refuses to run beside a running panel, and
// on data still in SQLite (checkLegacy).
func Migrate(ctx context.Context, dsn, dataDir string) (MigrateResult, error) {
	var res MigrateResult
	conn, err := connectPostgres(ctx, dsn, maintenanceSession)
	if err != nil {
		return res, err
	}
	defer conn.Close()
	guard, err := conn.Conn(ctx)
	if err != nil {
		return res, err
	}
	defer guard.Close()
	unlock, err := lockOffline(ctx, guard)
	if err != nil {
		return res, err
	}
	defer unlock()
	if err := recoverRestore(ctx, conn); err != nil {
		return res, err
	}
	if err := checkLegacy(ctx, conn, dataDir); err != nil {
		return res, err
	}
	if res.Before, err = schemaVersion(ctx, conn); err != nil {
		return res, err
	}
	if err := migrateUp(ctx, conn, postgresFS); err != nil {
		return res, err
	}
	res.After, err = schemaVersion(ctx, conn)
	return res, err
}

// openMaintenance opens maintenance connections, after bringing back what an interrupted
// restore set aside.
func openMaintenance(ctx context.Context, dsn string) (*sql.DB, error) {
	conn, err := connectPostgres(ctx, dsn, maintenanceSession)
	if err != nil {
		return nil, err
	}
	if err := recoverRestore(ctx, conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// migrateUp applies every pending PostgreSQL migration.
func migrateUp(ctx context.Context, conn *sql.DB, fsys fs.FS) error {
	p, err := postgresProvider(ctx, conn, fsys)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("PostgreSQL migrations: %w", err)
	}
	return nil
}

// schemaVersion is the applied PostgreSQL migration version.
func schemaVersion(ctx context.Context, conn *sql.DB) (int64, error) {
	p, err := postgresProvider(ctx, conn, postgresFS)
	if err != nil {
		return 0, err
	}
	return p.GetDBVersion(ctx)
}
