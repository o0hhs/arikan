-- name: CreateAgent :one
INSERT INTO campaign_agents (login, password_hash, note, tg_id, created_at, updated_at)
VALUES ($1, $2, $3, sqlc.narg(tg_id), $4, $4) RETURNING *;

-- name: GetAgent :one
SELECT * FROM campaign_agents WHERE id = $1;

-- name: GetAgentByLogin :one
SELECT * FROM campaign_agents WHERE login = $1;

-- name: UpdateAgent :execrows
UPDATE campaign_agents
SET login = sqlc.arg(login), note = sqlc.arg(note), disabled = sqlc.arg(disabled),
    tg_id = sqlc.narg(tg_id), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: UpdateAgentPassword :execrows
UPDATE campaign_agents SET password_hash = sqlc.arg(password_hash), updated_at = sqlc.arg(updated_at) WHERE id = sqlc.arg(id);

-- name: ListAgentsWithStats :many
-- Every agent with what it brought and earned: the arrivals, purchases and their money
-- come from the campaigns linked to the agent, the earned from the commissions that stand
-- (a refunded sale's commission is reverted), the paid from the recorded payouts.
SELECT a.*,
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_tags c WHERE c.agent_id = a.id AND c.deleted = 0), 0) AS BIGINT) AS links,
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_visits v JOIN campaign_tags c ON c.id = v.campaign_id WHERE c.agent_id = a.id), 0) AS BIGINT) AS visits,
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_events e JOIN campaign_tags c ON c.id = e.campaign_id WHERE c.agent_id = a.id AND e.kind = 'bought' AND e.reverted_at IS NULL), 0) AS BIGINT) AS bought,
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_events e JOIN campaign_tags c ON c.id = e.campaign_id WHERE c.agent_id = a.id AND e.kind = 'renewed' AND e.reverted_at IS NULL), 0) AS BIGINT) AS renewed,
  CAST(COALESCE((SELECT SUM(e.amount) FROM campaign_events e JOIN campaign_tags c ON c.id = e.campaign_id WHERE c.agent_id = a.id AND e.reverted_at IS NULL AND e.currency = 'RUB'), 0) AS BIGINT) AS revenue,
  CAST(COALESCE((SELECT SUM(ac.amount) FROM agent_commissions ac WHERE ac.agent_id = a.id AND ac.reverted_at IS NULL), 0) AS BIGINT) AS earned,
  CAST(COALESCE((SELECT SUM(ap.amount) FROM agent_payouts ap WHERE ap.agent_id = a.id), 0) AS BIGINT) AS paid,
  CAST(COALESCE((SELECT SUM(visits) FROM agent_usage WHERE agent_id = a.id), 0) AS BIGINT) AS usages
FROM campaign_agents a
ORDER BY a.id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: CountAgents :one
SELECT COUNT(*) AS total FROM campaign_agents;

-- name: AgentOverview :one
-- One agent's totals in one row, for the agent's own panel and for the admin's card.
SELECT
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_visits v JOIN campaign_tags c ON c.id = v.campaign_id WHERE c.agent_id = $1), 0) AS BIGINT) AS visits,
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_events e JOIN campaign_tags c ON c.id = e.campaign_id WHERE c.agent_id = $1 AND e.kind = 'bought' AND e.reverted_at IS NULL), 0) AS BIGINT) AS bought,
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_events e JOIN campaign_tags c ON c.id = e.campaign_id WHERE c.agent_id = $1 AND e.kind = 'renewed' AND e.reverted_at IS NULL), 0) AS BIGINT) AS renewed,
  CAST(COALESCE((SELECT SUM(e.amount) FROM campaign_events e JOIN campaign_tags c ON c.id = e.campaign_id WHERE c.agent_id = $1 AND e.reverted_at IS NULL AND e.currency = 'RUB'), 0) AS BIGINT) AS revenue,
  CAST(COALESCE((SELECT SUM(ac.amount) FROM agent_commissions ac WHERE ac.agent_id = $1 AND ac.reverted_at IS NULL), 0) AS BIGINT) AS earned,
  CAST(COALESCE((SELECT SUM(ap.amount) FROM agent_payouts ap WHERE ap.agent_id = $1), 0) AS BIGINT) AS paid,
  CAST(COALESCE((SELECT SUM(visits) FROM agent_usage WHERE agent_id = $1), 0) AS BIGINT) AS usages;

-- name: SetCampaignAgent :execrows
-- Binds a campaign to an agent with its percent, or unbinds it (NULLs).
UPDATE campaign_tags
SET agent_id = sqlc.narg(agent_id), agent_percent = sqlc.narg(agent_percent), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND deleted = 0;

