import * as React from "react";
import { cn } from "@/lib/utils";
import { errorDetailsFor, formatErrorDetails, type ErrorDetailsInfo } from "../../lib/errors";
import { t } from "../../lib/i18n";
import "./base.css";

// The error "details" disclosure: the server's raw text, field detail and the
// request ID. Shared by toasts and Alert / ErrorNotice. Copy prefers
// navigator.clipboard; on failure it selects the text so the user can copy it
// by hand.

function selectNode(node: Node): void {
  const selection = window.getSelection();
  if (!selection) return;
  const range = document.createRange();
  range.selectNodeContents(node);
  selection.removeAllRanges();
  selection.addRange(range);
}

export function ErrorDetails({ details, className }: { details: ErrorDetailsInfo; className?: string }) {
  const [open, setOpen] = React.useState(false);
  const [copyState, setCopyState] = React.useState<"idle" | "copied" | "failed">("idle");
  const textRef = React.useRef<HTMLPreElement>(null);
  const text = formatErrorDetails(details);
  const panelId = React.useId();

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setCopyState("copied");
    } catch {
      if (textRef.current) selectNode(textRef.current);
      setCopyState("failed");
    }
  }

  return (
    <div className={cn("tw:grid tw:gap-1 tw:text-xs", className)} data-error-details="">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={panelId}
        onClick={() => setOpen((value) => !value)}
        className="tw:w-fit tw:cursor-pointer tw:text-muted-foreground tw:underline-offset-2 hover:tw:underline"
      >
        {t("errorDetails.toggle")}
      </button>
      {open ? (
        <div id={panelId} className="tw:grid tw:gap-1">
          <pre
            ref={textRef}
            className="tw:m-0 tw:max-h-40 tw:overflow-auto tw:whitespace-pre-wrap tw:break-all tw:rounded-control tw:border tw:border-border tw:px-2 tw:py-1 tw:font-mono tw:text-muted-foreground"
          >
            {text}
          </pre>
          <div className="tw:flex tw:items-center tw:gap-2">
            <button
              type="button"
              onClick={() => void copy()}
              className="tw:cursor-pointer tw:rounded-control tw:border tw:border-border tw:px-2 tw:py-0.5 hover:tw:bg-muted"
            >
              {t("errorDetails.copy")}
            </button>
            <span role="status" className="tw:text-muted-foreground">
              {copyState === "copied" ? t("errorDetails.copied") : copyState === "failed" ? t("errorDetails.copyFailed") : ""}
            </span>
          </div>
        </div>
      ) : null}
    </div>
  );
}

/** Renders the registered details for a display message; renders nothing when none is registered (or the message is not a string). */
export function ErrorDetailsFor({ message, className }: { message: unknown; className?: string }) {
  const details = errorDetailsFor(message);
  return details ? <ErrorDetails details={details} className={className} /> : null;
}
