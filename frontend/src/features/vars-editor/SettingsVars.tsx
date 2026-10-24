import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Download, Pencil, Upload } from "lucide-react";
import {
  activateNamingVarsVersion,
  exportNamingVars,
  getNamingVars,
  getNamingVarsVersion,
  importNamingVars,
  isVersionConflict,
  toUserMessage,
  type NamingVarsDocument,
  type NamingVarsProgram,
  type NamingVarsVersion,
} from "../../lib/api/client";
import { Chip, DescriptionList, ConfirmDialog } from "../../ui";
import { Alert, Button, StateBlock } from "../../ui/base";
import { toast } from "../../ui/toast";
import { t, tryT } from "../../lib/i18n";
import { formatLocaleDateTime, joinSentences } from "../../lib/i18n/format";
import { configKeys } from "../../lib/queryKeys";
import { VarsEditorDialog } from "./VarsEditorDialog";
import { newestFirst } from "./varsEditor";

// Settings - naming variables: the program in use, version history, rollback, import/export. Editing happens in dialogs.

type EditorTarget = { kind: "edit"; source: string } | { kind: "view"; revision: number };

function originLabel(origin: NamingVarsVersion["origin"]): string {
  return tryT(`varsEditor.origin.${origin}`) ?? origin;
}

function timeLabel(value: string | null | undefined): string {
  return value ? formatLocaleDateTime(new Date(value)) : "";
}

function isExportDocument(value: unknown): value is NamingVarsDocument {
  if (!value || typeof value !== "object") return false;
  const record = value as Record<string, unknown>;
  return record.format === "bansoko-naming-vars" && typeof record.source === "string" && typeof record.digest === "string";
}

