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
//   yet" line) while the data is still on its way. When `skeleton` is provided,
//   renders a structural skeleton matching the target container while keeping
//   the status accessible name intact.
// - error: role=alert, thin offline stroke with ink text (the Alert error
//   look), with the registered error details toggle and an optional action.
// - restricted: the site or the current configuration cannot do this. Shown
//   in place of the control instead of failing after submit.
//
// One block per region. `className` is for outer layout only (margins, grid
// placement); never pass padding, borders, colors or type classes.

export type StateBlockKind = "empty" | "busy" | "error" | "restricted";
export type StateBlockSkeleton = "cards" | "config" | "list" | "log" | "table" | "settings";

const centered = "tw:grid tw:justify-items-center tw:gap-1 tw:px-4 tw:py-8 tw:text-center";
const iconCircle =
  "tw:mb-1 tw:flex tw:size-9 tw:items-center tw:justify-center tw:rounded-full tw:bg-muted tw:text-muted-foreground";
const skelBar = "tw:block tw:rounded-full tw:bg-muted tw:motion-safe:animate-pulse";

function StateSkeleton({ variant }: { variant: StateBlockSkeleton }) {
  switch (variant) {
    case "cards":
      return (
        <div aria-hidden="true" className="tw:grid tw:grid-cols-2 tw:gap-3 tw:sm:grid-cols-4 tw:lg:grid-cols-5">
          {[0, 1, 2, 3, 4].map((index) => (
            <div
              key={index}
              className="tw:flex tw:aspect-[2/3] tw:flex-col tw:justify-end tw:gap-1.5 tw:rounded-2xl tw:border tw:border-border tw:bg-card tw:p-3"
            >
              <span className={cn(skelBar, "tw:h-3 tw:w-4/5")} />
              <span className={cn(skelBar, "tw:h-2.5 tw:w-1/2")} />
            </div>
          ))}
        </div>
      );
    case "config":
      return (
        <div aria-hidden="true" className="tw:grid tw:gap-3">
          <div className="tw:flex tw:items-center tw:justify-between tw:gap-3 tw:rounded-control tw:border tw:border-border tw:bg-card tw:px-3 tw:py-2.5">
            <span className={cn(skelBar, "tw:h-3 tw:w-48")} />
            <span className={cn(skelBar, "tw:h-4 tw:w-20")} />
          </div>
          <div className="tw:flex tw:gap-4 tw:border-b tw:border-border tw:px-2 tw:pb-2.5">
            {[0, 1, 2, 3].map((index) => (
              <span key={index} className={cn(skelBar, "tw:h-3.5 tw:w-14")} />
            ))}
          </div>
          {[0, 1, 2].map((index) => (
            <div key={index} className="tw:grid tw:gap-2 tw:rounded-control tw:border tw:border-border tw:bg-card tw:p-3">
              <span className={cn(skelBar, "tw:h-3.5 tw:w-1/3")} />
              <span className={cn(skelBar, "tw:h-3 tw:w-4/5")} />
            </div>
          ))}
        </div>
      );
    case "list":
      return (
        <div aria-hidden="true" className="tw:grid tw:gap-2">
          {[0, 1, 2, 3].map((index) => (
            <div
              key={index}
              className="tw:grid tw:gap-2 tw:rounded-control tw:border tw:border-border tw:bg-card tw:px-3 tw:py-2.5"
            >
              <div className="tw:flex tw:items-center tw:justify-between tw:gap-3">
                <span className={cn(skelBar, "tw:h-3.5 tw:w-3/5")} />
                <span className={cn(skelBar, "tw:h-4 tw:w-14")} />
              </div>
              <span className={cn(skelBar, "tw:h-2.5 tw:w-2/5")} />
            </div>
          ))}
        </div>
      );
    case "log":
      return (
        <div aria-hidden="true" className="tw:grid tw:gap-1.5">
          {[0, 1, 2, 3, 4].map((index) => (
            <div
              key={index}
              className="tw:flex tw:items-center tw:gap-2.5 tw:rounded-md tw:border tw:border-border tw:bg-card tw:px-3 tw:py-2"
            >
              <span className={cn(skelBar, "tw:h-2.5 tw:w-14 tw:flex-none")} />
              <span className={cn(skelBar, "tw:h-4 tw:w-10 tw:flex-none")} />
              <span className={cn(skelBar, "tw:h-3 tw:flex-1")} />
            </div>
          ))}
        </div>
      );
    case "table":
      return (
        <div aria-hidden="true" className="tw:grid tw:divide-y tw:divide-border tw:rounded-control tw:border tw:border-border tw:bg-card">
          {[0, 1, 2, 3, 4].map((index) => (
            <div key={index} className="tw:flex tw:items-center tw:gap-3 tw:px-4 tw:py-2.5">
              <span className={cn(skelBar, "tw:size-4 tw:flex-none tw:rounded-sm")} />
              <div className="tw:grid tw:min-w-0 tw:flex-1 tw:gap-1.5">
                <span className={cn(skelBar, "tw:h-3.5 tw:w-1/2")} />
                <span className={cn(skelBar, "tw:h-2.5 tw:w-1/3")} />
              </div>
              <span className={cn(skelBar, "tw:h-4 tw:w-16 tw:flex-none")} />
            </div>
          ))}
        </div>
      );
    case "settings":
      return (
        <div aria-hidden="true" className="tw:grid tw:gap-3">
          {[0, 1, 2].map((index) => (
            <div key={index} className="tw:grid tw:gap-2.5 tw:rounded-control tw:border tw:border-border tw:bg-card tw:p-4">
              <div className="tw:flex tw:items-center tw:justify-between tw:gap-3">
                <span className={cn(skelBar, "tw:h-3.5 tw:w-36")} />
                <span className={cn(skelBar, "tw:h-5 tw:w-10")} />
              </div>
              <span className={cn(skelBar, "tw:h-2.5 tw:w-3/4")} />
            </div>
          ))}
        </div>
      );
  }
}

export function StateBlock({
  kind,
  title,
  description,
  action,
  skeleton,
  className,
}: {
  kind: StateBlockKind;
  title: string;
  description?: React.ReactNode;
  action?: React.ReactNode;
  skeleton?: StateBlockSkeleton;
  className?: string;
}) {
  if (kind === "error") {
    // Same rendering as Alert tone="error": a load or save failure looks the
    // same whichever of the two a surface uses.
    return <Alert tone="error" title={title} description={description} action={action} className={className} />;
  }
  if (kind === "busy") {
    if (skeleton) {
      return (
        <div role="status" data-state-block="busy" data-skeleton={skeleton} className={cn("tw:grid tw:min-w-0 tw:gap-3 tw:py-2", className)}>
          <span className="sr-only">{title}</span>
          {description && <span className="sr-only">{description}</span>}
          <StateSkeleton variant={skeleton} />
        </div>
      );
    }
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
