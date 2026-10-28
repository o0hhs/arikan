package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// The panel's roles. A superadmin keeps everything: the till, the nodes, the tariffs, the
// settings, naming admins. Support keeps one subscriber's card alone — the search, the
// status, the bonus days, the sessions, the block — and nothing else. Tech keeps the nodes,
// the protocols and the logs. A custom admin gets the areas named in its permissions list.

const (
	RoleSuperadmin = "superadmin"
	RoleSupport    = "support"
	RoleTech       = "tech"
	RoleCustom     = "custom"
)

// The permission names a custom admin may carry. The API gates paths by these names; the
// page uses the same list for its menu and checkboxes.
const (
	PermUsersRead       = "users.read"
	PermUsersWrite      = "users.write"
	PermBillingRead     = "billing.read"
	PermBillingWrite    = "billing.write"
	PermNodesRead       = "nodes.read"
	PermNodesWrite      = "nodes.write"
	PermSettingsRead    = "settings.read"
	PermSettingsWrite   = "settings.write"
	PermAutomationRead  = "automation.read"
	PermAutomationWrite = "automation.write"
	PermBackupsRead     = "backups.read"
	PermBackupsWrite    = "backups.write"
	PermApprovalsRead   = "approvals.read"
	PermApprovalsDecide = "approvals.decide"
	PermAdminsManage    = "admins.manage"
	PermAuditRead       = "audit.read"
	PermSupportCard     = "support.card"
)

var AllPermissions = []string{
	PermUsersRead, PermUsersWrite,
	PermBillingRead, PermBillingWrite,
	PermNodesRead, PermNodesWrite,
	PermSettingsRead, PermSettingsWrite,
	PermAutomationRead, PermAutomationWrite,
	PermBackupsRead, PermBackupsWrite,
	PermApprovalsRead, PermApprovalsDecide,
	PermAdminsManage, PermAuditRead, PermSupportCard,
}

func parsePermissions(raw string) []string {
	var out []string
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return normalizePermissions(out)
}

