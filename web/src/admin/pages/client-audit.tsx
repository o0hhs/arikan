// The panel's log for the tech role: the same card the settings show, on a page of its own.
import { AuditCard } from "./settings/audit";
import { PageHeader } from "../../components/ui";
import { t } from "../../i18n";

export function AuditPage() {
  return (
    <>
      <PageHeader title={t("nav.audit")} sub={t("audit.pageSub")} />
      <AuditCard />
    </>
  );
}
