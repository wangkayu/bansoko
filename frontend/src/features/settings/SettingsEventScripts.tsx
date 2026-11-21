import { useLayoutEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Braces, FileCode, FolderOpen, Unlink } from "lucide-react";
import { listEventActions, readEventActionScripts, saveEventAction, type EventActionBinding, type EventActionFile } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { notificationTriggerKindLabel } from "../../lib/labels";
import { eventActionKeys } from "../../lib/queryKeys";
import { Button, Checkbox, Dialog, Select, StateBlock, StatePill } from "../../ui/base";

type Kind = EventActionBinding["kind"];
const kinds: Kind[] = ["file_operation_completed", "download_submitted", "file_operation_failed"];
const timeouts = ["15", "30", "60"] as const;
const paneList = "tw:grid tw:min-w-0 tw:content-start tw:gap-1 tw:border-e tw:border-border tw:pe-3 tw:phone:border-e-0 tw:phone:border-b tw:phone:pe-0 tw:phone:pb-2";

export function SettingsEventScripts() {
  const [open, setOpen] = useState(false);
  const [event, setEvent] = useState<Kind>(kinds[0]!);
  const [drafts, setDrafts] = useState<Partial<Record<Kind, EventActionBinding>>>({});
  const [picking, setPicking] = useState(false);
  const [selected, setSelected] = useState<string>();
  const [justSaved, setJustSaved] = useState(false);
  const browseRef = useRef<HTMLButtonElement>(null);
  const firstFileRef = useRef<HTMLButtonElement>(null);
  const wasPicking = useRef(false);
  const client = useQueryClient();
  const bindings = useQuery({ queryKey: eventActionKeys.bindings, queryFn: ({ signal }) => listEventActions(signal), enabled: open });
  const files = useQuery({ queryKey: eventActionKeys.scripts(), queryFn: ({ signal }) => readEventActionScripts(undefined, signal), enabled: open && picking });
  const candidateName = selected ?? files.data?.[0]?.name;
  const preview = useQuery({ queryKey: eventActionKeys.scripts(candidateName), queryFn: ({ signal }) => readEventActionScripts(candidateName, signal), enabled: open && picking && !!candidateName });
  const candidate = preview.data?.[0];
  const save = useMutation({
    mutationFn: saveEventAction,
    onSuccess: (binding) => {
      client.setQueryData<EventActionBinding[]>(eventActionKeys.bindings, (rows) => rows?.map((row) => row.kind === binding.kind ? binding : row));
      setDrafts((current) => ({ ...current, [binding.kind]: undefined }));
      setJustSaved(true);
    },
    onError: () => { void client.invalidateQueries({ queryKey: eventActionKeys.bindings }); },
  });
  const hasFiles = !!files.data?.length;
  useLayoutEffect(() => {
    if (picking && hasFiles) firstFileRef.current?.focus();
    else if (!picking && wasPicking.current) browseRef.current?.focus();
    wasPicking.current = picking;
  }, [picking, hasFiles]);

  const previous = bindings.data?.find((binding) => binding.kind === event);
  const draft = drafts[event] ?? previous;
  const dirty = !!draft && !!previous && (draft.script !== previous.script || draft.enabled !== previous.enabled || draft.timeout_seconds !== previous.timeout_seconds);
  const canChoose = candidate?.check === "checkPassed";

  function change(fields: Partial<EventActionBinding>) {
    if (!draft) return;
    setDrafts((current) => ({ ...current, [event]: { ...draft, ...fields } }));
    setJustSaved(false);
    save.reset();
  }

  function requestClose() {
    if (picking) setPicking(false);
    else if (!save.isPending) setOpen(false);
  }

  return (
    <>
      <Button size="sm" onClick={() => setOpen(true)}>
        <FileCode size={14} aria-hidden="true" />{t("settings.eventScripts.title")}
      </Button>
      <Dialog open={open} tier="M" title={t(picking ? "settings.eventScripts.chooseTitle" : "settings.eventScripts.configure")}
        onRequestClose={requestClose}
        description={picking ? <><code>/config/scripts</code> · {t("settings.eventScripts.readOnly")}</> : t("settings.eventScripts.global")}
        headerStart={picking ? <Button variant="ghost" size="icon" aria-label={t("settings.eventScripts.back")} title={t("settings.eventScripts.back")}
          className="tw:-ms-1 tw:shrink-0" onClick={requestClose}><ArrowLeft size={16} aria-hidden="true" /></Button> : undefined}
        footer={picking ? <>
          {candidate && <span className="text-caption tw:mr-auto tw:text-muted-foreground tw:phone:w-full">{t("settings.eventScripts.notRun")}</span>}
          <Button variant="ghost" onClick={requestClose}>{t("settings.notifications.cancel")}</Button>
          <Button variant="primary" disabled={!canChoose} onClick={() => {
            if (!canChoose) return;
            if (draft?.script !== candidate.name) change({ script: candidate.name, check: candidate.check, enabled: false });
            setPicking(false);
          }}>{t("settings.eventScripts.choose")}</Button>
        </> : <Button variant="ghost" disabled={save.isPending} onClick={requestClose}>{t("settings.edit.close")}</Button>}>
        {picking ? (
          files.isError ? <ReadError retry={() => void files.refetch()} /> : !files.data ? <Loading /> : !hasFiles ? (
            <StateBlock kind="empty" title={t("settings.eventScripts.noFiles")} />
          ) : (
            <div data-event-script-picker className="tw:grid tw:min-w-0 tw:grid-cols-[13rem_minmax(0,1fr)] tw:gap-4 tw:phone:grid-cols-1">
              <div className={paneList} role="group" aria-label={t("settings.eventScripts.file")}>
                {files.data.map((file, index) => (
                  <Button key={file.name} ref={index === 0 ? firstFileRef : undefined} variant={file.name === candidateName ? "secondary" : "ghost"}
                    aria-pressed={file.name === candidateName} className="tw:h-auto! tw:min-h-9 tw:min-w-0 tw:justify-start tw:py-1.5 tw:text-left" onClick={() => setSelected(file.name)}>
                    <FileCode size={15} aria-hidden="true" className="tw:shrink-0" />
                    <span className="tw:flex tw:min-w-0 tw:flex-1 tw:flex-col tw:items-start tw:gap-0.5 tw:phone:flex-row tw:phone:flex-wrap tw:phone:items-center tw:phone:justify-between tw:phone:gap-x-2">
                      <code className="tw:min-w-0 tw:whitespace-normal tw:wrap-anywhere tw:text-xs">{file.name}</code><ScriptStatus check={file.check} />
                    </span>
                  </Button>
                ))}
              </div>
              {preview.isError ? <ReadError retry={() => void preview.refetch()} /> : !candidate ? <Loading /> : <section data-event-script-preview className="tw:grid tw:min-w-0 tw:content-start tw:gap-2">
                <h3 className="tw:m-0 tw:wrap-anywhere tw:text-sm tw:font-semibold"><code>{candidate.name}</code></h3>
                {candidate.interpreter && <div className="tw:flex tw:flex-wrap tw:gap-x-2 tw:text-xs">
                  <span className="tw:text-muted-foreground">{t("settings.eventScripts.interpreter")}</span><code className="tw:wrap-anywhere">{candidate.interpreter}</code>
                </div>}
                {candidate.source && <ScriptPreview source={candidate.source} truncated={candidate.truncated} />}
              </section>}
            </div>
          )
        ) : bindings.isError && !bindings.data ? <ReadError retry={() => void bindings.refetch()} /> : !draft ? <Loading /> : (
          <section className="tw:grid tw:min-w-0 tw:grid-cols-[11rem_minmax(0,1fr)] tw:gap-4 tw:phone:grid-cols-1">
            <div className={paneList} role="group" aria-label={t("settings.notifications.field.triggers")}>
              {kinds.map((kind) => {
                const action = bindings.data?.find((row) => row.kind === kind);
                return (
                  <Button key={kind} disabled={save.isPending} variant={kind === event ? "secondary" : "ghost"} aria-pressed={kind === event}
                    className="tw:h-auto! tw:min-h-9 tw:justify-start tw:py-1.5 tw:text-left" onClick={() => { setEvent(kind); setJustSaved(false); save.reset(); }}>
                    <span className="tw:grid tw:min-w-0 tw:gap-0.5">
                      {notificationTriggerKindLabel(kind)}
                      {action?.script && <span className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-1 tw:text-xs">
                        <code className="tw:whitespace-normal tw:wrap-anywhere">{action.script}</code>
                        {action.check && action.check !== "checkPassed" ? <ScriptStatus check={action.check} /> : (
                          <StatePill tone={action.enabled ? "online" : "muted"}>{t(`settings.eventScripts.${action.enabled ? "enabled" : "disabled"}`)}</StatePill>
                        )}
                      </span>}
                    </span>
                  </Button>
                );
              })}
            </div>
            <div className="tw:grid tw:min-w-0 tw:gap-4 tw:rounded-lg tw:border tw:border-border tw:bg-card tw:p-3">
              <div className="tw:flex tw:flex-wrap tw:items-center tw:justify-between tw:gap-2">
                <h3 className="tw:m-0 tw:text-sm tw:font-semibold">{notificationTriggerKindLabel(event)}</h3>
                <Checkbox checked={draft.enabled} disabled={save.isPending || !draft.script || (!draft.enabled && draft.check !== "checkPassed")} onCheckedChange={(enabled) => change({ enabled })}>{t("settings.eventScripts.enable")}</Checkbox>
              </div>
              <div className="tw:grid tw:min-w-0 tw:gap-2 tw:border-y tw:border-border tw:py-3">
                <span className="text-caption tw:text-muted-foreground">{t("settings.eventScripts.file")}</span>
                <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:justify-between tw:gap-2">
                  {draft.script ? <div className="tw:grid tw:min-w-0 tw:justify-items-start tw:gap-1">
                    <code className="tw:wrap-anywhere">{draft.script}</code>{draft.check && <ScriptStatus check={draft.check} />}
                  </div> : <span className="tw:text-sm tw:text-muted-foreground">{t("settings.eventScripts.empty")}</span>}
                  <div className="tw:flex tw:flex-wrap tw:gap-1">
                    <Button ref={browseRef} size="compact" disabled={save.isPending} onClick={() => {
                      setSelected(draft.check === "missingFile" ? undefined : draft.script ?? undefined);
                      void client.invalidateQueries({ queryKey: eventActionKeys.scripts() });
                      setPicking(true);
                    }}><FolderOpen size={16} aria-hidden="true" />{t("settings.eventScripts.browse")}</Button>
                    {draft.script && <Button variant="ghost" size="compact" disabled={save.isPending} onClick={() => change({ script: null, check: undefined, enabled: false })}>
                      <Unlink size={14} aria-hidden="true" />{t("settings.eventScripts.unbind")}
                    </Button>}
                  </div>
                </div>
                {!draft.script && previous?.script && <span className="text-caption tw:text-muted-foreground">{t("settings.eventScripts.unbindHint")}</span>}
              </div>
              <div className="tw:flex tw:flex-wrap tw:items-end tw:justify-between tw:gap-2">
                <div className="tw:grid tw:gap-1">
                  <span className="text-caption tw:text-muted-foreground">{t("settings.eventScripts.timeout")}</span>
                  <Select label={t("settings.eventScripts.timeout")} size="sm" value={String(draft.timeout_seconds)} disabled={save.isPending || !draft.script}
                    options={timeouts.map((value) => ({ value, label: t("settings.eventScripts.seconds", { n: value }) }))}
                    onChange={(value) => change({ timeout_seconds: Number(value) as EventActionBinding["timeout_seconds"] })} />
                </div>
                <Button variant="primary" size="compact" disabled={!dirty || save.isPending} onClick={() => save.mutate({
                  kind: draft.kind, script: draft.script, enabled: draft.enabled, timeout_seconds: draft.timeout_seconds, version: draft.version,
                })}>{t(save.isPending ? "settings.notifications.saving" : "settings.notifications.save")}</Button>
              </div>
              {justSaved && <span role="status" className="text-caption tw:text-muted-foreground">{t("settings.eventScripts.saved")}</span>}
              {save.isError && <span role="alert" className="text-caption tw:text-destructive">{t("settings.eventScripts.saveFailed")}</span>}
            </div>
          </section>
        )}
      </Dialog>
    </>
  );
}

