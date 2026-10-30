import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useBlocker } from "@tanstack/react-router";
import { Eye, Search } from "lucide-react";
import { useEffect, useState } from "react";
import { api, ApiError, errorText, unwrap, type Schemas } from "../../../api/client";
import { qk, useExitDns, useInbounds, useNodes } from "../../../api/hooks";
import { Card, FormActions, SaveBar } from "../../../components/layout";
import { Confirm, Drawer } from "../../../components/overlay";
import { SwitchRow } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { Button, EmptyState, ErrorState, Field, Pill, Segmented, Skeleton } from "../../../components/ui";
import { t, useLocale } from "../../../i18n";
import { useDraft } from "../../../lib/draft";
import { nodeLabel } from "../../../lib/node-label";
import { ExitDnsFields, exitDnsDraftOf, useSaveExitDns, type ExitDnsDraft } from "./exit-dns";
import { RulesEditor } from "./rules";
import { TrafficDetailCard } from "../traffic-detail";
import { useSaveSettings } from "./shared";
import { TemplateCard } from "./template";

type Routes = Schemas["Routes"];
type DNS = NonNullable<Routes["dns"]>;
type List = NonNullable<Routes["lists"]>[number];

/**
 * The parts of the routing, each in the section's menu; "yaml" is the own Clash profile.
 * The own rules and the rule lists by link are one screen (two ways to add rules), and the
 * apps that go past the VPN sit with the mode and the services they are an exception to.
 * An old link to "lists" opens the merged screen (see the router's search check).
 */
export const ROUTING_PARTS = ["mode", "rules", "dns", "advanced", "yaml"] as const;
export type RoutingPart = (typeof ROUTING_PARTS)[number];

// As many own lists as the server takes (subs.MaxLists).
const MAX_LISTS = 30;

const MODES = [
  { id: "ru_direct", title: "settings.routingRuDirect", sub: "settings.routingRuDirectSub" },
  { id: "all", title: "settings.routingAll", sub: "settings.routingAllSub" },
  { id: "blocked", title: "settings.routingBlocked", sub: "settings.routingBlockedSub" },
] as const;

// Where a service can go; "node:<id>" options follow from the nodes.
const TARGETS = [
  { id: "", label: "settings.routesFollow" },
  { id: "vpn", label: "settings.routesVpn" },
  { id: "direct", label: "settings.routesDirect" },
  { id: "block", label: "settings.routesBlock" },
] as const;

// Ready DNS sets for the device (GitHub issue #70): the panel's own is the empty one.
const DNS_PRESETS: { id: string; dns: DNS }[] = [
  { id: "panel", dns: {} },
  { id: "tunnel", dns: { nameserver: ["https://1.1.1.1/dns-query#PROXY", "https://8.8.8.8/dns-query#PROXY"], proxy_server_nameserver: ["1.1.1.1", "8.8.8.8"] } },
  {
    id: "ruYandex",
    dns: {
      // A bootstrap the server can reach without the tunnel: the public resolvers are
      // interfered with on a Russian server, and then nothing resolves at all.
      default_nameserver: ["77.88.8.8", "1.1.1.1"],
      nameserver: ["https://1.1.1.1/dns-query#PROXY", "https://8.8.8.8/dns-query#PROXY"],
      proxy_server_nameserver: ["77.88.8.8", "1.1.1.1"],
      policy: [
        { match: "+.ru", servers: ["https://77.88.8.8/dns-query"] },
        { match: "+.su", servers: ["https://77.88.8.8/dns-query"] },
        { match: "+.xn--p1ai", servers: ["https://77.88.8.8/dns-query"] },
      ],
    },
  },
];

// What is empty is left out, as the server leaves it: a draft that was emptied again is not a change.
function tidy(r: Routes): Routes {
  const out = { ...r };
  if (!Object.keys(out.services ?? {}).length) delete out.services;
  if (!out.direct?.length) delete out.direct;
  if (!out.lists?.length) delete out.lists;
  if (!out.servers?.auto && !out.servers?.interval && !out.servers?.no_countries) delete out.servers;
  if (!out.tune?.block_quic && !out.tune?.sniffer && !out.tune?.real_ip) delete out.tune;
  if (!out.dns?.nameserver?.length && !out.dns?.default_nameserver?.length && !out.dns?.proxy_server_nameserver?.length && !out.dns?.policy?.length) delete out.dns;
  return out;
}

const lines = (s: string) =>
  s
    .split("\n")
    .map((x) => x.trim())
    .filter(Boolean);

