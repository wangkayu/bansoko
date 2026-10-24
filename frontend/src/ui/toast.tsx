import type { ReactNode } from "react";
import { toast as sonner, type ExternalToast } from "sonner";
import { errorDetailsFor } from "../lib/errors";
import { ErrorDetails } from "./base/ErrorDetails";

// toast wrapper: same shape as sonner's toast.success/info/warning/error. When an
// error or warning message was produced by toUserMessage and carries "details",
// the toast attaches an expandable details section with a copy button.

const DETAIL_DURATION_MS = 10_000;

type Show = (message: ReactNode, data?: ExternalToast) => string | number;

function withDetails(show: Show): Show {
  return (message, ...rest) => {
    const details = errorDetailsFor(message);
    if (!details) return show(message, ...rest);
    const data = rest[0];
    return show(message, {
      duration: DETAIL_DURATION_MS,
      closeButton: true,
      ...data,
      description: data?.description ?? <ErrorDetails details={details} />,
    });
  };
}

export const toast = {
  success: ((message, ...rest) => sonner.success(message, ...rest)) as Show,
  info: ((message, ...rest) => sonner.info(message, ...rest)) as Show,
  warning: withDetails((message, ...rest) => sonner.warning(message, ...rest)),
  error: withDetails((message, ...rest) => sonner.error(message, ...rest)),
};