function ScriptPreview({ source, truncated }: { source: string; truncated?: boolean }) {
  const numbers = source.split("\n").map((_, index) => index + 1).join("\n");
  return <div className="tw:grid tw:min-w-0 tw:gap-1">
    <div className="tw:min-w-0 tw:rounded-lg tw:border tw:border-border tw:bg-muted/40">
      <h4 id="script-preview-label" className="text-caption tw:m-0 tw:flex tw:items-center tw:gap-1.5 tw:border-b tw:border-border tw:px-3 tw:py-2 tw:font-medium">
        <Braces size={14} aria-hidden="true" />{t("settings.eventScripts.content")}
      </h4>
      <pre tabIndex={0} aria-labelledby="script-preview-label" className="tw:m-0 tw:max-h-52 tw:overflow-auto tw:px-3 tw:py-2 tw:text-xs tw:leading-relaxed"><span className="tw:inline-flex tw:items-start tw:gap-3"><span aria-hidden="true" className="tw:select-none tw:text-right tw:text-muted-foreground tw:tabular-nums">{numbers}</span><code className="tw:font-[inherit]">{source}</code></span></pre>
    </div>
    {truncated && <span className="text-caption tw:text-muted-foreground">{t("settings.eventScripts.truncated")}</span>}
  </div>;
}

function Loading() { return <StateBlock kind="busy" title={t("settings.delivery.loading")} />; }

function ReadError({ retry }: { retry: () => void }) {
  return <StateBlock kind="error" title={t("settings.eventScripts.loadFailed")} action={<Button onClick={retry}>{t("ui.errorBoundary.retry")}</Button>} />;
}

function ScriptStatus({ check }: { check: EventActionFile["check"] }) {
  return <StatePill tone={check === "checkPassed" ? "online" : "warning"}>{t(`settings.eventScripts.${check}`)}</StatePill>;
}