// "+.cn: https://dns.alidns.com/dns-query, 223.5.5.5" per line.
function parsePolicy(text: string): NonNullable<DNS["policy"]> {
  return lines(text).map((l) => {
    const [match = "", rest = ""] = l.split(/:\s+/, 2);
    return { match: match.trim(), servers: rest.split(",").map((x) => x.trim()).filter(Boolean) };
  });
}
const policyText = (p: DNS["policy"]) => (p ?? []).map((x) => `${x.match}: ${x.servers.join(", ")}`).join("\n");
// `default` is nameserver-policy's bootstrap: the resolvers that turn the nameservers' own
// names into addresses. A node on a server whose plain public DNS is interfered with (a
// Russian one, usually) resolves nothing until this is a resolver it can reach.
const dnsTextOf = (dns: DNS | undefined) => ({
  default: (dns?.default_nameserver ?? []).join("\n"),
  nameserver: (dns?.nameserver ?? []).join("\n"),
  proxy: (dns?.proxy_server_nameserver ?? []).join("\n"),
  policy: policyText(dns?.policy),
});

/** The refusal of the server groups' settings; every other one of sub_routes is a route's. */
const serversRefused = (e: ApiError | null) => e?.codes.sub_routes === "routes_servers";

/** The part a refusal of the server names, so a save from another part shows where it went wrong. */
function partOf(e: ApiError): RoutingPart | null {
  if (e.fields.sub_rules) return "rules";
  const code = e.codes.sub_routes ?? "";
  if (code === "routes_servers") return "advanced";
  if (code === "routes_dns") return "dns";
  if (code.startsWith("routes_list")) return "rules"; // routes_list_name, routes_lists_many…
  if (code) return "mode"; // a service, an app or a target: the routes screen
  return null;
}

/**
 * The routing of the subscriptions, a part at a time: the mode with the services and the
 * apps that are its exceptions, the own rules with the rule lists by link, DNS on the
 * device and at the exit, the server groups and fine tuning, the own Clash profile. The
 * simple settings are one draft saved from one bar; the exit DNS and the own profile are
 * resources of their own and save from their own cards.
 */
