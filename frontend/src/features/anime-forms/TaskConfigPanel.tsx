import { useId, type ReactNode, type Ref } from "react";
import { ChevronRight } from "lucide-react";
import { StatePill } from "../../ui";
import { Reveal } from "../../ui/base";
import { cn } from "../../lib/utils";

// The settings column every task renders on the right: a title, one line of
// scope copy, the task's always-visible fields, and the folded "advanced" part
// (parser, filters, naming). One-shot step two and file-manager recognition use
// the same shell so the two panels read and fold identically; what differs is
// only what each task puts in the slots.
export function TaskConfigPanel({
  ariaLabel,
  title,
  note,
  actions,
  sectionRef,
  className,
  children,
  advanced,
}: {
  ariaLabel: string;
  title: string;
  /** One line saying where these choices apply (this task only). */
  note: ReactNode;
  /** Header actions beside the title (restore defaults). */
  actions?: ReactNode;
  sectionRef?: Ref<HTMLElement>;
  /** The caller's column layout (border, scroll, padding); the shell owns the stack. */
  className?: string;
  /** The task's always-visible fields. */
  children: ReactNode;
  /** The folded part: parser, filters and naming. */
  advanced?: {
    label: string;
    summary: ReactNode;
    changed?: boolean;
    changedLabel: string;
    open: boolean;
    onToggle: () => void;
    children: ReactNode;
  };
}) {
  const advancedId = useId();
  return (
    <section ref={sectionRef} aria-label={ariaLabel} className={cn("tw:grid tw:grid-cols-1 tw:content-start tw:gap-3", className)}>
      <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
        <h3 className="text-body tw:m-0 tw:flex-1">{title}</h3>
        {actions}
      </div>
      <p className="text-caption tw:m-0 tw:text-muted-foreground">{note}</p>

      {children}

      {advanced ? (
        <div className="tw:grid tw:gap-2 tw:border-t tw:border-border tw:pt-3">
          <button
            type="button"
            aria-expanded={advanced.open}
            aria-controls={advancedId}
            onClick={advanced.onToggle}
            className="tw:flex tw:w-full tw:min-w-0 tw:cursor-pointer tw:items-center tw:gap-2 tw:rounded-sm tw:border-0 tw:bg-transparent tw:p-0 tw:py-1 tw:text-left tw:text-foreground tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring"
          >
            <ChevronRight
              aria-hidden="true"
              className={cn(
                "icon-sm tw:flex-none tw:text-muted-foreground tw:transition-transform tw:duration-200 tw:ease-out tw:motion-reduce:transition-none",
                advanced.open && "tw:rotate-90",
              )}
            />
            <span className="text-body tw:flex-none">{advanced.label}</span>
            <span className="tw:min-w-0 tw:flex-1 tw:truncate tw:text-xs tw:text-muted-foreground">{advanced.summary}</span>
            {advanced.changed ? <StatePill tone="warning">{advanced.changedLabel}</StatePill> : null}
          </button>
          <Reveal open={advanced.open}>
            <div id={advancedId} className="tw:grid tw:gap-3 tw:pt-1">
              {advanced.children}
            </div>
          </Reveal>
        </div>
      ) : null}
    </section>
  );
}
