-- name: ListS3Backups :many
SELECT * FROM s3_backups ORDER BY id DESC LIMIT sqlc.arg(lim)::INT;

-- name: GetS3Backup :one
SELECT * FROM s3_backups WHERE id = $1;

-- name: GetS3BackupByKey :one
SELECT * FROM s3_backups WHERE object_key = $1 ORDER BY id DESC LIMIT 1;

-- name: AddS3Backup :one
INSERT INTO s3_backups (object_key, size, etag, kind, status, error, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: PruneS3Backups :execrows
DELETE FROM s3_backups WHERE created_at < sqlc.arg(before)::BIGINT;
