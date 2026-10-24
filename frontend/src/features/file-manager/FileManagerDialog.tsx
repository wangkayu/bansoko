import { useEffect, useState, type ReactNode } from "react";
import { useInfiniteQuery, useQuery, type UseQueryResult } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, File, FileText, FileVideo, Folder, HardDrive, Link2, Lock, RefreshCw } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  ApiError,
  getFileManagerEntry,
  getFileManagerOperation,
  listFileManagerEntries,
  listFileManagerMounts,
  toUserMessage,
  type FileManagerEntry,
  type FileManagerEntryDetail,
  type FileManagerEntryState,
  type FileManagerLinkedFile,
  type FileManagerMount,
  type FileManagerMountList,
  type FileManagerOperationView,
} from "../../lib/api/client";
import { formatBytes, formatDateTime } from "../../lib/labels";
import { fileManagerKeys } from "../../lib/queryKeys";
import { t, tryT } from "../../lib/i18n";
import { Button, Dialog, HelpTip, Input, StatePill, StateBlock } from "../../ui/base";
import { DescriptionList } from "../../ui/DescriptionList";
import { ReasonText } from "../../ui/ReasonText";
import { EntryOperations } from "./EntryOperations";
import { animeTitleIndex, mountLabel, operationInFlight } from "./operations";
import { ROOT, ancestorOf, baseName, normalizePath, parentOf, segmentsOf } from "./paths";

// FileManagerDialog — file management. Route-carried (`?tool=files&path=&file=`, see
//   shell/routeDialogs.ts), a right-side drawer; the directory below stays mounted.
// Browsing works in absolute paths: the path bar shows the current directory, and the quick
//   entries (server-supplied mounts) are only jump shortcuts, never a limit on what can be read or
//   written elsewhere. At drawer width it descends level by level "list -> details" (path bar +
//   breadcrumb + list).
// State is derived by the server from the existing records and refreshed through live events once
//   an operation finishes; for write actions in the details panel see EntryOperations (confirm
//   first, carrying an idempotency key and an expected version).
const PAGE_LIMIT = 100;
const OPERATION_POLL_MS = 2000;

const PANE = "tw:min-h-0 tw:min-w-0 tw:overflow-y-auto tw:px-4 tw:py-4";
const LIST = "tw:m-0 tw:grid tw:list-none tw:gap-1 tw:p-0";
const META = "tw:m-0 tw:text-xs tw:text-muted-foreground";
const SECTION_TITLE = "tw:m-0 tw:mb-2 tw:flex tw:items-center tw:gap-1 tw:text-sm tw:font-semibold";
const ROW =
  "tw:flex tw:w-full tw:min-h-11 tw:cursor-pointer tw:items-center tw:gap-2.5 tw:rounded-lg tw:border tw:border-transparent tw:px-2.5 tw:py-1.5 tw:text-left tw:text-sm tw:hover:bg-muted tw:focus-visible:outline-2 tw:focus-visible:outline-ring";
const ROW_ACTIVE = "tw:border-border tw:bg-muted";
// The clickable form of the lock badge: the outer button handles interaction only (hover underline, focus ring); the look is simply the warning state.
const LOCK_BUTTON =
  "tw:inline-flex tw:max-w-full tw:cursor-pointer tw:rounded-control tw:border-0 tw:bg-transparent tw:p-0 tw:text-left tw:hover:underline tw:focus-visible:outline-2 tw:focus-visible:outline-offset-1 tw:focus-visible:outline-ring";

type Tone = "online" | "warning" | "offline" | "info" | "muted";

const STATE_TONE: Record<FileManagerEntryState, Tone> = {
  in_progress: "info",
  managed: "online",
  detached: "muted",
};

export type FileManagerNavigation = { path?: string; file?: string };

