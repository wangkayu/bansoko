// "Download timeouts" setting (Settings → Download & Import): Global only, no per-anime setting.

import type { CopyShape } from "./types";
import type { downloadTimeoutsCopy } from "./zh-CN.download-timeouts";

export const enDownloadTimeoutsCopy = {
  title: "Download Timeouts",
  hint: "Changes only affect downloads started afterwards",
  helpLabel: "About download timeouts",
  help: "Both times are fixed when a download starts, so a change does not affect downloads already in progress. One-time downloads use the same settings.",
  edit: "Edit",
  editTitle: "Edit Download Timeouts",
  stallLabel: "No-progress timeout",
  stallHint: "A download that makes no progress for this long fails",
  waitLabel: "Maximum download time",
  waitHint: "A download not finished this long after it started fails",
  amountAria: "{field}: amount",
  unitAria: "{field}: unit",
  unit: {
    minutes: "Minutes",
    hours: "Hours",
    days: "Days",
  },
  amount: {
    minutes: "{n} min",
    hours: "{n} h",
    days: "{n} d",
  },
  invalidNumber: "Enter a whole number greater than 0",
  stallRange: "No-progress timeout must be between 10 minutes and 7 days",
  waitRange: "Maximum download time must be between 1 hour and 30 days",
  stallOverWait: "No-progress timeout can't exceed the maximum download time",
  save: "Save",
  saving: "Saving…",
  cancel: "Cancel",
  saved: "Download timeouts saved",
  saveFailed: "Couldn't save download timeouts",
} as const satisfies CopyShape<typeof downloadTimeoutsCopy>;
