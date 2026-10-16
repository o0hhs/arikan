package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"mikan/internal/panel/config"
	"mikan/internal/panel/pgdump"
	"mikan/internal/panel/store"
)

func databaseCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("database needs migrate, backup FILE or restore FILE")
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	dsn, err := store.DatabaseURL()
	if err != nil {
		return err
	}
	switch args[0] {
	case "migrate":
		if len(args) != 1 {
			return errors.New("usage: mikan database migrate")
		}
		res, err := store.Migrate(ctx, dsn, cfg.DataDir)
		if err != nil {
			return err
		}
		// The installer reads this line: an unchanged schema lets a failed update go back.
		fmt.Printf("PostgreSQL schema version: %d -> %d\n", res.Before, res.After)
		return nil
	case "backup":
		if len(args) != 2 {
			return errors.New("usage: mikan database backup FILE")
		}
		if err := pgdump.DatabaseBackup(ctx, dsn, args[1]); err != nil {
			return err
		}
		fmt.Println("Database copied to", args[1])
		return nil
	case "restore":
		if len(args) != 2 {
			return errors.New("usage: mikan database restore FILE (panel must be stopped)")
		}
		f, err := os.Open(args[1])
		if err != nil {
			return err
		}
		magic := make([]byte, 16)
		_, readErr := io.ReadFull(f, magic)
		f.Close()
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%s is too short to be a database backup", args[1])
		} else if readErr != nil {
			return readErr
		}
		if string(magic) == "SQLite format 3\x00" {
			return errors.New("this is a database of mikan 0.4 (SQLite): mikan 0.5.0.4 restores it and moves it to PostgreSQL; restore it there, then update")
		} else if string(magic[:5]) == "PGDMP" {
			if err := pgdump.Check(ctx, args[1]); err != nil {
				return err
			}
			err = store.RestorePostgres(ctx, dsn, cfg.DataDir, func(ctx context.Context) error {
				// An empty --dbname makes pg_restore connect (to the database PGDATABASE names,
				// like every other parameter here) instead of printing an SQL script.
				return pgdump.Tool(ctx, dsn, nil, "pg_restore", "--dbname=", "--clean", "--if-exists", "--single-transaction", "--exit-on-error", "--no-owner", "--no-acl", args[1])
			})
		} else {
			return errors.New("unsupported database backup format")
		}
		// The restored data are in place; only the old copy is left to clean up.
		var leftover *store.LeftoverSchemaError
		if errors.As(err, &leftover) {
			fmt.Fprintln(os.Stderr, "Warning:", leftover)
		} else if err != nil {
			return err
		}
		fmt.Println("Database restored; start the panel after restoring its matching files")
		return nil
	default:
		return fmt.Errorf("unknown database command %q", args[0])
	}
}
