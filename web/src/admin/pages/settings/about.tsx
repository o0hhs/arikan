import { GitFork, TriangleAlert } from "lucide-react";
import { useUpdates } from "../../../api/hooks";
import { Logo } from "../../../components/atmosphere";
import { t } from "../../../i18n";

/**
 * What this build is and is not: the fork notice the upstream terms (NOTICE.md, section
 * 7(c)) want visible in the panel, with the way to the fork's own support and to mikan.
 */
export function AboutCard() {
  const u = useUpdates();
  const version = u.data?.current;
  return (
    <section id="about" className="card glass reveal">
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.aboutTitle")}</h2>
          <div className="card-sub">{t("settings.aboutSub")}</div>
        </div>
      </div>
      <div className="flex items-center gap-3">
        <Logo size={40} />
        <div>
          <div className="font-display text-[17px] leading-6 font-semibold tracking-tight">Arikan{version ? ` ${version}` : ""}</div>
          <div className="text-[13px] text-[var(--md-on-surface-variant)]">{t("settings.aboutTagline")}</div>
        </div>
      </div>
      <div className="mt-4 flex items-start gap-2 rounded-2xl border border-[rgba(224,160,33,0.35)] bg-[var(--honey-50)] p-3 text-[13px] text-[var(--md-on-surface-variant)]" role="note">
        <TriangleAlert size={16} className="mt-0.5 shrink-0 text-[var(--honey-600)]" aria-hidden />
        <span>{t("settings.aboutNotice")}</span>
      </div>
      <div className="mt-4 flex flex-wrap gap-x-5 gap-y-2 text-[13px]">
        <a className="inline-flex items-center gap-1.5 font-medium text-[var(--md-primary)] hover:underline" href="https://github.com/o0hhs/arikan" rel="noreferrer" target="_blank">
          <GitFork size={15} aria-hidden /> {t("settings.aboutFork")}
        </a>
        <a className="inline-flex items-center gap-1.5 text-[var(--md-on-surface-variant)] hover:underline" href="https://github.com/Miroshka000/mikan" rel="noreferrer" target="_blank">
          {t("settings.aboutUpstream")}
        </a>
        <a className="inline-flex items-center gap-1.5 text-[var(--md-on-surface-variant)] hover:underline" href="https://o0hhs.github.io/arikan/" rel="noreferrer" target="_blank">
          {t("settings.aboutDocs")}
        </a>
      </div>
    </section>
  );
}
