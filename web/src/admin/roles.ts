// The panel's roles, as the pages see them: a superadmin keeps everything; support only
// the subscriber's card; tech the nodes, the protocols and the log. The API enforces the
// same by path; this keeps the menu and the routes from leading anywhere forbidden.
export type Role = "superadmin" | "support" | "tech";

const ALLOWED: Record<Role, string[]> = {
  superadmin: ["/"],
  support: ["/client"],
  tech: ["/nodes", "/inbounds", "/outbounds", "/audit"],
};

export function roleAllows(role: string | undefined, path: string): boolean {
  const r = (role ?? "superadmin") as Role;
  if (r === "superadmin") return true;
  const list = ALLOWED[r] ?? [];
  return list.some((p) => path === p || path.startsWith(p + "/"));
}

/** Where a role lands after a login or a stray link: its first open page. */
export function roleHome(role: string | undefined): string {
  switch (role) {
    case "support":
      return "/client";
    case "tech":
      return "/nodes";
    default:
      return "/";
  }
}
