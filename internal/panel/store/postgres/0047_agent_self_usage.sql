-- +goose Up
-- The agent's own Telegram account: who they are on the same platform the links live on,
-- so their own clicks of their own links are not counted as arrivals. It is one per agent;
-- an agent without one is never recognized as an agent.
ALTER TABLE campaign_agents ADD COLUMN tg_id BIGINT UNIQUE;

-- What an agent used themselves: counts of their own clicks, apart from the arrivals, so
-- the panel carries the agent's own activity too and it never inflates the referrals.
CREATE TABLE agent_usage (
  agent_id   BIGINT PRIMARY KEY REFERENCES campaign_agents(id) ON DELETE CASCADE,
  visits     BIGINT NOT NULL DEFAULT 0,
  first_seen BIGINT NOT NULL,
  last_seen  BIGINT NOT NULL
);

-- +goose Down
DROP TABLE agent_usage;
ALTER TABLE campaign_agents DROP COLUMN tg_id;
