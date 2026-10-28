package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/s3backup"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// S3 backups: the same encrypted dump as the Telegram one, kept in a bucket the admin
// owns. The page never returns secrets; a blank secret field means "keep what is stored".

type S3BackupView struct {
	Enabled          bool       `json:"enabled"`
	Configured       bool       `json:"configured"`
	Endpoint         string     `json:"endpoint"`
	Region           string     `json:"region"`
	Bucket           string     `json:"bucket"`
	Prefix           string     `json:"prefix"`
	AccessKeySet     bool       `json:"access_key_set"`
	SecretKeySet     bool       `json:"secret_key_set"`
	SessionTokenSet  bool       `json:"session_token_set"`
	PathStyle        bool       `json:"path_style"`
	Hour             int        `json:"hour"`
	RetentionDays    int        `json:"retention_days"`
	PasswordSet      bool       `json:"password_set"`
	Sending          bool       `json:"sending"`
	LastOK           *time.Time `json:"last_ok,omitempty"`
	LastTry          *time.Time `json:"last_try,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	LastSize         int64      `json:"last_size,omitempty"`
	LastKey          string     `json:"last_key,omitempty"`
	RestorePending   bool       `json:"restore_pending"`
	RestoreLastOK    *time.Time `json:"restore_last_ok,omitempty"`
	RestoreLastTry   *time.Time `json:"restore_last_try,omitempty"`
	RestoreLastError string     `json:"restore_last_error,omitempty"`
	RestoreLastKey   string     `json:"restore_last_key,omitempty"`
}

type S3BackupObjectView struct {
	ID        int64     `json:"id"`
	Key       string    `json:"key"`
	Size      int64     `json:"size"`
	ETag      string    `json:"etag,omitempty"`
	Kind      string    `json:"kind" enum:"scheduled,manual,pre_restore"`
	Status    string    `json:"status" enum:"ok,failed"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type s3BackupOutput struct{ Body S3BackupView }
type s3ObjectsOutput struct{ Body []S3BackupObjectView }

type patchS3BackupInput struct {
	Body struct {
		Enabled       *bool   `json:"enabled,omitempty"`
		Endpoint      *string `json:"endpoint,omitempty"`
		Region        *string `json:"region,omitempty"`
		Bucket        *string `json:"bucket,omitempty"`
		Prefix        *string `json:"prefix,omitempty"`
		AccessKey     *string `json:"access_key,omitempty"`
		SecretKey     *string `json:"secret_key,omitempty"`
		SessionToken  *string `json:"session_token,omitempty"`
		PathStyle     *bool   `json:"path_style,omitempty"`
		Hour          *int    `json:"hour,omitempty" minimum:"0" maximum:"23"`
		RetentionDays *int    `json:"retention_days,omitempty" minimum:"0" maximum:"3650"`
		Password      *string `json:"password,omitempty" maxLength:"256" doc:"Пароль шифрования архива, не короче 20 символов; не возвращается"`
	}
}

type s3RestoreInput struct {
	Body struct {
		Key string `json:"key" minLength:"1" maxLength:"1024"`
	}
}

func (h *handlers) registerS3Backups() {
	tags := []string{"backups"}
	huma.Register(h.api, huma.Operation{OperationID: "get-s3-backup", Method: http.MethodGet, Path: "/api/v1/s3-backup", Summary: "Бэкапы в S3", Tags: tags}, h.getS3Backup)
	huma.Register(h.api, huma.Operation{OperationID: "update-s3-backup", Method: http.MethodPatch, Path: "/api/v1/s3-backup", Summary: "Настроить S3-бэкапы", Tags: tags}, h.updateS3Backup)
	huma.Register(h.api, huma.Operation{OperationID: "run-s3-backup", Method: http.MethodPost, Path: "/api/v1/s3-backup/run", Summary: "Сделать S3-бэкап сейчас", Tags: tags, DefaultStatus: http.StatusAccepted}, h.runS3Backup)
	huma.Register(h.api, huma.Operation{OperationID: "list-s3-backup-objects", Method: http.MethodGet, Path: "/api/v1/s3-backup/objects", Summary: "История S3-бэкапов", Tags: tags}, h.listS3BackupObjects)
	huma.Register(h.api, huma.Operation{OperationID: "restore-s3-backup", Method: http.MethodPost, Path: "/api/v1/s3-backup/restore", Summary: "Восстановить базу из S3", Description: "Сначала делает свежий pre-restore бэкап, проверяет архив и просит хост перезапустить панель. Восстановление выполняется при следующем старте.", Tags: tags, DefaultStatus: http.StatusAccepted}, h.restoreS3Backup)
}

func (h *handlers) s3BackupView(ctx context.Context) (S3BackupView, error) {
	var v S3BackupView
	read := func(key string) (string, error) {
		s, _, err := settings.Get[string](ctx, h.d.Settings, key)
		return s, err
	}
	var err error
	if v.Enabled, err = h.d.Settings.On(ctx, s3backup.Enabled); err != nil {
		return v, err
	}
	if v.Endpoint, err = read(s3backup.KeyEndpoint); err != nil {
		return v, err
	}
	if v.Region, err = read(s3backup.KeyRegion); err != nil {
		return v, err
	}
	if v.Bucket, err = read(s3backup.KeyBucket); err != nil {
		return v, err
	}
	if v.Prefix, err = read(s3backup.KeyPrefix); err != nil {
		return v, err
	}
	v.Prefix = strings.Trim(strings.TrimSpace(v.Prefix), "/")
	access, err := read(s3backup.KeyAccessKey)
	if err != nil {
		return v, err
	}
	secret, err := read(s3backup.KeySecretKey)
	if err != nil {
		return v, err
	}
	token, err := read(s3backup.KeySessionToken)
	if err != nil {
		return v, err
	}
	password, err := read(s3backup.KeyPassword)
	if err != nil {
		return v, err
	}
	v.AccessKeySet, v.SecretKeySet, v.SessionTokenSet = access != "", secret != "", token != ""
	v.PasswordSet = len(password) >= s3backup.MinPassword
	v.PathStyle, _, err = settings.Get[bool](ctx, h.d.Settings, s3backup.KeyPathStyle)
	if err != nil {
		return v, err
	}
	v.Hour, v.RetentionDays = s3backup.Defaults()
	if h.d.S3Backups != nil {
		if v.Hour, err = h.d.S3Backups.Hour(ctx); err != nil {
			return v, err
		}
		if v.RetentionDays, err = h.d.S3Backups.RetentionDays(ctx); err != nil {
			return v, err
		}
		v.Sending = h.d.S3Backups.Busy()
	}
	v.Configured = v.Endpoint != "" && v.Bucket != "" && v.AccessKeySet && v.SecretKeySet
	if h.d.S3Backups != nil {
		st, err := h.d.S3Backups.State(ctx)
		if err != nil {
			return v, err
		}
		v.LastOK, v.LastTry, v.LastError, v.LastSize, v.LastKey = atTime(st.LastOK), atTime(st.LastTry), st.LastError, st.LastSize, st.LastKey
		rs, err := h.d.S3Backups.RestoreState(ctx)
		if err != nil {
			return v, err
		}
		v.RestoreLastOK, v.RestoreLastTry, v.RestoreLastError, v.RestoreLastKey = atTime(rs.LastOK), atTime(rs.LastTry), rs.LastError, rs.LastKey
	}
	if h.d.DataDir != "" {
		_, err := os.Stat(s3backup.PendingRestorePath(h.d.DataDir))
		v.RestorePending = err == nil
	}
	return v, nil
}

func atTime(unix int64) *time.Time {
	if unix == 0 {
		return nil
	}
	t := store.Time(unix)
	return &t
}

func (h *handlers) getS3Backup(ctx context.Context, _ *struct{}) (*s3BackupOutput, error) {
	v, err := h.s3BackupView(ctx)
	if err != nil {
		return nil, err
	}
	return &s3BackupOutput{Body: v}, nil
}

func (h *handlers) updateS3Backup(ctx context.Context, in *patchS3BackupInput) (*s3BackupOutput, error) {
	b := in.Body
	wasOn, err := h.d.Settings.On(ctx, s3backup.Enabled)
	if err != nil {
		return nil, err
	}
	if b.Password != nil && len(*b.Password) < s3backup.MinPassword {
		return nil, fieldErr("body.password", "backup_password_short")
	}
	// The effective values are checked before anything is written: switching on must not
	// leave a half-configured bucket behind.
	cur, err := h.s3BackupView(ctx)
	if err != nil {
		return nil, err
	}
	eff := cur
	if b.Endpoint != nil {
		eff.Endpoint = strings.TrimSpace(*b.Endpoint)
	}
	if b.Region != nil {
		eff.Region = strings.TrimSpace(*b.Region)
	}
	if b.Bucket != nil {
		eff.Bucket = strings.TrimSpace(*b.Bucket)
	}
	if b.Prefix != nil {
		eff.Prefix = strings.Trim(strings.TrimSpace(*b.Prefix), "/")
	}
	if b.AccessKey != nil {
		eff.AccessKeySet = strings.TrimSpace(*b.AccessKey) != ""
	}
	if b.SecretKey != nil {
		eff.SecretKeySet = strings.TrimSpace(*b.SecretKey) != ""
	}
	if b.SessionToken != nil {
		eff.SessionTokenSet = strings.TrimSpace(*b.SessionToken) != ""
	}
	if b.Password != nil {
		eff.PasswordSet = true
	}
	if b.Enabled != nil {
		eff.Enabled = *b.Enabled
	}
	if eff.Enabled {
		switch {
		case eff.Endpoint == "":
			return nil, fieldErr("body.endpoint", "s3_endpoint_required")
		case eff.Bucket == "":
			return nil, fieldErr("body.bucket", "s3_bucket_required")
		case !eff.AccessKeySet:
			return nil, fieldErr("body.access_key", "s3_access_key_required")
		case !eff.SecretKeySet:
			return nil, fieldErr("body.secret_key", "s3_secret_key_required")
		case !eff.PasswordSet:
			return nil, fieldErr("body.password", "no_backup_password")
		}
	}
	err = h.d.Store.Tx(ctx, func(q *db.Queries) error {
		set := settings.New(q)
		if b.Endpoint != nil {
			if err := settings.Set(ctx, set, s3backup.KeyEndpoint, eff.Endpoint); err != nil {
				return err
			}
		}
		if b.Region != nil {
			if err := settings.Set(ctx, set, s3backup.KeyRegion, eff.Region); err != nil {
				return err
			}
		}
		if b.Bucket != nil {
			if err := settings.Set(ctx, set, s3backup.KeyBucket, eff.Bucket); err != nil {
				return err
			}
		}
		if b.Prefix != nil {
			if err := settings.Set(ctx, set, s3backup.KeyPrefix, eff.Prefix); err != nil {
				return err
			}
		}
		if b.AccessKey != nil {
			if err := settings.Set(ctx, set, s3backup.KeyAccessKey, strings.TrimSpace(*b.AccessKey)); err != nil {
				return err
			}
		}
		if b.SecretKey != nil {
			if err := settings.Set(ctx, set, s3backup.KeySecretKey, strings.TrimSpace(*b.SecretKey)); err != nil {
				return err
			}
		}
		if b.SessionToken != nil {
			if err := settings.Set(ctx, set, s3backup.KeySessionToken, strings.TrimSpace(*b.SessionToken)); err != nil {
				return err
			}
		}
		if b.PathStyle != nil {
			if err := settings.Set(ctx, set, s3backup.KeyPathStyle, *b.PathStyle); err != nil {
				return err
			}
		}
		if b.Hour != nil {
			if err := settings.Set(ctx, set, s3backup.KeyHour, *b.Hour); err != nil {
				return err
			}
		}
		if b.RetentionDays != nil {
			if err := settings.Set(ctx, set, s3backup.KeyRetentionDays, *b.RetentionDays); err != nil {
				return err
			}
		}
		if b.Password != nil {
			if err := settings.Set(ctx, set, s3backup.KeyPassword, *b.Password); err != nil {
				return err
			}
		}
		if b.Enabled != nil {
			return settings.Set(ctx, set, s3backup.KeyEnabled, *b.Enabled)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if b.Enabled != nil && *b.Enabled && !wasOn && h.d.S3Backups != nil {
		if err := h.d.S3Backups.Enabling(ctx); err != nil {
			return nil, err
		}
	}
	details := map[string]any{}
	for key, value := range map[string]any{"enabled": b.Enabled, "endpoint": b.Endpoint, "bucket": b.Bucket, "hour": b.Hour, "retention_days": b.RetentionDays} {
		if value != nil {
			details[key] = value
		}
	}
	if b.SecretKey != nil || b.AccessKey != nil || b.SessionToken != nil || b.Password != nil {
		details["secrets"] = "changed"
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "s3.backup.update", "settings", s3backup.KeyEnabled, details)
	return h.getS3Backup(ctx, nil)
}

func (h *handlers) runS3Backup(ctx context.Context, _ *struct{}) (*s3BackupOutput, error) {
	if h.d.S3Backups == nil {
		return nil, huma.Error503ServiceUnavailable("backup_unavailable")
	}
	v, err := h.s3BackupView(ctx)
	if err != nil {
		return nil, err
	}
	switch {
	case !v.Configured:
		return nil, huma.Error409Conflict("s3_not_configured")
	case !v.PasswordSet:
		return nil, huma.Error409Conflict("no_backup_password")
	}
	if err := h.d.S3Backups.Start("manual"); err != nil {
		if errors.Is(err, s3backup.ErrBusy) {
			return nil, huma.Error409Conflict(err.Error())
		}
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "s3.backup.run", "settings", s3backup.KeyEnabled, nil)
	v.Sending = true
	return &s3BackupOutput{Body: v}, nil
}

func (h *handlers) listS3BackupObjects(ctx context.Context, _ *struct{}) (*s3ObjectsOutput, error) {
	if h.d.S3Backups == nil {
		return &s3ObjectsOutput{Body: []S3BackupObjectView{}}, nil
	}
	rows, err := h.d.S3Backups.History(ctx, 100)
	if err != nil {
		return nil, err
	}
	out := &s3ObjectsOutput{Body: make([]S3BackupObjectView, 0, len(rows))}
	for _, r := range rows {
		out.Body = append(out.Body, S3BackupObjectView{ID: r.ID, Key: r.ObjectKey, Size: r.Size, ETag: r.Etag, Kind: r.Kind, Status: r.Status, Error: r.Error, CreatedAt: store.Time(r.CreatedAt)})
	}
	return out, nil
}

func (h *handlers) restoreS3Backup(ctx context.Context, in *s3RestoreInput) (*s3BackupOutput, error) {
	if h.d.S3Backups == nil {
		return nil, huma.Error503ServiceUnavailable("backup_unavailable")
	}
	key := strings.TrimSpace(in.Body.Key)
	if key == "" {
		return nil, fieldErr("body.key", "s3_object_required")
	}
	if err := h.requireApproval(ctx, "s3.restore", "s3", key, approvalSummary("s3.restore", "s3", key), map[string]string{"key": key}); err != nil {
		return nil, err
	}
	if err := h.d.S3Backups.StartRestore(key); err != nil {
		switch {
		case errors.Is(err, s3backup.ErrPending):
			return nil, huma.Error409Conflict("restore_pending")
		case errors.Is(err, s3backup.ErrBusy):
			return nil, huma.Error409Conflict("backup_busy")
		case errors.Is(err, s3backup.ErrUnavailable):
			return nil, huma.Error503ServiceUnavailable("backup_unavailable")
		default:
			return nil, err
		}
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "s3.backup.restore", "s3", key, nil)
	return h.getS3Backup(ctx, nil)
}
