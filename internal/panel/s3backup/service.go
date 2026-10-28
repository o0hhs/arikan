// Package s3backup keeps the panel's database in an S3-compatible bucket. The archive is
// the same encrypted tar.gz Telegram backups use (tgbackup), so a file can be restored by
// hand with age and `arikan database restore` even when the panel is gone.
package s3backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mikan/internal/panel/pgdump"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgbackup"
)

const (
	KeyEnabled       = "s3_backup"
	KeyEndpoint      = "s3_backup_endpoint"
	KeyRegion        = "s3_backup_region"
	KeyBucket        = "s3_backup_bucket"
	KeyPrefix        = "s3_backup_prefix"
	KeyAccessKey     = "s3_backup_access_key"
	KeySecretKey     = "s3_backup_secret_key"
	KeySessionToken  = "s3_backup_session_token"
	KeyPathStyle     = "s3_backup_path_style"
	KeyHour          = "s3_backup_hour"
	KeyRetentionDays = "s3_backup_retention_days"
	KeyPassword      = "s3_backup_password"
	KeyState         = "s3_backup_state"
	KeyRestoreState  = "s3_backup_restore_state"

	DefaultHour          = 4
	DefaultRetentionDays = 30
	MinPassword          = 20
	SendTimeout          = 30 * time.Minute
	RestoreTimeout       = 60 * time.Minute
	tmpPrefix            = ".s3-backup-"
)

var Enabled = settings.Switch{Key: KeyEnabled}

var backupObject = regexp.MustCompile(`^arikan-.+-\d{8}-\d{6}-.+\.tar\.gz\.age$`)

