package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// The fork renumbered its migrations when it moved onto upstream's dev: what was 0032 in
// 0.5.5.6 is 0066 in 0.5.5.7, 0034 is 0037, and so on. A database that ran 0.5.5.6 has
// goose's history filled with the old numbers, and goose then refuses the new files: it
// reports the versions below the record as "missing (out-of-order)" and, worse, would run a
// migration whose number was already used for something else. The map below is that move,
// by the migration's name rather than its number, and renumberLegacy applies it once, before
// any migration runs.
//
// Only a database whose latest version is one of the old ones is touched: a fresh install
// and a database already moved are left alone. node_turn (0035 in 0.5.5.6) has no file now
// — the VK-call way in was cut — and its record is dropped; webhook_events (0080) was merged
// into 0054_webhooks by upstream, which has the column already.
//
// Upstream's dev also brought migrations the fork never had (user_lifecycle, sub_rules,
// sub_templates, the *no_auto pair, node_billing, admin_passkeys, node_inbound_tags,
// user_node_traffic, inbound_hosts, campaign_tags). Their numbers are below the database's
// after the move, so goose would refuse them as out-of-order; they are applied first, here,
// in their own transaction, before the history is renumbered.

// legacyMigration is one old migration and where its file lives now; New is 0 when the
// migration has no file any more.
type legacyMigration struct {
	Old int64
	New int64
}

// legacyMigrations is 0.5.5.6's own migrations (0032..0083, after upstream's 0001..0031 and
// the shared 0032..0036 of the old base) and the file each one became.
// upstreamMigrations are upstream's own that 0.5.5.6 did not have, by name and in the order
// their numbers run. They create and alter tables the fork's data does not touch.
//
// campaign_tags is not among them: the fork's 0.5.5.6 had a campaign_tags of its own (its
// 0033), and its tables are already there under that name. Upstream's 0073 would create them
// again; the fork's file of that name is its own, and the history keeps the row the old
// database had.
var upstreamMigrations = []string{
	"user_lifecycle", "sub_rules", "sub_templates", "node_no_auto", "inbound_no_auto",
	"node_billing", "admin_passkeys", "node_inbound_tags", "user_node_traffic", "inbound_hosts",
}

var legacyMigrations = []legacyMigration{
	{32, 66}, // referrals
	{33, 38}, // campaigns
	{34, 37}, // node_clients
	{35, 0},  // node_turn — cut in 0.5.5.7
	{36, 67}, // totp_step
	{38, 68}, // referral_rewards_rule_unique
	{39, 69}, // referral_late_refund
	{40, 70}, // tariff_access_codes
	{41, 72}, // custom_tariff
	{42, 71}, // outbounds
	{43, 74}, // referral_reward_grants
	{44, 75}, // campaign_agents
	{45, 76}, // tariff_closed_protocols
	{46, 42}, // user_sites
	{47, 77}, // agent_self_usage
	{48, 32}, // support_access
	{49, 33}, // addon_community
	{50, 34}, // vmess_to_vless
	{51, 36}, // extra_devices
	{52, 39}, // balancing
	{53, 40}, // node_http_redirect
	{54, 41}, // balance
	{55, 43}, // node_sni_router
	{56, 44}, // node_api_lockdown
	{57, 45}, // reality_time_ms
	{58, 46}, // server_groups
	{59, 47}, // tariff_servers
	{60, 48}, // node_front_page
	{61, 49}, // reality_time_zero
	{62, 50}, // support_extended
	{63, 51}, // tg_clients
	{64, 52}, // inbound_settings_gone
	{65, 53}, // bot_events_gone
	{66, 54}, // node_relay_traffic
	{67, 78}, // discounts
	{68, 79}, // device_packs
	{69, 80}, // emergency_nodes
	{70, 81}, // admin_users
	{71, 82}, // partners
	{72, 83}, // admin_roles
	{73, 84}, // tariff_delete
	{74, 56}, // webhooks
	{75, 85}, // auto_include
	{76, 86}, // admin_permissions
	{77, 87}, // automation
	{78, 88}, // s3_backups
	{79, 89}, // approvals
	{80, 0},  // webhook_events — merged into 0056_webhooks by upstream
	{81, 90}, // approval_executing
	{82, 91}, // inbound_api
	{83, 92}, // inbound_exit_warp
}

