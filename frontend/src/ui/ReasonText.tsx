import type { ReactNode } from "react";
import { t } from "../lib/i18n";
import { reasonView } from "../lib/i18n/reasons";

/** Localized explanation of a reason code; unregistered codes show "unknown reason" and put the raw code in a tooltip. */
export function ReasonText({ code }: { code: string | null | undefined }) {
  const view = reasonView(code);
  if (!view) return null;
  return view.title ? <span title={view.title}>{view.text}</span> : <>{view.text}</>;
}

const SLOT = "\u0000";

/** "Filtered (reason)": the reason may be a node carrying a tooltip; the parentheses and wording still come from the copy table. */
export function FilteredWithReason({ reason }: { reason?: ReactNode }) {
  const [before = "", after = ""] = t("verdict.filteredWithReason", { reason: SLOT }).split(SLOT);
  return (
    <>
      {before}
      {reason ?? t("common.unspecifiedReason")}
      {after}
    </>
  );
}
