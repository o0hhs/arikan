// The database, out of the panel: one file the admin keeps. What it is made of and how it
// comes back is the panel's own business (pgdump), and restoring stops the panel — so the
// card says so instead of offering a "restore" button that cannot work.
import { useState } from "react";
import { HardDriveDownload } from "lucide-react";
import { basePath } from "../../../api/client";
import { Button } from "../../../components/ui";
import { t } from "../../../i18n";

export function BackupCard() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // The dump is a stream, not JSON: fetch keeps the session cookie and lets an error be
  // read before anything is written to disk.
  const download = async () => {
    setBusy(true);
    setError("");
    try {
      const res = await fetch(`${basePath}/api/v1/database/backup`, { credentials: "same-origin", headers: { Accept: "application/octet-stream" } });
      if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
      const blob = await res.blob();
      const name = (res.headers.get("Content-Disposition") ?? "").match(/filename="([^"]+)"/)?.[1] ?? "arikan-backup.dump";
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = name;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section id="backup" className="card glass reveal">
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.backup")}</h2>
          <div className="card-sub">{t("settings.backupSub")}</div>
        </div>
        <Button size="sm" loading={busy} onClick={() => void download()}>
          <HardDriveDownload size={16} aria-hidden /> {busy ? t("settings.backupPreparing") : t("settings.backupDownload")}
        </Button>
      </div>
      {error ? <div className="banner err mb-3" role="alert">{t("settings.backupFailed", { error })}</div> : null}
      <p className="text-xs text-[var(--ink-500)]">{t("settings.backupHint")}</p>
    </section>
  );
}
