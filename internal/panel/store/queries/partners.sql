-- name: ListPartners :many
SELECT * FROM partners ORDER BY created_at, id;

-- name: ListPartnerBots :many
SELECT * FROM partner_bots ORDER BY partner_id, created_at, id;

-- name: GetPartner :one
SELECT * FROM partners WHERE id = $1;

-- name: CreatePartner :one
INSERT INTO partners (name, note, owner_tg_id, created_at) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: UpdatePartner :one
UPDATE partners SET name = $1, note = $2, owner_tg_id = COALESCE(sqlc.narg(owner_tg_id), owner_tg_id) WHERE id = sqlc.arg(id) RETURNING *;

-- name: DeletePartner :execrows
DELETE FROM partners WHERE id = $1;

-- name: CreatePartnerBot :one
INSERT INTO partner_bots (partner_id, code, bot_username, bot_token, commission_percent, created_at)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;

-- name: UpdatePartnerBot :one
UPDATE partner_bots SET bot_username = $1, bot_token = $2, commission_percent = $3, enabled = $4 WHERE id = $5 RETURNING *;

-- name: DeletePartnerBot :execrows
DELETE FROM partner_bots WHERE id = $1;

-- name: PartnerBotByCode :one
SELECT b.*, p.owner_tg_id FROM partner_bots b JOIN partners p ON p.id = b.partner_id WHERE b.code = $1 AND b.enabled;

-- name: GetPartnerBot :one
SELECT * FROM partner_bots WHERE id = $1;

-- name: SetChatPartner :execrows
-- The first partner code the account came with stays: a second one writes nothing.
UPDATE tg_chats SET partner_bot_id = $1 WHERE tg_id = $2 AND partner_bot_id IS NULL;

-- name: SetUserPartner :execrows
UPDATE users SET partner_bot_id = $1 WHERE id = $2 AND partner_bot_id IS NULL;

-- name: SetPaymentPartner :exec
UPDATE payments SET partner_bot_id = $1 WHERE id = $2;

-- name: ChatPartner :one
SELECT partner_bot_id FROM tg_chats WHERE tg_id = $1;

-- name: CreditPartner :one
UPDATE partner_bots SET balance_kop = balance_kop + sqlc.arg(amount_kop)::BIGINT WHERE id = sqlc.arg(id) RETURNING *;

-- name: AddPartnerTransaction :exec
INSERT INTO partner_transactions (partner_bot_id, payment_id, kind, amount_kop, created_at)
VALUES ($1, $2, $3, $4, $5);

-- name: PartnerTransactions :many
SELECT * FROM partner_transactions WHERE partner_bot_id = $1 ORDER BY id DESC LIMIT $2;

-- name: PartnerTurnover :one
SELECT CAST(COALESCE(sum(amount_kop), 0) AS BIGINT) FROM partner_transactions WHERE partner_bot_id = $1;

-- name: PartnerCommissionOfPayment :one
SELECT * FROM partner_transactions WHERE payment_id = $1 AND kind = 'commission' ORDER BY id DESC LIMIT 1;

-- name: DebitPartner :one
UPDATE partner_bots SET balance_kop = balance_kop - sqlc.arg(amount_kop)::BIGINT WHERE id = sqlc.arg(id) RETURNING *;

-- name: PartnerRefundOfPayment :one
SELECT * FROM partner_transactions WHERE payment_id = $1 AND kind = 'refund' ORDER BY id DESC LIMIT 1;
