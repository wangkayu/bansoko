import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { DatabaseBackup, Download, KeyRound, RefreshCw, Upload } from "lucide-react";
import { Button, HelpTip, Input } from "../../ui/base";
import { toast } from "../../ui/toast";
import { backupDownloadUrl, getBackupInfo, prepareBackupDownload, restoreBackup, toUserMessage } from "../../lib/api/client";
import { formatDateTime } from "../../lib/labels";
import { t, tryT } from "../../lib/i18n";
import { backupKeys } from "../../lib/queryKeys";
import { ConfirmDialog, DescriptionList, StateBlock } from "../../ui";
import { readBackupFileHeader, startDownload, waitForRestart, type BackupFileHeader } from "./backupFile";

// Data backup: exported as a file download, restored by uploading the backup file; the server keeps
//   no backup. On export the server first generates and verifies the backup, issues a one-time
//   download credential, and the browser downloads straight to disk. Once a restore is accepted the
//   service switches its data and restarts, invalidating every session; here we wait for the service
//   to come back before reporting the outcome.

type RestorePhase = "idle" | "uploading" | "restarting";

export function SettingsBackup() {
  const fileInput = useRef<HTMLInputElement>(null);
  const [exporting, setExporting] = useState(false);
  const [phase, setPhase] = useState<RestorePhase>("idle");
  const [pending, setPending] = useState<{ file: File; header: BackupFileHeader }>();
  const { data, error, isLoading, refetch } = useQuery({
    queryKey: backupKeys.info,
    queryFn: getBackupInfo,
    retry: false,
    enabled: phase === "idle",
  });

  async function handleExport() {
    if (exporting) return;
    setExporting(true);
    try {
      const prepared = await prepareBackupDownload();
      startDownload(backupDownloadUrl(prepared.ticket), prepared.file_name);
      toast.success(t("settings.backup.exported", { file: prepared.file_name }));
    } catch (cause) {
      toast.error(toUserMessage(cause, t("settings.backup.exportFailed")));
    } finally {
      setExporting(false);
    }
  }

  async function handleFileChosen(file: File | undefined) {
    if (fileInput.current) fileInput.current.value = "";
    if (!file || !data) return;
    const header = await readBackupFileHeader(file);
    if (!header) {
      toast.error(t("settings.backup.fileInvalid"));
      return;
    }
    if (header.schemaNumber !== data.schema_version) {
      toast.error(t("settings.backup.versionMismatch", { backup: header.schemaNumber, current: data.schema_version }));
      return;
    }
    setPending({ file, header });
  }

  async function handleRestore(file: File) {
    setPhase("uploading");
    try {
      await restoreBackup(file);
    } catch (cause) {
      setPhase("idle");
      toast.error(toUserMessage(cause, t("settings.backup.restoreFailed")));
      return;
    }
    setPhase("restarting");
    const outcome = await waitForRestart();
    if (outcome === "restored") {
      toast.success(t("settings.backup.restored"));
      return;
    }
    setPhase("idle");
    toast.error(outcome === "unchanged" ? t("settings.backup.unchanged") : t("settings.backup.timedOut"));
    void refetch();
  }

  const restoreBlocked = data && !data.restore_available
    ? tryT(`settings.backup.restoreUnavailable.${data.restore_unavailable_reason ?? "tools_unavailable"}`)
    : undefined;

  return (
    <section className="settings-updates">
      <div className="settings-updates-header">
        <h2 className="settings-updates-title text-title">{t("settings.backup.title")}</h2>
        <HelpTip label={t("settings.backup.helpLabel")}>{t("settings.backup.help")}</HelpTip>
      </div>
      <p className="settings-updates-description text-caption">{t("settings.backup.purpose")}</p>

      {phase === "uploading" && <StateBlock kind="busy" title={t("settings.backup.uploading")} />}
      {phase === "restarting" && <StateBlock kind="busy" title={t("settings.backup.restarting")} />}

      {phase === "idle" && isLoading && <StateBlock kind="busy" title={t("settings.backup.loading")} />}

      {phase === "idle" && error && !data && (
        <StateBlock
          kind="error"
          title={t("settings.backup.loadFailed")}
          description={toUserMessage(error, t("settings.backup.loadFailedFallback"))}
          action={
            <Button size="sm" onClick={() => void refetch()}>
              <RefreshCw size={14} />
              {t("settings.backup.reload")}
            </Button>
          }
        />
      )}

      {phase === "idle" && data && (
        <>
          <div className="settings-updates-card">
            <div className="settings-updates-card-row">
              <DatabaseBackup className="settings-updates-icon" />
              <span className="settings-updates-card-title text-body">{t("settings.backup.exportTitle")}</span>
              <span className="settings-updates-spacer" />
              <Button size="sm" disabled={exporting || !data.export_available} onClick={() => void handleExport()}>
                <Download size={14} />
                {exporting ? t("settings.backup.exporting") : t("settings.backup.exportAction")}
              </Button>
            </div>
            <p className="settings-updates-reason text-caption">
              {data.export_available ? t("settings.backup.exportHint") : t("settings.backup.exportUnavailable")}
            </p>
            <DescriptionList items={[{ label: t("settings.backup.schemaVersion"), value: data.schema_version }]} />
          </div>

          <div className="settings-updates-card">
            <div className="settings-updates-card-row">
              <Upload className="settings-updates-icon" />
              <span className="settings-updates-card-title text-body">{t("settings.backup.restoreTitle")}</span>
              <span className="settings-updates-spacer" />
              <Button size="sm" disabled={!data.restore_available} onClick={() => fileInput.current?.click()}>
                {t("settings.backup.restoreAction")}
              </Button>
              <Input
                ref={fileInput}
                type="file"
                accept=".bansoko-backup"
                hidden
                className="tw:hidden"
                data-testid="backup-file-input"
                onChange={(event) => void handleFileChosen(event.target.files?.[0])}
              />
            </div>
            <p className="settings-updates-reason text-caption">{restoreBlocked ?? t("settings.backup.restoreHint")}</p>
          </div>

          <div className="settings-updates-card">
            <div className="settings-updates-card-row">
              <KeyRound className="settings-updates-icon" />
              <span className="settings-updates-card-title text-body">{t("settings.backup.keyNoteTitle")}</span>
            </div>
            <p className="settings-updates-reason text-caption">{t("settings.backup.keyNote")}</p>
          </div>
        </>
      )}

      <ConfirmDialog
        open={Boolean(pending)}
        onOpenChange={(open) => {
          if (!open) setPending(undefined);
        }}
        title={t("settings.backup.confirmTitle")}
        description={
          pending ? (
            <>
              <p>{t("settings.backup.confirmReplace", { time: formatDateTime(pending.header.createdAt) })}</p>
              <p>{t("settings.backup.confirmKeep")}</p>
            </>
          ) : undefined
        }
        confirmLabel={t("settings.backup.confirmAction")}
        danger
        onConfirm={() => {
          if (pending) void handleRestore(pending.file);
          setPending(undefined);
        }}
      />
    </section>
  );
}
