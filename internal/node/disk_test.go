package node

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, size)
	for i := range raw {
		raw[i] = byte('a' + i%26)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// testEngine is an engine with a scratch data dir; the disk methods only read the dir and
// clean junk in it.
func testEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	e := &Engine{dataDir: dir, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return e, dir
}

func TestScanDiskFindsDirsAndJunk(t *testing.T) {
	e, dir := testEngine(t)
	writeFile(t, filepath.Join(dir, "update", "request"), 10)
	writeFile(t, filepath.Join(dir, "update", "status.json"), 20)
	writeFile(t, filepath.Join(dir, "update", "leftover.tmp"), 30)
	writeFile(t, filepath.Join(dir, "update", "old-0.5.1.3.tar.gz"), 40)
	writeFile(t, filepath.Join(dir, "mihomo", "tls", "node.crt"), 50)
	writeFile(t, filepath.Join(dir, "site", "www", "index.html"), 60)
	writeFile(t, filepath.Join(dir, "node-state.json.tmp"), 70)

	s := scanDisk(dir, e.log)
	if s.junk != 30+40+70 {
		t.Fatalf("junk = %d, want %d", s.junk, 30+40+70)
	}
	want := []string{"node-state.json.tmp", "update/leftover.tmp", "update/old-0.5.1.3.tar.gz"}
	if len(s.files) != len(want) {
		t.Fatalf("junk files = %v, want %v", s.files, want)
	}
	for i, f := range s.files {
		if f != want[i] {
			t.Fatalf("junk file %d = %q, want %q", i, f, want[i])
		}
	}
	// The directories come biggest first: mihomo carries the cert, update the leftovers.
	if len(s.dirs) < 3 {
		t.Fatalf("dirs = %v, want at least update, mihomo, site", s.dirs)
	}
	if s.dirs[0].Path != "mihomo" || s.dirs[0].Bytes != 50 {
		t.Fatalf("biggest dir = %+v, want mihomo 50", s.dirs[0])
	}
}

func TestCleanDiskRemovesOnlyJunk(t *testing.T) {
	e, dir := testEngine(t)
	writeFile(t, filepath.Join(dir, "update", "request"), 10)
	writeFile(t, filepath.Join(dir, "update", "status.json"), 20)
	writeFile(t, filepath.Join(dir, "mihomo", "tls", "node.crt"), 50)
	writeFile(t, filepath.Join(dir, "update", "leftover.tmp"), 30)
	writeFile(t, filepath.Join(dir, "node-state.json.tmp"), 70)

	st := e.CleanDisk()
	if st.Freed != 30+70 {
		t.Fatalf("freed = %d, want %d", st.Freed, 30+70)
	}
	if st.Junk != 0 || len(st.Files) != 0 {
		t.Fatalf("after clean: junk = %d files = %v, want none", st.Junk, st.Files)
	}
	for _, rel := range []string{"update/request", "update/status.json", "mihomo/tls/node.crt"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("%s was removed: %v", rel, err)
		}
	}
	for _, rel := range []string{"update/leftover.tmp", "node-state.json.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", rel, err)
		}
	}
}
