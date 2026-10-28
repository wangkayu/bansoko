import type { ObservabilityRecord } from "../lib/api/client";
import { MESSAGE_SPECS } from "../lib/observabilityCatalog.generated";
import { tryT } from "../lib/i18n";
import { subscriptionLogKindLabel } from "../lib/labels";

// One predicate set for the events tab: the row marker, the state filter and the log badge all
// read the states below, so a record the badge counts is always a record the 失败 filter lists.

export type BusinessEntry = Extract<ObservabilityRecord, { record_type: "business" }>["entry"];
export type DeliveryRecord = Extract<ObservabilityRecord, { record_type: "plugin" }>["delivery"];

export type LogState = "in_progress" | "success" | "failure" | "info";

export function entryState(entry: Pick<BusinessEntry, "outcome">): LogState {
  return entry.outcome === "success" ? "success" : entry.outcome === "failure" ? "failure" : "info";
}

/** A delivery with an unknown result shows as a failure, marked "unconfirmed". */
export function deliveryState(delivery: Pick<DeliveryRecord, "state">): LogState {
  switch (delivery.state) {
    case "delivered":
      return "success";
    case "failed":
    case "unknown":
      return "failure";
    default:
      return "in_progress";
  }
}

export function isUnconfirmedDelivery(delivery: Pick<DeliveryRecord, "state">): boolean {
  return delivery.state === "unknown";
}

/** The badge counts one record per seed: a failed acquisition result, or a failed / unconfirmed notification. */
export function isBadgeRecord(record: ObservabilityRecord): boolean {
  if (record.record_type === "business") {
    return record.entry?.kind === "acquisition_settled" && record.entry.outcome === "failure";
  }
  if (record.record_type === "plugin") return deliveryState(record.delivery) === "failure";
  return false;
}

type MessageParams = Record<string, string | number | null | undefined>;

/**
 * The row text of a record: always `log.msg.<message_code>` with its params. A spec with
 * optional params falls back to the bare form when one is absent (the catalog has no
 * placeholder-free variant of a message that needs its param).
 */
export function messageText(entry: Pick<BusinessEntry, "message_code" | "message_params" | "kind">): string {
  const code = entry.message_code;
  const spec = (MESSAGE_SPECS as Record<string, { params: readonly { name: string; required: boolean }[] } | undefined>)[code];
  const params = (entry.message_params ?? {}) as MessageParams;
  const vars: Record<string, string | number> = {};
  let complete = true;
  for (const param of spec?.params ?? []) {
    const value = params[param.name];
    if (value === undefined || value === null || value === "") complete = false;
    else vars[param.name] = value;
  }
  const bare = complete ? undefined : tryT(`log.msgBare.${code}`);
  if (bare !== undefined) return bare;
  return tryT(`log.msg.${code}`, vars) ?? subscriptionLogKindLabel(entry.kind);
}

/** The bare form of a message (no params), used where the file name is shown separately. */
export function bareMessageText(entry: Pick<BusinessEntry, "message_code" | "kind">): string {
  return tryT(`log.msgBare.${entry.message_code}`) ?? tryT(`log.msg.${entry.message_code}`) ?? subscriptionLogKindLabel(entry.kind);
}