export type FileManagerDialogProps = {
  open: boolean;
  /** The current folder's absolute path; unset means the root directory. */
  path?: string;
  /** The selected entry (absolute path). */
  file?: string;
  /** On open, pops the selected entry straight into this operation dialog (notification links `&op=match`); ignored when the operation is unavailable. */
  operation?: "match";
  /** Changes the folder or selection (replacing only the current history entry). Keys set to undefined are cleared. */
  onNavigate: (next: FileManagerNavigation) => void;
  onOpenAnime: (managedAnimeID: string) => void;
  onOpenRecord: (acquisitionID: string) => void;
  onClose: () => void;
};

export function FileManagerDialog({ open, path, file, operation, onNavigate, onOpenAnime, onOpenRecord, onClose }: FileManagerDialogProps) {
  const current = path ?? ROOT;
  const mounts = useQuery({ queryKey: fileManagerKeys.mounts, queryFn: listFileManagerMounts, enabled: open });
  const animeTitles = animeTitleIndex(mounts.data);
  // On phones one column at a time.
  // In the drawer a single column descends level by level: list -> details.
  const pane = file ? "detail" : "list";
  const folder = current === ROOT ? undefined : current;

  return (
    <Dialog open={open} onRequestClose={onClose} tier="R" title={t("fileManager.title")} bodyLayout="fill">
      <div className="tw:grid tw:min-h-0 tw:flex-1 tw:grid-cols-1">
        <section aria-label={t("fileManager.listAria")} className={cn(PANE, pane !== "list" && "tw:hidden")}>
          <PathBar mounts={mounts} path={current} onOpenFolder={(next) => onNavigate({ path: next === ROOT ? undefined : next, file: undefined })} />
          <EntryList
            key={current}
            path={current}
            animeTitles={animeTitles}
            onOpenFolder={(next) => onNavigate({ path: next === ROOT ? undefined : next, file: undefined })}
            onSelect={(entry) => onNavigate({ path: folder, file: entry })}
            onOpenAnime={onOpenAnime}
          />
        </section>
        <section aria-label={t("fileManager.detail.aria")} className={cn(PANE, pane !== "detail" && "tw:hidden")}>
          <div className="tw:mb-2">
            <Button variant="ghost" size="sm" className="tw:-ml-2" onClick={() => onNavigate({ path: folder, file: undefined })}>
              <ChevronLeft className="tw:size-4" aria-hidden="true" />
              {t("fileManager.backToList")}
            </Button>
          </div>
          {file ? (
            <EntryDetailPanel
              path={file}
              operation={operation}
              onOperationDialogClosed={() => onNavigate({ path: folder, file })}
              onOpenAnime={onOpenAnime}
              onOpenRecord={onOpenRecord}
              onSelect={(next) => {
                const parent = parentOf(next);
                onNavigate({ path: parent === ROOT ? undefined : parent, file: next });
              }}
            />
          ) : (
            <StateBlock kind="empty" title={t("fileManager.detail.empty")} />
          )}
        </section>
      </div>
    </Dialog>
  );
}

function stateLabel(state: FileManagerEntryState): string {
  return tryT(`fileManager.state.${state}`) ?? state;
}

export function StateTag({ state }: { state: FileManagerEntryState }) {
  return <StatePill tone={STATE_TONE[state] ?? "muted"} busy={state === "in_progress"}>{stateLabel(state)}</StatePill>;
}

/** The lock badge: a lock icon plus the anime managing this file; when the anime is present, clicking goes to its settings. */
function LockBadge({
  entry,
  animeTitles,
  onOpenAnime,
}: {
  entry: Pick<FileManagerEntry, "locked" | "managed_anime_id">;
  animeTitles: Map<string, string>;
  onOpenAnime: (managedAnimeID: string) => void;
}) {
  if (!entry.locked) return null;
  const animeID = entry.managed_anime_id;
  const title = animeID ? animeTitles.get(animeID) : undefined;
  const label = title ? t("fileManager.lockedBy", { title }) : t("fileManager.locked");
  if (!animeID || !title) {
    return (
      <StatePill tone="warning" icon={Lock}>
        {label}
      </StatePill>
    );
  }
  return (
    <button type="button" className={LOCK_BUTTON} title={t("fileManager.openLockedAnime")} onClick={() => onOpenAnime(animeID)}>
      <StatePill tone="warning" icon={Lock}>
        {label}
      </StatePill>
    </button>
  );
}

