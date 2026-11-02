import { useEffect, useMemo, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, ChevronDown, ChevronRight, File, FileText, FileVideo, Folder, HardDrive, Link2, Lock, RefreshCw, TextCursorInput, X } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  ApiError,
  listFileManagerEntries,
  listFileManagerRoots,
  toUserMessage,
  type FileManagerEntry,
  type FileManagerRecognitionResult,
  type FileManagerRoot,
  type FileManagerRootList,
} from "../../lib/api/client";
import { formatBytes, formatDateTime } from "../../lib/labels";
import { fileManagerKeys } from "../../lib/queryKeys";
import { t, tryT } from "../../lib/i18n";
import { Button, Checkbox, Dialog, DropdownMenu, Input, Segmented, StateBlock, Switch, type ChoiceOption, type MenuEntry } from "../../ui/base";
import { activeRootOf } from "./operations";
import { baseName, normalizePath, parentOf, segmentsOf } from "./paths";
import { TransferDialog, type BatchOutcome } from "./TransferDialog";
import { RenameDialog } from "./RenameDialog";
import { DeleteDialog } from "./DeleteDialog";
import { RecognitionDialog } from "./RecognitionDialog";
import { ManagedFileDialog } from "./ManagedFileDialog";
import { RootsDialog } from "./RootsDialog";

// FileManagerDialog — file management. Route-carried (`?tool=files&path=`, see
//   shell/routeDialogs.ts), a centered large dialog; the directory below stays mounted.
// Browsing works in absolute paths inside the owner-added library directories (roots):
//   the root selector and breadcrumb navigate, and the hidden path field takes any
//   absolute path. The browse layer itself is read-only: every write is a dialog asked
//   for from the selection bar (copy / move / rename / recognize / delete), and a locked
//   entry opens its read-only managed detail instead of a checkbox.
const PAGE_LIMIT = 100;
const OUTSIDE_ROOTS = "FILE_MANAGER_PATH_OUTSIDE_ROOTS";

const PANE = "tw:min-h-0 tw:min-w-0 tw:overflow-y-auto tw:px-4 tw:pt-4";
const LIST = "tw:m-0 tw:grid tw:list-none tw:gap-1 tw:p-0";
const META = "tw:m-0 tw:text-xs tw:text-muted-foreground";
const ROW =
  "tw:flex tw:w-full tw:min-h-11 tw:cursor-pointer tw:items-center tw:gap-2.5 tw:rounded-lg tw:border tw:border-transparent tw:px-2.5 tw:py-1.5 tw:text-left tw:text-sm tw:hover:bg-muted tw:focus-visible:outline-2 tw:focus-visible:outline-ring";
const ROW_ACTIVE = "tw:border-border tw:bg-muted";
// Phone: a count line over two rows of equal-width actions, so five actions never
// crowd one line. From `sm` up the bar is the single flexible row it always was.
const STICKY_BAR =
  "tw:sticky tw:bottom-0 tw:z-10 tw:mt-3 tw:grid tw:grid-cols-6 tw:items-center tw:gap-1.5 tw:rounded-xl tw:border tw:border-border tw:bg-popover tw:p-2 tw:shadow-2 tw:sm:flex tw:sm:flex-wrap";

type TypeFilter = "all" | "video" | "subtitle" | "image";

type Overlay =
  | { kind: "transfer"; mode: "copy" | "move"; paths: string[] }
  | { kind: "rename"; path: string; name: string }
  | { kind: "recognize"; paths: string[]; singleVideo: boolean }
  | { kind: "delete"; files: { path: string; name: string; sizeBytes?: number }[] }
  | { kind: "managed"; path: string }
  | { kind: "roots"; prefill?: string };

export type FileManagerDialogProps = {
  open: boolean;
  /** The current folder's absolute path; unset opens the first library directory. */
  path?: string;
  /** Changes the folder (replacing only the current history entry); undefined clears back to the first library directory. */
  onNavigate: (path: string | undefined) => void;
  onOpenAnime: (managedAnimeID: string) => void;
  onOpenRecord: (acquisitionID: string) => void;
  onClose: () => void;
};

