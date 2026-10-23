// Device packs: extra device places a buyer adds to a tariff. The pack rides the
// subscription — its price adds to every term, its slots widen the device limit — and is
// chosen again on every invoice, so a renewal without a pack takes the places back.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Archive, ArchiveRestore, Pencil } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { onFormError } from "../../lib/fields";
import { qk, useDevicePacks } from "../../api/hooks";
import { Confirm, Drawer, DrawerFooter } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { Field, Pill, Skeleton } from "../../components/ui";
import { Switch } from "../../components/switch";
import { t } from "../../i18n";
import { rubles } from "../../lib/format";
import { isKopecks, isStars, rubKopecks } from "../../lib/validate";

type Pack = Schemas["DevicePackView"];

/** The packs; a new one is started from the page's header (`creating`) or the empty list. */
export function DevicePacksCard({ creating, onCreateClose }: { creating: boolean; onCreateClose: () => void }) {
  const packs = useDevicePacks();
  const qc = useQueryClient();
  const toast = useToast();
  const [edit, setEdit] = useState<Pack | "new" | null>(null);
  const [archive, setArchive] = useState<Pack | null>(null);
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/device-packs/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.devicePacks });
      setArchive(null);
      toast.ok(t("devicePacks.archived"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const restore = useMutation({
    mutationFn: (id: number) => unwrap(api.POST("/api/v1/device-packs/{id}/restore", { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.devicePacks });
      toast.ok(t("devicePacks.restored"));
    },
    onError: (e) => toast.error(errorText(e)),
  });

  return (
    <section className="card glass reveal">
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("devicePacks.title")}</h2>
          <div className="card-sub">{t("devicePacks.sub")}</div>
        </div>
      </div>
      <QueryBoundary query={packs} pending={<Skeleton style={{ height: 64 }} />}>
        {(list) =>
          list.length === 0 ? (
            <p className="text-xs text-[var(--md-on-surface-variant)]">{t("devicePacks.none")}</p>
          ) : (
            <ul className="row-list">
              {list.map((p) => (
                <li key={p.id} className={`flex flex-wrap items-center justify-between gap-3 py-3${p.archived ? " opacity-60" : ""}`}>
                  <div className="min-w-0">
                    <div className="text-[13px] font-semibold">{p.title}</div>
                    <div className="text-xs text-[var(--md-on-surface-variant)]">
                      {t("devicePacks.slotsLine", { n: p.slots })} · {t("devicePacks.perMonth")}
                    </div>
                    <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-[var(--md-on-surface-variant)]">
                      {p.archived ? <Pill tone="off">{t("devicePacks.archivedPill")}</Pill> : p.on_sale ? <Pill tone="ok">{t("tariffs.onSale")}</Pill> : <Pill tone="off">{t("packages.notOnSale")}</Pill>}
                      {p.price_rub > 0 ? <span className="num">{rubles(p.price_rub)}</span> : null}
                      {p.price_stars != null ? <span className="num">⭐ {p.price_stars}</span> : null}
                    </div>
                  </div>
                  <div className="flex gap-1">
                    <button type="button" className="icon-btn" aria-label={t("devicePacks.editLabel", { name: p.title })} onClick={() => setEdit(p)}>
                      <Pencil size={16} />
                    </button>
                    {p.archived ? (
                      <button type="button" className="icon-btn" aria-label={t("devicePacks.restoreLabel", { name: p.title })} disabled={restore.isPending} onClick={() => restore.mutate(p.id)}>
                        <ArchiveRestore size={16} />
                      </button>
                    ) : (
                      <button type="button" className="icon-btn" aria-label={t("devicePacks.archiveLabel", { name: p.title })} onClick={() => setArchive(p)}>
                        <Archive size={16} />
                      </button>
                    )}
                  </div>
                </li>
              ))}
            </ul>
          )
        }
      </QueryBoundary>
      <DevicePackDrawer pack={creating ? "new" : edit} onClose={() => { setEdit(null); onCreateClose(); }} />
      <Confirm
        open={!!archive}
        onOpenChange={(v) => !v && setArchive(null)}
        title={t("devicePacks.archiveTitle", { name: archive?.title ?? "" })}
        text={t("devicePacks.archiveText")}
        confirm={t("tariffs.archiveConfirm")}
        loading={remove.isPending}
        onConfirm={() => archive && remove.mutate(archive.id)}
      />
    </section>
  );
}