export function RoutingSection({ s, part, onPart }: { s: Schemas["SettingsView"]; part: RoutingPart; onPart: (p: RoutingPart) => void }) {
  const save = useSaveSettings();
  const exitQ = useExitDns();
  const saveExit = useSaveExitDns();
  const { draft, setDraft, dirty: routingDirty, reset } = useDraft({ mode: s.sub_routing, routes: s.sub_routes, rules: s.sub_rules });
  const exit = useDraft<ExitDnsDraft | null>(exitQ.data ? exitDnsDraftOf(exitQ.data) : null);
  const routes = draft.routes;
  const setRoutes = (f: (r: Routes) => Routes) => setDraft((d) => ({ ...d, routes: tidy(f(d.routes)) }));
  // The DNS fields keep their own text, so they are put back with the rest.
  const [dnsText, setDnsText] = useState(() => dnsTextOf(routes.dns));
  const [preview, setPreview] = useState(false);
  const [yaml, setYaml] = useState(s.sub_template);
  const [askOff, setAskOff] = useState(false);
  const discard = () => {
    reset();
    setDnsText(dnsTextOf(s.sub_routes.dns));
  };
  const submit = () => {
    if (!routingDirty) return;
    save.mutate(
      { sub_routing: draft.mode, sub_routes: draft.routes, sub_rules: draft.rules },
      {
        // The server keeps its own form of what it is given (a list it trims, a section it
        // drops when empty): the draft takes the answer back, or the bar stays for good.
        onSuccess: (v) => {
          setDraft({ mode: v.sub_routing, routes: v.sub_routes, rules: v.sub_rules });
          setDnsText(dnsTextOf(v.sub_routes.dns));
        },
        onError: (e) => {
          const p = e instanceof ApiError ? partOf(e) : null;
          if (p && p !== part) onPart(p);
        },
      },
    );
  };
  const apiErr = save.error instanceof ApiError ? save.error : null;
  const errors = apiErr?.fields ?? {};
  const starter = () => unwrap(api.POST("/api/v1/settings/routes/preview", { body: { sub_routing: draft.mode, sub_routes: draft.routes, starter: true } })).then((r) => r.profile);
  const live = s.sub_template.trim() !== "";
  // The own profile is the whole routing for the apps that take it: a simple part says so
  // and offers the one way back, instead of looking editable and doing nothing.
  const locked = live && part !== "yaml";
  const offing = save.isPending && save.variables?.sub_template === "";
  const turnOff = () =>
    save.mutate(
      { sub_template: "" },
      {
        onSuccess: (v) => {
          setYaml(() => v.sub_template);
          setAskOff(false);
        },
      },
    );
  // The exit DNS is a node's own setting: it works under the own YAML too. The device's DNS
  // is mostly dead under the profile (Clash and sing-box take DNS from it), but it still
  // gives Happ and INCY their resolvers, so it stays editable with a note.
  const modeErr = apiErr && ["routes_service", "routes_direct", "routes_target"].includes(apiErr.codes.sub_routes ?? "") ? errors.sub_routes : undefined;
  // A target names no screen of its own: a list's target and a service's land on both.
  const listErr = apiErr && ((apiErr.codes.sub_routes ?? "").startsWith("routes_list") || apiErr.codes.sub_routes === "routes_target") ? errors.sub_routes : undefined;
  // The settings show one tab at a time: leaving the routing (or the page) drops the draft,
  // the exit DNS and the own profile being edited, so ask first.
  const unsaved = routingDirty || exit.dirty || yaml !== s.sub_template;
  const leave = useBlocker({
    shouldBlockFn: ({ current, next }) => unsaved && (current.pathname !== next.pathname || (next.search as { tab?: string }).tab !== "routing"),
    enableBeforeUnload: () => unsaved,
    withResolver: true,
  });
  const saveExitNow = () => {
    if (exit.dirty && exit.draft) saveExit.mutate(exit.draft, { onSuccess: (nv) => exit.setDraft(exitDnsDraftOf(nv)) });
  };

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">{live && part !== "yaml" ? <Pill tone="warn">{t("settings.routesYamlOn")}</Pill> : null}</div>
        <Button size="sm" onClick={() => setPreview(true)} aria-haspopup="dialog">
          <Eye size={16} aria-hidden /> {t("settings.routesShowProfile")}
        </Button>
      </div>

      {locked && part !== "dns" ? <YamlLocked saving={offing} onOff={() => setAskOff(true)} onOpen={() => onPart("yaml")} /> : null}

      {part === "mode" && !locked ? (
        <ModePart
          mode={draft.mode}
          setMode={(mode) => setDraft((d) => ({ ...d, mode }))}
          routes={routes}
          setRoutes={setRoutes}
          error={modeErr}
        />
      ) : null}
      {part === "rules" && !locked ? (
        <>
          <ListsCard routes={routes} setRoutes={setRoutes} error={listErr} />
          <Card title={t("settings.rules")} sub={t("settings.routingPartsSub.rules")} i={2}>
            <RulesEditor
              value={draft.rules}
              onChange={(f) => setDraft((d) => ({ ...d, rules: f(d.rules) }))}
              error={errors.sub_rules}
              badLine={errors.sub_rules && typeof apiErr?.values.sub_rules === "number" ? apiErr.values.sub_rules : 0}
              targets={s.rule_targets}
            />
          </Card>
        </>
      ) : null}
      {part === "dns" ? (
        <>
          <Card title={t("settings.routesDnsDevice")} sub={t("settings.routesDnsDeviceSub")}>
            {live ? <div className="banner info mb-4">{t("settings.routesDnsYamlNote")}</div> : null}
            <DeviceDns
              text={dnsText}
              onPreset={(dns) => {
                setRoutes((r) => ({ ...r, dns }));
                setDnsText(dnsTextOf(dns));
              }}
              onText={(next) => {
                setDnsText(next);
                setRoutes((r) => ({
                  ...r,
                  dns: {
                    default_nameserver: lines(next.default),
                    nameserver: lines(next.nameserver),
                    proxy_server_nameserver: lines(next.proxy),
                    policy: parsePolicy(next.policy),
                  },
                }));
              }}
            />
          </Card>
          <Card title={t("settings.exitDns.title")} sub={t("settings.exitDns.lead")} i={2}>
            {exitQ.isError ? <ErrorState text={errorText(exitQ.error)} onRetry={() => void exitQ.refetch()} /> : null}
            {exitQ.data && exit.draft ? (
              <form
                noValidate
                onSubmit={(e) => {
                  e.preventDefault();
                  saveExitNow();
                }}
              >
                <ExitDnsFields v={exitQ.data} draft={exit.draft} setDraft={(f) => exit.setDraft((d) => (d ? f(d) : d))} error={saveExit.error} />
                <FormActions dirty={exit.dirty} saving={saveExit.isPending} onReset={() => exit.reset()} />
              </form>
            ) : exitQ.isPending ? (
              <Skeleton className="h-32 w-full rounded-2xl" />
            ) : null}
            <p className="mt-3 text-xs text-[var(--ink-500)]">{t("settings.exitDns.own")}</p>
          </Card>
        </>
      ) : null}
      {part === "advanced" && !locked ? <AdvancedPart routes={routes} setRoutes={setRoutes} error={serversRefused(apiErr) ? errors.sub_routes : undefined} /> : null}
      {part === "yaml" ? <TemplateCard s={s} text={yaml} setText={setYaml} starter={starter} /> : null}

      <TrafficDetailCard />

      {part !== "yaml" ? <p className="px-1 text-xs text-[var(--ink-500)]">{t("settings.routesNote")}</p> : null}

      <SaveBar dirty={routingDirty} saving={save.isPending} onSave={submit} onReset={discard} />

      <Drawer open={preview} onOpenChange={setPreview} title={t("settings.routesPreview")}>
        <div className="pt-4">
          {preview ? <PreviewCard mode={draft.mode} routes={draft.routes} rules={part === "yaml" ? undefined : draft.rules} template={live || part === "yaml" ? yaml : undefined} /> : null}
        </div>
      </Drawer>
      <Confirm
        open={leave.status === "blocked"}
        onOpenChange={(open) => !open && leave.reset?.()}
        title={t("settings.page.leaveTitle")}
        text={t("settings.routesLeaveText")}
        confirm={t("settings.page.leaveConfirm")}
        danger
        onConfirm={() => leave.proceed?.()}
      />
      <Confirm
        open={askOff}
        onOpenChange={setAskOff}
        title={t("settings.templateOffSure")}
        text={t("settings.routesYamlOffText")}
        confirm={t("settings.routesYamlOff")}
        loading={offing}
        danger
        onConfirm={turnOff}
      />
    </div>
  );
}

