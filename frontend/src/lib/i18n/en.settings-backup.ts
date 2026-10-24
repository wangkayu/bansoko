// Settings · Backups: export to a downloaded file, restore from an uploaded one.

import type { CopyShape } from "./types";
import type { settingsBackupCopy } from "./zh-CN.settings-backup";

export const enSettingsBackupCopy = {
  backup: {
    title: "Backups",
    purpose: "Export the current data to a backup file, or restore from one",
    help: "A backup file holds subscriptions, settings, records and encrypted saved passwords, but not the encryption key. The server keeps no backups, so keep the file yourself. A backup restores only onto a service with the same data version.",
    helpLabel: "About: what a backup holds",
    loading: "Loading backup info…",
    loadFailed: "Couldn't load backup info",
    loadFailedFallback: "Try again later",
    reload: "Reload",
    schemaVersion: "Data version",
    exportTitle: "Export backup",
    exportHint: "Download a backup file with all current data",
    exportAction: "Export",
    exporting: "Exporting…",
    exported: "Downloading {file}",
    exportFailed: "Export failed",
    exportUnavailable: "The database backup tools are missing on this deployment, so export is off",
    restoreTitle: "Restore from backup",
    restoreHint: "Upload a backup file to replace all current data",
    restoreAction: "Choose backup file…",
    restoreUnavailable: {
      tools_unavailable: "The database backup tools are missing on this deployment, so restore is off",
      database_unsupported: "This deployment's database connection doesn't support restoring from the web page",
      privileges_insufficient: "The database account may not create databases, so restoring from the web page is off",
      restore_in_progress: "Restoring",
    },
    fileInvalid: "The file isn't a valid backup",
    versionMismatch: "The backup's data version ({backup}) doesn't match the current version ({current}), so it can't be restored",
    confirmTitle: "Restore backup?",
    confirmReplace: "All current data will be replaced with the backup from {time}",
    confirmKeep: "Downloader tasks and media files are not affected. This can't be undone; export first to keep the current data. The service restarts and you sign in again.",
    confirmAction: "Restore",
    uploading: "Uploading and checking the backup…",
    restarting: "Backup loaded; the service is restarting…",
    restored: "Restored. Sign in again.",
    unchanged: "The restore didn't finish. Current data is unchanged.",
    timedOut: "The service didn't come back. Check the service status.",
    restoreFailed: "Restore failed",
    keyNoteTitle: "Save the encryption key separately",
    keyNote: "Backups don't include the encryption key in config/secrets.json. Keep a copy of that file with your backups, or saved passwords and tokens can't be decrypted after a restore.",
  },
} as const satisfies CopyShape<typeof settingsBackupCopy>;
