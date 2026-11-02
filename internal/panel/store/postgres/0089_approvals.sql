-- +goose Up
-- Dangerous actions can wait for a second admin: the requester writes what it wants to do,
-- another admin approves or rejects it, and the action is performed once with the approval.
CREATE TABLE approval_requests (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  action       TEXT NOT NULL,
  target_type  TEXT NOT NULL,
  target_id    TEXT NOT NULL,
  summary      TEXT NOT NULL DEFAULT '',
  payload      TEXT NOT NULL DEFAULT '{}',
  status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'expired', 'used')),
  requested_by BIGINT NOT NULL,
  requested_at BIGINT NOT NULL,
  decided_by   BIGINT,
  decided_at   BIGINT,
  expires_at   BIGINT NOT NULL,
  used_at      BIGINT
);
CREATE INDEX approval_requests_pending ON approval_requests (status, expires_at);
CREATE INDEX approval_requests_requested ON approval_requests (requested_by, requested_at DESC);

-- +goose Down
DROP TABLE approval_requests;
