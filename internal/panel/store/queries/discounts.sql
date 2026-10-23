-- name: GetDiscount :one
SELECT * FROM discounts WHERE id = sqlc.arg(id) AND deleted=0;

-- name: ListDiscounts :many
SELECT * FROM discounts WHERE deleted=0 ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: CountDiscounts :one
SELECT COUNT(*) FROM discounts WHERE deleted=0;

-- name: ListActiveDiscounts :many
-- The discounts that stand now: on, not deleted, and inside their window.
SELECT * FROM discounts WHERE enabled=1 AND deleted=0 AND starts_at<=sqlc.arg(now) AND ends_at>sqlc.arg(now)
ORDER BY id;

-- name: ListActiveDiscountsForTariff :many
-- The discounts that stand now for one tariff: the ones about it, and the ones about every
-- tariff (no row of their own).
SELECT d.* FROM discounts d
WHERE d.enabled=1 AND d.deleted=0 AND d.starts_at<=sqlc.arg(now) AND d.ends_at>sqlc.arg(now)
  AND (NOT EXISTS (SELECT 1 FROM discount_tariffs t WHERE t.discount_id=d.id)
       OR EXISTS (SELECT 1 FROM discount_tariffs t WHERE t.discount_id=d.id AND t.tariff_id=sqlc.arg(tariff_id)))
ORDER BY d.id;

-- name: CreateDiscount :one
INSERT INTO discounts
(name,description,type,value,currency,starts_at,ends_at,max_uses,used_count,min_order,max_discount,notify_enabled,notify_days,notify_audience,notify_text,enabled,deleted,created_at,created_by)
VALUES (sqlc.arg(name),sqlc.arg(description),sqlc.arg(type),sqlc.arg(value),sqlc.arg(currency),sqlc.arg(starts_at),sqlc.arg(ends_at),sqlc.narg(max_uses),0,sqlc.arg(min_order),sqlc.arg(max_discount),sqlc.arg(notify_enabled),sqlc.arg(notify_days),sqlc.arg(notify_audience),sqlc.arg(notify_text),sqlc.arg(enabled),0,sqlc.arg(created_at),sqlc.narg(created_by)) RETURNING *;

-- name: UpdateDiscount :one
UPDATE discounts SET
name=sqlc.arg(name),description=sqlc.arg(description),type=sqlc.arg(type),value=sqlc.arg(value),currency=sqlc.arg(currency),
starts_at=sqlc.arg(starts_at),ends_at=sqlc.arg(ends_at),max_uses=sqlc.narg(max_uses),min_order=sqlc.arg(min_order),max_discount=sqlc.arg(max_discount),
notify_enabled=sqlc.arg(notify_enabled),notify_days=sqlc.arg(notify_days),notify_audience=sqlc.arg(notify_audience),notify_text=sqlc.arg(notify_text),
enabled=sqlc.arg(enabled) WHERE id=sqlc.arg(id) AND deleted=0 RETURNING *;

-- name: DeleteDiscount :execrows
UPDATE discounts SET deleted=1, enabled=0 WHERE id=sqlc.arg(id) AND deleted=0;

-- name: SetDiscountEnabled :execrows
UPDATE discounts SET enabled=sqlc.arg(enabled) WHERE id=sqlc.arg(id) AND deleted=0;

-- name: IncrementDiscountUse :execrows
-- One use of the discount, while it has uses left.
UPDATE discounts SET used_count=used_count+1 WHERE id=sqlc.arg(id) AND deleted=0 AND enabled=1
  AND (max_uses IS NULL OR used_count<max_uses);

-- name: DecrementDiscountUse :execrows
UPDATE discounts SET used_count=CASE WHEN used_count>0 THEN used_count-1 ELSE 0 END WHERE id=sqlc.arg(id);

-- name: SetDiscountTariffs :exec
DELETE FROM discount_tariffs WHERE discount_id=sqlc.arg(discount_id);

-- name: AddDiscountTariff :exec
INSERT INTO discount_tariffs (discount_id,tariff_id) VALUES (sqlc.arg(discount_id),sqlc.arg(tariff_id)) ON CONFLICT DO NOTHING;

-- name: ListDiscountTariffs :many
SELECT t.tariff_id FROM discount_tariffs t WHERE t.discount_id=sqlc.arg(discount_id) ORDER BY t.tariff_id;

-- name: ListDiscountTariffNames :many
SELECT t.name FROM discount_tariffs d JOIN tariffs t ON t.id=d.tariff_id WHERE d.discount_id=sqlc.arg(discount_id) ORDER BY t.name;

