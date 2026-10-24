import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ApiError,
  dryRunNamingVars,
  isVersionConflict,
  listNamingVarsReleases,
  saveNamingVarsVersion,
  toUserMessage,
  type ErrorDetail,
  type NamingVarsProgram,
  type NamingVarsState,
} from "../../lib/api/client";
import { useDirtyConfirm } from "../../ui";
import { Alert, Button, Dialog, Input, SpinnerIcon, Tabs } from "../../ui/base";
import { toast } from "../../ui/toast";
import { t } from "../../lib/i18n";
import { configKeys } from "../../lib/queryKeys";
import { FactCatalog } from "../templateFacts/FactCatalog";
import type { CodeEditorHandle } from "./CodeEditor";
import { VarsRunPanel, type DryRunView } from "./VarsRunPanel";
import {
  catalogFacts,
  dryRunTargets,
  inputToken,
  markersFor,
  normalizeSource,
  problemText,
  saveBlocker,
  type DryRunSelection,
  type EditorMarker,
} from "./varsEditor";

// CodeMirror loads only when the editor opens; it never adds to the settings page's main bundle.
const CodeEditor = lazy(() => import("./CodeEditor"));

const DRY_RUN_DEBOUNCE_MS = 400;

type Problem = { code: string; message: string; start?: number; end?: number };

function problemsOf(details: readonly ErrorDetail[]): Problem[] {
  return details.map((detail) => ({ code: detail.code, message: detail.message, start: detail.start, end: detail.end }));
}

