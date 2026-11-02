-- +goose Up
-- Who may run the bot's admin side, and as what: a superadmin keeps the till, the nodes
-- and the tariffs and names the others; support sees one subscriber's card (days, sessions,
-- the block) and the sleeping trials; tech sees the nodes' summary. The panel's own admins
-- (the web session) are a different list: this one is Telegram accounts.

CREATE TABLE admin_users (
  tg_id      BIGINT PRIMARY KEY,
  username   TEXT NOT NULL DEFAULT '',
  role       TEXT NOT NULL CHECK (role IN ('superadmin','support','tech')),
  created_at BIGINT NOT NULL,
  created_by BIGINT
);

-- +goose Down
DROP TABLE IF EXISTS admin_users;
