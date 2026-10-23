package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// The panel's roles. A superadmin keeps everything: the till, the nodes, the tariffs, the
// settings, naming admins. Support keeps one subscriber's card alone — the search, the
// status, the bonus days, the sessions, the block — and nothing else. Tech keeps the nodes,
// the protocols and the logs. The role of a request is its admin's, session or API key.

const (
	RoleSuperadmin = "superadmin"
	RoleSupport    = "support"
	RoleTech       = "tech"
)

// roleAllows is what a role may call, by the path under /api/v1: auth is everyone's (the
// panel reads who it talks to), and the support card is the support's own. The support
// grants stay the superadmin's: the card prefix does not open them. Tech reads what the
// node pages need and writes the nodes, the protocols and the filters; everything else is
// the superadmin's.
func roleAllows(role, method, path string) bool {
	hit := func(prefixes ...string) bool {
		for _, p := range prefixes {
			if path == p || strings.HasPrefix(path, p+"/") {
				return true
			}
		}
		return false
	}
	switch role {
	case RoleSuperadmin, "":
		return true // an admin from before the roles keeps everything
	case RoleSupport:
		return strings.HasPrefix(path, "/auth/") || hit("/support/card")
	case RoleTech:
		if strings.HasPrefix(path, "/auth/") {
			return true
		}
		if path == "/support/card" || strings.HasPrefix(path, "/support/card/") {
			return false // the subscriber's card is not the tech's
		}
		if hit("/nodes", "/inbounds", "/outbounds", "/filters") {
			return true
		}
		if method == http.MethodGet || method == http.MethodHead {
			// What the node pages read beside the nodes: presets, pools, the channel, the
			// speed tests, the log.
			return hit("/presets", "/pools", "/balance", "/speedtests", "/node-checks", "/node", "/stats", "/dashboard", "/audit", "/traffic-detail", "/updates", "/exit-dns")
		}
		return false
	}
	return false
}

// adminRole is the admin's role; a store that cannot answer gives "": no role, no access.
func (h *handlers) adminRole(ctx context.Context, adminID int64) string {
	a, err := h.d.Store.Q.GetAdmin(ctx, adminID)
	if err != nil {
		return ""
	}
	return a.Role
}

// roleForRequest is the role the middleware puts into the context for the handlers.
type roleKeyType struct{}

var keyRole roleKeyType

// roleOf is the request's role; ok false: a support operator's session or a test without
// one (both keep their own rules).
func roleOf(ctx context.Context) (string, bool) {
	r, ok := ctx.Value(keyRole).(string)
	return r, ok
}

// roleRefusal is the answer a role does not open: the panel's page says which role was not
// enough, and nothing of the request's is read or written.
func roleRefusal(h *handlers, ctx huma.Context) {
	_ = huma.WriteErr(h.api, ctx, http.StatusForbidden, "role_forbidden")
}

// requireSuperadmin refuses a handler to any role but the superadmin's (naming admins, the
// support grants): the role gate opens the path to no one else, and this is the same
// refusal inside the handler, so a lost path does not let a role through.
func (h *handlers) requireSuperadmin(ctx context.Context) error {
	role, ok := roleOf(ctx)
	if ok && role != RoleSuperadmin && role != "" {
		return huma.Error403Forbidden("role_forbidden")
	}
	if !ok {
		sess := sessionOf(ctx)
		if r := h.adminRole(ctx, sess.AdminID); r != RoleSuperadmin && r != "" {
			return huma.Error403Forbidden("role_forbidden")
		}
	}
	return nil
}