// legacyTop is the last version 0.5.5.6 knew: a database there, and not one past it, is
// renumbered.
const legacyTop = 83

// renumberLegacy moves a 0.5.5.6 database's history onto the numbers 0.5.5.7's files have.
// It is a no-op for a fresh database, one already moved and one of another version.
func renumberLegacy(ctx context.Context, conn *sql.DB) error {
	ver, err := legacyVersion(ctx, conn)
	if err != nil || ver != legacyTop {
		return err
	}
	// Upstream's own migrations the fork never had are applied first: goose will not run them
	// after the move, as their numbers sit below the version it leaves.
	if err := applyUpstream(ctx, conn); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// The old numbers go first, so a new one is never taken while an old one of it is still
	// there (a unique version per row keeps the table honest).
	if _, err := tx.ExecContext(ctx, "UPDATE goose_db_version SET version_id = version_id + 100000 WHERE version_id > 36"); err != nil {
		return fmt.Errorf("renumber migrations: %w", err)
	}
	for _, m := range legacyMigrations {
		old := m.Old
		if old > 36 {
			old += 100000
		}
		if m.New == 0 {
			if _, err := tx.ExecContext(ctx, "DELETE FROM goose_db_version WHERE version_id=$1", old); err != nil {
				return fmt.Errorf("renumber migrations: %w", err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, "UPDATE goose_db_version SET version_id=$1, tstamp=now() WHERE version_id=$2", m.New, old); err != nil {
			return fmt.Errorf("renumber migrations: %w", err)
		}
	}
	// Two of upstream's files make a table the fork's database already has under another
	// history: campaign_tags (its 0073; the fork's own 0033 is 0038 now) and webhook_endpoints
	// (its 0056; the fork's 0074_webhooks made only the deliveries table, and its tools read an
	// endpoint's link). Neither may run again: goose would stop on "already exists". The first
	// has the tables, and is recorded as applied; the second's endpoint table is made here, in
	// the fork's shape, and the file is recorded too.
	if _, err := tx.ExecContext(ctx, "SELECT 1 FROM campaign_tags LIMIT 1"); err != nil {
		return fmt.Errorf("renumber migrations: campaign_tags: %w", err)
	}
	for _, name := range []string{"campaign_tags"} {
		v, ok := versionOfFile(name)
		if !ok {
			return fmt.Errorf("renumber migrations: no file for %s", name)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO goose_db_version (version_id, is_applied, tstamp) SELECT $1, true, now() WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id=$1)", v); err != nil {
			return fmt.Errorf("renumber migrations: %w", err)
		}
	}
	if v, ok := versionOfFile("webhooks"); ok {
		var has bool
		if err := tx.QueryRowContext(ctx, "SELECT to_regclass('webhook_endpoints') IS NOT NULL").Scan(&has); err != nil {
			return fmt.Errorf("renumber migrations: %w", err)
		}
		if !has {
			// The fork made only the deliveries table (its 0074); upstream's file makes the
			// endpoints and its own deliveries. The old rows are the fork's own deliveries,
			// with no endpoint behind them: they are kept aside under a name of their own.
			// The file runs right after the move (its version is above the old top), and it
			// records itself; only the fork's own history row is dropped here so goose sees
			// the file as pending.
			if err := migrateLegacyWebhooks(ctx, tx); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM goose_db_version WHERE version_id=$1", v); err != nil {
				return fmt.Errorf("renumber migrations: %w", err)
			}
		}
	}
	return tx.Commit()
}

