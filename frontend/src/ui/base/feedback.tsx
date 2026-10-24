import * as React from "react";
import { Progress as BaseProgress } from "@base-ui/react/progress";
import { AlertTriangle, CheckCircle2, Info, XCircle } from "lucide-react";
import { cn } from "@/lib/utils";
import { ErrorDetailsFor } from "./ErrorDetails";
import "./base.css";

// Inline feedback: alert banners and progress bars. Status hues (online /
// warning / offline) are carried by a thin tinted stroke and the icon — never a
// filled surface, never the text color.

export type AlertTone = "error" | "warning" | "info" | "success";

const alertBorder: Record<AlertTone, string> = {
  error: "tw:border-offline/40",
  warning: "tw:border-warning/40",
  info: "tw:border-primary/40",
  success: "tw:border-online/40",
};
const alertIcon: Record<AlertTone, { icon: React.ComponentType<{ className?: string }>; className: string }> = {
  error: { icon: XCircle, className: "tw:text-offline" },
  warning: { icon: AlertTriangle, className: "tw:text-warning" },
  info: { icon: Info, className: "tw:text-primary" },
  success: { icon: CheckCircle2, className: "tw:text-online" },
};

/** Bounded banner. Announced as role="alert" for every tone. */
export function Alert({
  tone,
  title,
  description,
  action,
  className,
}: {
  tone: AlertTone;
  title: React.ReactNode;
  description?: React.ReactNode;
  action?: React.ReactNode;
  className?: string;
}) {
  const { icon: Icon, className: iconClass } = alertIcon[tone];
  return (
    <div
      role="alert"
      data-tone={tone}
      className={cn(
        "tw:flex tw:gap-2 tw:rounded-control tw:border tw:px-3 tw:py-2 tw:text-sm tw:text-foreground",
        description ? "tw:items-start" : "tw:items-center",
        alertBorder[tone],
        className,
      )}
    >
      <Icon className={cn("tw:size-4 tw:flex-none", description ? "tw:mt-0.5" : "", iconClass)} aria-hidden="true" />
      <div className="tw:grid tw:min-w-0 tw:flex-1 tw:gap-0.5">
        <span className={description ? "tw:font-medium" : ""}>{title}</span>
        {description ? <span className="tw:text-xs tw:text-muted-foreground">{description}</span> : null}
        {tone === "error" || tone === "warning" ? <ErrorDetailsFor message={title} /> : null}
      </div>
      {action ? <div className="tw:flex-none">{action}</div> : null}
    </div>
  );
}

/** Determinate progress bar with the percentage beside it. */
export function Progress({
  value,
  label,
  tone = "default",
  showValue = true,
  className,
}: {
  /** 0–100. */
  value: number;
  /** Accessible name; the bar is otherwise unlabeled. */
  label: string;
  /** `success` switches the bar to the online hue (complete). */
  tone?: "default" | "success";
  showValue?: boolean;
  className?: string;
}) {
  const clamped = Math.min(100, Math.max(0, value));
  return (
    <BaseProgress.Root value={clamped} aria-label={label} className={cn("tw:flex tw:items-center tw:gap-2", className)}>
      <BaseProgress.Track className="tw:h-1.5 tw:min-w-0 tw:flex-1 tw:overflow-hidden tw:rounded-full tw:bg-muted">
        <BaseProgress.Indicator
          className={cn("tw:h-full tw:rounded-full tw:transition-[width] tw:duration-300", tone === "success" ? "tw:bg-online" : "tw:bg-primary")}
        />
      </BaseProgress.Track>
      {showValue ? (
        <BaseProgress.Value className="tw:w-10 tw:flex-none tw:text-right tw:text-xs tw:tabular-nums tw:text-muted-foreground" />
      ) : null}
    </BaseProgress.Root>
  );
}
