-- +goose Up
-- The history of S3 backups: what was uploaded, when, and whether it was a scheduled,
-- manual or pre-restore copy. The objects themselves live in the bucket.
CREATE TABLE s3_backups (
  id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  object_key TEXT NOT NULL,
  size       BIGINT NOT NULL DEFAULT 0,
  etag       TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL DEFAULT 'scheduled' CHECK (kind IN ('scheduled', 'manual', 'pre_restore')),
  status     TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'failed')),
  error      TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL
);
CREATE INDEX s3_backups_created ON s3_backups (created_at DESC);

-- +goose Down
DROP TABLE s3_backups;