-- name: ListAgentCampaignsWithStats :many
-- The agent's own links, each with its arrivals, sales and the commissions that stand.
SELECT c.*,
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_visits v WHERE v.campaign_id = c.id), 0) AS BIGINT) AS visits,
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_events e WHERE e.campaign_id = c.id AND e.kind = 'bought' AND e.reverted_at IS NULL), 0) AS BIGINT) AS bought,
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_events e WHERE e.campaign_id = c.id AND e.kind = 'renewed' AND e.reverted_at IS NULL), 0) AS BIGINT) AS renewed,
  CAST(COALESCE((SELECT SUM(e.amount) FROM campaign_events e WHERE e.campaign_id = c.id AND e.reverted_at IS NULL AND e.currency = 'RUB'), 0) AS BIGINT) AS rub,
  CAST(COALESCE((SELECT SUM(ac.amount) FROM agent_commissions ac WHERE ac.campaign_id = c.id AND ac.reverted_at IS NULL), 0) AS BIGINT) AS earned
FROM campaign_tags c
WHERE c.agent_id = $1 AND c.deleted = 0
ORDER BY c.id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: CountAgentCampaigns :one
SELECT COUNT(*) AS total FROM campaign_tags WHERE agent_id = $1 AND deleted = 0;

-- name: ListAgentReferrals :many
-- The people an agent's links brought, each with their purchases and what they spent
-- (RUB only, the same money the campaign totals keep).
SELECT v.chat_id, v.tag, v.first_seen,
  CAST(COALESCE(tc.username, '') AS TEXT) AS username, CAST(COALESCE(tc.first_name, '') AS TEXT) AS first_name,
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_events e WHERE e.chat_id = v.chat_id AND e.campaign_id = v.campaign_id AND e.kind = 'bought' AND e.reverted_at IS NULL), 0) AS BIGINT) AS bought,
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_events e WHERE e.chat_id = v.chat_id AND e.campaign_id = v.campaign_id AND e.kind = 'renewed' AND e.reverted_at IS NULL), 0) AS BIGINT) AS renewed,
  CAST(COALESCE((SELECT SUM(e.amount) FROM campaign_events e WHERE e.chat_id = v.chat_id AND e.campaign_id = v.campaign_id AND e.reverted_at IS NULL AND e.currency = 'RUB'), 0) AS BIGINT) AS rub
FROM campaign_visits v
JOIN campaign_tags c ON c.id = v.campaign_id
LEFT JOIN tg_chats tc ON tc.tg_id = v.chat_id
WHERE c.agent_id = $1
ORDER BY v.id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: CountAgentReferrals :one
SELECT COUNT(*) AS total
FROM campaign_visits v JOIN campaign_tags c ON c.id = v.campaign_id
WHERE c.agent_id = $1;

-- name: CreateAgentCommission :execrows
-- Called for a payment the campaign attribution already stands behind, on the same
-- transaction; 0 rows means the payment paid once already.
INSERT INTO agent_commissions (agent_id, campaign_id, payment_id, kind, percent, sale_amount, amount, currency, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (payment_id) DO NOTHING;

-- name: RevertAgentCommissionsByPayment :execrows
UPDATE agent_commissions SET reverted_at = sqlc.arg(reverted_at) WHERE payment_id = sqlc.arg(payment_id) AND reverted_at IS NULL;

-- name: CreateAgentPayout :one
INSERT INTO agent_payouts (agent_id, amount, note, created_at)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: ListAgentPayouts :many
SELECT * FROM agent_payouts WHERE agent_id = $1
ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: CountAgentPayouts :one
SELECT COUNT(*) AS total FROM agent_payouts WHERE agent_id = $1;

-- name: CreateAgentSession :exec
INSERT INTO agent_sessions (id_hash, agent_id, csrf_token, created_at, last_seen_at, expires_at, ip, user_agent)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetAgentSession :one
SELECT * FROM agent_sessions WHERE id_hash = $1;

-- name: TouchAgentSession :execrows
UPDATE agent_sessions SET last_seen_at = $1 WHERE id_hash = $2;

-- name: DeleteAgentSession :exec
DELETE FROM agent_sessions WHERE id_hash = $1;

-- name: DeleteAgentSessions :exec
DELETE FROM agent_sessions WHERE agent_id = $1;

-- name: DeleteStaleAgentSessions :exec
DELETE FROM agent_sessions WHERE expires_at < sqlc.arg(now) OR last_seen_at < sqlc.arg(idle_before);

-- name: AgentByTgID :one
-- The agent whose own Telegram account this is; an unknown or switched-off one answers
-- nothing, so only a real agent is recognized.
SELECT id FROM campaign_agents WHERE tg_id = $1 AND disabled = 0 LIMIT 1;

-- name: AddAgentUsage :execrows
-- One more click of his own link by the agent; first_seen keeps the first one.
INSERT INTO agent_usage (agent_id, visits, first_seen, last_seen)
VALUES ($1, 1, $2, $2)
ON CONFLICT (agent_id) DO UPDATE SET visits = agent_usage.visits + 1, last_seen = GREATEST(agent_usage.last_seen, excluded.last_seen);