func normalizePermissions(in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p != "" && slices.Contains(AllPermissions, p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	// A write permission includes its read half: a page that can change a record must be
	// able to list it first.
	readOf := map[string]string{
		PermUsersWrite: PermUsersRead, PermBillingWrite: PermBillingRead,
		PermNodesWrite: PermNodesRead, PermSettingsWrite: PermSettingsRead,
		PermAutomationWrite: PermAutomationRead, PermBackupsWrite: PermBackupsRead,
		PermApprovalsDecide: PermApprovalsRead,
	}
	for _, p := range slices.Clone(out) {
		if r := readOf[p]; r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return out
}

func permissionsJSON(in []string) string {
	raw, _ := json.Marshal(normalizePermissions(in))
	return string(raw)
}

func hasPermission(role string, permissions []string, permission string) bool {
	switch role {
	case RoleSuperadmin, "":
		return true
	case RoleCustom:
		return slices.Contains(permissions, permission)
	default:
		return false
	}
}

func hit(path string, prefixes ...string) bool {
	for _, p := range prefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// readLikePath names the POSTs that only look: checks, previews and tests. A custom admin
// with the read permission may call them without the write one.
func readLikePath(path string) bool {
	for _, suffix := range []string{"/check", "/doctor", "/geo", "/reach", "/preview", "/validate", "/scan-targets", "/check-target", "/check-russia", "/speedtest", "/catalog"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

// permissionFor is the permission a path under /api/v1 needs. "" means every signed-in
// admin may call it (their own account); a path outside every area is closed to custom
// admins and to the built-in roles that do not name it.
func permissionFor(method, path string) string {
	// The admin's own account: who they are, their password, 2FA, sessions and keys.
	if strings.HasPrefix(path, "/auth/") || hit(path, "/api-keys") {
		return ""
	}
	if hit(path, "/support/card") {
		return PermSupportCard
	}
	// Support grants, recordings and the terminal stay the superadmin's.
	if hit(path, "/support") {
		return ""
	}
	if hit(path, "/admins") {
		return PermAdminsManage
	}
	if hit(path, "/audit") {
		return PermAuditRead
	}
	if hit(path, "/approvals") {
		if method == http.MethodGet || method == http.MethodHead {
			return PermApprovalsRead
		}
		if strings.HasSuffix(path, "/approve") || strings.HasSuffix(path, "/reject") {
			return PermApprovalsDecide
		}
		return PermApprovalsRead
	}
	mutating := method != http.MethodGet && method != http.MethodHead && !readLikePath(path)
	area := func(readPerm, writePerm string) string {
		if mutating {
			return writePerm
		}
		return readPerm
	}
	switch {
	case hit(path, "/automation"):
		return area(PermAutomationRead, PermAutomationWrite)
	case hit(path, "/s3-backup", "/telegram/backup", "/database/backup"):
		return area(PermBackupsRead, PermBackupsWrite)
	case hit(path, "/users", "/folders", "/import"):
		return area(PermUsersRead, PermUsersWrite)
	case hit(path, "/settings", "/sub-page", "/sub-docs", "/routes", "/monitor", "/telegram", "/webhooks"):
		return area(PermSettingsRead, PermSettingsWrite)
	case hit(path, "/payments", "/promocodes", "/discounts", "/device-packs", "/referrals", "/campaigns", "/partners", "/balance", "/balances", "/topups", "/tariffs", "/packages", "/analytics", "/agents"):
		return area(PermBillingRead, PermBillingWrite)
	case hit(path, "/nodes", "/inbounds", "/outbounds", "/filters", "/pools", "/presets", "/node", "/stats", "/dashboard", "/traffic-detail", "/exit-dns", "/updates", "/system", "/logs", "/metrics", "/torrent", "/speedtests", "/node-checks", "/cascade", "/warp", "/turn"):
		return area(PermNodesRead, PermNodesWrite)
	}
	return ""
}

// roleAllows is what a role may call, by the path under /api/v1. Auth and the admin's own
// keys are everyone's; the support card is the support's own; custom permissions gate the
// rest. The built-in support and tech keep their old path rules.
func roleAllows(role string, permissions []string, method, path string) bool {
	switch role {
	case RoleSuperadmin, "":
		return true // an admin from before the roles keeps everything
	case RoleSupport:
		return strings.HasPrefix(path, "/auth/") || hit(path, "/support/card")
	case RoleTech:
		if strings.HasPrefix(path, "/auth/") {
			return true
		}
		if hit(path, "/support/card") || hit(path, "/support") {
			return false
		}
		if hit(path, "/nodes", "/inbounds", "/outbounds", "/filters") {
			return true
		}
		if method == http.MethodGet || method == http.MethodHead {
			return hit(path, "/presets", "/pools", "/balance", "/speedtests", "/node-checks", "/node", "/stats", "/dashboard", "/audit", "/traffic-detail", "/updates", "/exit-dns")
		}
		return false
	case RoleCustom:
		perm := permissionFor(method, path)
		if perm == "" {
			return strings.HasPrefix(path, "/auth/") || hit(path, "/api-keys")
		}
		return slices.Contains(permissions, perm)
	}
	return false
}

// adminAccess is the admin's role and permissions; a store that cannot answer gives no
// role and no access.
func (h *handlers) adminAccess(ctx context.Context, adminID int64) (string, []string) {
	a, err := h.d.Store.Q.GetAdmin(ctx, adminID)
	if err != nil {
		return "", nil
	}
	return a.Role, parsePermissions(a.Permissions)
}

func (h *handlers) adminRole(ctx context.Context, adminID int64) string {
	role, _ := h.adminAccess(ctx, adminID)
	return role
}

type roleKeyType struct{}
type permissionsKeyType struct{}

var (
	keyRole        roleKeyType
	keyPermissions permissionsKeyType
)

// roleOf is the request's role; ok false: a support operator's session or a test without
// one (both keep their own rules).
func roleOf(ctx context.Context) (string, bool) {
	r, ok := ctx.Value(keyRole).(string)
	return r, ok
}

func permissionsOf(ctx context.Context) []string {
	p, _ := ctx.Value(keyPermissions).([]string)
	return p
}

// requestAccess is the role and permissions of whoever sent the request: the middleware's
// context, or the session's admin for handlers called directly by tests.
func (h *handlers) requestAccess(ctx context.Context) (string, []string) {
	if role, ok := roleOf(ctx); ok {
		return role, permissionsOf(ctx)
	}
	sess := sessionOf(ctx)
	if sess.AdminID == 0 {
		return "", nil
	}
	return h.adminAccess(ctx, sess.AdminID)
}

func roleRefusal(h *handlers, ctx huma.Context) {
	_ = huma.WriteErr(h.api, ctx, http.StatusForbidden, "role_forbidden")
}

// requirePermission refuses a handler to a role without the named permission. The path
// gate already does this; handlers that change who may do what repeat it, so a lost path
// does not let a role through.
func (h *handlers) requirePermission(ctx context.Context, permission string) error {
	role, perms := h.requestAccess(ctx)
	if role == "" && sessionOf(ctx).AdminID == 0 {
		return nil
	}
	if hasPermission(role, perms, permission) {
		return nil
	}
	return huma.Error403Forbidden("role_forbidden")
}

// requireSuperadmin is kept for the support grants and the terminal: those are not a
// custom admin's business even with admins.manage.
func (h *handlers) requireSuperadmin(ctx context.Context) error {
	role, ok := roleOf(ctx)
	if ok && role != RoleSuperadmin && role != "" {
		return huma.Error403Forbidden("role_forbidden")
	}
	if !ok {
		sess := sessionOf(ctx)
		if sess.AdminID == 0 {
			return nil
		}
		if r := h.adminRole(ctx, sess.AdminID); r != RoleSuperadmin && r != "" {
			return huma.Error403Forbidden("role_forbidden")
		}
	}
	return nil
}
