// The panel's admins: who may log in and as what. A superadmin names the others, changes
// a role and takes an account off; the last superadmin cannot be demoted or removed, and
// nobody removes themselves (the server refuses both). A custom role carries the exact
// permissions the admin picked.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../../api/client";
import { useToast } from "../../../components/toast";
import { Button, Field, Pill } from "../../../components/ui";
import { t, type Key } from "../../../i18n";
import { fieldErrors } from "../../../lib/fields";
import { PERMISSION_GROUPS, type Permission } from "../../roles";

type AdminRow = Schemas["AdminRowView"];
type Role = AdminRow["role"];
const qk = ["panel-admins"] as const;
const ROLES: Role[] = ["superadmin", "support", "tech", "custom"];

function useAdmins() {
  return useQuery({ queryKey: qk, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/admins", { signal })) });
}

export function AdminsCard() {
  const admins = useAdmins();
  const qc = useQueryClient();
  const toast = useToast();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("support");
  const [permissions, setPermissions] = useState<Permission[]>([]);
  const changed = () => void qc.invalidateQueries({ queryKey: qk });
  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/admins", { body: { username: username.trim(), password, role, permissions: role === "custom" ? permissions : undefined } })),
    onSuccess: () => {
      setUsername("");
      setPassword("");
      setPermissions([]);
      changed();
      toast.ok(t("admins.created"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const setAdminRole = useMutation({
    mutationFn: (v: { id: number; role: Role; permissions: Permission[] }) =>
      unwrap(api.PUT("/api/v1/admins/{id}", { params: { path: { id: v.id } }, body: { role: v.role, permissions: v.role === "custom" ? v.permissions : undefined } })),
    onSuccess: () => {
      changed();
      toast.ok(t("admins.saved"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/admins/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      changed();
      toast.ok(t("admins.removed"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (username.trim() && password.length >= 8 && (role !== "custom" || permissions.length > 0)) create.mutate();
  };
  const errors = fieldErrors(create.error);
  return (
    <section className="card glass reveal">
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("admins.title")}</h2>
          <div className="card-sub">{t("admins.sub")}</div>
        </div>
      </div>
      <ul className="row-list">
        {(admins.data ?? []).map((a) => (
          <li key={a.id} className="py-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="min-w-0">
                <div className="text-[13px] font-medium">{a.username}</div>
                <div className="mt-1 flex items-center gap-2 text-xs text-[var(--md-on-surface-variant)]">
                  {a.totp_enabled ? <Pill tone="ok">{t("admins.totp")}</Pill> : null}
                  {a.role === "custom" ? <Pill tone="warn">{t("admins.roles.custom")}</Pill> : null}
                  {a.last_login_at ? <span>{t("admins.lastLogin", { at: new Date(a.last_login_at).toLocaleString() })}</span> : null}
                </div>
              </div>
              <div className="flex items-center gap-2">
                <select
                  className="input max-w-[170px]"
                  value={a.role}
                  onChange={(e) => {
                    const next = e.target.value as Role;
                    const perms = next === "custom" ? (a.permissions.length ? a.permissions : (["users.read"] as Permission[])) : [];
                    setAdminRole.mutate({ id: a.id, role: next, permissions: perms });
                  }}
                  aria-label={t("admins.role")}
                >
                  {ROLES.map((r) => (
                    <option key={r} value={r}>
                      {t(`admins.roles.${r}`)}
                    </option>
                  ))}
                </select>
                <button type="button" className="icon-btn" aria-label={t("admins.removeLabel", { name: a.username })} onClick={() => remove.mutate(a.id)}>
                  <Trash2 size={16} />
                </button>
              </div>
            </div>
            {a.role === "custom" ? (
              <PermissionPicker
                value={a.permissions}
                disabled={setAdminRole.isPending}
                onChange={(next) => setAdminRole.mutate({ id: a.id, role: "custom", permissions: next })}
              />
            ) : null}
          </li>
        ))}
      </ul>
      <form onSubmit={submit} className="mt-4 grid gap-2 sm:grid-cols-[1fr_1fr_150px_auto] sm:items-end">
        <Field label={t("admins.username")} htmlFor="ad-name">
          <input id="ad-name" className="input" value={username} onChange={(e) => setUsername(e.target.value)} maxLength={64} autoComplete="off" />
        </Field>
        <Field label={t("admins.password")} htmlFor="ad-pass">
          <input id="ad-pass" className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} minLength={8} autoComplete="new-password" />
        </Field>
        <Field label={t("admins.role")} htmlFor="ad-role">
          <select id="ad-role" className="input" value={role} onChange={(e) => setRole(e.target.value as Role)}>
            {ROLES.map((r) => (
              <option key={r} value={r}>
                {t(`admins.roles.${r}`)}
              </option>
            ))}
          </select>
        </Field>
        <Button type="submit" variant="primary" loading={create.isPending} disabled={!username.trim() || password.length < 8 || (role === "custom" && permissions.length === 0)}>
          <Plus size={18} aria-hidden /> {t("admins.add")}
        </Button>
      </form>
      {role === "custom" ? (
        <PermissionPicker value={permissions} onChange={setPermissions} error={errors.permissions} />
      ) : null}
      <p className="mt-2 text-xs text-[var(--md-on-surface-variant)]">{t("admins.hint")}</p>
    </section>
  );
}

function PermissionPicker({ value, onChange, disabled, error }: { value: Permission[]; onChange: (v: Permission[]) => void; disabled?: boolean; error?: string }) {
  const toggle = (p: Permission) => {
    if (value.includes(p)) {
      if (value.length === 1) return;
      onChange(value.filter((x) => x !== p));
    } else {
      onChange([...value, p]);
    }
  };
  return (
    <fieldset className="mt-3 rounded-xl border border-[var(--hairline)] p-3">
      <legend className="px-1 text-xs font-medium text-[var(--md-on-surface-variant)]">{t("admins.permissionsTitle")}</legend>
      <div className="grid gap-3 sm:grid-cols-2">
        {PERMISSION_GROUPS.map((g) => (
          <div key={g.key}>
            <div className="mb-1 text-xs font-semibold">{t(`admins.permissionGroups.${g.key}` as Key)}</div>
            <div className="flex flex-col gap-1">
              {g.permissions.map((p) => (
                <label key={p} className="flex items-center gap-2 text-[13px]">
                  <input type="checkbox" className="check" checked={value.includes(p)} disabled={disabled || (value.length === 1 && value.includes(p))} onChange={() => toggle(p)} />
                  {t(`admins.permissions.${p}` as Key)}
                </label>
              ))}
            </div>
          </div>
        ))}
      </div>
      {error ? <p className="mt-2 text-xs text-[var(--md-error)]">{error}</p> : null}
    </fieldset>
  );
}