/**
 * What a simple part shows while the own Clash profile is live: the profile, not this
 * setting, decides what the apps get — and one button turns it off and comes back here.
 */
function YamlLocked({ saving, onOff, onOpen }: { saving: boolean; onOff: () => void; onOpen: () => void }) {
  return (
    <Card title={t("settings.routesYamlLocked")} sub={t("settings.routesYamlLockedSub")}>
      <p className="text-[13px] text-[var(--ink-600)]">{t("settings.routesYamlLockedText")}</p>
      <div className="mt-4 flex flex-wrap gap-2">
        <Button variant="primary" loading={saving} onClick={onOff}>
          {t("settings.routesYamlOff")}
        </Button>
        <Button variant="ghost" onClick={onOpen}>
          {t("settings.routesYamlOpen")}
        </Button>
      </div>
    </Card>
  );
}

/* ------------------------------------------------------------------ parts */

type SetRoutes = (f: (r: Routes) => Routes) => void;

function useTargetSelect() {
  const nodes = useNodes();
  const nodeName = (n: { id: number; name: string; local: boolean }) => t("settings.routesOnServer", { name: n.name || (n.local ? t("settings.routesThisServer") : `#${n.id}`) });
  return (id: string, value: string, onChange: (v: string) => void, opts: { follow?: boolean; label?: string; className?: string } = {}) => (
    <select id={id} className={opts.className ?? "input max-w-[220px]"} value={value} onChange={(e) => onChange(e.target.value)} aria-label={opts.label}>
      {TARGETS.filter((x) => opts.follow !== false || x.id).map((x) => (
        <option key={x.id} value={x.id}>
          {t(x.label)}
        </option>
      ))}
      {(nodes.data ?? []).length ? (
        <optgroup label={t("settings.routesServer")}>
          {(nodes.data ?? []).map((n) => (
            <option key={n.id} value={`node:${n.id}`}>
              {nodeName(n)}
            </option>
          ))}
        </optgroup>
      ) : null}
    </select>
  );
}

function useCatalog() {
  return useQuery({ queryKey: ["routes-catalog"], queryFn: ({ signal }) => unwrap(api.GET("/api/v1/settings/routes/catalog", { signal })), staleTime: Infinity });
}

/**
 * The "YouTube without ads" offer: one switch sends YouTube, Discord and Telegram through
 * the chosen node, and the node's own host gets the DPI bypass (the switch in its settings,
 * nfqws). The exit must be direct — the whole point is the node's Russian address, not a
 * foreign one — so the card names it and offers to move the node's connections back to
 * direct. The subscription page shows its card while the routes say the offer is on
 * (Routes.YouTubeAdFree).
 */
