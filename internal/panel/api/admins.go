package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/store/db"
)

// The panel's admins and their roles: the superadmin names the others, changes a role and
// takes an account off. The last superadmin cannot be demoted or deleted, and no one
// removes themselves: the panel must always keep someone who can name admins.

type adminRowView struct {
	ID          int64    `json:"id"`
	Username    string   `json:"username"`
	Role        string   `json:"role" enum:"superadmin,support,tech,custom"`
	Permissions []string `json:"permissions" enum:"users.read,users.write,billing.read,billing.write,nodes.read,nodes.write,settings.read,settings.write,automation.read,automation.write,backups.read,backups.write,approvals.read,approvals.decide,admins.manage,audit.read,support.card"`
	TOTPEnabled bool     `json:"totp_enabled"`
	LastLoginAt *string  `json:"last_login_at,omitempty"`
}

type adminListOutput struct{ Body []adminRowView }
type adminRowOutput struct{ Body adminRowView }

type adminCreateInput struct {
	Body struct {
		Username    string   `json:"username" minLength:"3" maxLength:"64"`
		Password    string   `json:"password" minLength:"8" maxLength:"200"`
		Role        string   `json:"role" enum:"superadmin,support,tech,custom"`
		Permissions []string `json:"permissions,omitempty"`
	}
}

type adminRoleInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Role        string   `json:"role" enum:"superadmin,support,tech,custom"`
		Permissions []string `json:"permissions,omitempty"`
	}
}

func (h *handlers) registerAdmins() {
	tags := []string{"admins"}
	huma.Register(h.api, huma.Operation{OperationID: "list-admins", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodGet, Path: "/api/v1/admins", Summary: "Администраторы панели", Tags: tags}, h.listAdmins)
	huma.Register(h.api, huma.Operation{OperationID: "create-admin", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPost, Path: "/api/v1/admins", Summary: "Добавить администратора", Tags: tags, DefaultStatus: http.StatusCreated}, h.createAdmin)
	huma.Register(h.api, huma.Operation{OperationID: "set-admin-role", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPut, Path: "/api/v1/admins/{id}", Summary: "Сменить роль администратора", Tags: tags}, h.setAdminRole)
	huma.Register(h.api, huma.Operation{OperationID: "delete-admin", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodDelete, Path: "/api/v1/admins/{id}", Summary: "Удалить администратора", Tags: tags, DefaultStatus: http.StatusNoContent}, h.deleteAdmin)
}

func adminRow(a db.Admin) adminRowView {
	v := adminRowView{ID: a.ID, Username: a.Username, Role: a.Role, Permissions: parsePermissions(a.Permissions), TOTPEnabled: a.TotpSecret.Valid}
	if v.Permissions == nil {
		v.Permissions = []string{}
	}
	if a.LastLoginAt.Valid {
		s := timeText(a.LastLoginAt.Int64)
		v.LastLoginAt = &s
	}
	return v
}

func (h *handlers) listAdmins(ctx context.Context, _ *struct{}) (*adminListOutput, error) {
	if err := h.requirePermission(ctx, PermAdminsManage); err != nil {
		return nil, err
	}
	rows, err := h.d.Store.Q.ListAdmins(ctx)
	if err != nil {
		return nil, err
	}
	out := &adminListOutput{Body: make([]adminRowView, 0, len(rows))}
	for _, a := range rows {
		out.Body = append(out.Body, adminRow(a))
	}
	return out, nil
}

func (h *handlers) createAdmin(ctx context.Context, in *adminCreateInput) (*adminRowOutput, error) {
	if err := h.requirePermission(ctx, PermAdminsManage); err != nil {
		return nil, err
	}
	permissions := []string{}
	if in.Body.Role == RoleCustom {
		permissions = normalizePermissions(in.Body.Permissions)
		if len(permissions) == 0 {
			return nil, fieldErr("body.permissions", "permissions_required")
		}
	}
	if err := h.checkAdminGrant(ctx, in.Body.Role, permissions); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Body.Username)
	if name == "" {
		return nil, huma.Error400BadRequest("bad_username")
	}
	hash, err := auth.HashPassword(in.Body.Password)
	if err != nil {
		return nil, huma.Error400BadRequest("bad_password")
	}
	a, err := h.d.Store.Q.CreateAdminWithPermissions(ctx, db.CreateAdminWithPermissionsParams{Username: name, PasswordHash: hash, Role: in.Body.Role, Permissions: permissionsJSON(permissions), CreatedAt: h.d.Now().Unix()})
	if err != nil {
		return nil, huma.Error400BadRequest("admin_exists")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "admin.create", "admin", strconv.FormatInt(a.ID, 10), map[string]any{"username": a.Username, "role": a.Role, "permissions": permissions})
	return &adminRowOutput{Body: adminRow(a)}, nil
}