/** The path bar: quick entries (server-supplied mounts) plus an absolute path input. */
function PathBar({ mounts, path, onOpenFolder }: { mounts: UseQueryResult<FileManagerMountList>; path: string; onOpenFolder: (path: string) => void }) {
  const [draft, setDraft] = useState(path);
  const [invalid, setInvalid] = useState(false);
  useEffect(() => {
    setDraft(path);
    setInvalid(false);
  }, [path]);

  const go = () => {
    const next = normalizePath(draft);
    if (next === undefined) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    onOpenFolder(next);
  };

  return (
    <div className="tw:mb-3 tw:grid tw:gap-2">
      <section aria-label={t("fileManager.locationsAria")}>
        <h3 className={SECTION_TITLE}>{t("fileManager.locations")}</h3>
        {mounts.error ? (
          <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
            <span className={META}>{toUserMessage(mounts.error, t("fileManager.mountsLoadFailed"))}</span>
            <Button size="sm" variant="link" onClick={() => void mounts.refetch()}>
              {t("fileManager.retry")}
            </Button>
          </div>
        ) : !mounts.data ? (
          <StateBlock kind="busy" title={t("fileManager.mountsLoading")} />
        ) : mounts.data.mounts.length === 0 ? (
          <p className={META}>{t("fileManager.choosePath")}</p>
        ) : (
          <ul className={cn(LIST, "tw:flex tw:flex-wrap tw:gap-1")}>
            {mounts.data.mounts.map((mount) => (
              <li key={mount.key} className="tw:inline-flex tw:items-center tw:gap-1.5">
                <Button
                  size="sm"
                  variant={mount.available && mount.path === path ? "secondary" : "ghost"}
                  disabled={!mount.available}
                  onClick={() => onOpenFolder(mount.path)}
                >
                  <MountIcon mount={mount} />
                  {mountLabel(mount)}
                </Button>
                {!mount.available ? <span className={META}>{mountUnavailableLabel(mount)}</span> : null}
              </li>
            ))}
          </ul>
        )}
      </section>
      <form
        className="tw:flex tw:items-center tw:gap-2"
        onSubmit={(event) => {
          event.preventDefault();
          go();
        }}
      >
        <Input
          aria-label={t("fileManager.pathBar.label")}
          value={draft}
          onChange={(event) => {
            setDraft(event.target.value);
            setInvalid(false);
          }}
          aria-invalid={invalid ? true : undefined}
          autoComplete="off"
          spellCheck={false}
        />
        <Button size="sm" variant="secondary" type="submit">
          {t("fileManager.pathBar.go")}
        </Button>
      </form>
      {invalid ? <p className={META}>{t("fileManager.pathBar.absolute")}</p> : null}
    </div>
  );
}

function MountIcon({ mount }: { mount: FileManagerMount }) {
  const className = "tw:size-4 tw:flex-none tw:text-muted-foreground";
  return mount.kind === "anime" ? <Folder className={className} aria-hidden="true" /> : <HardDrive className={className} aria-hidden="true" />;
}

function mountUnavailableLabel(mount: FileManagerMount): string {
  const reason = mount.unavailable_reason;
  return (reason ? tryT(`fileManager.mountUnavailable.${reason}`) : undefined) ?? mount.path;
}