// applyUpstream runs upstream's own migrations the fork never had, before the history moves:
// goose will not run them afterwards, as their numbers sit below the version the move
// leaves. Each is applied with goose's own provider, one at a time, so a database that
// already has one (a re-run after a crash) is not asked again.
func applyUpstream(ctx context.Context, conn *sql.DB) error {
	for _, name := range upstreamMigrations {
		if name == "campaign_tags" {
			continue
		}
		v, ok := versionOfFile(name)
		if !ok {
			return fmt.Errorf("migration %s: no file", name)
		}

		var n int
		if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM goose_db_version WHERE version_id=$1 AND is_applied", v).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		// The SQL of the one file is run as it is, then recorded: a provider would refuse to
		// be built while the files and the history disagree, which is exactly this state.
		if err := runUpstreamFile(ctx, conn, v, name); err != nil {
			return err
		}
	}
	return nil
}

// legacyVersion is the newest applied migration, or 0 when there is none.
func legacyVersion(ctx context.Context, conn *sql.DB) (int64, error) {
	var exists bool
	if err := conn.QueryRowContext(ctx, "SELECT to_regclass('goose_db_version') IS NOT NULL").Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	var v sql.NullInt64
	err := conn.QueryRowContext(ctx, "SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return v.Int64, nil
}

// runUpstreamFile runs the up half of upstream's migration v straight from its file and
// records the version, the way goose would: one transaction, the +goose Up statements.
func runUpstreamFile(ctx context.Context, conn *sql.DB, v int64, name string) error {
	raw, err := fs.ReadFile(postgresFS, fmt.Sprintf("%04d_%s.sql", v, name))
	if err != nil {
		return err
	}
	stmts, err := gooseUpStatements(string(raw))
	if err != nil {
		return fmt.Errorf("migration %d: %w", v, err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("migration %d (%s): %w", v, name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO goose_db_version (version_id, is_applied, tstamp) VALUES ($1, true, now())", v); err != nil {
		return fmt.Errorf("migration %d: %w", v, err)
	}
	return tx.Commit()
}

// gooseUpStatements are the statements between "-- +goose Up" and "-- +goose Down".
func gooseUpStatements(src string) ([]string, error) {
	var out []string
	inUp := false
	var cur []string
	flush := func() {
		if s := strings.TrimSpace(strings.Join(cur, "\n")); s != "" {
			out = append(out, s)
		}
		cur = nil
	}
	for _, line := range strings.Split(src, "\n") {
		switch strings.TrimSpace(line) {
		case "-- +goose Up":
			inUp = true
			continue
		case "-- +goose Down":
			flush()
			return out, nil
		}
		if !inUp {
			continue
		}
		if strings.Contains(line, ";") && !strings.HasPrefix(strings.TrimSpace(line), "--") {
			cur = append(cur, line)
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return out, nil
}

// versionOfFile is the version a migration of this name has in the embedded files.
func versionOfFile(name string) (int64, bool) {
	entries, err := fs.Glob(postgresFS, "*_"+name+".sql")
	if err != nil || len(entries) != 1 {
		return 0, false
	}
	var v int64
	if _, err := fmt.Sscanf(entries[0], "%d_", &v); err != nil {
		return 0, false
	}
	return v, true
}

// migrateLegacyWebhooks makes room for upstream's webhooks over the fork's: the fork's
// deliveries table has another shape and no endpoints, so its rows are renamed away (nothing
// reads them after the move: the events they carried are in the feed a week at most, and the
// new worker starts from the endpoints the admin sets again) and the file's tables are
// created by goose right after.
func migrateLegacyWebhooks(ctx context.Context, tx *sql.Tx) error {
	var hasOld bool
	if err := tx.QueryRowContext(ctx, "SELECT to_regclass('webhook_deliveries') IS NOT NULL").Scan(&hasOld); err != nil {
		return fmt.Errorf("renumber migrations: %w", err)
	}
	if hasOld {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE webhook_deliveries RENAME TO webhook_deliveries_before_0_5_5_7"); err != nil {
			return fmt.Errorf("renumber migrations: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DROP INDEX IF EXISTS webhook_deliveries_due"); err != nil {
			return fmt.Errorf("renumber migrations: %w", err)
		}
	}
	return nil
}
