import type { ReactNode } from "react";
import { ListFilter, type LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button, Input } from "../../ui/base";
import { t } from "../../lib/i18n";

// The toolbar parts of the catalog and the subscribed wall: a single line with no card chrome, all
//   controls from ui/base, controls on the same line equally tall (36px).

/** The title filter: icon + placeholder, no filter label. */
export function FilterInput({
  value,
  onChange,
  autoFocus,
  className,
}: {
  value: string;
  onChange: (value: string) => void;
  autoFocus?: boolean;
  className?: string;
}) {
  return (
    <Input
      type="search"
      aria-label={t("grid.filter")}
      placeholder={t("grid.filterPlaceholder")}
      leadingIcon={<ListFilter className="tw:size-4" />}
      fullWidth={className === undefined}
      className={className}
      value={value}
      autoFocus={autoFocus}
      onChange={(event) => onChange(event.target.value)}
    />
  );
}

/** Phone: filters fold into one icon button that expands the filter beneath the toolbar (staying expanded while conditions exist). */
export function FilterToggle({ open, onToggle }: { open: boolean; onToggle: () => void }) {
  return (
    <Button size="icon" aria-label={t("grid.filter")} title={t("grid.filter")} aria-expanded={open} onClick={onToggle}>
      <ListFilter className="tw:size-4" aria-hidden />
    </Button>
  );
}

export type StatusTone = "online" | "warning" | "offline" | "muted";

const statusToneClass: Record<StatusTone, string> = {
  online: "tw:text-online",
  warning: "tw:text-warning",
  offline: "tw:text-offline",
  muted: "tw:text-muted-foreground",
};

/** The site data status: one small line, e.g. "101 titles, updated 10 hours ago". */
export function StatusLine({ text, tone, Icon }: { text: string; tone: StatusTone; Icon: LucideIcon }) {
  return (
    <p role="status" className="tw:m-0 tw:flex tw:min-w-0 tw:items-center tw:gap-1.5 tw:text-xs tw:text-muted-foreground">
      <Icon aria-hidden className={cn("tw:size-3.5 tw:flex-none", statusToneClass[tone])} />
      <span className="tw:truncate">{text}</span>
    </p>
  );
}

export function ToolbarRow({ children, end }: { children?: ReactNode; end?: ReactNode }) {
  return (
    <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-2">
      {children}
      {end && <div className="tw:ml-auto tw:flex tw:min-w-0 tw:items-center tw:gap-2">{end}</div>}
    </div>
  );
}