export function SettingsVars() {
  const queryClient = useQueryClient();
  const state = useQuery({ queryKey: configKeys.namingVars, queryFn: getNamingVars });
  const [target, setTarget] = useState<EditorTarget>();
  const [rollback, setRollback] = useState<NamingVarsVersion>();
  const [pendingImport, setPendingImport] = useState<NamingVarsDocument>();
  const [busy, setBusy] = useState<"export" | "import" | "rollback">();
  const [error, setError] = useState<string>();

  const viewing = target?.kind === "view" ? target.revision : undefined;
  const viewed = useQuery({
    queryKey: [...configKeys.namingVars, "version", viewing],
    queryFn: () => getNamingVarsVersion(viewing as number),
    enabled: viewing !== undefined,
  });

  async function refresh() {
    return queryClient.fetchQuery({ queryKey: configKeys.namingVars, queryFn: getNamingVars, staleTime: 0 });
  }

  async function afterWrite(saved: NamingVarsProgram) {
    queryClient.setQueryData(configKeys.namingVars, (current: typeof state.data) =>
      current ? { ...current, active: saved } : current,
    );
    await queryClient.invalidateQueries({ queryKey: configKeys.namingVars });
  }

  async function doRollback(version: NamingVarsVersion) {
    if (!state.data) return;
    setBusy("rollback");
    setError(undefined);
    try {
      const saved = await activateNamingVarsVersion(version.revision, state.data.active.version.revision);
      toast.success(t("varsEditor.rolledBack", { n: saved.version.revision }));
      await afterWrite(saved);
    } catch (cause) {
      if (isVersionConflict(cause)) {
        toast.warning(t("varsEditor.stale"));
        await refresh();
      } else {
        setError(toUserMessage(cause, t("varsEditor.rollbackFailed")));
      }
    } finally {
      setBusy(undefined);
    }
  }

  async function doExport() {
    setBusy("export");
    setError(undefined);
    try {
      const document = await exportNamingVars();
      const blob = new Blob([`${JSON.stringify(document, null, 2)}\n`], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const link = window.document.createElement("a");
      link.href = url;
      link.download = `naming-vars-v${document.revision}.json`;
      link.click();
      URL.revokeObjectURL(url);
      toast.success(t("varsEditor.exported", { n: document.revision }));
    } catch (cause) {
      setError(toUserMessage(cause, t("varsEditor.exportFailed")));
    } finally {
      setBusy(undefined);
    }
  }

  // The file chooser is opened from code: the page has no use for a resident file field.
  function chooseFile() {
    const input = window.document.createElement("input");
    input.type = "file";
    input.accept = "application/json,.json";
    input.onchange = () => void pickFile(input.files?.[0]);
    input.click();
  }

  async function pickFile(file: File | undefined) {
    if (!file) return;
    setError(undefined);
    try {
      const parsed: unknown = JSON.parse(await file.text());
      if (!isExportDocument(parsed)) {
        setError(t("varsEditor.importNotFile"));
        return;
      }
      setPendingImport(parsed);
    } catch {
      setError(t("varsEditor.importNotFile"));
    }
  }

  async function doImport(document: NamingVarsDocument) {
    if (!state.data) return;
    setBusy("import");
    setError(undefined);
    try {
      const saved = await importNamingVars(document, state.data.active.version.revision);
      toast.success(t("varsEditor.imported", { n: saved.version.revision }));
      await afterWrite(saved);
    } catch (cause) {
      if (isVersionConflict(cause)) {
        toast.warning(t("varsEditor.stale"));
        await refresh();
      } else {
        setError(toUserMessage(cause, t("varsEditor.importFailed")));
      }
    } finally {
      setBusy(undefined);
    }
  }

  const data = state.data;
  const active = data?.active.version;

  return (
    <div className="settings-shared-stack">
      <section className="settings-shared-grid">
        <div className="settings-shared-header">
          <h2 className="settings-shared-title text-title">{t("varsEditor.title")}</h2>
          <span className="settings-shared-muted text-caption">{t("varsEditor.description")}</span>
        </div>

        {state.isLoading && (
          <StateBlock kind="busy" title={t("varsEditor.loading")} />
        )}
        {state.error && <Alert tone="error" title={toUserMessage(state.error, t("varsEditor.loadFailed"))} />}
        {error && <Alert tone="error" title={error} />}

        {data && active && (
          <div className="settings-shared-card">
            <div className="settings-shared-header">
              <h3 className="settings-shared-card-title text-body">
                {t("varsEditor.current")}
                {" · "}
                {t("varsEditor.version", { n: active.revision })}
              </h3>
              <div className="settings-shared-inline">
                <Button variant="ghost" size="sm" loading={busy === "export"} onClick={() => void doExport()}>
                  <Download size={14} />
                  {t("varsEditor.export")}
                </Button>
                <Button variant="ghost" size="sm" loading={busy === "import"} onClick={chooseFile}>
                  <Upload size={14} />
                  {t("varsEditor.import")}
                </Button>
                <Button size="sm" onClick={() => setTarget({ kind: "edit", source: data.active.source })}>
                  <Pencil size={14} />
                  {t("varsEditor.edit")}
                </Button>
              </div>
            </div>
            <div className="settings-shared-offset">
              <DescriptionList
                items={[
                  { label: t("varsEditor.fields.origin"), value: originLabel(active.origin) },
                  { label: t("varsEditor.fields.activatedAt"), value: timeLabel(active.activated_at) },
                  { label: t("varsEditor.fields.note"), value: active.note || t("varsEditor.noNote") },
                  { label: t("varsEditor.fields.size"), value: t("varsEditor.size", { n: active.source_bytes }) },
                ]}
              />
            </div>
          </div>
        )}

        {data && (
          <div className="settings-shared-card">
            <div className="settings-shared-header">
              <h3 className="settings-shared-card-title text-body">{t("varsEditor.history")}</h3>
              <span className="settings-shared-muted text-caption">{t("varsEditor.historyHint")}</span>
            </div>
            {data.versions.length === 0 ? (
              <p className="settings-shared-muted text-caption">{t("varsEditor.historyEmpty")}</p>
            ) : (
              <ul className="settings-shared-list" aria-label={t("varsEditor.history")}>
                {newestFirst(data.versions).map((version) => (
                  <li key={version.revision} className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:justify-between tw:gap-2" data-revision={version.revision}>
                    <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-2">
                      <span className="text-body">{t("varsEditor.version", { n: version.revision })}</span>
                      {version.active && <Chip tone="accent">{t("varsEditor.activeBadge")}</Chip>}
                      <span className="settings-shared-muted text-caption">
                        {[originLabel(version.origin), timeLabel(version.created_at), version.note].filter(Boolean).join(" · ")}
                      </span>
                    </div>
                    <div className="settings-shared-inline">
                      <Button variant="ghost" size="sm" onClick={() => setTarget({ kind: "view", revision: version.revision })}>
                        {t("varsEditor.view")}
                      </Button>
                      {!version.active && (
                        <Button variant="ghost" size="sm" loading={busy === "rollback"} onClick={() => setRollback(version)}>
                          {t("varsEditor.rollback")}
                        </Button>
                      )}
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </section>

      {data && target?.kind === "edit" && (
        <VarsEditorDialog
          key={`edit:${data.active.version.revision}:${target.source.length}`}
          state={data}
          initialSource={target.source}
          readOnly={false}
          onClose={() => setTarget(undefined)}
          onSaved={(saved) => {
            setTarget(undefined);
            void afterWrite(saved);
          }}
          onReload={refresh}
        />
      )}
      {data && target?.kind === "view" && viewed.data && (
        <VarsEditorDialog
          key={`view:${target.revision}`}
          state={data}
          title={t("varsEditor.editor.viewTitle", { n: viewed.data.version.revision })}
          initialSource={viewed.data.source}
          readOnly
          onClose={() => setTarget(undefined)}
          onSaved={() => undefined}
          onReload={refresh}
          onUseAsDraft={() => setTarget({ kind: "edit", source: viewed.data?.source ?? data.active.source })}
        />
      )}

      <ConfirmDialog
        open={rollback !== undefined}
        onOpenChange={(open) => {
          if (!open) setRollback(undefined);
        }}
        title={t("varsEditor.rollbackTitle", { n: rollback?.revision ?? 0 })}
        description={t("varsEditor.rollbackHint")}
        confirmLabel={t("varsEditor.rollbackConfirm")}
        onConfirm={() => {
          if (rollback) void doRollback(rollback);
        }}
      />
      <ConfirmDialog
        open={pendingImport !== undefined}
        onOpenChange={(open) => {
          if (!open) setPendingImport(undefined);
        }}
        title={t("varsEditor.importTitle")}
        description={
          pendingImport
            ? joinSentences(
                t("varsEditor.importHint"),
                pendingImport.note
                  ? t("varsEditor.importDetailNote", { revision: pendingImport.revision, note: pendingImport.note })
                  : t("varsEditor.importDetail", { revision: pendingImport.revision, bytes: new TextEncoder().encode(pendingImport.source).length }),
              )
            : undefined
        }
        confirmLabel={t("varsEditor.importConfirm")}
        onConfirm={() => {
          if (pendingImport) void doImport(pendingImport);
        }}
      />
    </div>
  );
}
