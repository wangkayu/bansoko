import { Fragment, type ReactNode } from "react";
import { cn } from "@/lib/utils";
import { Tooltip } from "./base";

const PLACEHOLDER_TOKEN = /(⟨[^⟩]+⟩)/g;

export type TargetPlaceholderTone = "neutral" | "error";

const PLACEHOLDER_TONE_CLASS: Record<TargetPlaceholderTone, string> = {
  // Normal pre-execution slot (OneShot): calm muted fill matching the BoundSubtitles
  // format-badge family so literal filename text remains the primary focus.
  neutral: "tw:rounded tw:bg-muted tw:px-1 tw:text-muted-foreground",
  // Unresolved runtime fallback (file-manager recognition): rose tint matching the
  // offline status hue so missing runtime facts stand out immediately.
  error: "tw:rounded tw:bg-offline/12 tw:px-1 tw:text-offline",
};

/**
 * Renders a target path while keeping literal characters intact for copy/paste.
 * `⟨...⟩` execution-phase placeholders receive a restrained inline chip treatment
 * shared by OneShot (`neutral`) and file-manager recognition (`error`).
 */
export function TargetPathText({
  value,
  placeholderTone = "neutral",
  placeholderHint,
}: {
  value: string;
  placeholderTone?: TargetPlaceholderTone;
  placeholderHint?: ReactNode;
}) {
  if (!value.includes("⟨")) return <>{value}</>;
  const parts = value.split(PLACEHOLDER_TOKEN);
  const tokenClass = PLACEHOLDER_TONE_CLASS[placeholderTone];
  return (
    <>
      {parts.map((part, index) => {
        if (!part.startsWith("⟨") || !part.endsWith("⟩")) {
          return <Fragment key={index}>{part}</Fragment>;
        }
        if (placeholderHint === undefined) {
          return (
            <span key={index} data-placeholder-token={placeholderTone} className={tokenClass}>
              {part}
            </span>
          );
        }
        return (
          <Tooltip key={index} content={placeholderHint}>
            <span
              tabIndex={0}
              data-placeholder-token={placeholderTone}
              className={cn(
                tokenClass,
                "tw:cursor-help tw:focus-visible:outline-2 tw:focus-visible:outline-offset-1 tw:focus-visible:outline-ring",
              )}
            >
              {part}
            </span>
          </Tooltip>
        );
      })}
    </>
  );
}
