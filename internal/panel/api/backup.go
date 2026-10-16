package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/pgdump"
	"mikan/internal/panel/store"
)

// The database out of the panel: the same archive "mikan database backup" writes and the
// bot sends to Telegram, streamed to the admin's browser. Restoring it needs the panel
// stopped, so the file is for the installer or the CLI — the panel keeps serving.

// backupDownloadTimeout bounds one download: a dump of a panel that has run for years is
// still tens of megabytes, but nothing should hold the request open forever.
const backupDownloadTimeout = 15 * time.Minute

func (h *handlers) registerDatabaseBackup() {
	huma.Register(h.api, huma.Operation{
		OperationID: "download-database-backup",
		Method:      http.MethodGet,
		Path:        "/api/v1/database/backup",
		Summary:     "Скачать резервную копию базы",
		Tags:        []string{"settings"},
		Metadata:    sessionOnly,
		Extensions:  sessionOnlyExt,
		Responses: map[string]*huma.Response{"200": {Description: "Архив pg_dump (custom)", Content: map[string]*huma.MediaType{
			"application/octet-stream": {},
		}}},
	}, h.downloadBackup)
}

func (h *handlers) downloadBackup(ctx context.Context, _ *struct{}) (*huma.StreamResponse, error) {
	dsn, err := store.DatabaseURL()
	if err != nil {
		return nil, huma.Error409Conflict("backup_no_database", &huma.ErrorDetail{Message: err.Error()})
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "database.download", "database", "", nil)
	name := "arikan-" + h.d.Now().UTC().Format("2006-01-02-1504") + ".dump"
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		hctx.SetHeader("Content-Type", "application/octet-stream")
		hctx.SetHeader("Content-Disposition", `attachment; filename="`+name+`"`)
		hctx.SetHeader("X-Content-Type-Options", "nosniff")
		// The stream is the response: once the first byte is out, a failure cannot become a
		// status any more. A dump that was cut short is not a valid archive — pg_restore
		// refuses it ("unexpected end of file"), which is how the download itself is checked.
		ctx, cancel := context.WithTimeout(ctx, backupDownloadTimeout)
		defer cancel()
		if err := pgdump.WriteTo(ctx, dsn, hctx.BodyWriter()); err != nil {
			h.d.Log.Error("database download", "err", err)
		}
	}}, nil
}
