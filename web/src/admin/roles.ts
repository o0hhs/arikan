import type { Schemas } from "../api/client";

// The panel's roles, as the pages see them. The API gates every path by the same
// permissions; this keeps the menu and the routes from leading anywhere forbidden.
export type Role = "superadmin" | "support" | "tech" | "custom";
export type Permission = Schemas["AdminView"]["permissions"][number];

/** The checkboxes of a custom admin, grouped by the area they open. */
export const PERMISSION_GROUPS: { key: string; permissions: Permission[] }[] = [
  { key: "users", permissions: ["users.read", "users.write"] },
  { key: "billing", permissions: ["billing.read", "billing.write"] },
  { key: "nodes", permissions: ["nodes.read", "nodes.write"] },
  { key: "settings", permissions: ["settings.read", "settings.write"] },
  { key: "automation", permissions: ["automation.read", "automation.write"] },
  { key: "backups", permissions: ["backups.read", "backups.write"] },
  { key: "approvals", permissions: ["approvals.read", "approvals.decide"] },
  { key: "other", permissions: ["admins.manage", "audit.read", "support.card"] },
];

type Area = { prefix: string; permission: Permission | Permission[] };

// A custom admin's pages. The prefixes are the menu's sections; a nested page inherits
// its section's permission. Addons stay a superadmin's own: they can act as an admin.
const CUSTOM_AREAS: Area[] = [
  { prefix: "/client", permission: "support.card" },
  { prefix: "/", permission: "nodes.read" },
  { prefix: "/analytics", permission: "billing.read" },
  { prefix: "/users", permission: "users.read" },
  { prefix: "/tariffs", permission: "billing.read" },
  { prefix: "/inbounds", permission: "nodes.read" },
  { prefix: "/nodes", permission: "nodes.read" },
  { prefix: "/outbounds", permission: "nodes.read" },
  { prefix: "/payments", permission: "billing.read" },
  { prefix: "/automation", permission: "automation.read" },
  { prefix: "/approvals", permission: ["approvals.read", "approvals.decide"] },
  { prefix: "/diagnostics", permission: "nodes.read" },
  { prefix: "/audit", permission: "audit.read" },
  { prefix: "/settings", permission: "settings.read" },
  { prefix: "/api-docs", permission: "settings.read" },
];

const has = (permissions: string[], p: Permission | Permission[]) => (Array.isArray(p) ? p.some((x) => permissions.includes(x)) : permissions.includes(p));

function customAllows(permissions: string[], path: string): boolean {
  if (path === "/no-access") return true;
  const area = CUSTOM_AREAS.filter((a) => path === a.prefix || path.startsWith(a.prefix === "/" ? "/" : a.prefix + "/")).sort((a, b) => b.prefix.length - a.prefix.length)[0];
  return !!area && has(permissions, area.permission);
}

export function roleAllows(role: string | undefined, permissions: string[] | undefined, path: string): boolean {
  const r = (role ?? "superadmin") as Role;
  if (r === "superadmin") return true;
  if (path === "/no-access") return true;
  const perms = permissions ?? [];
  switch (r) {
    case "support":
      return path === "/client" || path.startsWith("/client/");
    case "tech":
      return ["/nodes", "/inbounds", "/outbounds", "/audit"].some((p) => path === p || path.startsWith(p + "/"));
    case "custom":
      return customAllows(perms, path);
    default:
      return false;
  }
}

/** Where a role lands after a login or a stray link: its first open page. */
export function roleHome(role: string | undefined, permissions?: string[]): string {
  switch (role) {
    case "support":
      return "/client";
    case "tech":
      return "/nodes";
    case "custom": {
      const order = ["/", "/analytics", "/users", "/tariffs", "/inbounds", "/nodes", "/outbounds", "/payments", "/automation", "/approvals", "/diagnostics", "/audit", "/settings", "/client"];
      return order.find((p) => roleAllows(role, permissions, p)) ?? "/no-access";
    }
    default:
      return "/";
  }
}
