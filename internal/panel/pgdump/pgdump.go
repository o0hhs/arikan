// Package pgdump copies the panel's PostgreSQL database. The archive the admin downloads
// from the panel, the one the bot sends to Telegram and the one "mikan database backup"
// writes are all made here: pg_dump reads one consistent snapshot even while the panel
// runs, and every archive is checked the same way before it is trusted — a backup must
// bring the panel's tables, not code.
package pgdump

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mikan/internal/panel/store"
)

// DatabaseBackup writes the panel's schema to path, as pg_dump reads it while the panel
// runs. The file must not exist: a backup never overwrites one.
func DatabaseBackup(ctx context.Context, dsn, path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	err = WriteTo(ctx, dsn, f)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return fmt.Errorf("PostgreSQL backup failed: %w", err)
	}
	return nil
}

// WriteTo streams the panel's schema into w in pg_dump's custom format. What the reader
// gets is a consistent snapshot of the schema the panel writes to. The archive carries its
// own header and trailer: a stream cut short (a timeout, a closed browser) fails
// pg_restore with "unexpected end of file" instead of looking like a backup.
func WriteTo(ctx context.Context, dsn string, w io.Writer) error {
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	schema, err := store.CurrentSchema(qctx, dsn)
	cancel()
	if err != nil {
		return fmt.Errorf("backup schema: %w", err)
	}
	return Tool(ctx, dsn, w, "pg_dump", "--format=custom", "--no-owner", "--no-acl", "--schema="+schema)
}

// ToolOutputLimit is how much of a client's diagnostics an error carries: the tail, where
// the reason is.
const ToolOutputLimit = 2000

// toolOutput is the tail of a client's diagnostics with the password masked: libpq
// takes it from PGPASSWORD and does not print it, the mask is for a server that echoes.
// Output is the tail of a client's diagnostics with the password masked.
func Output(dsn, out string) string {
	out = strings.TrimSpace(out)
	if cfg, err := store.ParseDatabaseURL(dsn); err == nil && cfg.Password != "" {
		out = strings.ReplaceAll(out, cfg.Password, "***")
	}
	if len(out) > ToolOutputLimit {
		cut := len(out) - ToolOutputLimit
		for cut < len(out) && !utf8.RuneStart(out[cut]) {
			cut++
		}
		out = "…" + out[cut:]
	}
	if out == "" {
		return ""
	}
	return ": " + out
}

// Env is what a PostgreSQL client needs from the panel's environment: the connection of
// the panel's database and nothing else — MIKAN_* holds secrets, and no tool gets them.
func Env(dsn string) ([]string, error) {
	// PGDATABASE does not expand a URI into user/password/host. Give libpq each
	// field explicitly, especially with UID 65532 which has no OS login in the image.
	cfg, err := store.ParseDatabaseURL(dsn)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, errors.New("database backup/restore requires a postgres:// or postgresql:// MIKAN_DATABASE_URL")
	}
	// Only what a PostgreSQL client needs from the panel's environment: MIKAN_* holds the
	// panel's secrets, and no tool gets them.
	var env []string
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		switch {
		case name == "PATH", name == "HOME", name == "TZ", name == "TMPDIR", name == "LANG", strings.HasPrefix(name, "LC_"):
			env = append(env, value)
		}
	}
	env = append(env, "PGHOST="+cfg.Host, "PGPORT="+strconv.Itoa(int(cfg.Port)), "PGUSER="+cfg.User, "PGPASSWORD="+cfg.Password, "PGDATABASE="+cfg.Database, "PGCONNECT_TIMEOUT=10")
	query := u.Query()
	for key, variable := range map[string]string{"sslmode": "PGSSLMODE", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY", "sslrootcert": "PGSSLROOTCERT", "sslcrl": "PGSSLCRL", "sslcrldir": "PGSSLCRLDIR", "sslpassword": "PGSSLPASSWORD", "sslcertmode": "PGSSLCERTMODE", "gssencmode": "PGGSSENCMODE", "channel_binding": "PGCHANNELBINDING", "target_session_attrs": "PGTARGETSESSIONATTRS", "options": "PGOPTIONS"} {
		if value := query.Get(key); value != "" {
			env = append(env, variable+"="+value)
		}
	}
	return env, nil
}

// postgresTool runs a PostgreSQL client with the credentials in its environment, never in
// its arguments; stdout, when not nil, takes what the client writes there.
// Tool runs a PostgreSQL client with the credentials in its environment, never in its
// arguments; stdout, when not nil, takes what the client writes there.
func Tool(ctx context.Context, dsn string, stdout io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var err error
	cmd.Env, err = Env(dsn)
	if err != nil {
		return err
	}
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w%s", name, err, Output(dsn, stderr.String()))
	}
	return nil
}

// dumpEntryTypes are the kinds of object a dump of the panel's schema holds. pg_restore
// runs what an archive's entries say as the database role it connects as, so an archive
// with anything else (a function, a trigger, an extension, a rule) is refused before it
// runs: a backup that came from a chat or a download must not bring code along.
var dumpEntryTypes = []string{
	// Longest first: "TABLE DATA" before "TABLE".
	"SEQUENCE OWNED BY", "FK CONSTRAINT", "SEQUENCE SET", "TABLE DATA",
	"CONSTRAINT", "SEQUENCE", "DEFAULT", "COMMENT", "SCHEMA", "TABLE", "INDEX",
}

// Check lists an archive's entries (pg_restore -l reads the file alone) and refuses
// one with an entry the panel's own dumps never have.
func Check(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, "pg_restore", "--list", path)
	cmd.Env = []string{} // reads a file, needs nothing from the panel
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("pg_restore --list failed: %w%s", err, Output("", stderr.String()))
	}
	return CheckList(out)
}

func CheckList(list []byte) error {
	sc := bufio.NewScanner(bytes.NewReader(list))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		// "<id>; <catalog oid> <oid> <TYPE> <schema> <name> <owner>"
		fields := strings.Fields(line)
		if len(fields) < 4 || !strings.HasSuffix(fields[0], ";") {
			return fmt.Errorf("the archive has an entry this panel cannot read: %q", line)
		}
		rest := strings.Join(fields[3:], " ")
		known := false
		for _, typ := range dumpEntryTypes {
			if rest == typ || strings.HasPrefix(rest, typ+" ") {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("the archive is not a dump of this panel's tables: it has %q", rest)
		}
	}
	return sc.Err()
}