function DevicePackDrawer({ pack, onClose }: { pack: Pack | "new" | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [title, setTitle] = useState("");
  const [slots, setSlots] = useState("2");
  const [rub, setRub] = useState("50");
  const [stars, setStars] = useState("");
  const [onSale, setOnSale] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  useEffect(() => {
    if (!pack) return;
    const p = pack === "new" ? null : pack;
    setTitle(p?.title ?? "");
    setSlots(String(p?.slots ?? 2));
    setRub(p ? String(p.price_rub / 100) : "50");
    setStars(p?.price_stars != null ? String(p.price_stars) : "");
    setOnSale(p?.on_sale ?? false);
    setErrors({});
  }, [pack]);

  const save = useMutation({
    mutationFn: (body: Schemas["DevicePackBody"]) =>
      pack === "new" || !pack ? unwrap(api.POST("/api/v1/device-packs", { body })) : unwrap(api.PUT("/api/v1/device-packs/{id}", { params: { path: { id: pack.id } }, body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.devicePacks });
      toast.ok(pack === "new" ? t("devicePacks.created") : t("devicePacks.saved"));
      onClose();
    },
    onError: (e) => {
      onFormError(e, toast, setErrors);
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const errs: Record<string, string> = {};
    const slotsN = Number(slots);
    const starsN = Number(stars);
    const rubN = rubKopecks(rub);
    if (!title.trim()) errs.title = t("devicePacks.errTitle");
    if (!Number.isInteger(slotsN) || slotsN < 1 || slotsN > 47) errs.slots = t("devicePacks.errSlots");
    if (stars.trim() && !isStars(starsN)) errs.price_stars = t("tariffs.errStars");
    if (rub.trim() && !isKopecks(rubN)) errs.price_rub = t("tariffs.errRub");
    setErrors(errs);
    if (Object.keys(errs).length) return;
    save.mutate({
      title: title.trim(),
      slots: slotsN,
      price_rub: rub.trim() ? rubN : 0,
      price_stars: stars.trim() ? starsN : undefined,
      on_sale: onSale,
      // PUT replaces the pack: keep its place in the list.
      sort: pack && pack !== "new" ? pack.sort : 0,
    });
  };

  return (
    <Drawer
      open={!!pack}
      onOpenChange={(v) => !v && onClose()}
      title={pack === "new" ? t("devicePacks.new") : t("devicePacks.edit")}
      meta={t("devicePacks.drawerMeta")}
      footer={
        <DrawerFooter form="device-pack-form" onCancel={onClose} loading={save.isPending}>
          {t("common.save")}
        </DrawerFooter>
      }
    >
      <form id="device-pack-form" onSubmit={submit} className="pt-5" noValidate>
        <Field label={t("devicePacks.name")} htmlFor="dp-title" error={errors.title}>
          <input id="dp-title" className="input" value={title} onChange={(e) => setTitle(e.target.value)} maxLength={60} placeholder={t("devicePacks.namePlaceholder")} aria-invalid={!!errors.title} autoFocus />
        </Field>
        <Field label={t("devicePacks.slots")} htmlFor="dp-slots" hint={t("devicePacks.slotsHint")} error={errors.slots}>
          <div className="flex items-center gap-2">
            <input id="dp-slots" className="input max-w-[100px]" inputMode="numeric" value={slots} onChange={(e) => setSlots(e.target.value)} aria-invalid={!!errors.slots} />
            {[2, 7].map((n) => (
              <button key={n} type="button" className="chip-btn" onClick={() => setSlots(String(n))}>
                +{n}
              </button>
            ))}
          </div>
        </Field>
        <div className="border-t border-[var(--hairline)] pt-4" role="group" aria-label={t("tariffs.sale")}>
          <div className="mb-3 flex items-start justify-between gap-3">
            <div>
              <div className="text-[13px] font-semibold">{t("tariffs.sale")}</div>
              <div className="text-xs text-[var(--md-on-surface-variant)]">{t("devicePacks.saleSub")}</div>
            </div>
            <Switch checked={onSale} onChange={setOnSale} label={t("tariffs.onSale")} />
          </div>
          <div className="grid gap-x-3 sm:grid-cols-2">
            <Field label={t("tariffs.priceRub")} htmlFor="dp-rub" hint={t("tariffs.priceRubHint")} error={errors.price_rub}>
              <div className="flex items-center gap-2">
                <input id="dp-rub" className="input max-w-[140px]" inputMode="decimal" value={rub} onChange={(e) => setRub(e.target.value)} placeholder="50" aria-invalid={!!errors.price_rub} />
                <span className="text-[var(--md-on-surface-variant)]">₽</span>
              </div>
            </Field>
            <Field label={t("tariffs.priceStars")} htmlFor="dp-stars" error={errors.price_stars}>
              <div className="flex items-center gap-2">
                <input id="dp-stars" className="input max-w-[140px]" inputMode="numeric" value={stars} onChange={(e) => setStars(e.target.value)} aria-invalid={!!errors.price_stars} />
                <span className="text-[var(--md-on-surface-variant)]">⭐</span>
              </div>
            </Field>
          </div>
        </div>
      </form>
    </Drawer>
  );
}
