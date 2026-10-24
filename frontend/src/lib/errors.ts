import { t, tryT } from "./i18n";
import { joinList, labelValue, withNote } from "./i18n/format";

// Error copy is the frontend's job (copy-guide §4): look up `error.*` by ApiError.code first,
// falling back to "The operation failed, please retry". The server's raw message, field
// details and request ID stay out of the main copy and go into the expandable "details".

export type ErrorFieldDetail = { path?: string; label?: string; message: string };

export type ErrorDetailsInfo = {
  code?: string;
  status?: number;
  /** The server's raw message (usually English). */
  message?: string;
  fields: ErrorFieldDetail[];
  requestId?: string;
};

export type UserError = { message: string; details?: ErrorDetailsInfo };

/** The structural shape of ApiError; avoids a circular dependency on api/client. */
export type ApiErrorShape = {
  status: number;
  code?: string;
  message: string;
  requestId?: string;
  details?: ReadonlyArray<{ field?: string | null; code?: string; message: string }>;
};

/** Field path -> display field name: takes the last segment (array indices stripped) and looks up `field.*`; undefined when unmapped. */
export function fieldLabel(path: string | null | undefined): string | undefined {
  if (!path) return undefined;
  const segment = path.replace(/\[[^\]]*\]/g, "").split(".").filter(Boolean).pop();
  return segment ? tryT(`field.${segment}`) : undefined;
}

export function describeError(apiError: ApiErrorShape | undefined, cause: unknown, fallback: string): UserError {
  if (apiError) {
    const known = apiError.code ? tryT(`error.${apiError.code}`) : undefined;
    const base = known ?? t("common.operationFailed");
    const fields: ErrorFieldDetail[] = (apiError.details ?? []).map((item) => ({
      path: item.field ?? undefined,
      label: fieldLabel(item.field),
      message: item.message,
    }));
    const labels = [...new Set(fields.map((field) => field.label).filter((label): label is string => !!label))];
    return {
      message: labels.length > 0 ? withNote(base, joinList(labels)) : base,
      details: {
        code: apiError.code,
        status: apiError.status,
        message: apiError.message,
        fields,
        requestId: apiError.requestId,
      },
    };
  }
  const raw = cause instanceof Error && cause.message ? cause.message : undefined;
  return { message: fallback, details: raw ? { message: raw, fields: [] } : undefined };
}

/** The plain text of the collapsible "details", also what the copy button copies. */
export function formatErrorDetails(details: ErrorDetailsInfo): string {
  const lines: string[] = [];
  if (details.code) lines.push(labelValue(t("errorDetails.code"), details.code));
  if (details.status !== undefined) lines.push(labelValue(t("errorDetails.status"), details.status));
  if (details.message) lines.push(labelValue(t("errorDetails.message"), details.message));
  for (const field of details.fields) {
    const name = field.path ?? field.label ?? t("errorDetails.fields");
    lines.push(labelValue(t("errorDetails.fields"), `${name} - ${field.message}`));
  }
  if (details.requestId) lines.push(labelValue(t("errorDetails.requestId"), details.requestId));
  return lines.join("\n");
}

// toUserMessage returns a plain string (150 toast / Alert call sites depend on it); details
// are registered under the display copy and retrieved by the toast wrapper and Alert /
// ErrorNotice. A later write of the same copy overwrites the earlier one; capacity is bounded.
const MAX_REMEMBERED = 64;
const remembered = new Map<string, ErrorDetailsInfo>();

export function rememberErrorDetails(message: string, details: ErrorDetailsInfo | undefined): void {
  remembered.delete(message);
  if (!details) return;
  remembered.set(message, details);
  if (remembered.size > MAX_REMEMBERED) {
    const oldest = remembered.keys().next().value;
    if (oldest !== undefined) remembered.delete(oldest);
  }
}

export function errorDetailsFor(message: unknown): ErrorDetailsInfo | undefined {
  return typeof message === "string" ? remembered.get(message) : undefined;
}