export function FileManagerDialog({ open, path, onNavigate, onOpenAnime, onOpenRecord, onClose }: FileManagerDialogProps) {
  const roots = useQuery({ queryKey: fileManagerKeys.roots, queryFn: listFileManagerRoots, enabled: open });
  const rootList = useMemo(() => roots.data?.roots ?? [], [roots.data]);
  // Without a carried path the dialog opens at the first usable library directory.
  const current = path ?? rootList.find((root) => root.available)?.path;

  const [filter, setFilter] = useState<TypeFilter>("all");
  const [hideManaged, setHideManaged] = useState(false);
  const [selection, setSelection] = useState<ReadonlySet<string>>(new Set());
  const [overlay, setOverlay] = useState<Overlay | null>(null);

  const listing = useInfiniteQuery({
    queryKey: fileManagerKeys.entries(current ?? ""),
    queryFn: ({ pageParam }) => listFileManagerEntries({ path: current ?? "", cursor: pageParam, limit: PAGE_LIMIT }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
    enabled: open && current !== undefined,
  });
  const entries = useMemo(() => listing.data?.pages.flatMap((page) => page.entries) ?? [], [listing.data]);
  const truncated = listing.data?.pages.some((page) => page.truncated) ?? false;
  // A path outside every root is its own state, not a load failure: the fix is
  // adding the directory, not retrying.
  const outsideRoots = listing.error instanceof ApiError && listing.error.code === OUTSIDE_ROOTS;

  // Switching folder or filter empties the selection; a refresh that dropped files prunes it.
  useEffect(() => {
    setSelection(new Set());
  }, [current, filter, hideManaged]);
  const entryPaths = useMemo(() => new Set(entries.map((entry) => entry.path)), [entries]);
  useEffect(() => {
    setSelection((previous) => {
      const next = new Set([...previous].filter((candidate) => entryPaths.has(candidate)));
      return next.size === previous.size ? previous : next;
    });
  }, [entryPaths]);

  const folders = entries.filter((entry) => entry.kind === "directory");
  const visibleFiles = entries.filter(
    (entry) =>
      entry.kind !== "directory" &&
      (filter === "all" || bucketOf(entry) === filter) &&
      (!hideManaged || !entry.locked),
  );

  const openFolder = (next: string) => onNavigate(next);
  const toggle = (path: string) =>
    setSelection((previous) => {
      const next = new Set(previous);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  const setAll = (paths: string[], on: boolean) =>
    setSelection((previous) => {
      const next = new Set(previous);
      for (const path of paths) {
        if (on) next.add(path);
        else next.delete(path);
      }
      return next;
    });

  // Removing the browsed root leaves the dialog at the next usable root
  // (prototype: 移除当前根会自动切到下一个，全部移除就回到空状态).
  function handleRootRemoved(removed: string) {
    if (current !== undefined && (current === removed || current.startsWith(`${removed}/`))) {
      const next = rootList.find((root) => root.available && root.path !== removed);
      onNavigate(next ? next.path : undefined);
    }
  }

  const selectedPaths = visibleFiles.filter((entry) => selection.has(entry.path)).map((entry) => entry.path);
  const singleRename = selectedPaths.length === 1 ? visibleFiles.find((entry) => entry.path === selectedPaths[0]) : undefined;

  function reportBatch(outcome: BatchOutcome) {
    if (outcome.failed > 0) {
      toast.error(t("fileManager.batch.doneWithFailed", { n: outcome.submitted, failed: outcome.failed }), {
        description: outcome.firstFailure ?? undefined,
      });
    } else if (outcome.skipped > 0) {
      toast.success(t("fileManager.batch.doneWithSkipped", { n: outcome.submitted, skipped: outcome.skipped }));
    } else if (outcome.submitted > 0) {
      toast.success(t("fileManager.batch.done", { n: outcome.submitted }));
    }
    if (outcome.submitted > 0) setSelection(new Set());
    setOverlay(null);
  }

  function reportRecognition(result: FileManagerRecognitionResult, skippedConflicts: number) {
    const done = result.renamed + result.moved;
    if (skippedConflicts > 0) toast.success(t("fileManager.batch.doneWithSkipped", { n: done, skipped: skippedConflicts }));
    else toast.success(t("fileManager.batch.done", { n: done }));
    if (done > 0) setSelection(new Set());
    setOverlay(null);
  }

  return (
    <>
      <Dialog open={open} onRequestClose={onClose} tier="L" title={t("fileManager.title")} bodyLayout="fill">
        <section aria-label={t("fileManager.listAria")} className={cn(PANE, selectedPaths.length > 0 ? "tw:pb-0" : "tw:pb-4")}>
          <BrowserBar
            roots={roots}
            path={current}
            onOpenFolder={openFolder}
            onManageRoots={(prefill) => setOverlay({ kind: "roots", prefill })}
            refreshing={listing.isRefetching}
            onRefresh={() => void listing.refetch()}
          />
          {roots.error ? (
            <StateBlock
              kind="error"
              title={toUserMessage(roots.error, t("fileManager.roots.loadFailed"))}
              action={
                <Button size="sm" variant="link" onClick={() => void roots.refetch()}>
                  {t("fileManager.retry")}
                </Button>
              }
            />
          ) : !roots.data ? (
            <StateBlock kind="busy" title={t("fileManager.roots.loading")} />
          ) : rootList.length === 0 ? (
            <StateBlock
              kind="empty"
              title={t("fileManager.noRoots.title")}
              description={t("fileManager.noRoots.body")}
              action={
                <Button size="sm" variant="secondary" onClick={() => setOverlay({ kind: "roots" })}>
                  {t("fileManager.noRoots.add")}
                </Button>
              }
            />
          ) : outsideRoots ? (
            <StateBlock
              kind="restricted"
              title={t("fileManager.outside.title")}
              description={t("fileManager.outside.body")}
              action={
                <Button size="sm" variant="secondary" onClick={() => setOverlay({ kind: "roots", prefill: nearestDirectory(current) })}>
                  {t("fileManager.outside.add")}
                </Button>
              }
            />
          ) : (
            <>
              <Toolbar
                filter={filter}
                onFilter={setFilter}
                hideManaged={hideManaged}
                onHideManaged={setHideManaged}
                visibleFiles={visibleFiles}
                selection={selection}
                onSelectAll={setAll}
              />
              {listing.error ? (
                <StateBlock
                  kind="error"
                  title={toUserMessage(listing.error, t("fileManager.listLoadFailed"))}
                  action={
                    <Button size="sm" variant="link" onClick={() => void listing.refetch()}>
                      {t("fileManager.retry")}
                    </Button>
                  }
                />
              ) : !listing.data ? (
                <StateBlock kind="busy" title={t("fileManager.listLoading")} />
              ) : entries.length === 0 ? (
                <StateBlock kind="empty" title={t("fileManager.emptyFolder")} />
              ) : (
                <>
                  {truncated ? <p className={META}>{t("fileManager.truncated", { n: 10000 })}</p> : null}
                  <ul aria-label={t("fileManager.listAria")} className={LIST}>
                    {folders.map((entry) => (
                      <li key={entry.path}>
                        <FolderRow entry={entry} onOpenFolder={openFolder} />
                      </li>
                    ))}
                    {visibleFiles.map((entry) => (
                      <li key={entry.path}>
                        <FileRow entry={entry} selected={selection.has(entry.path)} onToggle={toggle} onOpenManaged={(path) => setOverlay({ kind: "managed", path })} />
                      </li>
                    ))}
                  </ul>
                  {visibleFiles.length === 0 ? <p className={META}>{t("fileManager.browse.noMatchingFiles")}</p> : null}
                  {listing.hasNextPage ? (
                    <Button size="sm" className="tw:justify-self-center" loading={listing.isFetchingNextPage} onClick={() => void listing.fetchNextPage()}>
                      {t("fileManager.loadMore")}
                    </Button>
                  ) : null}
                </>
              )}
            </>
          )}
          {selectedPaths.length > 0 ? (
            <div className="tw:sticky tw:bottom-0 tw:z-10 tw:-mx-4 tw:bg-popover tw:px-4 tw:pb-4 tw:pt-3">
              <div className={STICKY_BAR}>
                <span className="tw:col-span-5 tw:order-1 tw:mr-auto tw:text-xs tw:font-medium tw:sm:order-none">
                  {t("fileManager.selection.count", { n: selectedPaths.length })}
                </span>
                <Button size="sm" variant="secondary" className="tw:col-span-2 tw:order-3 tw:w-full tw:sm:order-none tw:sm:w-auto" onClick={() => setOverlay({ kind: "transfer", mode: "copy", paths: selectedPaths })}>
                  {t("fileManager.selection.copy")}
                </Button>
                <Button size="sm" variant="secondary" className="tw:col-span-2 tw:order-3 tw:w-full tw:sm:order-none tw:sm:w-auto" onClick={() => setOverlay({ kind: "transfer", mode: "move", paths: selectedPaths })}>
                  {t("fileManager.selection.move")}
                </Button>
                <span
                  className="tw:col-span-2 tw:order-3 tw:flex tw:sm:order-none"
                  title={singleRename ? undefined : t("fileManager.selection.renameSingleHint")}
                >
                  <Button
                    size="sm"
                    variant="secondary"
                    className="tw:w-full tw:sm:w-auto"
                    disabled={!singleRename}
                    onClick={() => singleRename && setOverlay({ kind: "rename", path: singleRename.path, name: singleRename.name })}
                  >
                    {t("fileManager.selection.rename")}
                  </Button>
                </span>
                <Button
                  size="sm"
                  variant="secondary"
                  className="tw:col-span-3 tw:order-4 tw:w-full tw:sm:order-none tw:sm:w-auto"
                  onClick={() =>
                    setOverlay({
                      kind: "recognize",
                      paths: selectedPaths,
                      singleVideo: visibleFiles.filter((entry) => selection.has(entry.path) && bucketOf(entry) === "video").length === 1,
                    })
                  }
                >
                  {t("fileManager.selection.recognize")}
                </Button>
                <Button
                  size="sm"
                  variant="danger"
                  className="tw:col-span-3 tw:order-4 tw:w-full tw:sm:order-none tw:sm:w-auto"
                  onClick={() => setOverlay({ kind: "delete", files: selectedPaths.map((path) => snapshotOf(entries, path)) })}
                >
                  {t("fileManager.selection.delete")}
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={t("fileManager.selection.clear")}
                  title={t("fileManager.selection.clear")}
                  className="tw:col-span-1 tw:order-2 tw:justify-self-end tw:sm:order-none"
                  onClick={() => setSelection(new Set())}
                >
                  <X className="tw:size-4" aria-hidden="true" />
                </Button>
              </div>
            </div>
          ) : null}
        </section>
      </Dialog>
      {overlay?.kind === "transfer" ? (
        <TransferDialog
          mode={overlay.mode}
          sourceDir={current ?? ""}
          paths={overlay.paths}
          roots={rootList}
          onClose={() => setOverlay(null)}
          onFinished={reportBatch}
        />
      ) : null}
      {overlay?.kind === "rename" ? (
        <RenameDialog
          path={overlay.path}
          name={overlay.name}
          directory={current ?? ""}
          siblingNames={entries.filter((entry) => entry.kind !== "directory").map((entry) => entry.name)}
          onClose={() => setOverlay(null)}
          onFinished={reportBatch}
        />
      ) : null}
      {overlay?.kind === "recognize" ? (
        <RecognitionDialog paths={overlay.paths} singleVideo={overlay.singleVideo} onClose={() => setOverlay(null)} onFinished={reportRecognition} />
      ) : null}
      {overlay?.kind === "delete" ? (
        <DeleteDialog files={overlay.files} onClose={() => setOverlay(null)} onFinished={reportBatch} />
      ) : null}
      {overlay?.kind === "managed" ? (
        <ManagedFileDialog path={overlay.path} onOpenAnime={onOpenAnime} onOpenRecord={onOpenRecord} onClose={() => setOverlay(null)} />
      ) : null}
      {overlay?.kind === "roots" ? (
        <RootsDialog roots={rootList} activeRootID={activeRootOf(rootList, current)?.id} prefill={overlay.prefill} onRemoved={handleRootRemoved} onClose={() => setOverlay(null)} />
      ) : null}
    </>
  );
}

const IMAGE_EXTENSION = /\.(jpe?g|png|webp|gif|bmp|avif|heic)$/i;

function bucketOf(entry: Pick<FileManagerEntry, "name" | "file_kind" | "role">): "video" | "subtitle" | "image" | "other" {
  // file_kind classifies every file by its extension; role only exists where
  // owner records name the file, so it cannot drive a browse filter. Images are
  // a browse-only bucket: the backend kind list stays video/subtitle/other.
  const kind = entry.file_kind ?? entry.role;
  if (kind === "video") return "video";
  if (kind === "subtitle") return "subtitle";
  if (IMAGE_EXTENSION.test(entry.name)) return "image";
  return "other";
}

/** The closest ancestor of an outside-roots path to offer as the new root; adding still validates on the server. */
function nearestDirectory(path: string | undefined): string | undefined {
  return path === undefined || path === "/" ? path : parentOf(path);
}

/** Name and size for a path in the loaded listing; the delete dialog lists what it will remove. */
function snapshotOf(entries: readonly FileManagerEntry[], path: string): { path: string; name: string; sizeBytes?: number } {
  const entry = entries.find((candidate) => candidate.path === path);
  return { path, name: entry?.name ?? baseName(path), sizeBytes: entry?.size_bytes };
}

function Toolbar({
  filter,
  onFilter,
  hideManaged,
  onHideManaged,
  visibleFiles,
  selection,
  onSelectAll,
}: {
  filter: TypeFilter;
  onFilter: (next: TypeFilter) => void;
  hideManaged: boolean;
  onHideManaged: (next: boolean) => void;
  visibleFiles: readonly FileManagerEntry[];
  selection: ReadonlySet<string>;
  onSelectAll: (paths: string[], on: boolean) => void;
}) {
  const filterOptions: ChoiceOption<TypeFilter>[] = [
    { value: "all", label: t("fileManager.browse.filter.all") },
    { value: "video", label: t("fileManager.browse.filter.video") },
    { value: "subtitle", label: t("fileManager.browse.filter.subtitle") },
    { value: "image", label: t("fileManager.browse.filter.image") },
  ];
  const selectable = visibleFiles.filter((entry) => !entry.locked);
  const selectedHere = selectable.filter((entry) => selection.has(entry.path)).length;
  const allHere = selectable.length > 0 && selectedHere === selectable.length;
  return (
    // Phone: two calm rows — select-all and the count, then the type filter and the
    // managed switch. From `sm` up the same controls sit on one line.
    <div className="tw:mb-2 tw:grid tw:grid-cols-[minmax(0,1fr)_auto] tw:items-center tw:gap-x-3 tw:gap-y-2 tw:sm:flex tw:sm:flex-wrap">
      {selectable.length > 0 ? (
        <Checkbox
          checked={allHere}
          indeterminate={selectedHere > 0 && !allHere}
          onCheckedChange={(next) => onSelectAll(selectable.map((entry) => entry.path), next)}
          size="sm"
          className="tw:order-1 tw:sm:order-none"
        >
          {t("fileManager.browse.selectAll")}
        </Checkbox>
      ) : null}
      <Segmented
        value={filter}
        onValueChange={onFilter}
        options={filterOptions}
        size="compact"
        aria-label={t("fileManager.browse.filterAria")}
        className="tw:order-3 tw:sm:order-none"
      />
      <div className="tw:order-4 tw:flex tw:items-center tw:gap-2 tw:justify-self-end tw:sm:order-none">
        <Switch aria-label={t("fileManager.browse.hideManaged")} checked={hideManaged} onCheckedChange={onHideManaged} />
        <span className="tw:text-xs tw:text-muted-foreground">{t("fileManager.browse.hideManaged")}</span>
      </div>
      <span className={cn(META, "tw:order-2 tw:ml-auto tw:justify-self-end tw:sm:order-none")}>{t("fileManager.browse.count", { n: visibleFiles.length })}</span>
    </div>
  );
}

// The browse shell: root selector, breadcrumb, read-only marker and refresh on one
// row. The path field stays behind its own button — the prototype navigates with the
// root selector and the breadcrumb, and only the owner's own path needs an input.
function BrowserBar({
  roots,
  path,
  onOpenFolder,
  onManageRoots,
  refreshing,
  onRefresh,
}: {
  roots: ReturnType<typeof useQuery<FileManagerRootList>>;
  path: string | undefined;
  onOpenFolder: (path: string) => void;
  onManageRoots: (prefill?: string) => void;
  refreshing: boolean;
  onRefresh: () => void;
}) {
  const [pathOpen, setPathOpen] = useState(false);
  const [draft, setDraft] = useState(path);
  const [invalid, setInvalid] = useState(false);
  useEffect(() => {
    setDraft(path);
    setInvalid(false);
  }, [path]);

  const list = roots.data?.roots ?? [];
  const active = activeRootOf(list, path);
  const items: MenuEntry[] =
    list.length === 0
      ? [{ key: "no-roots", disabled: true, label: t("fileManager.roots.noneMenu"), onSelect: () => {} }]
      : list.map((root) => ({
          key: root.id,
          icon: <Folder className="tw:size-4" aria-hidden="true" />,
          disabled: !root.available,
          label: (
            <span className="tw:flex tw:min-w-0 tw:flex-1 tw:items-baseline tw:gap-2">
              <span className="tw:truncate">{root.label}</span>
              <span className="tw:truncate tw:text-xs tw:text-muted-foreground">{root.path}</span>
              {!root.available ? <span className="tw:flex-none tw:text-xs tw:text-muted-foreground">{rootUnavailableLabel(root)}</span> : null}
              {active && root.id === active.id ? <Check className="tw:ml-auto tw:size-3.5 tw:flex-none tw:text-primary" aria-hidden="true" /> : null}
            </span>
          ),
          onSelect: () => onOpenFolder(root.path),
        }));
  items.push(
    { type: "divider", key: "roots-divider" },
    { key: "manage-roots", label: t("fileManager.roots.manage"), onSelect: () => onManageRoots() },
  );

  const go = () => {
    const next = normalizePath(draft ?? "");
    if (next === undefined) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    setPathOpen(false);
    onOpenFolder(next);
  };

  return (
    <div className="tw:mb-3 tw:grid tw:gap-2">
      <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
        <DropdownMenu
          align="start"
          items={items}
          trigger={
            <Button
              variant="secondary"
              size="sm"
              className="tw:min-w-0 tw:max-w-none tw:sm:max-w-[min(360px,100%)]"
              aria-label={t("fileManager.roots.selector")}
            >
              {active ? (
                <Folder className="tw:size-4 tw:flex-none tw:text-muted-foreground" aria-hidden="true" />
              ) : (
                <HardDrive className="tw:size-4 tw:flex-none tw:text-muted-foreground" aria-hidden="true" />
              )}
              <span className="tw:truncate">{active ? active.label : t("fileManager.roots.none")}</span>
              {/* Phone drops the path here: the breadcrumb line below carries the location. */}
              <span className="tw:hidden tw:truncate tw:text-xs tw:font-normal tw:text-muted-foreground tw:sm:inline">
                {active ? active.path : t("fileManager.roots.noneHint")}
              </span>
              <ChevronDown className="tw:size-3.5 tw:flex-none tw:text-muted-foreground" aria-hidden="true" />
            </Button>
          }
        />
        {active ? <Breadcrumb path={path ?? active.path} base={active} onOpenFolder={onOpenFolder} /> : null}
        <div className="tw:order-2 tw:ml-auto tw:flex tw:items-center tw:gap-1 tw:sm:order-3">
          <span className="tw:hidden tw:rounded-full tw:border tw:border-dashed tw:border-border tw:px-2.5 tw:py-0.5 tw:text-badge tw:text-muted-foreground tw:sm:inline">
            {t("fileManager.browse.readOnly")}
          </span>
          <Button
            size="sm"
            variant="ghost"
            aria-label={t("fileManager.pathBar.toggle")}
            title={t("fileManager.pathBar.toggle")}
            aria-expanded={pathOpen}
            onClick={() => setPathOpen((open) => !open)}
          >
            <TextCursorInput className="tw:size-4" aria-hidden="true" />
          </Button>
          <Button
            size="sm"
            variant="ghost"
            aria-label={t("fileManager.refresh")}
            title={t("fileManager.refresh")}
            loading={refreshing}
            onClick={onRefresh}
          >
            <RefreshCw className="tw:size-4" aria-hidden="true" />
          </Button>
        </div>
      </div>
      {pathOpen ? (
        <form
          className="tw:flex tw:items-center tw:gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            go();
          }}
        >
          <Input
            aria-label={t("fileManager.pathBar.label")}
            value={draft ?? ""}
            onChange={(event) => {
              setDraft(event.target.value);
              setInvalid(false);
            }}
            aria-invalid={invalid ? true : undefined}
            autoComplete="off"
            spellCheck={false}
            autoFocus
          />
          <Button size="sm" variant="secondary" type="submit">
            {t("fileManager.pathBar.go")}
          </Button>
        </form>
      ) : null}
      {invalid ? <p className={META}>{t("fileManager.pathBar.absolute")}</p> : null}
    </div>
  );
}

function rootUnavailableLabel(root: FileManagerRoot): string {
  const reason = root.unavailable_reason;
  return (reason ? tryT(`fileManager.roots.unavailable.${reason}`) : undefined) ?? root.path;
}

/** Path from the library directory the selector names down to the current folder. */
function Breadcrumb({ path, base, onOpenFolder }: { path: string; base: FileManagerRoot; onOpenFolder: (path: string) => void }) {
  const basePath = base.path;
  const segments = segmentsOf(path.slice(basePath.length));
  const crumbs = [
    { label: base.label, target: basePath },
    ...segments.map((segment, index) => ({
      label: segment,
      target: `${basePath}/${segments.slice(0, index + 1).join("/")}`,
    })),
  ];
  return (
    <nav
      aria-label={t("fileManager.breadcrumbAria")}
      className={cn(
        "tw:order-3 tw:flex tw:w-full tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-x-0.5 tw:text-xs tw:sm:order-2 tw:sm:w-auto",
        // A phone whose only crumb is the root has nothing to jump to, and the
        // selector above already names it: leave the line out entirely.
        crumbs.length === 1 && "tw:hidden tw:sm:flex",
      )}
    >
      {crumbs.map((crumb, index) => {
        const last = index === crumbs.length - 1;
        // On a phone the selector above already names the root, so the crumb
        // that repeats it steps aside (and takes its leading "/" with it).
        const repeatsRoot = index === 0 && crumbs.length > 1;
        const separatorFollowsRoot = index === 1 && crumbs.length > 1;
        return (
          <span key={`${crumb.target}-${index}`} className={cn("tw:flex tw:min-w-0 tw:items-center", repeatsRoot && "tw:hidden tw:sm:flex")}>
            {index > 0 ? (
              <span aria-hidden="true" className={cn("tw:flex-none tw:px-0.5 tw:text-muted-foreground", separatorFollowsRoot && "tw:hidden tw:sm:inline")}>
                /
              </span>
            ) : null}
            {last ? (
              <span aria-current="location" className="tw:break-words tw:px-1 tw:py-0.5 tw:font-medium">
                {crumb.label}
              </span>
            ) : (
              <button
                type="button"
                className="tw:min-w-0 tw:max-w-full tw:cursor-pointer tw:truncate tw:rounded-sm tw:border-0 tw:bg-transparent tw:px-1 tw:py-0.5 tw:font-normal tw:text-muted-foreground tw:hover:bg-muted tw:hover:text-foreground tw:focus-visible:outline-2 tw:focus-visible:outline-ring"
                onClick={() => onOpenFolder(crumb.target)}
              >
                {crumb.label}
              </button>
            )}
          </span>
        );
      })}
    </nav>
  );
}

function EntryIcon({ entry }: { entry: Pick<FileManagerEntry, "kind" | "role" | "file_kind"> }) {
  const className = "tw:size-4 tw:flex-none tw:text-muted-foreground";
  if (entry.kind === "directory") return <Folder className={className} aria-hidden="true" />;
  if (entry.kind === "symlink") return <Link2 className={className} aria-hidden="true" />;
  const fileKind = entry.file_kind ?? entry.role;
  if (fileKind === "video") return <FileVideo className={className} aria-hidden="true" />;
  if (fileKind === "subtitle") return <FileText className={className} aria-hidden="true" />;
  return <File className={className} aria-hidden="true" />;
}

function rowMeta(entry: FileManagerEntry): string {
  const meta: string[] = [];
  if (entry.kind !== "directory" && entry.size_bytes !== undefined) meta.push(formatBytes(entry.size_bytes));
  if (entry.modified_at) meta.push(formatDateTime(entry.modified_at));
  return meta.join(" · ");
}

function FolderRow({ entry, onOpenFolder }: { entry: FileManagerEntry; onOpenFolder: (path: string) => void }) {
  return (
    <button type="button" className={ROW} onClick={() => onOpenFolder(entry.path)}>
      <EntryIcon entry={entry} />
      <span className="tw:grid tw:min-w-0 tw:flex-1 tw:gap-0.5">
        <span className="tw:break-words">{entry.name}</span>
      </span>
      <ChevronRight className="tw:size-4 tw:flex-none tw:text-muted-foreground" aria-hidden="true" />
    </button>
  );
}

// A locked row is read-only: grayed, carrying the lock instead of a checkbox; clicking opens the managed detail.
function FileRow({
  entry,
  selected,
  onToggle,
  onOpenManaged,
}: {
  entry: FileManagerEntry;
  selected: boolean;
  onToggle: (path: string) => void;
  onOpenManaged: (path: string) => void;
}) {
  const meta = rowMeta(entry);
  if (entry.locked) {
    return (
      <button type="button" className={cn(ROW, "tw:opacity-60")} title={t("fileManager.locked")} onClick={() => onOpenManaged(entry.path)}>
        <EntryIcon entry={entry} />
        <span className="tw:grid tw:min-w-0 tw:flex-1 tw:gap-0.5">
          <span className="tw:break-words">{entry.name}</span>
          {meta ? <span className={META}>{meta}</span> : null}
        </span>
        <Lock className="tw:size-4 tw:flex-none tw:text-warning" aria-label={t("fileManager.locked")} />
      </button>
    );
  }
  return (
    <div className={cn(ROW, selected && ROW_ACTIVE)}>
      <Checkbox
        checked={selected}
        onCheckedChange={() => onToggle(entry.path)}
        size="sm"
        aria-label={t("fileManager.browse.selectFile", { name: entry.name })}
      />
      <button
        type="button"
        className="tw:flex tw:min-w-0 tw:flex-1 tw:cursor-pointer tw:items-center tw:gap-2.5 tw:py-1.5 tw:text-left tw:text-sm tw:focus-visible:outline-2 tw:focus-visible:outline-ring"
        aria-pressed={selected ? "true" : undefined}
        onClick={() => onToggle(entry.path)}
      >
        <EntryIcon entry={entry} />
        <span className="tw:grid tw:min-w-0 tw:flex-1 tw:gap-0.5">
          <span className="tw:break-words">{entry.name}</span>
          {meta ? <span className={META}>{meta}</span> : null}
        </span>
      </button>
    </div>
  );
}
