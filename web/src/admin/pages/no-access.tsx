import { ShieldOff } from "lucide-react";
import { EmptyState, PageHeader } from "../../components/ui";
import { t } from "../../i18n";

/** An account whose permissions open no page: the API is still there, the menu is not. */
export function NoAccessPage() {
  return (
    <>
      <PageHeader title={t("noAccess.title")} sub={t("noAccess.sub")} />
      <EmptyState icon={ShieldOff} title={t("noAccess.title")} text={t("noAccess.text")} />
    </>
  );
}