func (h *handlers) setAdminRole(ctx context.Context, in *adminRoleInput) (*adminRowOutput, error) {
	if err := h.requirePermission(ctx, PermAdminsManage); err != nil {
		return nil, err
	}
	cur, err := h.d.Store.Q.GetAdmin(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	permissions := []string{}
	if in.Body.Role == RoleCustom {
		permissions = normalizePermissions(in.Body.Permissions)
		if len(permissions) == 0 {
			return nil, fieldErr("body.permissions", "permissions_required")
		}
	}
	if err := h.checkAdminGrant(ctx, in.Body.Role, permissions); err != nil {
		return nil, err
	}
	if err := h.checkAdminTarget(ctx, cur); err != nil {
		return nil, err
	}
	if cur.Role == RoleSuperadmin && in.Body.Role != RoleSuperadmin {
		n, err := h.d.Store.Q.CountAdminsWithRole(ctx, RoleSuperadmin)
		if err != nil {
			return nil, err
		}
		if n <= 1 {
			return nil, huma.Error400BadRequest("last_superadmin")
		}
	}
	if n, err := h.d.Store.Q.SetAdminRoleAndPermissions(ctx, db.SetAdminRoleAndPermissionsParams{Role: in.Body.Role, Permissions: permissionsJSON(permissions), ID: in.ID}); err != nil || n != 1 {
		if err != nil {
			return nil, err
		}
		return nil, huma.Error404NotFound("not_found")
	}
	a, err := h.d.Store.Q.GetAdmin(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "admin.role", "admin", strconv.FormatInt(a.ID, 10), map[string]any{"role": a.Role, "permissions": parsePermissions(a.Permissions)})
	return &adminRowOutput{Body: adminRow(a)}, nil
}

func (h *handlers) deleteAdmin(ctx context.Context, in *userIDInput) (*struct{}, error) {
	if err := h.requirePermission(ctx, PermAdminsManage); err != nil {
		return nil, err
	}
	sess := sessionOf(ctx)
	if in.ID == sess.AdminID {
		return nil, huma.Error400BadRequest("admin_self")
	}
	cur, err := h.d.Store.Q.GetAdmin(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	if err := h.checkAdminTarget(ctx, cur); err != nil {
		return nil, err
	}
	if cur.Role == RoleSuperadmin {
		n, err := h.d.Store.Q.CountAdminsWithRole(ctx, RoleSuperadmin)
		if err != nil {
			return nil, err
		}
		if n <= 1 {
			return nil, huma.Error400BadRequest("last_superadmin")
		}
	}
	n, err := h.d.Store.Q.DeleteAdmin(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.audit(ctx, sess.AdminID, "admin.delete", "admin", strconv.FormatInt(in.ID, 10), map[string]any{"username": cur.Username})
	return &struct{}{}, nil
}

// checkAdminGrant stops a custom admin from creating a role or a permission set wider than
// its own: admins.manage is not a way to become a superadmin.
func (h *handlers) checkAdminGrant(ctx context.Context, role string, permissions []string) error {
	actorRole, actorPerms := h.adminAccess(ctx, sessionOf(ctx).AdminID)
	if actorRole == RoleSuperadmin || actorRole == "" {
		return nil
	}
	if role != RoleCustom {
		return huma.Error403Forbidden("role_forbidden")
	}
	for _, p := range permissions {
		if !slices.Contains(actorPerms, p) {
			return huma.Error403Forbidden("role_forbidden")
		}
	}
	return nil
}

func (h *handlers) checkAdminTarget(ctx context.Context, target db.Admin) error {
	actorRole, _ := h.adminAccess(ctx, sessionOf(ctx).AdminID)
	if actorRole != RoleSuperadmin && actorRole != "" && target.Role == RoleSuperadmin {
		return huma.Error403Forbidden("role_forbidden")
	}
	return nil
}

// timeText is a unix time as the row shows it.
func timeText(sec int64) string {
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}
