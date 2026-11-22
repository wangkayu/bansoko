import { Suspense, useRef, useState, type ReactNode } from "react";
import { BookOpen, BookmarkPlus, PackageCheck, ScanSearch } from "lucide-react";
import { toUserMessage, type SubscribeResult } from "../../lib/api/client";
import { lazyEntry } from "../../lib/lazyEntry";
import { getCandidateStore } from "../../lib/candidate/serverCandidateStore";
import { CandidateAlreadyActivated, CandidateNotReady, CandidateVersionConflict } from "../../lib/candidate/port";
import { t } from "../../lib/i18n";
import { broadcastDayLabel } from "../../lib/i18n/broadcastDay";
import { getCatalogProvider, providerLabel } from "../../lib/providers/registry";
import type { CatalogEntryView, ProviderKey } from "../../lib/providers/viewmodels";
import { PHONE_MEDIA_QUERY, useMediaQuery } from "../../lib/useMediaQuery";
import { Button, Checkbox, Dialog, Segmented, Select, StateBlock, Tooltip } from "../../ui/base";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { LazyPendingBar } from "../../ui/LazyPendingBar";
import { StatePill } from "../../ui/StatePill";
import { ThemedToaster } from "../../ui/ThemedToaster";
import { toast } from "../../ui/toast";
import { Poster } from "./card/Poster";
import { catalogListMatches } from "./catalogListFilter";
import { createCatalogListSubscriber } from "./catalogListSubscription";
import { FilterInput } from "./GridToolbar";
import { seasonLabel } from "./season";
import { SynopsisDialog } from "./SynopsisDialog";

const AnimeConfigDialog = lazyEntry(() => import("../anime-config/AnimeConfigDialog"), (module) => module.AnimeConfigDialog);

type Status = "all" | "new" | "live" | "ended";
type Format = NonNullable<CatalogEntryView["releaseFormat"]>;
const formats = { tv: "formatTv", movie: "formatMovie", ova: "formatOva", ona: "formatOna" } as const;
const statusKeys = { all: "grid.statsAll", new: "grid.list.notSubscribed", live: "grid.card.subscribed", ended: "anime.state.ended" } as const;
const rowKey = (entry: CatalogEntryView) => entry.providerEntryRef;

function entryStatus(entry: CatalogEntryView): Exclude<Status, "all"> {
  if (entry.lifecycleState === "retired") return "ended";
  return entry.subscribed || entry.subscriptionState ? "live" : "new";
}

function schedule(entry: CatalogEntryView) {
  return [entry.releaseFormat && t(`grid.card.synopsis.${formats[entry.releaseFormat]}`),
    entry.broadcastDay && entry.broadcastDay !== "none" && broadcastDayLabel(entry.broadcastDay)].filter(Boolean).join(" · ");
}

function releaseInfo(entry: CatalogEntryView) {
  if (entry.source === "custom") return entry.instanceLabel;
  if (entry.releaseState === "no_releases") return t("grid.card.noReleases");
  return entry.releaseCount != null ? t("grid.card.releaseCount", { n: entry.releaseCount }) : entry.lastUpdated;
}

function Score({ entry }: { entry: CatalogEntryView }) {
  return entry.score == null ? null : <span className="tw:block tw:text-xs tw:text-muted-foreground tw:tabular-nums" data-catalog-score>
    {t("grid.card.synopsis.score", { score: entry.score.toFixed(1) })}
  </span>;
}

