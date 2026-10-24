import { Inbox, Loader2, Lock } from "lucide-react";
import { cn } from "@/lib/utils";
import { Alert } from "./base/feedback";

// StateBlock — the one empty / busy / error / restricted surface. Every kind
// answers: what state, what it means, what to do next (the optional action
// slot).
//
// - empty: nothing here yet, or nothing matches. Centered icon, title,
//   optional description and CTA.
// - busy: a query is still loading. Never show an empty state (or a "no data
//   yet" line) while the data is still on its way.
// - error: role=alert, thin offline stroke with ink text (the Alert error
//   look), with the registered error details toggle and an optional action.
// - restricted: the site or the current configuration cannot do this. Shown
//   in place of the control instead of failing after submit.
//
// One block per region. `className` is for outer layout only (margins, grid
// placement); never pass padding, borders, colors or type classes.

export type StateBlockKind = "empty" | "busy" | "error" | "restricted";

const centered = "tw:grid tw:justify-items-center tw:gap-1 tw:px-4 tw:py-8 tw:text-center";
const iconCircle =
  "tw:mb-1 tw:flex tw:size-9 tw:items-center tw:justify-center tw:rounded-full tw:bg-muted tw:text-muted-foreground";

export function StateBlock({
  kind,
  title,
  description,
  action,
  className,
}: {
  kind: StateBlockKind;
  title: string;
  description?: React.ReactNode;
  action?: React.ReactNode;
  className?: string;
}) {
  if (kind === "error") {
    // Same rendering as Alert tone="error": a load or save failure looks the
    // same whichever of the two a surface uses.
    return <Alert tone="error" title={title} description={description} action={action} className={className} />;
  }
  if (kind === "busy") {
    return (
      <div role="status" data-state-block="busy" className={cn(centered, className)}>
        <span className="tw:inline-flex tw:items-center tw:gap-2 tw:text-sm tw:text-muted-foreground">
          <Loader2 className="tw:size-4 tw:flex-none tw:motion-safe:animate-spin" aria-hidden="true" />
          {title}
        </span>
        {description && <span className="tw:text-xs tw:text-muted-foreground">{description}</span>}
      </div>
    );
  }
  const Icon = kind === "restricted" ? Lock : Inbox;
  return (
    <div data-state-block={kind} className={cn(centered, className)}>
      <span aria-hidden="true" className={iconCircle}>
        <Icon className="tw:size-4" />
      </span>
      <p className="tw:m-0 tw:text-sm tw:font-medium tw:text-foreground">{title}</p>
      {description && <p className="tw:m-0 tw:max-w-prose tw:text-xs tw:text-muted-foreground">{description}</p>}
      {action && <div className="tw:mt-2 tw:flex tw:flex-wrap tw:items-center tw:justify-center tw:gap-2">{action}</div>}
    </div>
  );
}
