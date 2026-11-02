-- +goose Up
-- An approved request is claimed before its action runs. A crash leaves it in executing,
-- where the housekeeping can return it to approved (or expire it) instead of losing it.
ALTER TABLE approval_requests DROP CONSTRAINT IF EXISTS approval_requests_status_check;
ALTER TABLE approval_requests ADD CONSTRAINT approval_requests_status_check
  CHECK (status IN ('pending', 'approved', 'rejected', 'expired', 'used', 'executing'));
ALTER TABLE approval_requests ADD COLUMN executing_at BIGINT;
CREATE INDEX approval_requests_executing ON approval_requests (status, executing_at) WHERE status = 'executing';

-- +goose Down
DROP INDEX IF EXISTS approval_requests_executing;
ALTER TABLE approval_requests DROP COLUMN IF EXISTS executing_at;
ALTER TABLE approval_requests DROP CONSTRAINT IF EXISTS approval_requests_status_check;
ALTER TABLE approval_requests ADD CONSTRAINT approval_requests_status_check
  CHECK (status IN ('pending', 'approved', 'rejected', 'expired', 'used'));