function YouTubeCard({ routes, setRoutes }: { routes: Routes; setRoutes: SetRoutes }) {
  const nodesQ = useNodes();
  const inboundsQ = useInbounds();
  const qc = useQueryClient();
  const toast = useToast();
  const nodes = (nodesQ.data ?? []).filter((n) => n.enabled);
  const inbounds = inboundsQ.data ?? [];
  const [pick, setPick] = useState(0);
  const [ask, setAsk] = useState(false);
  const yt = routes.services?.youtube ?? "";
  const entry = yt.startsWith("node:") ? Number(yt.slice(5)) : 0;
  const on = entry > 0 && routes.services?.ads === "block";
  useEffect(() => {
    if (pick === 0 && entry > 0) setPick(entry);
  }, [entry, pick]);
  useEffect(() => {
    if (pick === 0 && nodes.length > 0) setPick(nodes[0]!.id);
  }, [nodes, pick]);
  const entryNode = nodes.find((n) => n.id === entry);
  const own = inbounds.filter((i) => i.node_id === entry && i.enabled);
  const direct = own.length > 0 && own.every((i) => i.outbound === "direct");
  const foreign = own.filter((i) => i.outbound !== "direct").length;
  const current = !own.length
    ? t("settings.youtubeNoInbounds")
    : direct
      ? t("settings.youtubeExitDirectGood")
      : t("settings.youtubeExitForeign", { n: foreign });
  const move = useMutation({
    mutationFn: (body: Schemas["SetExitsInputBody"]) => unwrap(api.POST("/api/v1/inbounds/exit", { body })),
    onSuccess: () => {
      toast.ok(t("settings.youtubeExitDirectDone"));
      void qc.invalidateQueries({ queryKey: qk.inbounds });
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const apply = (want: boolean) => {
    const id = want ? pick || nodes[0]?.id || 0 : 0;
    if (want && !id) return;
    if (want) setPick(id);
    setRoutes((r) => {
      const services = { ...(r.services ?? {}) };
      if (want) {
        services.youtube = `node:${id}`;
        services.discord = `node:${id}`;
        services.ads = "block";
      } else {
        for (const key of ["youtube", "discord"]) if (services[key]?.startsWith("node:")) delete services[key];
        if (services.ads === "block") delete services.ads;
      }
      return { ...r, services, tune: { ...(r.tune ?? {}), block_quic: want ? true : r.tune?.block_quic } };
    });
  };
  const changeEntry = (id: number) => {
    setPick(id);
    if (!on) return;
    setRoutes((r) => {
      const services = { ...(r.services ?? {}) };
      for (const key of ["youtube", "discord"]) if (services[key]?.startsWith("node:")) services[key] = `node:${id}`;
      return { ...r, services };
    });
  };
  return (
    <Card title={t("settings.youtubeTitle")} sub={t("settings.youtubeSub")}>
      {!nodes.length ? (
        <p className="text-[13px] text-[var(--ink-500)]">{t("settings.youtubeNoNodes")}</p>
      ) : (
        <>
          <SwitchRow label={t("settings.youtubeOn")} sub={t("settings.youtubeEntryHint")} checked={on} onChange={apply} />
          <div className="mt-3">
            <Field label={t("settings.youtubeEntry")} htmlFor="yt-entry">
              <select id="yt-entry" className="input" value={pick || ""} onChange={(e) => changeEntry(Number(e.target.value))}>
                {nodes.map((n) => (
                  <option key={n.id} value={n.id}>
                    {nodeLabel(n)}
                  </option>
                ))}
              </select>
            </Field>
          </div>
          {on ? (
            <div className="mt-3 flex flex-wrap items-center gap-2">
              <Button size="sm" variant="ghost" disabled={!own.length || direct || move.isPending} onClick={() => setAsk(true)}>
                {t("settings.youtubeExitDirectNow")}
              </Button>
              <span className="text-xs text-[var(--ink-500)]">{current}</span>
            </div>
          ) : null}
          <p className="mt-3 text-xs text-[var(--ink-500)]">{t("settings.youtubeDpiHint")}</p>
        </>
      )}
      <Confirm
        open={ask}
        onOpenChange={setAsk}
        title={t("settings.youtubeDirectConfirmTitle")}
        text={t("settings.youtubeDirectConfirmText", { entry: entryNode ? nodeLabel(entryNode) : `#${entry}` })}
        confirm={t("settings.youtubeDirectConfirm")}
        danger
        onConfirm={() => {
          setAsk(false);
          move.mutate({ ids: own.map((i) => i.id), outbound: "direct" });
        }}
      />
    </Card>
  );
}

/** The mode, then each service as a tile with where it goes; the apps set apart go past the VPN. */
function ModePart({ mode, setMode, routes, setRoutes, error }: { mode: Schemas["SettingsView"]["sub_routing"]; setMode: (m: Schemas["SettingsView"]["sub_routing"]) => void; routes: Routes; setRoutes: SetRoutes; error?: string }) {
  const catalog = useCatalog();
  const locale = useLocale();
  const targetSelect = useTargetSelect();
  const [q, setQ] = useState("");
  const services = catalog.data?.services ?? [];
  const nameOf = (svc: (typeof services)[number]) => (locale === "en" ? svc.name_en : svc.name);
  const shown = services.filter((svc) => !q.trim() || nameOf(svc).toLowerCase().includes(q.trim().toLowerCase()) || svc.id.includes(q.trim().toLowerCase()));
  const set = Object.keys(routes.services ?? {}).length;
  const setService = (id: string, target: string) =>
    setRoutes((r) => {
      const next = { ...(r.services ?? {}) };
      if (target) next[id] = target;
      else delete next[id];
      return { ...r, services: next };
    });
  return (
    <>
      <YouTubeCard routes={routes} setRoutes={setRoutes} />
      <Card title={t("settings.routing")} sub={t("settings.routingHint")}>
        <div className="grid gap-2 sm:grid-cols-3" role="radiogroup" aria-label={t("settings.routing")}>
          {MODES.map((m) => (
            <button key={m.id} type="button" role="radio" aria-checked={mode === m.id} className="opt" onClick={() => setMode(m.id)}>
              <span className="font-semibold">{t(m.title)}</span>
              <span className="text-xs text-[var(--ink-500)]">{t(m.sub)}</span>
            </button>
          ))}
        </div>
      </Card>
      <Card title={t("settings.routesServices")} sub={t("settings.routesServicesHint")} i={2} aside={set ? <Pill tone="off">{t("settings.routesChanged", { n: set })}</Pill> : null}>
        <div className="mb-3 flex items-center gap-2">
          <div className="relative w-full max-w-[320px]">
            <Search size={16} className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-[var(--ink-400)]" aria-hidden />
            <input className="input w-full pl-9" type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("settings.routesSearch")} aria-label={t("settings.routesSearch")} />
          </div>
        </div>
        {catalog.isError ? <ErrorState text={errorText(catalog.error)} onRetry={() => void catalog.refetch()} /> : null}
        {catalog.isPending ? (
          <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3" aria-busy="true">
            {[0, 1, 2, 3, 4, 5].map((i) => (
              <Skeleton key={i} className="h-[88px] w-full rounded-2xl" />
            ))}
          </div>
        ) : null}
        {catalog.data && !shown.length ? <EmptyState icon={Search} search title={t("settings.routesSearchEmpty")} text={t("settings.routesSearchEmptyText")} /> : null}
        <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
          {shown.map((svc) => {
            const id = `s-svc-${svc.id}`;
            const value = routes.services?.[svc.id] ?? "";
            return (
              <div key={svc.id} className="panel-soft flex flex-col gap-2 p-3" style={value ? { borderColor: "var(--mikan-400)" } : undefined}>
                <label htmlFor={id} className="flex min-w-0 items-center gap-2 text-sm font-medium text-[var(--ink-900)]">
                  <span className="text-lg leading-none" aria-hidden>
                    {svc.icon}
                  </span>
                  <span className="truncate">{nameOf(svc)}</span>
                </label>
                {targetSelect(id, value, (v) => setService(svc.id, v), { className: "input h-9 w-full text-[13px]" })}
              </div>
            );
          })}
        </div>
        {error ? (
          <p className="mt-3 text-xs text-[var(--berry-600)]" role="alert">
            {error}
          </p>
        ) : null}
      </Card>
      <DirectAppsCard routes={routes} setRoutes={setRoutes} />
    </>
  );
}

/** The own rule lists by link: rules the apps download, each sent one way like a service. */
function ListsCard({ routes, setRoutes, error }: { routes: Routes; setRoutes: SetRoutes; error?: string }) {
  const targetSelect = useTargetSelect();
  const setList = (i: number, patch: Partial<List>) => setRoutes((r) => ({ ...r, lists: (r.lists ?? []).map((l, j) => (j === i ? { ...l, ...patch } : l)) }));
  const addList = () => setRoutes((r) => ({ ...r, lists: [...(r.lists ?? []), { name: `list-${(r.lists?.length ?? 0) + 1}`, url: "", behavior: "classical", format: "yaml", target: "vpn" }] }));
  const dropList = (i: number) => setRoutes((r) => ({ ...r, lists: (r.lists ?? []).filter((_, j) => j !== i) }));
  const lists = routes.lists ?? [];
  return (
    <Card title={t("settings.routesLists")} sub={t("settings.routesListsHint")}>
      <div className="flex flex-col gap-3">
        {lists.map((l, i) => (
          <div key={i} className="panel-soft grid gap-2 p-3 sm:grid-cols-[160px_minmax(0,1fr)]">
            <input className="input mono" value={l.name} onChange={(e) => setList(i, { name: e.target.value.toLowerCase() })} maxLength={32} aria-label={t("settings.routesListName")} placeholder="whitelist" />
            <input className="input mono" value={l.url} onChange={(e) => setList(i, { url: e.target.value.trim() })} maxLength={500} inputMode="url" autoComplete="off" spellCheck={false} aria-label={t("settings.routesListUrl")} placeholder="https://raw.githubusercontent.com/…/list.yaml" />
            <div className="flex flex-wrap items-center gap-2 sm:col-span-2">
              <select className="input max-w-[170px]" value={l.behavior} onChange={(e) => setList(i, { behavior: e.target.value })} aria-label={t("settings.routesListBehavior")}>
                <option value="classical">{t("settings.routesListClassical")}</option>
                <option value="domain">{t("settings.routesListDomain")}</option>
                <option value="ipcidr">{t("settings.routesListIP")}</option>
              </select>
              <select className="input max-w-[110px]" value={l.format} onChange={(e) => setList(i, { format: e.target.value })} aria-label={t("settings.routesListFormat")}>
                <option value="yaml">YAML</option>
                <option value="text">TXT</option>
                <option value="mrs">MRS</option>
              </select>
              {targetSelect(`s-list-${i}`, l.target, (v) => setList(i, { target: v }), { follow: false, label: t("settings.routesListTarget") })}
              <Button variant="ghost" className="ml-auto" onClick={() => dropList(i)} aria-label={t("settings.routesListRemoveNamed", { name: l.name })}>
                {t("settings.routesListRemove")}
              </Button>
            </div>
          </div>
        ))}
        {!lists.length ? <p className="text-[13px] text-[var(--ink-500)]">{t("settings.routesListsEmpty")}</p> : null}
        <div>
          <Button variant="ghost" onClick={addList} disabled={lists.length >= MAX_LISTS}>
            + {t("settings.routesListAdd")}
          </Button>
        </div>
        {error ? (
          <p className="text-xs text-[var(--berry-600)]" role="alert">
            {error}
          </p>
        ) : null}
      </div>
    </Card>
  );
}

/** The apps and sites that go past the VPN: they need a local address. */
function DirectAppsCard({ routes, setRoutes }: { routes: Routes; setRoutes: SetRoutes }) {
  const catalog = useCatalog();
  const locale = useLocale();
  const toggleDirect = (id: string) =>
    setRoutes((r) => {
      const has = (r.direct ?? []).includes(id);
      return { ...r, direct: has ? (r.direct ?? []).filter((x) => x !== id) : [...(r.direct ?? []), id] };
    });
  return (
    <Card title={t("settings.routesApps")} sub={t("settings.routesAppsHint")} i={3}>
      {catalog.isError ? <ErrorState text={errorText(catalog.error)} onRetry={() => void catalog.refetch()} /> : null}
      {catalog.isPending ? <Skeleton className="h-40 w-full rounded-2xl" /> : null}
      <div className="grid gap-2 sm:grid-cols-2">
        {(catalog.data?.direct ?? []).map((d) => {
          const on = (routes.direct ?? []).includes(d.id);
          return (
            <label key={d.id} className="panel-soft flex cursor-pointer items-start gap-3 p-3 text-sm" style={on ? { borderColor: "var(--mikan-400)" } : undefined}>
              <input type="checkbox" className="check mt-0.5 shrink-0" checked={on} onChange={() => toggleDirect(d.id)} />
              <span>{locale === "en" ? d.name_en : d.name}</span>
            </label>
          );
        })}
      </div>
    </Card>
  );
}

/** The DNS of the device's Clash profile: a ready set or the servers as mihomo writes them. */
function DeviceDns({ text, onPreset, onText }: { text: ReturnType<typeof dnsTextOf>; onPreset: (dns: DNS) => void; onText: (t: ReturnType<typeof dnsTextOf>) => void }) {
  const edit = (k: keyof typeof text) => (e: React.ChangeEvent<HTMLTextAreaElement>) => onText({ ...text, [k]: e.target.value });
  return (
    <>
      <div className="mb-4 flex flex-wrap gap-2" role="group" aria-label={t("settings.routesDnsPresets")}>
        {DNS_PRESETS.map((p) => (
          <button key={p.id} type="button" className="chip-btn" onClick={() => onPreset(p.dns)}>
            {t(`settings.routesDnsPreset.${p.id}` as "settings.routesDnsPreset.panel")}
          </button>
        ))}
      </div>
      <Field label={t("settings.routesDnsServers")} htmlFor="s-dns-ns" hint={t("settings.routesDnsHint")}>
        <textarea id="s-dns-ns" className="input mono min-h-[72px]" value={text.nameserver} onChange={edit("nameserver")} spellCheck={false} placeholder="https://1.1.1.1/dns-query#PROXY" />
      </Field>
      <Field label={t("settings.routesDnsDefault")} htmlFor="s-dns-default" hint={t("settings.routesDnsDefaultHint")}>
        <textarea id="s-dns-default" className="input mono min-h-[56px]" value={text.default} onChange={edit("default")} spellCheck={false} placeholder="77.88.8.8" />
      </Field>
      <Field label={t("settings.routesDnsProxy")} htmlFor="s-dns-proxy" hint={t("settings.routesDnsProxyHint")}>
        <textarea id="s-dns-proxy" className="input mono min-h-[56px]" value={text.proxy} onChange={edit("proxy")} spellCheck={false} placeholder="1.1.1.1" />
      </Field>
      <Field label={t("settings.routesDnsPolicy")} htmlFor="s-dns-policy" hint={t("settings.routesDnsPolicyHint")}>
        <textarea id="s-dns-policy" className="input mono min-h-[72px]" value={text.policy} onChange={edit("policy")} spellCheck={false} placeholder="+.cn: https://dns.alidns.com/dns-query" />
      </Field>
    </>
  );
}

/** How the server groups look in the apps, and the switches for experts. */
function AdvancedPart({ routes, setRoutes, error }: { routes: Routes; setRoutes: SetRoutes; error?: string }) {
  const servers = routes.servers ?? {};
  const setServers = (patch: Partial<NonNullable<Routes["servers"]>>) => setRoutes((r) => ({ ...r, servers: { ...(r.servers ?? {}), ...patch } }));
  const tune = routes.tune ?? {};
  const setTune = (patch: Partial<NonNullable<Routes["tune"]>>) => setRoutes((r) => ({ ...r, tune: { ...(r.tune ?? {}), ...patch } }));
  return (
    <>
      <Card title={t("settings.routesServers")} sub={t("settings.routesServersSub")}>
        <Field
          label={t("settings.routesAutoKind")}
          hint={t(servers.auto === "fallback" ? "settings.routesAutoFallbackHint" : servers.auto === "load-balance" ? "settings.routesAutoBalanceHint" : "settings.routesAutoFastestHint")}
        >
          <Segmented
            label={t("settings.routesAutoKind")}
            value={servers.auto === "fallback" ? "fallback" : servers.auto === "load-balance" ? "balance" : "fastest"}
            onChange={(v) => setServers({ auto: v === "fallback" ? "fallback" : v === "balance" ? "load-balance" : undefined })}
            options={[
              { value: "fastest", label: t("settings.routesAutoFastest") },
              { value: "fallback", label: t("settings.routesAutoFallback") },
              { value: "balance", label: t("settings.routesAutoBalance") },
            ]}
          />
        </Field>
        <Field label={t("settings.routesInterval")} htmlFor="s-interval" hint={t("settings.routesIntervalHint")}>
          <select id="s-interval" className="input max-w-[280px]" value={servers.interval ?? 0} onChange={(e) => setServers({ interval: Number(e.target.value) || undefined })}>
            {[...new Set([0, 60, 120, 600, 1800, servers.interval ?? 0])].map((n) => (
              <option key={n} value={n}>
                {n ? t("settings.routesIntervalSec", { n }) : t("settings.routesIntervalDefault")}
              </option>
            ))}
          </select>
        </Field>
        <SwitchRow label={t("settings.routesCountries")} sub={t("settings.routesCountriesSub")} checked={!servers.no_countries} onChange={(v) => setServers({ no_countries: v ? undefined : true })} />
        {error ? (
          <p className="mt-3 text-xs text-[var(--berry-600)]" role="alert">
            {error}
          </p>
        ) : null}
      </Card>
      <Card title={t("settings.routesTune")} sub={t("common.forExperts")} i={2}>
        <SwitchRow label={t("settings.routesQuic")} sub={t("settings.routesQuicSub")} checked={!!tune.block_quic} onChange={(v) => setTune({ block_quic: v || undefined })} />
        <SwitchRow label={t("settings.routesSniffer")} sub={t("settings.routesSnifferSub")} checked={!!tune.sniffer} onChange={(v) => setTune({ sniffer: v || undefined })} />
        <SwitchRow label={t("settings.routesRealIP")} sub={t("settings.routesRealIPSub")} checked={!!tune.real_ip} onChange={(v) => setTune({ real_ip: v || undefined })} />
      </Card>
    </>
  );
}

// The profile a user with every connection gets, before anything is saved: the unsaved
// rules too, or the own profile being edited.
function PreviewCard({ mode, routes, rules, template }: { mode: Schemas["SettingsView"]["sub_routing"]; routes: Routes; rules?: string; template?: string }) {
  const preview = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/api/v1/settings/routes/preview", {
          body: { sub_routing: mode, sub_routes: routes, ...(rules !== undefined ? { sub_rules: rules } : {}), ...(template?.trim() ? { sub_template: template } : {}) },
        }),
      ),
  });
  // Shown at once when the panel opens; the button asks again after more changes.
  const { mutate } = preview;
  useEffect(() => mutate(), [mutate]);
  return (
    <section>
      <p className="mb-3 text-[13px] text-[var(--ink-500)]">{t("settings.routesPreviewSub")}</p>
      <Button variant="ghost" loading={preview.isPending} onClick={() => preview.mutate()}>
        {preview.data ? t("settings.routesPreviewAgain") : t("settings.routesPreviewShow")}
      </Button>
      {preview.error ? (
        <p className="mt-3 text-xs text-[var(--md-error)]" role="alert">
          {errorText(preview.error)}
        </p>
      ) : null}
      {preview.data ? <pre className="panel-soft mono mt-3 max-h-[70vh] overflow-auto p-3 text-[12px] leading-[1.45]">{preview.data.profile}</pre> : null}
    </section>
  );
}
