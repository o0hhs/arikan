-- +goose Up
-- Event-driven automation: a rule names a feed event, optional conditions and Telegram
-- actions. Runs are kept as the journal of what a rule did, including dry runs.
CREATE TABLE automation_rules (
  id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name             TEXT NOT NULL,
  enabled          INTEGER NOT NULL DEFAULT 1,
  trigger          TEXT NOT NULL,
  conditions       TEXT NOT NULL DEFAULT '{}',
  actions          TEXT NOT NULL DEFAULT '[]',
  cooldown_seconds BIGINT NOT NULL DEFAULT 3600 CHECK (cooldown_seconds >= 0),
  dry_run          INTEGER NOT NULL DEFAULT 0,
  created_at       BIGINT NOT NULL,
  updated_at       BIGINT NOT NULL,
  last_run_at      BIGINT,
  runs_count       BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX automation_rules_enabled_trigger ON automation_rules (trigger) WHERE enabled = 1;

CREATE TABLE automation_runs (
  id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  rule_id    BIGINT NOT NULL REFERENCES automation_rules(id) ON DELETE CASCADE,
  event_id   BIGINT NOT NULL,
  event_type TEXT NOT NULL,
  user_id    BIGINT,
  tg_id      BIGINT,
  status     TEXT NOT NULL CHECK (status IN ('ok', 'skipped', 'failed')),
  dry_run    INTEGER NOT NULL DEFAULT 0,
  detail     TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  UNIQUE (rule_id, event_id)
);
CREATE INDEX automation_runs_created ON automation_runs (created_at DESC);
CREATE INDEX automation_runs_rule_created ON automation_runs (rule_id, created_at DESC);

-- +goose Down
DROP TABLE automation_runs;
DROP TABLE automation_rules;