function Breadcrumb({ path, onOpenFolder }: { path: string; onOpenFolder: (path: string) => void }) {
  const segments = segmentsOf(path);
  return (
    <nav aria-label={t("fileManager.breadcrumbAria")} className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-0.5 tw:text-sm">
      <Button size="sm" variant="link" onClick={() => onOpenFolder(ROOT)}>
        {t("fileManager.mount.root")}
      </Button>
      {segments.map((segment, index) => {
        const target = ancestorOf(path, index + 1);
        const last = index === segments.length - 1;
        return (
          <span key={target} className="tw:flex tw:min-w-0 tw:items-center tw:gap-0.5">
            <ChevronRight className="tw:size-3.5 tw:flex-none tw:text-muted-foreground" aria-hidden="true" />
            {last ? (
              <span aria-current="location" className="tw:break-all tw:px-1 tw:font-medium">
                {segment}
              </span>
            ) : (
              <Button size="sm" variant="link" onClick={() => onOpenFolder(target)}>
                {segment}
              </Button>
            )}
          </span>
        );
      })}
    </nav>
  );
}

function EntryList({
  path,
  animeTitles,
  onOpenFolder,
  onSelect,
  onOpenAnime,
}: {
  path: string;
  animeTitles: Map<string, string>;
  onOpenFolder: (path: string) => void;
  onSelect: (path: string) => void;
  onOpenAnime: (managedAnimeID: string) => void;
}) {
  const query = useInfiniteQuery({
    queryKey: fileManagerKeys.entries(path),
    queryFn: ({ pageParam }) => listFileManagerEntries({ path, cursor: pageParam, limit: PAGE_LIMIT }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
  });
  const entries = query.data?.pages.flatMap((page) => page.entries) ?? [];
  const truncated = query.data?.pages.some((page) => page.truncated) ?? false;

  // Bound subtitles hang beneath their video on the same page; when the video is not in the current list, the subtitle gets its own row.
  const listed = new Set(entries.map((entry) => entry.path));
  const children = new Map<string, FileManagerEntry[]>();
  const topLevel: FileManagerEntry[] = [];
  for (const entry of entries) {
    if (entry.companion_of && entry.companion_of !== entry.path && listed.has(entry.companion_of)) {
      const list = children.get(entry.companion_of) ?? [];
      list.push(entry);
      children.set(entry.companion_of, list);
    } else {
      topLevel.push(entry);
    }
  }

  return (
    <div className="tw:grid tw:gap-3">
      <div className="tw:flex tw:items-start tw:justify-between tw:gap-2">
        <Breadcrumb path={path} onOpenFolder={onOpenFolder} />
        <Button
          size="sm"
          variant="ghost"
          aria-label={t("fileManager.refresh")}
          title={t("fileManager.refresh")}
          loading={query.isRefetching}
          onClick={() => void query.refetch()}
        >
          <RefreshCw className="tw:size-4" aria-hidden="true" />
        </Button>
      </div>
      {query.error ? (
        <StateBlock kind="error"
          title={toUserMessage(query.error, t("fileManager.listLoadFailed"))}
          action={
            <Button size="sm" variant="link" onClick={() => void query.refetch()}>
              {t("fileManager.retry")}
            </Button>
          }
        />
      ) : !query.data ? (
        <StateBlock kind="busy" title={t("fileManager.listLoading")} />
      ) : entries.length === 0 ? (
        <StateBlock kind="empty" title={t("fileManager.emptyFolder")} />
      ) : (
        <>
          {truncated ? <p className={META}>{t("fileManager.truncated", { n: 10000 })}</p> : null}
          <ul aria-label={t("fileManager.listAria")} className={LIST}>
            {topLevel.map((entry) => (
              <li key={entry.path}>
                <EntryRow entry={entry} animeTitles={animeTitles} onOpenFolder={onOpenFolder} onSelect={onSelect} onOpenAnime={onOpenAnime} />
                {children.get(entry.path)?.length ? (
                  <ul className={cn(LIST, "tw:mt-1 tw:pl-6")}>
                    {children.get(entry.path)!.map((child) => (
                      <li key={child.path}>
                        <EntryRow entry={child} nested animeTitles={animeTitles} onOpenFolder={onOpenFolder} onSelect={onSelect} onOpenAnime={onOpenAnime} />
                      </li>
                    ))}
                  </ul>
                ) : null}
              </li>
            ))}
          </ul>
          {query.hasNextPage ? (
            <Button size="sm" className="tw:justify-self-center" loading={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
              {t("fileManager.loadMore")}
            </Button>
          ) : null}
        </>
      )}
    </div>
  );
}

function EntryIcon({ entry }: { entry: Pick<FileManagerEntry, "kind" | "role"> }) {
  const className = "tw:size-4 tw:flex-none tw:text-muted-foreground";
  if (entry.kind === "directory") return <Folder className={className} aria-hidden="true" />;
  if (entry.kind === "symlink") return <Link2 className={className} aria-hidden="true" />;
  if (entry.role === "video") return <FileVideo className={className} aria-hidden="true" />;
  if (entry.role === "subtitle") return <FileText className={className} aria-hidden="true" />;
  return <File className={className} aria-hidden="true" />;
}

// A row is two side-by-side buttons (the row itself and the lock badge), not one button nested in the other.
function EntryRow({
  entry,
  nested = false,
  selected,
  animeTitles,
  onOpenFolder,
  onSelect,
  onOpenAnime,
}: {
  entry: FileManagerEntry;
  nested?: boolean;
  selected?: string;
  animeTitles: Map<string, string>;
  onOpenFolder: (path: string) => void;
  onSelect: (path: string) => void;
  onOpenAnime: (managedAnimeID: string) => void;
}) {
  const directory = entry.kind === "directory";
  const meta: string[] = [];
  if (!directory && entry.size_bytes !== undefined) meta.push(formatBytes(entry.size_bytes));
  if (entry.modified_at) meta.push(formatDateTime(entry.modified_at));
  if (!nested && entry.companion_of) meta.push(t("fileManager.followsVideo", { name: baseName(entry.companion_of) }));
  return (
    <div className={cn(ROW, selected === entry.path && ROW_ACTIVE)}>
      <button
        type="button"
        className="tw:flex tw:min-w-0 tw:flex-1 tw:cursor-pointer tw:items-center tw:gap-2.5 tw:py-1.5 tw:text-left tw:text-sm tw:focus-visible:outline-2 tw:focus-visible:outline-ring"
        aria-current={selected === entry.path ? "true" : undefined}
        onClick={() => (directory ? onOpenFolder(entry.path) : onSelect(entry.path))}
      >
        <EntryIcon entry={entry} />
        <span className="tw:grid tw:min-w-0 tw:flex-1 tw:gap-0.5">
          <span className="tw:break-all">{entry.name}</span>
          {meta.length > 0 ? <span className={META}>{meta.join(" · ")}</span> : null}
        </span>
        {directory ? <ChevronRight className="tw:size-4 tw:flex-none tw:text-muted-foreground" aria-hidden="true" /> : null}
      </button>
      <span className="tw:flex tw:flex-none tw:flex-wrap tw:items-center tw:justify-end tw:gap-1">
        {entry.not_aligned ? (
          <StatePill tone="warning">
            {t("fileManager.notAligned")}
          </StatePill>
        ) : null}
        <LockBadge entry={entry} animeTitles={animeTitles} onOpenAnime={onOpenAnime} />
        <StateTag state={entry.state} />
      </span>
    </div>
  );
}

export function EntryDetailPanel({
  path,
  operation,
  onOperationDialogClosed,
  onOpenAnime,
  onOpenRecord,
  onSelect,
}: {
  path: string;
  operation?: "match";
  onOperationDialogClosed?: () => void;
  onOpenAnime: (managedAnimeID: string) => void;
  onOpenRecord: (acquisitionID: string) => void;
  onSelect: (path: string) => void;
}) {
  const query = useQuery({
    queryKey: fileManagerKeys.entry(path),
    queryFn: () => getFileManagerEntry(path),
    // While a user operation has not finished, re-read every 2 seconds until a terminal state.
    refetchInterval: (state) => (operationInFlight(state.state.data?.last_operation) ? OPERATION_POLL_MS : false),
  });

  // After a move or rename the original path no longer exists: follow the operation result to the new path.
  const [tracked, setTracked] = useState<string | undefined>();
  const gone = query.error instanceof ApiError && query.error.code === "FILE_MANAGER_PATH_NOT_FOUND";
  const last = query.data?.last_operation;
  useEffect(() => {
    if (last && operationInFlight(last) && (last.operation === "move" || last.operation === "rename")) setTracked(last.id);
  }, [last]);
  const followed = useQuery({
    queryKey: fileManagerKeys.operation(tracked ?? ""),
    queryFn: () => getFileManagerOperation(tracked!),
    enabled: gone && tracked !== undefined,
    refetchInterval: (state) => (state.state.data && !operationInFlight(state.state.data) ? false : OPERATION_POLL_MS),
  });
  const landed = followed.data;
  useEffect(() => {
    if (landed?.state === "completed" && landed.result_path && landed.result_path !== path) {
      setTracked(undefined);
      onSelect(landed.result_path);
    }
  }, [landed, path, onSelect]);

  const onOperation = (view: FileManagerOperationView) => {
    if (view.operation === "move" || view.operation === "rename") setTracked(view.id);
  };

  const relocating = landed?.state === "completed" && !!landed.result_path && landed.result_path !== path;
  if (gone && tracked !== undefined && (!landed || operationInFlight(landed) || relocating)) return <StateBlock kind="busy" title={t("fileManager.detail.loading")} />;
  if (query.error) {
    return (
      <StateBlock kind="error"
        title={toUserMessage(query.error, t("fileManager.detail.loadFailed"))}
        action={
          <Button size="sm" variant="link" onClick={() => void query.refetch()}>
            {t("fileManager.retry")}
          </Button>
        }
      />
    );
  }
  if (!query.data) return <StateBlock kind="busy" title={t("fileManager.detail.loading")} />;
  return (
    <EntryDetailView
      detail={query.data}
      operation={operation}
      onOperationDialogClosed={onOperationDialogClosed}
      onOpenAnime={onOpenAnime}
      onOpenRecord={onOpenRecord}
      onSelect={onSelect}
      onOperation={onOperation}
    />
  );
}

export function EntryDetailView({
  detail,
  operation,
  onOperationDialogClosed,
  onOpenAnime,
  onOpenRecord,
  onSelect,
  onOperation,
}: {
  detail: FileManagerEntryDetail;
  /** The operation dialog a link asked to open directly (`&op=match`). */
  operation?: "match";
  onOperationDialogClosed?: () => void;
  onOpenAnime: (managedAnimeID: string) => void;
  onOpenRecord: (acquisitionID: string) => void;
  onSelect: (path: string) => void;
  onOperation?: (view: FileManagerOperationView) => void;
}) {
  const entry = detail.entry;
  const items: { label: ReactNode; value: ReactNode }[] = [
    {
      label: t("fileManager.detail.state"),
      value: (
        <span className="tw:inline-flex tw:items-center tw:gap-1">
          <StateTag state={entry.state} />
          <HelpTip label={t("fileManager.detail.state")}>{tryT(`fileManager.stateHelp.${entry.state}`) ?? stateLabel(entry.state)}</HelpTip>
        </span>
      ),
    },
  ];
  if (detail.detached_reason) {
    items.push({ label: t("fileManager.detail.reason"), value: tryT(`fileManager.detachedReason.${detail.detached_reason}`) ?? detail.detached_reason });
  }
  items.push({
    label: entry.role ? t("fileManager.detail.role") : t("fileManager.detail.kind"),
    value: entry.role ? (tryT(`fileManager.role.${entry.role}`) ?? entry.role) : (tryT(`fileManager.kind.${entry.kind}`) ?? entry.kind),
  });
  if (entry.size_bytes !== undefined) items.push({ label: t("fileManager.detail.size"), value: formatBytes(entry.size_bytes) });
  if (entry.modified_at) items.push({ label: t("fileManager.detail.modified"), value: formatDateTime(entry.modified_at) });
  if (detail.anime) {
    const anime = detail.anime;
    items.push({
      label: t("fileManager.detail.anime"),
      value: (
        <Button size="sm" variant="link" title={t("fileManager.detail.openAnime")} onClick={() => onOpenAnime(anime.managed_anime_id)}>
          {anime.title}
        </Button>
      ),
    });
  }
  if (detail.acquisition_id) {
    const acquisitionID = detail.acquisition_id;
    items.push({
      label: t("fileManager.detail.record"),
      value: (
        <Button size="sm" variant="link" onClick={() => onOpenRecord(acquisitionID)}>
          {t("fileManager.detail.openRecord")}
        </Button>
      ),
    });
  }
  if (detail.in_progress.length > 0) {
    items.push({
      label: t("fileManager.detail.processing"),
      value: (
        <ul className={LIST}>
          {detail.in_progress.map((owner, index) => (
            <li key={`${owner.kind}:${owner.acquisition_id ?? index}`} className="tw:flex tw:flex-wrap tw:items-center tw:gap-x-2">
              <span>{tryT(`fileManager.inProgressKind.${owner.kind}`) ?? owner.kind}</span>
              {owner.acquisition_id && owner.acquisition_id !== detail.acquisition_id ? (
                <Button size="sm" variant="link" onClick={() => onOpenRecord(owner.acquisition_id!)}>
                  {t("fileManager.detail.openRecord")}
                </Button>
              ) : null}
            </li>
          ))}
        </ul>
      ),
    });
  }
  if (detail.episode) {
    const ep = detail.episode;
    items.push({
      label: t("fileManager.detail.episode"),
      value: (
        <span>
          {ep.state !== "resolved" && ep.failure_code ? (
            <ReasonText code={ep.failure_code} />
          ) : (
            t(`fileManager.episodeState.${ep.state}`)
          )}
          {ep.raw_value ? ` · ${ep.raw_value}` : ""}
        </span>
      ),
    });
  }
  if (detail.video) {
    items.push({ label: t("fileManager.detail.video"), value: <LinkedFileView file={detail.video} onSelect={onSelect} /> });
  }

  return (
    <div className="tw:grid tw:gap-4">
      <h3 className="tw:m-0 tw:flex tw:items-start tw:gap-2 tw:text-base tw:font-semibold tw:break-all">
        <span className="tw:mt-0.5">
          <EntryIcon entry={entry} />
        </span>
        {entry.name}
      </h3>
      <DescriptionList items={items} />
      <EntryOperations detail={detail} onOperation={onOperation} initialDialog={operation} onDialogClosed={onOperationDialogClosed} />
      {entry.kind === "directory" ? <p className={META}>{t("fileManager.detail.folderHint")}</p> : null}
      {detail.companions.length > 0 ? (
        <section>
          <h4 className={SECTION_TITLE}>{t("fileManager.detail.companions")}</h4>
          <ul className={LIST}>
            {detail.companions.map((companion, index) => (
              <li key={companion.path ?? index}>
                <LinkedFileView file={companion} onSelect={onSelect} />
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </div>
  );
}

function LinkedFileView({ file, onSelect }: { file: FileManagerLinkedFile; onSelect: (path: string) => void }) {
  const role = tryT(`fileManager.role.${file.role}`) ?? file.role;
  const name = file.path ? baseName(file.path) : role;
  return (
    <span className="tw:inline-flex tw:flex-wrap tw:items-center tw:gap-x-2 tw:gap-y-1">
      {file.present && file.path ? (
        <Button size="sm" variant="link" className="tw:break-all" onClick={() => onSelect(file.path!)}>
          {name}
        </Button>
      ) : (
        <span className="tw:break-all">{name}</span>
      )}
      {!file.present ? <span className={META}>{t("fileManager.detail.missing")}</span> : null}
      {file.not_aligned ? (
        <StatePill tone="warning">
          {t("fileManager.notAligned")}
        </StatePill>
      ) : null}
    </span>
  );
}
