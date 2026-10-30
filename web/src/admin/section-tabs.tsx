import { useQuery } from "@tanstack/react-query";
import { Link, useRouterState } from "@tanstack/react-router";
import { meQuery } from "../api/hooks";
import { t, type Key } from "../i18n";
import { roleAllows } from "./roles";
import "./section-tabs.css";

// One section, one row of tabs over the routes it already has: the address of every page
// stays what it was, and the role's own pages stay the only ones it sees.
type Tab = { to: "/nodes" | "/inbounds" | "/outbounds" | "/diagnostics" | "/addons/filters"; key: Key };

const SERVERS: readonly Tab[] = [
  { to: "/nodes", key: "nav.nodes" },
  { to: "/inbounds", key: "nav.inbounds" },
  { to: "/outbounds", key: "nav.outbounds" },
  { to: "/diagnostics", key: "nav.diagnostics" },
  { to: "/addons/filters", key: "nav.filter" },
];

export function ServersTabs() {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const me = useQuery({ ...meQuery });
  const role = me.data?.admin.role;
  const permissions = me.data?.admin.permissions ?? [];
  // A support operator has no servers at all; a tech admin keeps the three it may open.
  const shown = SERVERS.filter((x) => roleAllows(role, permissions, x.to));
  if (shown.length < 2) return null;
  return (
    <nav className="section-tabs" aria-label={t("nav.nodes")}>
      {shown.map((x) => {
        const on = x.to === path || path.startsWith(`${x.to}/`);
        return (
          <Link key={x.to} to={x.to} aria-current={on ? "page" : undefined}>
            {t(x.key)}
          </Link>
        );
      })}
    </nav>
  );
}
