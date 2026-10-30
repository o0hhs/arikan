import { useNavigate, useSearch } from "@tanstack/react-router";
import { ArrowDownToLine, Cpu, Globe, LayoutTemplate, LifeBuoy, Link2, Route, ShieldCheck } from "lucide-react";
import { useSettings } from "../../../api/hooks";
import { useSupportMode } from "../../../api/support";
import { SectionNav } from "../../../components/layout";
import { QueryBoundary } from "../../../components/query";
import { ThemeCard } from "../../../components/theme";
import { FontCard } from "../../../components/font";
import { PerfCard } from "../../../components/perf";
import { PageHeader, Skeleton } from "../../../components/ui";
import { t, type Key } from "../../../i18n";
import { SETTINGS_PARTS, SETTINGS_TABS, type SettingsSearch } from "../../search";
import { ClosedForSupport } from "../../support-closed";
import { AboutCard } from "./about";
import { AppCard } from "./app";
import { AuditCard } from "./audit";
import { BackupCard } from "./backup";
import { S3BackupCard } from "./s3-backup";
import { DashboardCard } from "./dashboard";
import { MonitorCard } from "./monitor";
import { LanguageCard, ServiceCard, UpdatesCard } from "./general";
import { GoalsCard } from "./goals";
import { ImportCard, LegacyLinksCard } from "./import";
import { ROUTING_PARTS, RoutingSection, type RoutingPart } from "./routing";
import { AccessCard, ApiCard, CertificateCard, PasswordCard, SessionsCard, TwoFactorCard } from "./security";
import { SupportCard } from "./support";
import { AppsCard, DevicesCard, InactiveCard, ProtocolsCard, SubDomainCard, SubNodesCard, SubPortCard, SubscriptionCard } from "./subscription";
import { AdminsCard } from "./admins";
import { SiteCard } from "./site";
import { HappCard } from "./happ";
import { WebhooksCard } from "./webhooks";
import { SubPageSettings, type SubPagePart } from "./sub-page";

type Tab = SettingsSearch["tab"];

const ICONS = { general: Globe, subscription: Link2, page: LayoutTemplate, routing: Route, security: ShieldCheck, support: LifeBuoy, system: Cpu, import: ArrowDownToLine } as const;

const PART_LABELS: Record<string, Key> = {
  look: "settings.page.tab.look",
  brand: "settings.page.tab.brand",
  blocks: "settings.page.tab.blocks",
  apps: "settings.page.tab.apps",
  docs: "settings.page.tab.docs",
  css: "settings.page.tab.css",
  mode: "settings.routingParts.mode",
  rules: "settings.routingParts.rules",
  dns: "settings.routingParts.dns",
  advanced: "settings.routingParts.advanced",
  yaml: "settings.routingParts.yaml",
};

/**
 * Settings in sections listed beside the content (a select on phones); the section and its
 * part are in the URL, so a link opens them. Forms keep one readable width; the editors with
 * a live preview take the whole column.
 */
export function SettingsPage() {
  const settings = useSettings();
  // A support operator has no account of their own here: the credentials, the sessions and the keys are closed to them.
  const operator = !!useSupportMode();
  const { tab, part } = useSearch({ from: "/_app/settings" });
  const navigate = useNavigate({ from: "/settings" });
  const go = (next: Tab, nextPart?: string) => void navigate({ search: { tab: next, part: nextPart }, replace: true });
  // The routing opens where it is in use: the own YAML once there is one. An old link to the
  // simple mode ("simple") opens the mode, and the router turns an old "lists" into the
  // merged rules-and-lists screen.
  const routingPart: RoutingPart = ROUTING_PARTS.includes(part as RoutingPart) ? (part as RoutingPart) : settings.data?.sub_template.trim() && part !== "simple" ? "yaml" : "mode";
  const pagePart: SubPagePart = (part as SubPagePart | undefined) ?? "look";
  const shownPart = tab === "page" ? pagePart : tab === "routing" ? routingPart : undefined;
  const wide = tab === "page" || tab === "routing";
  return (
    <>
      <PageHeader title={t("nav.settings")} sub={t("settings.subtitle")} />
      <SectionNav
        label={t("settings.sections")}
        sections={SETTINGS_TABS.map((id) => ({
          id,
          label: t(`settings.tabs.${id}`),
          icon: ICONS[id],
          parts: id in SETTINGS_PARTS ? SETTINGS_PARTS[id as keyof typeof SETTINGS_PARTS].map((p) => ({ id: p, label: t(PART_LABELS[p]!) })) : undefined,
        }))}
        value={tab}
        part={shownPart}
        onChange={go}
        narrow={!wide}
      >
        {tab === "page" ? (
          // The editor loads its own data and keeps its own draft.
          <SubPageSettings section={pagePart} onSection={(p) => go("page", p)} />
        ) : (
          <QueryBoundary query={settings} pending={<Skeleton style={{ height: 320, borderRadius: 20 }} />} wrap={(state) => <section className="card glass">{state}</section>}>
            {(s) =>
              tab === "general" ? (
                <>
                  <ServiceCard s={s} />
                  <DashboardCard />
                  <LanguageCard s={s} />
                  <ThemeCard />
                  <FontCard />
                  <PerfCard />
                  <AppCard s={s} />
                </>
              ) : tab === "subscription" ? (
                <>
                  <SubDomainCard s={s} />
                  <DevicesCard s={s} />
                  <AppsCard s={s} />
                  <HappCard s={s} />
                  <SubscriptionCard s={s} />
                  <ProtocolsCard s={s} />
                  <InactiveCard s={s} />
                  <SubPortCard s={s} />
                  <SubNodesCard s={s} />
                </>
              ) : tab === "routing" ? (
                <RoutingSection s={s} part={routingPart} onPart={(p) => go("routing", p)} />
              ) : tab === "security" ? (
                operator ? (
                  <>
                    <ClosedForSupport />
                    <CertificateCard s={s} />
                  </>
                ) : (
                  <>
                    <PasswordCard />
                    <TwoFactorCard />
                    <SessionsCard />
                    <AdminsCard />
                    <AuditCard />
                    <AccessCard s={s} />
                    <SiteCard s={s} />
                    <MonitorCard />
                    <CertificateCard s={s} />
                    <ApiCard />
                  </>
                )
              ) : tab === "support" ? (
                // Who may let a helper in is the admin's own business: an operator sees it closed.
                operator ? <ClosedForSupport /> : <SupportCard />
              ) : tab === "system" ? (
                <>
                  <UpdatesCard />
                  <AboutCard />
                  <BackupCard />
                  <S3BackupCard />
                  <GoalsCard />
                  <WebhooksCard s={s} />
                </>
              ) : (
                <>
                  <ImportCard />
                  <LegacyLinksCard />
                </>
              )
            }
          </QueryBoundary>
        )}
      </SectionNav>
    </>
  );
}
