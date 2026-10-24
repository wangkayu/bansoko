import { cn } from "@/lib/utils";
import { statusDot, statusIcon, statusText, statusBusyDot, type StatePillTone } from "./base/tones";

export type { StatePillTone } from "./base/tones";

// StatePill — live status: a round 6px dot + label, no box. Calm tones (online /
// info / muted) read as muted inline text; attention tones (warning / offline)
// add a soft tinted ground in the tone's ink so they stand out. Colour never
// carries the meaning alone: the label is always present and the dot/icon is
// aria-hidden. `busy` (loading / refreshing) pulses the dot, only when motion
// is allowed. Category and marker labels (default template, connection kind, …)
// use Chip.

export function StatePill({
  tone = "muted",
  busy = false,
  icon: Icon,
  title,
  children,
  className,
}: {
  tone?: StatePillTone;
  /** In-progress state (loading, refreshing): the dot pulses. */
  busy?: boolean;
  /** Replaces the dot (tone-coloured, aria-hidden). */
  icon?: React.ComponentType<{ className?: string; "aria-hidden"?: React.AriaAttributes["aria-hidden"] }>;
  title?: string;
  children: React.ReactNode;
  /** Outer layout only (margins, shrink, wrapping). */
  className?: string;
}) {
  return (
    <span title={title} data-tone={tone} className={cn(statusText[tone], className)}>
      {Icon ? (
        <Icon aria-hidden="true" className={statusIcon[tone]} />
      ) : (
        <span aria-hidden="true" className={busy ? statusBusyDot : statusDot[tone]} />
      )}
      {children}
    </span>
  );
}
