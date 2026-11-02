import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Folder } from "lucide-react";
import {
  listFileManagerEntries,
  submitFileManagerOperation,
  toUserMessage,
  type FileManagerRoot,
} from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { fileManagerKeys } from "../../lib/queryKeys";
import { Alert, Button, Dialog, StateBlock } from "../../ui/base";
import { ConflictDialog } from "./ConflictDialog";
import { readSource, targetOccupancy, useIdempotencyKeys, type SourceFile } from "./batch";
import { joinPath, segmentsOf } from "./paths";
import { activeRootOf } from "./operations";

// TransferDialog — copy or move the selection to a folder picked inside the
// dialog: library-directory buttons, a breadcrumb and the subfolder list walk
// the target; nothing is chosen up front about conflicts. The start reads every
// file's revision, checks each target, and only then submits — an occupied
// target becomes the skip / version / replace / cancel choice; replace is
// offered only when every occupied target is a plain file; cancel changes
// nothing.
export function TransferDialog({
  mode,
  sourceDir,
  paths,
  roots,
  onClose,
  onFinished,
}: {
  mode: "copy" | "move";
  /** The folder the selection lives in (targets must leave it). */
  sourceDir: string;
  paths: string[];
  /** The library directories; every target must land inside one of them. */
  roots: readonly FileManagerRoot[];
  onClose: () => void;
  onFinished: (outcome: BatchOutcome) => void;
}) {
  const queryClient = useQueryClient();
  const idempotency = useIdempotencyKeys();
  // The target starts at the source's own root (the deepest one holding it) —
  // a move usually stays inside the library it came from.
  const [destPath, setDestPath] = useState(activeRootOf(roots, sourceDir)?.path ?? sourceDir);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);
  const [pending, setPending] = useState<{ targets: MarkedSource[]; occupied: Set<string>; overwritable: boolean } | null>(null);

  const listing = useQuery({
    queryKey: fileManagerKeys.folders(destPath),
    queryFn: () => listFileManagerEntries({ path: destPath }),
  });
  const folders = (listing.data?.entries ?? []).filter((entry) => entry.kind === "directory");

  const sameFolder = destPath === sourceDir;

  async function run(targets: MarkedSource[], occupied: Set<string>, conflict: "skip" | "version" | "overwrite") {
    setBusy(true);
    setError(null);
    const outcome: BatchOutcome = { submitted: 0, skipped: 0, failed: 0 };
    const failures: string[] = [];
    for (const target of targets) {
      if (conflict === "skip" && occupied.has(target.target)) {
        outcome.skipped++;
        continue;
      }
      const body = {
        operation: mode,
        path: target.path,
        expected_revision: target.revision,
        target: {
          path: target.target,
          conflict: occupied.has(target.target) && conflict !== "skip" ? conflict : ("block" as const),
        },
      };
      try {
        await submitFileManagerOperation(body, idempotency(target.path));
        outcome.submitted++;
      } catch (cause) {
        outcome.failed++;
        failures.push(toUserMessage(cause, t("fileManager.batch.fileFailed", { name: target.name })));
      }
    }
    void queryClient.invalidateQueries({ queryKey: fileManagerKeys.all });
    if (failures.length > 0) outcome.firstFailure = failures[0];
    setBusy(false);
    onFinished(outcome);
  }

  async function start() {
    if (busy || checking || sameFolder || paths.length === 0) return;
    setChecking(true);
    setError(null);
    try {
      const sources: MarkedSource[] = [];
      for (const path of paths) {
        const source = await readSource(path);
        sources.push({ ...source, target: joinPath(destPath, source.name) });
      }
      const occupied = new Set<string>();
      let overwritable = true;
      for (const source of sources) {
        const occupancy = await targetOccupancy(source.target);
        if (occupancy === "free") continue;
        occupied.add(source.target);
        if (occupancy === "locked") overwritable = false;
      }
      if (occupied.size > 0) {
        setPending({ targets: sources, occupied, overwritable });
        return;
      }
      await run(sources, occupied, "version");
    } catch (cause) {
      setError(toUserMessage(cause, t("fileManager.transfer.checkFailed")));
    } finally {
      setChecking(false);
    }
  }

  return (
    <>
      <Dialog
        open
        tier="M"
        title={t(mode === "copy" ? "fileManager.transfer.copyTitle" : "fileManager.transfer.moveTitle")}
        description={t(mode === "copy" ? "fileManager.transfer.copySub" : "fileManager.transfer.moveSub", { n: paths.length })}
        onRequestClose={busy || checking ? () => {} : onClose}
        closeDisabled={busy || checking}
        footer={
          <>
            <span className="tw:mr-auto tw:min-w-0 tw:break-all tw:text-xs tw:text-muted-foreground">
              {t("fileManager.transfer.target", { path: destPath })}
            </span>
            <Button onClick={onClose} disabled={busy || checking}>
              {t("act.cancel")}
            </Button>
            <Button variant="primary" loading={busy || checking} disabled={sameFolder} onClick={() => void start()}>
              {t(mode === "copy" ? "fileManager.transfer.startCopy" : "fileManager.transfer.startMove")}
            </Button>
          </>
        }
      >
        <div className="tw:grid tw:gap-4 tw:lg:grid-cols-[1fr_320px]">
          <section aria-label={t("fileManager.transfer.pickerAria")} className="tw:grid tw:min-w-0 tw:gap-2">
            {roots.length > 0 ? (
              <div className="tw:flex tw:flex-wrap tw:gap-1">
                {roots.map((root) => (
                  <Button
                    key={root.id}
                    size="sm"
                    variant={root.available && root.path === destPath ? "secondary" : "ghost"}
                    disabled={!root.available}
                    onClick={() => setDestPath(root.path)}
                  >
                    {root.label}
                  </Button>
                ))}
              </div>
            ) : null}
            <nav aria-label={t("fileManager.breadcrumbAria")} className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-0.5 tw:text-sm">
              {(() => {
                const base = activeRootOf(roots, destPath);
                const basePath = base?.path ?? destPath;
                const segments = segmentsOf(destPath.slice(basePath.length));
                return (
                  <>
                    {base ? (
                      <Button size="sm" variant="link" onClick={() => setDestPath(basePath)}>
                        {base.label}
                      </Button>
                    ) : null}
                    {segments.map((segment, index) => {
                      const target = `${basePath}/${segments.slice(0, index + 1).join("/")}`;
                      return (
                        <span key={target} className="tw:flex tw:min-w-0 tw:items-center tw:gap-0.5">
                          <ChevronRight className="tw:size-3.5 tw:flex-none tw:text-muted-foreground" aria-hidden="true" />
                          {index === segments.length - 1 ? (
                            <span aria-current="location" className="tw:break-all tw:px-1 tw:font-medium">
                              {segment}
                            </span>
                          ) : (
                            <Button size="sm" variant="link" onClick={() => setDestPath(target)}>
                              {segment}
                            </Button>
                          )}
                        </span>
                      );
                    })}
                  </>
                );
              })()}
            </nav>
            {listing.error ? (
              <Alert tone="error" title={toUserMessage(listing.error, t("fileManager.listLoadFailed"))} />
            ) : folders.length === 0 ? (
              <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("fileManager.transfer.noSubfolders")}</p>
            ) : (
              <ul className="tw:m-0 tw:grid tw:list-none tw:gap-1 tw:p-0">
                {folders.map((folder) => (
                  <li key={folder.path}>
                    <Button variant="ghost" className="tw:w-full tw:justify-start" onClick={() => setDestPath(folder.path)}>
                      <Folder className="tw:size-4 tw:flex-none tw:text-muted-foreground" aria-hidden="true" />
                      <span className="tw:min-w-0 tw:flex-1 tw:truncate">{folder.name}</span>
                      <ChevronRight className="tw:size-4 tw:flex-none tw:text-muted-foreground" aria-hidden="true" />
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </section>
          <section aria-label={t("fileManager.transfer.summaryAria")} className="tw:grid tw:min-w-0 tw:content-start tw:gap-3">
            <StateBlock kind="empty" title={t("fileManager.transfer.selected", { n: paths.length })} />
            <div className="tw:grid tw:gap-1">
              <span className="tw:text-xs tw:font-medium">{t("fileManager.transfer.conflictPolicy")}</span>
              <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("fileManager.transfer.conflictHint")}</p>
            </div>
            {sameFolder ? <Alert tone="warning" title={t("fileManager.transfer.sameFolder")} /> : null}
            {error ? <Alert tone="error" title={error} /> : null}
          </section>
        </div>
      </Dialog>
      {pending ? (
        <ConflictDialog
          targets={[...pending.occupied]}
          busy={busy}
          onCancel={() => setPending(null)}
          onSkip={() => void run(pending.targets, pending.occupied, "skip")}
          onVersion={() => void run(pending.targets, pending.occupied, "version")}
          onOverwrite={pending.overwritable ? () => void run(pending.targets, pending.occupied, "overwrite") : undefined}
        />
      ) : null}
    </>
  );
}

/** One selected file with the target it would take in the destination folder. */
type MarkedSource = SourceFile & { target: string };

export type BatchOutcome = { submitted: number; skipped: number; failed: number; firstFailure?: string };