// State is what the last uploads did. Only error codes are kept: a bucket's answer may
// name hosts, keys and request ids that must not reach the page or the audit journal.
type State struct {
	LastOK      int64  `json:"last_ok,omitempty"`
	LastTry     int64  `json:"last_try,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	LastSize    int64  `json:"last_size,omitempty"`
	LastKey     string `json:"last_key,omitempty"`
	LastOKDay   int64  `json:"last_ok_day,omitempty"`
	LastTryHour int64  `json:"last_try_hour,omitempty"`
}

// RestoreState is the restore that was prepared for the next panel start.
type RestoreState struct {
	LastTry   int64  `json:"last_try,omitempty"`
	LastOK    int64  `json:"last_ok,omitempty"`
	LastError string `json:"last_error,omitempty"`
	LastKey   string `json:"last_key,omitempty"`
	Pending   bool   `json:"pending,omitempty"`
}

var (
	ErrNotConfigured = errors.New("s3_not_configured")
	ErrNoPassword    = errors.New("no_backup_password")
	ErrBusy          = errors.New("backup_busy")
	ErrUnavailable   = errors.New("backup_unavailable")
	ErrRestoreBusy   = errors.New("restore_busy")
	ErrPending       = errors.New("restore_pending")
)

// Restarter is the host channel the panel already uses for a restart request.
type Restarter interface {
	Restart() (string, error)
}

type Service struct {
	dump    tgbackup.Dumper
	set     *settings.Settings
	st      *store.Store
	dataDir string
	name    func(context.Context) string
	host    Restarter
	now     func() time.Time
	log     *slog.Logger
	mu      sync.Mutex

	lifeMu  sync.Mutex
	life    context.Context
	closed  bool
	started sync.WaitGroup
}

func New(dump tgbackup.Dumper, set *settings.Settings, st *store.Store, dataDir string, name func(context.Context) string, host Restarter, now func() time.Time, log *slog.Logger) *Service {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{dump: dump, set: set, st: st, dataDir: dataDir, name: name, host: host, now: now, log: log}
}

// PendingRestorePath is where a prepared restore waits for the next panel start.
func PendingRestorePath(dataDir string) string {
	return filepath.Join(dataDir, "restore", "pending.dump")
}

func (s *Service) Cleanup() {
	if s.dataDir == "" {
		return
	}
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			if err := os.RemoveAll(filepath.Join(s.dataDir, e.Name())); err != nil {
				s.log.Warn("s3 backup: a leftover was not removed", "path", e.Name(), "err", err)
			}
		}
	}
}

func (s *Service) Run(ctx context.Context) {
	s.Cleanup()
	s.lifeMu.Lock()
	s.life = ctx
	s.lifeMu.Unlock()
	defer func() {
		s.lifeMu.Lock()
		s.closed = true
		s.lifeMu.Unlock()
		s.started.Wait()
	}()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if s.due(ctx) {
			if _, err := s.Send(ctx, "scheduled"); err != nil && !errors.Is(err, ErrBusy) {
				s.log.Warn("s3 backup", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) due(ctx context.Context) bool {
	on, err := s.set.On(ctx, Enabled)
	if err != nil || !on {
		return false
	}
	hour, err := s.hour(ctx)
	if err != nil {
		return false
	}
	st, err := s.State(ctx)
	if err != nil {
		return false
	}
	now := s.now().UTC()
	day, h := now.Unix()/86400, now.Unix()/3600
	return now.Hour() >= hour && st.LastOKDay < day && st.LastTryHour < h
}

func (s *Service) Enabling(ctx context.Context) error {
	hour, err := s.hour(ctx)
	if err != nil {
		return err
	}
	st, err := s.State(ctx)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	if now.Hour() >= hour && st.LastOKDay < now.Unix()/86400 {
		st.LastOKDay = now.Unix() / 86400
		return settings.Set(ctx, s.set, KeyState, st)
	}
	return nil
}

func (s *Service) Busy() bool {
	if s.mu.TryLock() {
		s.mu.Unlock()
		return false
	}
	return true
}

func (s *Service) Start(kind string) error {
	if kind == "" {
		kind = "manual"
	}
	if s.Busy() {
		return ErrBusy
	}
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if s.closed {
		return ErrBusy
	}
	life := s.life
	if life == nil {
		life = context.Background()
	}
	s.started.Go(func() {
		ctx, cancel := context.WithTimeout(life, SendTimeout)
		defer cancel()
		if _, err := s.Send(ctx, kind); err != nil && !errors.Is(err, ErrBusy) {
			s.log.Warn("s3 backup", "err", err)
		}
	})
	return nil
}

func (s *Service) Send(ctx context.Context, kind string) (State, error) {
	if !s.mu.TryLock() {
		return State{}, ErrBusy
	}
	defer s.mu.Unlock()
	return s.sendLocked(ctx, kind)
}

func (s *Service) sendLocked(ctx context.Context, kind string) (State, error) {
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	st, err := s.State(ctx)
	if err != nil {
		return st, err
	}
	now := s.now().UTC()
	st.LastTry, st.LastTryHour = now.Unix(), now.Unix()/3600
	size, key, err := s.upload(ctx, kind, now)
	st.LastError = ""
	if err != nil {
		st.LastError = Code(err)
	} else {
		st.LastOK, st.LastOKDay, st.LastSize, st.LastKey = now.Unix(), now.Unix()/86400, size, key
	}
	sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer scancel()
	if serr := settings.Set(sctx, s.set, KeyState, st); serr != nil && err == nil {
		err = serr
	}
	return st, err
}

func (s *Service) upload(ctx context.Context, kind string, now time.Time) (int64, string, error) {
	if s.dataDir == "" {
		return 0, "", ErrUnavailable
	}
	cfg, password, err := s.config(ctx)
	if err != nil {
		return 0, "", err
	}
	if len(password) < MinPassword {
		return 0, "", ErrNoPassword
	}
	tmp, err := os.MkdirTemp(s.dataDir, tmpPrefix)
	if err != nil {
		return 0, "", err
	}
	defer os.RemoveAll(tmp)
	path, size, err := tgbackup.MakeArchive(ctx, s.dump, tmp, password, now)
	if err != nil {
		return 0, "", err
	}
	key := objectKey(cfg.Prefix, s.name(ctx), now, kind)
	client, err := NewClient(cfg, nil, s.now)
	if err != nil {
		return 0, key, err
	}
	etag, err := client.PutFile(ctx, key, path, "application/octet-stream")
	if err != nil {
		s.record(ctx, key, size, "", kind, "failed", Code(err))
		return 0, key, err
	}
	s.record(ctx, key, size, etag, kind, "ok", "")
	s.retain(ctx, client, cfg, now)
	return size, key, nil
}

func (s *Service) config(ctx context.Context) (Config, string, error) {
	read := func(key string) (string, error) {
		v, _, err := settings.Get[string](ctx, s.set, key)
		return v, err
	}
	endpoint, err := read(KeyEndpoint)
	if err != nil {
		return Config{}, "", err
	}
	region, err := read(KeyRegion)
	if err != nil {
		return Config{}, "", err
	}
	bucket, err := read(KeyBucket)
	if err != nil {
		return Config{}, "", err
	}
	prefix, err := read(KeyPrefix)
	if err != nil {
		return Config{}, "", err
	}
	access, err := read(KeyAccessKey)
	if err != nil {
		return Config{}, "", err
	}
	secret, err := read(KeySecretKey)
	if err != nil {
		return Config{}, "", err
	}
	token, err := read(KeySessionToken)
	if err != nil {
		return Config{}, "", err
	}
	pathStyle, _, err := settings.Get[bool](ctx, s.set, KeyPathStyle)
	if err != nil {
		return Config{}, "", err
	}
	password, err := read(KeyPassword)
	if err != nil {
		return Config{}, "", err
	}
	cfg := Config{Endpoint: strings.TrimSpace(endpoint), Region: strings.TrimSpace(region), Bucket: strings.TrimSpace(bucket), Prefix: strings.Trim(strings.TrimSpace(prefix), "/"), AccessKey: strings.TrimSpace(access), SecretKey: strings.TrimSpace(secret), SessionToken: strings.TrimSpace(token), PathStyle: pathStyle}
	if err := cfg.validate(); err != nil {
		return Config{}, "", fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	return cfg, password, nil
}

func (s *Service) hour(ctx context.Context) (int, error) {
	hour, ok, err := settings.Get[int](ctx, s.set, KeyHour)
	if err != nil {
		return DefaultHour, err
	}
	if !ok {
		return DefaultHour, nil
	}
	if hour < 0 || hour > 23 {
		return DefaultHour, nil
	}
	return hour, nil
}

func (s *Service) State(ctx context.Context) (State, error) {
	st, _, err := settings.Get[State](ctx, s.set, KeyState)
	return st, err
}

func (s *Service) RestoreState(ctx context.Context) (RestoreState, error) {
	st, _, err := settings.Get[RestoreState](ctx, s.set, KeyRestoreState)
	return st, err
}

func (s *Service) History(ctx context.Context, limit int32) ([]db.S3Backup, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.st.Q.ListS3Backups(ctx, limit)
}

func (s *Service) record(ctx context.Context, key string, size int64, etag, kind, status, code string) {
	_, err := s.st.Q.AddS3Backup(ctx, db.AddS3BackupParams{ObjectKey: key, Size: size, Etag: etag, Kind: kind, Status: status, Error: code, CreatedAt: s.now().Unix()})
	if err != nil {
		s.log.Warn("s3 backup history", "err", err)
	}
}

func (s *Service) retain(ctx context.Context, client *Client, cfg Config, now time.Time) {
	days, ok, err := settings.Get[int](ctx, s.set, KeyRetentionDays)
	if err != nil || !ok || days <= 0 {
		return
	}
	cutoff := now.AddDate(0, 0, -days)
	prefix := cfg.Prefix
	if prefix != "" {
		prefix += "/"
	}
	objects, err := client.List(ctx, prefix)
	if err != nil {
		s.log.Warn("s3 backup retention: list", "err", err)
		return
	}
	for _, o := range objects {
		if !o.LastModified.Before(cutoff) || !isBackupObject(o.Key) {
			continue
		}
		if err := client.Delete(ctx, o.Key); err != nil {
			s.log.Warn("s3 backup retention: delete", "key", o.Key, "err", err)
		}
	}
	if _, err := s.st.Q.PruneS3Backups(ctx, cutoff.Unix()); err != nil {
		s.log.Warn("s3 backup retention: history", "err", err)
	}
}

// isBackupObject keeps retention inside the panel's own archives: a shared bucket or a
// prefix with other files must not lose them to the backup schedule.
func isBackupObject(key string) bool { return backupObject.MatchString(path.Base(key)) }

// StartRestore prepares a restore in the background: a fresh pre-restore backup, the
// chosen object, its check, and a pending dump for the next start. The panel is not
// stopped here; the host is asked to restart it after the dump is ready.
func (s *Service) StartRestore(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("s3_object_required")
	}
	if s.host == nil || s.dataDir == "" {
		return ErrUnavailable
	}
	if s.Busy() {
		return ErrBusy
	}
	if _, err := os.Stat(PendingRestorePath(s.dataDir)); err == nil {
		return ErrPending
	}
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if s.closed {
		return ErrBusy
	}
	life := s.life
	if life == nil {
		life = context.Background()
	}
	s.started.Go(func() {
		ctx, cancel := context.WithTimeout(life, RestoreTimeout)
		defer cancel()
		if err := s.restore(ctx, key); err != nil && !errors.Is(err, ErrBusy) {
			s.log.Warn("s3 restore", "key", key, "err", err)
		}
	})
	return nil
}

func (s *Service) restore(ctx context.Context, key string) error {
	if !s.mu.TryLock() {
		return ErrBusy
	}
	defer s.mu.Unlock()
	now := s.now().UTC()
	rs, err := s.RestoreState(ctx)
	if err != nil {
		return err
	}
	rs.LastTry, rs.LastKey, rs.LastError = now.Unix(), key, ""
	saveRestore := func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := settings.Set(sctx, s.set, KeyRestoreState, rs); err != nil {
			s.log.Warn("s3 restore state", "err", err)
		}
	}
	// A restore must never be the only copy of the data: this backup goes first and its
	// failure cancels the restore.
	if _, err := s.sendLocked(ctx, "pre_restore"); err != nil {
		rs.LastError = Code(err)
		saveRestore()
		return fmt.Errorf("pre-restore backup: %w", err)
	}
	cfg, password, err := s.config(ctx)
	if err != nil {
		rs.LastError = Code(err)
		saveRestore()
		return err
	}
	if len(password) < MinPassword {
		rs.LastError = ErrNoPassword.Error()
		saveRestore()
		return ErrNoPassword
	}
	tmp, err := os.MkdirTemp(s.dataDir, tmpPrefix)
	if err != nil {
		rs.LastError = "restore_failed"
		saveRestore()
		return err
	}
	defer os.RemoveAll(tmp)
	client, err := NewClient(cfg, nil, s.now)
	if err != nil {
		rs.LastError = Code(err)
		saveRestore()
		return err
	}
	archive := filepath.Join(tmp, "backup.tar.gz.age")
	if err := client.GetFile(ctx, key, archive); err != nil {
		rs.LastError = Code(err)
		saveRestore()
		return err
	}
	dump, err := tgbackup.Unpack(archive, tmp, password)
	if err != nil {
		rs.LastError = "restore_unpack_failed"
		saveRestore()
		return err
	}
	if err := pgdump.Check(ctx, dump); err != nil {
		rs.LastError = "restore_check_failed"
		saveRestore()
		return err
	}
	pending := PendingRestorePath(s.dataDir)
	if err := os.MkdirAll(filepath.Dir(pending), 0o700); err != nil {
		rs.LastError = "restore_failed"
		saveRestore()
		return err
	}
	if err := os.Rename(dump, pending); err != nil {
		rs.LastError = "restore_failed"
		saveRestore()
		return err
	}
	rs.Pending = true
	rs.LastOK = now.Unix()
	saveRestore()
	if _, err := s.host.Restart(); err != nil {
		rs.LastError = "restart_failed"
		saveRestore()
		return err
	}
	return nil
}

// MarkRestored clears the pending marker after the next start has loaded the dump.
func MarkRestored(ctx context.Context, set *settings.Settings, now time.Time) error {
	st, _, err := settings.Get[RestoreState](ctx, set, KeyRestoreState)
	if err != nil {
		return err
	}
	st.Pending = false
	st.LastError = ""
	st.LastOK = now.Unix()
	return settings.Set(ctx, set, KeyRestoreState, st)
}

func objectKey(prefix, server string, now time.Time, kind string) string {
	name := "arikan-" + safeName(server) + "-" + now.UTC().Format("20060102-150405") + "-" + safeName(kind) + ".tar.gz.age"
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		return prefix + "/" + name
	}
	return name
}

func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		case r == ':', r == ' ':
			return '-'
		}
		return -1
	}, s)
	if s == "" {
		return "panel"
	}
	return s
}

// Code is the error code kept in State and shown by the page.
func Code(err error) string {
	for _, known := range []error{ErrNotConfigured, ErrNoPassword, ErrBusy, ErrUnavailable, ErrRestoreBusy, ErrPending, tgbackup.ErrDump, tgbackup.ErrUnavailable} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	var se *Error
	if errors.As(err, &se) {
		switch se.Status {
		case 401, 403:
			return "s3_forbidden"
		case 404:
			return "s3_not_found"
		default:
			return "s3_error"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "backup_timeout"
	}
	return "backup_failed"
}

// Defaults are what the page shows before the admin changes anything.
func Defaults() (hour, retention int) { return DefaultHour, DefaultRetentionDays }

// RetentionDays is exported for the API's view; an absent or invalid value is the default.
func (s *Service) RetentionDays(ctx context.Context) (int, error) {
	days, ok, err := settings.Get[int](ctx, s.set, KeyRetentionDays)
	if err != nil {
		return DefaultRetentionDays, err
	}
	if !ok || days < 0 {
		return DefaultRetentionDays, nil
	}
	return days, nil
}

// Hour is exported for the API's view.
func (s *Service) Hour(ctx context.Context) (int, error) { return s.hour(ctx) }

// Prefix is exported for the API's view.
func (s *Service) Prefix(ctx context.Context) (string, error) {
	v, _, err := settings.Get[string](ctx, s.set, KeyPrefix)
	return strings.Trim(strings.TrimSpace(v), "/"), err
}
