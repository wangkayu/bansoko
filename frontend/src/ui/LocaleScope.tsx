import { Fragment, type ReactNode } from "react";
import { useLocale } from "../lib/i18n";

// Remount the subtree when the UI language changes: copy is read by t() at
// render time, and the route elements are stable references a plain re-render
// never reaches into. Route state lives in the URL and query caches in the
// QueryClient, so both survive the remount.
export function LocaleScope({ children }: { children: ReactNode }) {
  const locale = useLocale();
  return <Fragment key={locale}>{children}</Fragment>;
}
