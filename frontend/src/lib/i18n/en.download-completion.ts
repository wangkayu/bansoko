// "After download" setting (Settings → Download & Import, one Global value) and the completion stage of an execution record.

import type { CopyShape } from "./types";
import type { downloadCompletionCopy } from "./zh-CN.download-completion";

export const enDownloadCompletionCopy = {
  title: "After Download",
  hint: "What to do with the download client task once all files are imported",
  helpLabel: "About after download",
  help: "Runs only after all files of the torrent have been processed. A failed download keeps its task so it can be retried. A change only affects torrents whose download starts afterwards.",
  field: "Action",
  action: {
    seed: "Keep seeding",
    pause: "Pause the task",
    remove: "Remove the task, keep files",
    remove_with_data: "Remove the task and its files",
  },
  moveSeedNote: "With the move import method the files are moved away, so there is nothing left in the download folder to seed",
  seedHelp: "With the move import method the files are moved away, so there is nothing left in the download folder to seed. Hard links and copies are not affected.",
  removeDataWarning: "Files with no recognized episode, file types that are not processed and files in sub-folders are deleted too. If it cannot be confirmed that no library file is affected, nothing is deleted and the execution record says why",
  edit: "Edit",
  editTitle: "Edit After Download",
  limited: "Set seeding limits",
  limitsHint: "The limits are written to the download client, which acts when the first one is reached. Older qBittorrent versions cannot set the action per task and use the client's own global setting",
  seedingTime: "Seeding time",
  unitLabel: "Unit",
  unit: {
    hours: "Hours",
    minutes: "Minutes",
  },
  ratio: "Share ratio",
  atLimitLabel: "When a limit is reached",
  atLimit: {
    stop: "Pause the task",
    remove: "Remove the task, keep files",
    remove_with_data: "Remove the task and its files",
  },
  limitsLabel: "Seeding limits",
  noLimit: "No limit",
  timeHours: "{n} h",
  timeMinutes: "{n} min",
  ratioValue: "Ratio {n}",
  limitRequired: "Enter a seeding time or a share ratio",
  timeInvalid: "Seeding time must be whole minutes between 1 minute and 365 days",
  ratioInvalid: "Share ratio must be between 0.01 and 9998",
  saved: "After download saved",
  saveFailed: "Failed to save after download",
  saving: "Saving…",
  save: "Save",
  cancel: "Cancel",
} as const satisfies CopyShape<typeof downloadCompletionCopy>;