// The variables-program editor (a dialog): code, live dry-run, problem list, input reference.
//   Viewing historical versions is read-only. Saving is CAS with an If-Match version; on conflict
//   the draft is kept and the user chooses.
export function VarsEditorDialog({
  title: titleOverride,
  initialSource,
  state,
  readOnly,
  onClose,
  onSaved,
  onReload,
  onUseAsDraft,
}: {
  /** Revision shown while reading a past version; omitted when editing. */
  title?: string;
  /** Where the draft starts: the active program, or a past version loaded as a draft. */
  initialSource: string;
  state: NamingVarsState;
  readOnly: boolean;
  onClose: () => void;
  onSaved: (saved: NamingVarsProgram) => void;
  /** Re-reads the latest state (used after a conflict). */
  onReload: () => Promise<NamingVarsState>;
  onUseAsDraft?: () => void;
}) {
  const editorRef = useRef<CodeEditorHandle>(null);
  const [draft, setDraft] = useState(initialSource);
  const [note, setNote] = useState("");
  const [baseRevision, setBaseRevision] = useState(state.active.version.revision);
  const [busy, setBusy] = useState(false);
  const [saveError, setSaveError] = useState<string>();
  const [saveProblems, setSaveProblems] = useState<Problem[]>([]);
  const [conflict, setConflict] = useState<number>();
  const [tab, setTab] = useState("run");
  const [selection, setSelection] = useState<DryRunSelection>(() => ({
    samples: state.samples.map((sample) => sample.ref),
    releaseId: "",
    customTitle: "",
  }));
  const [dry, setDry] = useState<DryRunView>({ source: "", pending: true });
  const [markers, setMarkers] = useState<EditorMarker[]>([]);

  const normalized = normalizeSource(draft);
  const releases = useQuery({ queryKey: [...configKeys.namingVars, "releases"], queryFn: listNamingVarsReleases, staleTime: 30_000 });
  const targets = useMemo(() => dryRunTargets(selection), [selection]);

  // Live dry-run: run after a short delay once the draft or the selected release changes; stale responses are discarded.
  useEffect(() => {
    let cancelled = false;
    setDry((current) => ({ ...current, pending: true }));
    const timer = window.setTimeout(() => {
      dryRunNamingVars(normalized, targets)
        .then((result) => {
          if (!cancelled) setDry({ source: normalized, result, pending: false });
        })
        .catch((cause) => {
          if (!cancelled) setDry({ source: normalized, error: toUserMessage(cause, t("varsEditor.run.failed")), pending: false });
        });
    }, DRY_RUN_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [normalized, targets]);

  // After editing, problems from the last failed save no longer correspond to the current text.
  useEffect(() => {
    setSaveProblems([]);
  }, [normalized]);

  const fresh = dry.source === normalized;
  const dryProblems: Problem[] = useMemo(
    () => (fresh && dry.result ? dry.result.diagnostics.map((d) => ({ code: d.code, message: d.message, start: d.start, end: d.end })) : []),
    [fresh, dry.result],
  );
  const problems = useMemo(() => {
    const seen = new Set<string>();
    return [...saveProblems, ...dryProblems].filter((problem) => {
      const key = `${problem.code}:${problem.start ?? ""}:${problem.message}`;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
  }, [saveProblems, dryProblems]);

  // Markers update only when the result corresponds to the current text; while editing, old markers travel with the text inside the editor.
  useEffect(() => {
    if (!fresh && saveProblems.length === 0) return;
    const runtime = fresh && dry.result
      ? dry.result.targets.flatMap((target) => target.diagnostics)
      : [];
    setMarkers([
      ...markersFor(normalized, problems, "error"),
      ...markersFor(normalized, runtime, "warning"),
    ]);
  }, [fresh, dry.result, problems, saveProblems.length, normalized]);

  const blocker = readOnly ? undefined : saveBlocker(draft, state.active.source, state.limits.max_source_bytes);
  const dirty = !readOnly && (normalized !== normalizeSource(initialSource) || note.trim() !== "");

  const { requestClose, confirmDialog } = useDirtyConfirm({
    dirty,
    onClose,
    description: t("settings.edit.discardConfirm"),
    confirmLabel: t("common.discardChanges"),
    danger: true,
  });

  const save = useCallback(async () => {
    setBusy(true);
    setSaveError(undefined);
    setSaveProblems([]);
    setConflict(undefined);
    try {
      const saved = await saveNamingVarsVersion(normalized, note, baseRevision);
      toast.success(t("varsEditor.editor.saved", { n: saved.version.revision }));
      onSaved(saved);
    } catch (cause) {
      if (isVersionConflict(cause)) {
        setConflict(cause.currentVersion);
      } else if (cause instanceof ApiError && cause.code === "NAMING_VARS_INVALID") {
        setSaveProblems(problemsOf(cause.details ?? []));
        setSaveError(toUserMessage(cause, t("varsEditor.editor.saveFailed")));
      } else {
        setSaveError(toUserMessage(cause, t("varsEditor.editor.saveFailed")));
      }
    } finally {
      setBusy(false);
    }
  }, [normalized, note, baseRevision, onSaved]);

  async function loadLatest() {
    const latest = await onReload();
    setDraft(latest.active.source);
    setNote("");
    setBaseRevision(latest.active.version.revision);
    setConflict(undefined);
    setSaveError(undefined);
  }

  function keepMine() {
    if (conflict === undefined) return;
    setBaseRevision(conflict);
    setConflict(undefined);
    setSaveError(undefined);
  }

  const facts = useMemo(() => catalogFacts(state.input_facts), [state.input_facts]);
  const blockerText =
    blocker === "tooLarge"
      ? t("varsEditor.editor.blocked.tooLarge", { max: state.limits.max_source_bytes })
      : blocker
        ? t(`varsEditor.editor.blocked.${blocker}`)
        : undefined;

  const title = titleOverride ?? t("varsEditor.editor.title");
  const description = readOnly ? t("varsEditor.editor.viewDescription") : t("varsEditor.editor.description");

  return (
    <>
      <Dialog
        open
        tier="L"
        onRequestClose={requestClose}
        title={title}
        description={description}
        footer={
          <>
            {dirty && <span className="tw:mr-auto tw:text-xs tw:text-muted-foreground">{t("varsEditor.editor.unsavedHint")}</span>}
            {blockerText && !busy && dirty && <span className="tw:text-xs tw:text-muted-foreground">{blockerText}</span>}
            <Button variant="ghost" onClick={requestClose}>{t("varsEditor.editor.close")}</Button>
            {readOnly ? (
              onUseAsDraft && <Button variant="primary" onClick={onUseAsDraft}>{t("varsEditor.editor.useAsDraft")}</Button>
            ) : (
              <Button variant="primary" loading={busy} disabled={blocker !== undefined} onClick={() => void save()}>
                {busy ? t("varsEditor.editor.saving") : t("varsEditor.editor.save")}
              </Button>
            )}
          </>
        }
      >
        <div className="tw:grid tw:gap-4 tw:min-[900px]:grid-cols-[minmax(0,1fr)_minmax(0,22rem)]">
          <div className="tw:grid tw:min-w-0 tw:content-start tw:gap-3">
            <div className="tw:h-[22rem]">
              <Suspense
                fallback={
                  <div role="status" className="tw:flex tw:h-full tw:items-center tw:justify-center tw:gap-2 tw:text-sm tw:text-muted-foreground">
                    <SpinnerIcon />
                    {t("varsEditor.editor.loadingEditor")}
                  </div>
                }
              >
                <CodeEditor ref={editorRef} value={draft} onChange={setDraft} markers={markers} readOnly={readOnly} label={t("varsEditor.editor.codeLabel")} />
              </Suspense>
            </div>

            <section aria-label={t("varsEditor.editor.problems")} className="tw:grid tw:gap-1" data-testid="vars-problems">
              <h3 className="tw:m-0 tw:text-xs tw:font-medium tw:text-muted-foreground">{t("varsEditor.editor.problems")}</h3>
              {problems.length === 0 ? (
                <p className="naming-muted text-caption">{dry.pending && !fresh ? t("varsEditor.run.pending") : t("varsEditor.editor.noProblems")}</p>
              ) : (
                <ul className="tw:m-0 tw:grid tw:list-none tw:gap-1 tw:p-0">
                  {problems.map((problem, index) => (
                    <li key={`${problem.code}:${problem.start ?? index}:${index}`} className="tw:break-words tw:text-xs tw:text-destructive">
                      {problem.code === "interrupted" ? t("varsEditor.editor.problemRetry") : problemText(normalized, problem)}
                    </li>
                  ))}
                </ul>
              )}
            </section>

            {conflict !== undefined && (
              <Alert
                tone="error"
                title={t("varsEditor.conflictTitle")}
                description={t("varsEditor.conflictBody", { current: conflict, base: baseRevision })}
                action={
                  <span className="tw:flex tw:flex-wrap tw:gap-2">
                    <Button size="sm" onClick={() => void loadLatest()}>{t("varsEditor.conflictReload")}</Button>
                    <Button size="sm" title={t("varsEditor.conflictKeepHint")} onClick={keepMine}>{t("varsEditor.conflictKeep")}</Button>
                  </span>
                }
              />
            )}
            {saveError && conflict === undefined && <Alert tone="error" title={saveError} />}

            {!readOnly && (
              <label className="tw:grid tw:min-w-0 tw:gap-1">
                <span className="tw:text-xs tw:text-muted-foreground">{t("varsEditor.editor.note")}</span>
                <Input value={note} maxLength={state.limits.max_note_length} placeholder={t("varsEditor.editor.notePlaceholder")} onChange={(event) => setNote(event.target.value)} />
              </label>
            )}
          </div>

          <div className="tw:min-w-0">
            <Tabs
              value={tab}
              onChange={setTab}
              items={[
                {
                  key: "run",
                  label: t("varsEditor.editor.tabs.run"),
                  children: (
                    <div className="tw:pt-3">
                      <VarsRunPanel
                        samples={state.samples}
                        releases={releases.data ?? []}
                        releasesFailed={releases.isError}
                        selection={selection}
                        onSelectionChange={setSelection}
                        view={dry}
                        draft={normalized}
                      />
                    </div>
                  ),
                },
                {
                  key: "facts",
                  label: t("varsEditor.editor.tabs.facts"),
                  children: (
                    <div className="tw:grid tw:gap-2 tw:pt-3">
                      <p className="naming-muted text-caption">{t("varsEditor.facts.hint")}</p>
                      <FactCatalog facts={facts} token={inputToken} onInsert={(token) => editorRef.current?.insert(token)} />
                    </div>
                  ),
                },
              ]}
            />
          </div>
        </div>
      </Dialog>
      {confirmDialog}
    </>
  );
}
