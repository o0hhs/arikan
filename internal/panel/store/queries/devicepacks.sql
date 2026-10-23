-- name: ListDevicePacksOnSale :many
SELECT * FROM device_packs WHERE archived = false AND on_sale = true ORDER BY sort, id;

-- name: ListDevicePacks :many
SELECT * FROM device_packs ORDER BY sort, id;

-- name: GetDevicePack :one
SELECT * FROM device_packs WHERE id = $1;

-- name: CreateDevicePack :one
INSERT INTO device_packs (title, slots, price_rub, price_stars, sort, on_sale, archived, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: UpdateDevicePack :one
UPDATE device_packs
SET title = $1, slots = $2, price_rub = $3, price_stars = $4, sort = $5, on_sale = $6
WHERE id = $7
RETURNING *;

-- name: ArchiveDevicePack :execrows
UPDATE device_packs SET archived = true WHERE id = $1;

-- name: UnarchiveDevicePack :execrows
UPDATE device_packs SET archived = false WHERE id = $1;
