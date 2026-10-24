import type { ReactNode } from "react";
import { Button, Switch } from "./base";
import { t } from "../lib/i18n";
import { cn } from "../lib/utils";

// One save per surface: a status text + a save button in the dialog footer.
// The button is enabled only while unsaved changes exist; on failure the status
// text names the part that failed.

export function SaveStatus({ count, failed, className }: { count: number; failed?: string; className?: string }) {
  if (failed) {
    return (
      <span role="alert" className={cn("tw:min-w-0 tw:text-xs tw:text-destructive", className)}>
        {t("act.partFailed", { part: failed })}
      </span>
    );
  }
  if (count === 0) return null;
  return (
    <span role="status" className={cn("tw:inline-flex tw:min-w-0 tw:items-center tw:gap-1.5 tw:text-xs tw:text-muted-foreground", className)}>
      <span aria-hidden="true" className="tw:size-1.5 tw:rounded-full tw:bg-warning" />
      {t("act.unsavedCount", { n: count })}
    </span>
  );
}

export function SaveButton({
  count,
  busy,
  onSave,
  primary = true,
}: {
  count: number;
  busy: boolean;
  onSave: () => void;
  /** Drop to secondary when the region already has another primary action (at most one solid primary per region). */
  primary?: boolean;
}) {
  return (
    <Button variant={primary ? "primary" : "secondary"} disabled={count === 0} loading={busy} onClick={onSave}>
      {busy ? t("act.saving") : t("act.save")}
    </Button>
  );
}

/** The whole "status + save" footer for a settings section. */
export function SaveBar({
  count,
  busy,
  failed,
  onSave,
  leading,
}: {
  count: number;
  busy: boolean;
  failed?: string;
  onSave: () => void;
  leading?: ReactNode;
}) {
  return (
    <div className="tw:flex tw:w-full tw:flex-wrap tw:items-center tw:justify-end tw:gap-2">
      <SaveStatus count={count} failed={failed} className="tw:mr-auto" />
      {leading}
      <SaveButton count={count} busy={busy} onSave={onSave} />
    </div>
  );
}

/**
 * Unsaved marker next to a tab name. Visual only (aria-hidden): the tab's
 * accessible name stays unchanged, and the footer status text announces the
 * number of unsaved spots.
 */
export function UnsavedDot({ className }: { className?: string }) {
  return (
    <span
      aria-hidden="true"
      data-unsaved-dot=""
      title={t("act.tabUnsaved")}
      className={cn("tw:inline-block tw:size-1.5 tw:flex-none tw:rounded-full tw:bg-warning", className)}
    />
  );
}

/**
 * A switch row inside a card: name and description on the left, the switch on
 * the right (the switch itself carries the state).
 */
export function SwitchRow({
  label,
  description,
  checked,
  onCheckedChange,
  disabled,
  loading,
  className,
}: {
  label: ReactNode;
  description?: ReactNode;
  checked: boolean;
  onCheckedChange: (next: boolean) => void;
  disabled?: boolean;
  loading?: boolean;
  className?: string;
}) {
  return (
    <div className={cn("tw:flex tw:min-w-0 tw:items-center tw:justify-between tw:gap-3", className)}>
      <div className="tw:grid tw:min-w-0 tw:gap-0.5">
        <span className="tw:text-sm tw:text-foreground">{label}</span>
        {description ? <span className="tw:text-xs tw:text-muted-foreground">{description}</span> : null}
      </div>
      {/* The switch itself shows the state; no separate on/off text next to it. */}
      <div className="tw:flex tw:flex-none tw:items-center">
        <Switch
          checked={checked}
          onCheckedChange={onCheckedChange}
          disabled={disabled}
          loading={loading}
          aria-label={typeof label === "string" ? label : t("act.on")}
        />
      </div>
    </div>
  );
}
