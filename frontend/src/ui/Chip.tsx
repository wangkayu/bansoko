import { cn } from "@/lib/utils";
import { chipTone, type ChipTone } from "./base/tones";

export type { ChipTone } from "./base/tones";

// Chip — category / marker label: connection kind, version, trigger, "default
// template". A soft filled capsule, no outline and no dot, so it never reads as
// a live status (StatePill) or a seed tag.

export function Chip({
  tone = "neutral",
  title,
  children,
  className,
}: {
  tone?: ChipTone;
  title?: string;
  children: React.ReactNode;
  /** Outer layout only (margins). */
  className?: string;
}) {
  return (
    <span title={title} data-tone={tone} className={cn(chipTone[tone], className)}>
      {children}
    </span>
  );
}