-- name: CreateDiscountRedemption :one
INSERT INTO discount_redemptions
(discount_id,user_id,tg_id,payment_id,status,redeemed_at,discount_amount,original_amount,final_amount,currency)
VALUES (sqlc.arg(discount_id),sqlc.narg(user_id),sqlc.arg(tg_id),sqlc.narg(payment_id),sqlc.arg(status),sqlc.arg(redeemed_at),sqlc.arg(discount_amount),sqlc.arg(original_amount),sqlc.arg(final_amount),sqlc.arg(currency)) RETURNING *;

-- name: GetDiscountRedemptionByPayment :one
SELECT * FROM discount_redemptions WHERE payment_id=sqlc.arg(payment_id);

-- name: MarkDiscountApplied :execrows
UPDATE discount_redemptions SET status='applied', user_id=COALESCE(sqlc.narg(user_id),user_id) WHERE id=sqlc.arg(id) AND status='reserved';

-- name: ReleaseDiscountRedemption :execrows
UPDATE discount_redemptions SET status='released' WHERE id=sqlc.arg(id) AND status='reserved';

-- name: ReleaseDiscountRedemptionByPayment :execrows
UPDATE discount_redemptions SET status='released' WHERE payment_id=sqlc.arg(payment_id) AND status='reserved';

-- name: ListDiscountRedemptionsWithDiscount :many
SELECT r.*, d.name AS discount_name FROM discount_redemptions r JOIN discounts d ON d.id=r.discount_id
ORDER BY r.id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: CountDiscountRedemptions :one
SELECT COUNT(*) FROM discount_redemptions;

-- name: DiscountStats :one
-- What a discount brought: the orders at the lower price and what they gave off.
SELECT COUNT(CASE WHEN r.status='applied' THEN 1 END) AS orders, CAST(COALESCE(SUM(CASE WHEN r.status='applied' THEN r.discount_amount ELSE 0 END),0) AS BIGINT) AS discount_amount, CAST(COALESCE(SUM(CASE WHEN r.status='applied' THEN r.final_amount ELSE 0 END),0) AS BIGINT) AS revenue FROM discount_redemptions r WHERE r.discount_id=sqlc.arg(discount_id);

-- name: ListDiscountBuyers :many
-- The accounts that bought the discount: the mailing's buyers.
SELECT DISTINCT r.tg_id FROM discount_redemptions r WHERE r.discount_id=sqlc.arg(discount_id) AND r.status='applied' ORDER BY r.tg_id;

-- name: CountDiscountBuyers :one
SELECT COUNT(DISTINCT r.tg_id) FROM discount_redemptions r WHERE r.discount_id=sqlc.arg(discount_id) AND r.status='applied';

-- name: ListDiscountSubscribers :many
-- The accounts with a subscription on one of the tariffs the discount is about, that did not
-- block the bot: the mailing's subscribers.
SELECT DISTINCT l.tg_id FROM tg_links l
  JOIN users u ON u.id=l.user_id
  LEFT JOIN tg_chats c ON c.tg_id=l.tg_id
WHERE COALESCE(c.blocked,0)=0 AND u.status='active'
  AND (NOT EXISTS (SELECT 1 FROM discount_tariffs t WHERE t.discount_id=sqlc.arg(discount_id))
       OR EXISTS (SELECT 1 FROM discount_tariffs t WHERE t.discount_id=sqlc.arg(discount_id) AND t.tariff_id=u.tariff_id))
ORDER BY l.tg_id;

-- name: CountDiscountSubscribers :one
SELECT COUNT(DISTINCT l.tg_id) FROM tg_links l
  JOIN users u ON u.id=l.user_id
  LEFT JOIN tg_chats c ON c.tg_id=l.tg_id
WHERE COALESCE(c.blocked,0)=0 AND u.status='active'
  AND (NOT EXISTS (SELECT 1 FROM discount_tariffs t WHERE t.discount_id=sqlc.arg(discount_id))
       OR EXISTS (SELECT 1 FROM discount_tariffs t WHERE t.discount_id=sqlc.arg(discount_id) AND t.tariff_id=u.tariff_id));

-- name: DiscountNoticeSent :one
SELECT EXISTS (SELECT 1 FROM discount_notices WHERE discount_id=sqlc.arg(discount_id) AND tg_id=sqlc.arg(tg_id) AND kind=sqlc.arg(kind));

-- name: ListDiscountNotices :many
-- The reminders already told of one discount: the accounts and the kinds.
SELECT tg_id, kind FROM discount_notices WHERE discount_id=sqlc.arg(discount_id);

-- name: AddDiscountNotice :execrows
INSERT INTO discount_notices (discount_id,tg_id,kind,sent_at) VALUES (sqlc.arg(discount_id),sqlc.arg(tg_id),sqlc.arg(kind),sqlc.arg(sent_at)) ON CONFLICT DO NOTHING;

-- name: PruneDiscountNotices :exec
DELETE FROM discount_notices WHERE sent_at < sqlc.arg(before);
