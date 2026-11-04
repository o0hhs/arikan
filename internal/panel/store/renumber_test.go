package store

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

// The map covers every migration 0.5.5.6 had below its top, and no target repeats: a
// repeated one would overwrite another row and lose a version.
func TestLegacyMigrationsMapIsComplete(t *testing.T) {
	seenOld := map[int64]bool{}
	seenNew := map[int64]bool{}
	for _, m := range legacyMigrations {
		if seenOld[m.Old] {
			t.Fatalf("%d twice", m.Old)
		}
		seenOld[m.Old] = true
		if m.New == 0 {
			continue
		}
		if seenNew[m.New] {
			t.Fatalf("%d twice as a target", m.New)
		}
		seenNew[m.New] = true
		if _, ok := versionOfFile(migrationName(m.New)); !ok {
			t.Fatalf("%d -> %d: no file of that number", m.Old, m.New)
		}
	}
	// 37 was never a file of 0.5.5.6 (its numbers skipped it).
	for v := int64(32); v <= legacyTop; v++ {
		if v == 37 {
			continue
		}
		if !seenOld[v] {
			t.Fatalf("%d is missing", v)
		}
	}
}

// migrationName is the file name of a version, from the embedded files.
func migrationName(v int64) string {
	entries, _ := fs.Glob(postgresFS, fmt.Sprintf("%04d_*.sql", v))
	if len(entries) != 1 {
		return ""
	}
	name := entries[0]
	return strings.TrimSuffix(name[strings.IndexByte(name, '_')+1:], ".sql")
}

// A database the fork's 0.5.5.6 left moves to the current schema: every version above the
// old top is applied, and the history has no duplicate.
func TestRenumberLegacyUpgradesA0556Database(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// The fixture is fresh (at the current schema): its history is replaced by 0.5.5.6's,
	// the way a database updated from that release has it.
	if _, err := s.DB.ExecContext(ctx, "DELETE FROM goose_db_version"); err != nil {
		t.Fatal(err)
	}
	for v := int64(0); v <= 31; v++ {
		if _, err := s.DB.ExecContext(ctx, "INSERT INTO goose_db_version (version_id, is_applied, tstamp) VALUES ($1, true, now())", v); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range legacyMigrations {
		if _, err := s.DB.ExecContext(ctx, "INSERT INTO goose_db_version (version_id, is_applied, tstamp) VALUES ($1, true, now())", m.Old); err != nil {
			t.Fatal(err)
		}
	}
	if v, _ := legacyVersion(ctx, s.DB); v != legacyTop {
		t.Fatalf("the fixture is at %d, want %d", v, legacyTop)
	}
	if err := renumberLegacy(ctx, s.DB); err != nil {
		t.Fatal(err)
	}
	if v, _ := legacyVersion(ctx, s.DB); v <= legacyTop {
		t.Fatalf("after the move the version is %d", v)
	}
	if _, err := postgresProvider(ctx, s.DB, postgresFS); err != nil {
		t.Fatalf("the moved history does not take the files: %v", err)
	}
}