export function CatalogListDialog({ source, season, entries, loading, error, onReload, onClose, onBrowse, onSubscribed, children }: {
  source: ProviderKey; season: string; entries: readonly CatalogEntryView[]; loading: boolean; error?: string;
  onReload: () => void; onClose: () => void; onBrowse: (entry: CatalogEntryView) => void;
  onSubscribed: () => Promise<unknown>;
  children?: ReactNode;
}) {
  const phone = useMediaQuery(PHONE_MEDIA_QUERY);
  const custom = source === "custom";
  const [status, setStatus] = useState<Status>("new");
  const [query, setQuery] = useState("");
  const [available, setAvailable] = useState(false);
  const [format, setFormat] = useState<"" | Format>("");
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [confirm, setConfirm] = useState<CatalogEntryView[]>([]);
  const [startAfter, setStartAfter] = useState(false);
  const [busy, setBusy] = useState(false);
  const [batchProgress, setBatchProgress] = useState<{ current: number; total: number } | null>(null);
  const [completed, setCompleted] = useState<ReadonlyMap<string, SubscribeResult>>(() => new Map());
  const [failures, setFailures] = useState<{ key: string; title: string; message: string }[]>([]);
  const [synopsis, setSynopsis] = useState<CatalogEntryView>();
  const [editing, setEditing] = useState<CatalogEntryView>();
  const [subscribe] = useState(() => createCatalogListSubscriber(getCandidateStore()));
  const running = useRef(false);

  const statusOf = (entry: CatalogEntryView) => {
    const state = entryStatus(entry);
    return state === "new" && completed.has(rowKey(entry)) ? "live" : state;
  };
  const canSelect = (entry: CatalogEntryView) => statusOf(entry) === "new" && (!custom || Boolean(entry.candidateId));
  const matches = catalogListMatches(query);
  const matched = entries.filter((entry) => matches(entry) && (!available || entry.releaseState !== "no_releases") &&
    (!format || entry.releaseFormat === format));
  const visible = matched.filter((entry) => status === "all" || statusOf(entry) === status);
  const selectable = visible.filter(canSelect);
  const picked = selectable.filter((entry) => selected.has(rowKey(entry)));
  const allSelected = picked.length > 0 && picked.length === selectable.length;

  const changeFilter = (apply: () => void) => { setSelected(new Set()); apply(); };
  const openConfirm = (targets: CatalogEntryView[]) => { setStartAfter(false); setConfirm(targets); };

  async function runBatch() {
    if (running.current) return;
    const keys = new Set(confirm.map(rowKey));
    const targets = entries.filter((entry) => keys.has(rowKey(entry)) && canSelect(entry));
    if (!targets.length) { toast.info(t("catalog.changed")); return; }
    running.current = true;
    setBusy(true);
    setBatchProgress({ current: 1, total: targets.length });
    setFailures([]);
    const succeeded = new Map<string, SubscribeResult>();
    const failed: typeof failures = [];
    try {
      // Run one activation at a time: starting a subscription also admits scheduler work.
      for (let index = 0; index < targets.length; index += 1) {
        const entry = targets[index]!;
        setBatchProgress({ current: index + 1, total: targets.length });
        const key = rowKey(entry);
        try {
          succeeded.set(key, await subscribe(entry, startAfter));
        } catch (cause) {
          const message = cause instanceof CandidateAlreadyActivated || cause instanceof CandidateNotReady || cause instanceof CandidateVersionConflict
            ? cause.message
            : toUserMessage(cause, t("grid.subscribeFailed", { title: entry.title }));
          failed.push({ key, title: entry.title, message });
        }
      }
      setCompleted((current) => new Map([...current, ...succeeded]));
      setSelected(new Set(failed.map(({ key }) => key)));
      setFailures(failed);
      await onSubscribed();
      if (!failed.length) toast.success(t("grid.list.success", { n: succeeded.size }));
      else toast.error(t("grid.list.partial", { ok: succeeded.size, failed: failed.length }));
    } finally {
      running.current = false;
      setBusy(false);
      setBatchProgress(null);
    }
  }

  const selectAll = <Checkbox size="sm" checked={allSelected} disabled={busy || !selectable.length}
    indeterminate={picked.length > 0 && !allSelected} aria-label={t("grid.list.selectAll")}
    onCheckedChange={(checked) => setSelected(new Set(checked ? selectable.map(rowKey) : []))} />;

  function pick(entry: CatalogEntryView) {
    const key = rowKey(entry);
    return canSelect(entry) && <Checkbox size="sm" checked={selected.has(key)} disabled={busy}
      aria-label={t("grid.manage.selectRow", { title: entry.title })} onCheckedChange={(checked) => setSelected((current) => {
        const next = new Set(current);
        if (checked) next.add(key); else next.delete(key);
        return next;
      })} />;
  }

  function chip(entry: CatalogEntryView) {
    const state = statusOf(entry);
    const label = t(statusKeys[state]);
    return state === "new" ? <span className="tw:text-xs tw:text-muted-foreground">{label}</span> :
      <StatePill tone={state === "live" ? "online" : "muted"}>{label}</StatePill>;
  }

  function titleButton(entry: CatalogEntryView) {
    return <button type="button" aria-haspopup="dialog" disabled={busy} title={entry.title}
      className="tw:block tw:min-w-0 tw:max-w-full tw:truncate tw:text-start tw:font-medium tw:text-accent-foreground tw:underline-offset-2 tw:hover:underline tw:focus-visible:rounded-sm tw:focus-visible:outline-2 tw:focus-visible:-outline-offset-2 tw:focus-visible:outline-ring tw:disabled:text-muted-foreground tw:disabled:no-underline tw:phone:overflow-visible tw:phone:whitespace-normal tw:phone:wrap-anywhere"
      onClick={() => {
        const result = completed.get(rowKey(entry));
        setEditing(!entry.managedAnimeRef && result ? {
          ...entry,
          managedAnimeRef: result.managed_anime.id,
          subscribed: true,
          subscriptionState: result.subscription.state,
          lifecycleState: result.subscription.lifecycle_state,
        } : entry);
      }}>{entry.title}</button>;
  }

  function recordActivation(entry: CatalogEntryView, result: SubscribeResult) {
    const key = rowKey(entry);
    setCompleted((current) => new Map(current).set(key, result));
    setSelected((current) => { const next = new Set(current); next.delete(key); return next; });
    setFailures((current) => current.filter((failure) => failure.key !== key));
    void onSubscribed();
  }

  function actions(entry: CatalogEntryView) {
    if (statusOf(entry) === "ended" || (custom && !entry.candidateId)) return null;
    const action = (label: string, name: string, Icon: typeof BookOpen, onClick: () => void, subscribe = false) => {
      const button = <Button size="sm" variant="ghost" disabled={busy} aria-haspopup="dialog" aria-label={name} onClick={onClick}
        className={`tw:w-full tw:gap-1! tw:px-0! ${subscribe ? "tw:not-disabled:border-accent-foreground/40 tw:not-disabled:text-accent-foreground tw:not-disabled:hover:border-accent-foreground" : ""}`}>
        <Icon aria-hidden className="tw:size-3.5 tw:shrink-0 tw:phone:size-4" />{!phone && label}
      </Button>;
      return phone ? <Tooltip content={label}>{button}</Tooltip> : button;
    };
    return <div data-catalog-actions className={custom
      ? "tw:grid tw:shrink-0 tw:grid-cols-[4rem_5.25rem] tw:gap-1 tw:phone:mr-2 tw:phone:grid-cols-[repeat(2,2rem)] tw:phone:gap-0.5"
      : "tw:grid tw:shrink-0 tw:grid-cols-[4.25rem_4rem_5.25rem] tw:gap-1 tw:phone:mr-2 tw:phone:grid-cols-[repeat(3,2rem)] tw:phone:gap-0.5"}>
      {!custom && action(t("grid.list.synopsis"), t("grid.card.viewSynopsisFor", { title: entry.title }), BookOpen, () => setSynopsis(entry))}
      {action(t("grid.card.browse"), t("grid.list.browseFor", { title: entry.title }), ScanSearch, () => onBrowse(entry))}
      {canSelect(entry) && action(t("anime.subscribe"), t("grid.list.subscribeFor", { title: entry.title }), BookmarkPlus, () => openConfirm([entry]), true)}
    </div>;
  }

  const availableButton = !custom && <Button size={phone ? "icon" : "compact"} disabled={busy}
    aria-pressed={available} aria-label={t("grid.availableOnly")} title={t("grid.availableOnly")}
    className="tw:shrink-0" onClick={() => changeFilter(() => setAvailable(!available))}>
    <PackageCheck aria-hidden className="tw:size-4 tw:shrink-0" />{!phone && t("grid.availableOnly")}
  </Button>;

  return (
    <Dialog open tier="L" bodyLayout="fill" closeDisabled={busy} onRequestClose={() => { if (!running.current) onClose(); }}
      title={t("grid.list.title", { source: providerLabel(source) })}
      description={t("grid.list.count", { scope: custom ? t("grid.list.customScope") : seasonLabel(season), visible: visible.length, total: entries.length })}
      footer={<div className="tw:flex tw:w-full tw:flex-wrap tw:items-center tw:gap-2">
        <span className="tw:min-w-0 tw:flex-1 tw:text-sm tw:text-muted-foreground" aria-live="polite">{t("grid.list.selected", { n: picked.length })}</span>
        {picked.length > 0 && <Button size="sm" disabled={busy} onClick={() => setSelected(new Set())}>{t("grid.manage.clearSel")}</Button>}
        <Button size="sm" variant="primary" loading={busy} disabled={!picked.length} onClick={() => openConfirm(picked)}>
          {busy && batchProgress && batchProgress.total > 1
            ? t("grid.list.batchProgress", batchProgress)
            : picked.length ? t("grid.list.batchCount", { n: picked.length }) : t("grid.list.batch")}
        </Button>
      </div>}>
      <ThemedToaster inCatalogList />
      <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-2 tw:border-b tw:border-border tw:px-6 tw:py-2.5 tw:phone:px-4">
        <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-2 tw:phone:basis-full tw:phone:flex-nowrap tw:max-[350px]:gap-1">
          <Segmented<Status> aria-label={t("grid.statsAria")} size="toolbar" value={status} disabled={busy}
            className="tw:min-w-0 tw:max-w-full tw:overflow-x-auto tw:[scrollbar-width:none] tw:max-[350px]:[&_[role=radio]]:px-[5px]"
            onValueChange={(next) => changeFilter(() => setStatus(next))}
            options={(custom ? ["new", "all", "live", "ended"] as const : ["new", "all", "live"] as const).map((value) => ({
              value, label: t(statusKeys[value]), count: matched.filter((entry) => value === "all" || statusOf(entry) === value).length, tone: "primary",
            }))} />
          {!custom && <div className="tw:w-26 tw:shrink-0 tw:max-[350px]:w-24">
            <Select<"" | Format> label={t("grid.list.format")} size="xs" value={format} disabled={busy}
              onChange={(next) => changeFilter(() => setFormat(next))}
              options={[{ value: "", label: t("grid.list.allFormats") }, ...Object.entries(formats).map(([value, key]) => ({ value: value as Format, label: t(`grid.card.synopsis.${key}`) }))]} />
          </div>}
          {!phone && availableButton}
        </div>
        <div className="tw:ml-auto tw:flex tw:min-w-0 tw:items-center tw:gap-2 tw:phone:order-first tw:phone:basis-full">
          <FilterInput value={query} onChange={(next) => { if (!running.current) changeFilter(() => setQuery(next)); }}
            className="tw:w-64 tw:phone:w-auto tw:phone:flex-1"
            placeholder={t(custom ? "grid.list.filterCustom" : "grid.list.filterTitle")} hint={t("grid.list.filterHint")} />
          {phone && availableButton}
        </div>
      </div>
      <div className="tw:min-h-0 tw:flex-1 tw:overflow-auto" data-catalog-list>
        {failures.length > 0 && <ul className="tw:grid tw:gap-1 tw:border-b tw:border-border tw:px-4 tw:py-2 tw:text-sm" aria-live="polite">
          {failures.map(({ key, title, message }) => <li key={key} className="tw:wrap-anywhere"><strong>{title}: </strong>{message}</li>)}
        </ul>}
        {loading ? <StateBlock kind="busy" skeleton="table" title={t("grid.loading")} /> : error ? (
          <StateBlock kind="error" title={error} action={<Button size="sm" onClick={onReload}>{t("grid.retry")}</Button>} />
        ) : !visible.length ? (
          <StateBlock kind="empty" title={t("search.noMatch")} description={t("grid.empty.filteredDesc")} />
        ) : phone ? (
          <ul>
            <li className="tw:sticky tw:top-0 tw:z-[1] tw:flex tw:items-center tw:gap-2 tw:border-b tw:border-border tw:bg-popover tw:px-4 tw:py-2 tw:text-xs tw:text-muted-foreground">
              {selectAll}{t("grid.list.visible", { n: visible.length })}
            </li>
            {visible.map((entry) => {
              const detail = [schedule(entry), releaseInfo(entry)].filter(Boolean).join(" · ");
              return <li key={rowKey(entry)} data-catalog-row className={`tw:grid tw:grid-cols-[1rem_minmax(0,1fr)] tw:items-center tw:gap-2.5 tw:border-b tw:border-border/60 tw:px-4 tw:py-1.5 ${selected.has(rowKey(entry)) ? "tw:bg-accent/40" : ""}`}>
                <span>{pick(entry)}</span>
                <div className="tw:min-w-0">
                  <div className="tw:flex tw:items-start tw:gap-2">
                    <div className="tw:flex tw:min-w-0 tw:flex-1 tw:items-baseline tw:text-sm">
                      {titleButton(entry)}
                      {entry.originalTitle && entry.originalTitle !== entry.title && (
                        <span className="tw:@container/alias tw:min-w-0 tw:flex-1">
                          <span className="tw:ml-1.5 tw:hidden tw:truncate tw:text-xs tw:text-muted-foreground tw:@min-[2.5rem]/alias:block" title={entry.originalTitle}>{entry.originalTitle}</span>
                        </span>
                      )}
                    </div>
                    <span className="tw:shrink-0 tw:whitespace-nowrap">{chip(entry)}</span>
                  </div>
                  <div className="tw:flex tw:min-w-0 tw:items-center tw:gap-1.5">
                    <div className="tw:min-w-0 tw:flex-1">
                      <div className="tw:truncate tw:text-xs tw:text-muted-foreground" title={detail} data-catalog-detail>{detail}</div>
                      <Score entry={entry} />
                    </div>
                    {actions(entry)}
                  </div>
                </div>
              </li>;
            })}
          </ul>
        ) : (
          <table className="tw:w-full tw:table-fixed tw:border-collapse tw:text-sm">
            <colgroup><col className="tw:w-10" /><col />{!custom && <col className="tw:w-[16%]" />}<col className="tw:w-[22%]" /><col className="tw:w-24" /><col className={custom ? "tw:w-[11.25rem]" : "tw:w-[15.5rem]"} /></colgroup>
            <thead className="tw:sticky tw:top-0 tw:z-[1] tw:bg-popover tw:text-left tw:text-xs tw:text-muted-foreground">
              <tr className="tw:border-b tw:border-border tw:[&>th]:px-2 tw:[&>th]:py-2 tw:[&>th]:font-medium">
                <th>{selectAll}</th><th>{t("grid.manage.colTitle")}</th>{!custom && <th>{t("grid.list.format")}</th>}
                <th>{t(custom ? "grid.duplicateLabel" : "grid.list.feed")}</th><th>{t("grid.manage.colState")}</th>
                <th scope="col" className="tw:pl-3!">{t("grid.list.actions")}</th>
              </tr>
            </thead>
            <tbody>{visible.map((entry) => (
              <tr key={rowKey(entry)} data-catalog-row className={`tw:border-b tw:border-border/60 tw:hover:bg-muted/50 ${selected.has(rowKey(entry)) ? "tw:bg-accent/40" : ""}`}>
                <td className="tw:px-2">{pick(entry)}</td>
                <td className="tw:px-2 tw:py-1.5"><div className="tw:flex tw:items-center tw:gap-2">
                  <div className="tw:relative tw:h-9 tw:w-6 tw:shrink-0 tw:overflow-hidden tw:rounded tw:[&_.poster-initial]:text-sm!" aria-hidden="true"><Poster entry={entry} /></div>
                  <div className="tw:min-w-0">{titleButton(entry)}
                    {entry.originalTitle && <div className="tw:truncate tw:text-xs tw:text-muted-foreground" title={entry.originalTitle}>{entry.originalTitle}</div>}
                  </div>
                </div></td>
                {!custom && <td className="tw:px-2 tw:text-xs tw:text-muted-foreground">{schedule(entry) || "—"}</td>}
                <td className="tw:px-2 tw:text-xs tw:text-muted-foreground">
                  {(releaseInfo(entry) || entry.score == null) && <div className="tw:truncate" title={releaseInfo(entry)} data-catalog-detail>{releaseInfo(entry) || "—"}</div>}
                  <Score entry={entry} />
                </td>
                <td className="tw:px-2">{chip(entry)}</td><td className="tw:pl-3 tw:pr-6">{actions(entry)}</td>
              </tr>
            ))}</tbody>
          </table>
        )}
      </div>
      {synopsis && source !== "custom" && <SynopsisDialog entry={synopsis} provider={source} onRequestClose={() => setSynopsis(undefined)} />}
      {children}
      {editing && <Suspense fallback={<LazyPendingBar label={editing.title} />}>
        <AnimeConfigDialog open entry={editing} provider={getCatalogProvider(editing.source)} sourceLabel={providerLabel(editing.source)}
          initialTab="metadata" onOpenChange={(open) => { if (!open) setEditing(undefined); }}
          onDraftSaved={() => void onSubscribed()} onCandidateStateChanged={() => void onSubscribed()}
          onSubscribed={(result) => recordActivation(editing, result)} />
      </Suspense>}
      {confirm.length > 0 && <ConfirmDialog open onOpenChange={(open) => { if (!open) setConfirm([]); }}
        title={t("grid.list.confirmTitle", { n: confirm.length })} confirmLabel={t("grid.list.confirm")}
        description={<div className="tw:grid tw:gap-2">
          <ul className="tw:max-h-[30dvh] tw:list-disc tw:overflow-auto tw:pl-5">{confirm.map((entry) => <li key={rowKey(entry)}>{entry.title}</li>)}</ul>
          <p className="tw:m-0 tw:text-muted-foreground">{t(custom ? "grid.list.presets" : "grid.list.defaults")}</p>
          <Checkbox checked={startAfter} onCheckedChange={setStartAfter}>{t("grid.list.start")}</Checkbox>
        </div>} onConfirm={() => void runBatch()} />}
    </Dialog>
  );
}
