import { useId, useMemo, useState, type ReactNode } from "react";
import { ChevronRight, Folder } from "lucide-react";
import type { AnimeDraft, RSSListManifestFile } from "../../lib/api/client";
import { formatBytes } from "../../lib/labels";
import { t } from "../../lib/i18n";
import { ReasonText } from "../../ui/ReasonText";
import { Button } from "../../ui";
import { Checkbox, Dialog } from "../../ui/base";
import { cn } from "../../lib/utils";
import { episodesRangeLabel } from "../rss-list/model";
import {
  buildPickerTree,
  directoryState,
  effectiveFileSelection,
  fileCheckable,
  folderSummary,
  initialExpandedFolders,
  sameFileChoice,
  selectedVideoCount,
  toggleDirectorySelection,
  toggleFileSelection,
  type FolderSummary,
  type PickerFolder,
} from "./fileSelection";

// The one-shot file picker: a secondary dialog over one seed's torrent, shown as a folder tree.
//   Each row is the server's decision (selectable, reason, a video's recognition, the video a
//   subtitle follows); the dialog renders it and keeps the user's ticks as local state. Nothing
//   here re-computes a rule and nothing here makes a request: confirming hands the new set to the
//   caller, which reads the preview once.

type UnclassifiedRoute = NonNullable<AnimeDraft["business"]>["unclassified_route"];

/** What happens to a checked video: its episode, the movie main video, or the unknown-file route. */
function recognitionText(file: RSSListManifestFile, route: UnclassifiedRoute | undefined): string | undefined {
  switch (file.recognition) {
    case "episode":
      if (!file.episodes?.length) return undefined;
      return file.episodes.length === 1 ? t("rss.episode", { n: file.episodes[0]! }) : t("rss.episodes", { range: episodesRangeLabel(file.episodes) });
    case "movie":
      return t("oneshot.fileMovie");
    case "unrecognized":
      return route === "route" ? t("oneshot.fileUnrecognizedRoute") : t("oneshot.fileUnrecognizedSkip");
    default:
      return undefined;
  }
}

/** Why a file is not processed: the server's reason, or a subtitle left with no video to follow. */
function skippedReason(file: RSSListManifestFile): ReactNode {
  return <ReasonText code={file.selectable ? "subtitle_binding_missing" : file.reason_code} />;
}

function nameOf(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1);
}

/** A folder row's recursive summary: "15 个视频 · 已选 12 · 字幕 24", or that nothing below it is processed. */
function summaryText(summary: FolderSummary): string {
  if (summary.videos === 0) return t("oneshot.filePickerSummaryNone");
  const parts = [t("oneshot.filePickerSummaryVideos", { n: summary.videos }), t("oneshot.filePickerSummarySelected", { n: summary.selectedVideos })];
  if (summary.subtitles > 0) {
    parts.push(
      summary.selectedSubtitles === summary.subtitles
        ? t("oneshot.filePickerSummarySubtitles", { n: summary.subtitles })
        : t("oneshot.filePickerSummarySubtitlesPartial", { selected: summary.selectedSubtitles, total: summary.subtitles }),
    );
  }
  return parts.join(" · ");
}

/** A tap margin around a row checkbox (larger on phones); a tap in the margin counts as tapping it. */
function TapTarget({ onTap, children }: { onTap?: () => void; children: ReactNode }) {
  return (
    <span
      className="tw:-m-1 tw:flex tw:flex-none tw:p-1 tw:phone:-m-2.5 tw:phone:p-2.5"
      onClick={(event) => {
        if (event.target === event.currentTarget) onTap?.();
      }}
    >
      {children}
    </span>
  );
}

const foldButton =
  "tw:grid tw:size-6 tw:flex-none tw:cursor-pointer tw:place-items-center tw:rounded-sm tw:border-0 tw:bg-transparent tw:p-0 tw:text-muted-foreground tw:hover:text-foreground tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring tw:phone:size-8";

function Chevron({ open }: { open: boolean }) {
  return (
    <ChevronRight
      aria-hidden="true"
      className={cn("icon-sm tw:flex-none tw:transition-transform tw:duration-200 tw:ease-out tw:motion-reduce:transition-none", open && "tw:rotate-90")}
    />
  );
}

type Context = {
  files: readonly RSSListManifestFile[];
  selection: ReadonlySet<string>;
  route: UnclassifiedRoute | undefined;
  expanded: ReadonlySet<string>;
  unfoldedOthers: ReadonlySet<string>;
  onToggle: (path: string) => void;
  onToggleDirectory: (path: string) => void;
  onFold: (path: string) => void;
  onFoldOthers: (path: string) => void;
};

function FileLine({ file, caption, context }: { file: RSSListManifestFile; caption: ReactNode; context: Context }) {
  const checkable = fileCheckable(file, context.selection);
  const toggle = checkable ? () => context.onToggle(file.source_path) : undefined;
  return (
    <div className="tw:flex tw:items-start tw:gap-2 tw:py-0.5">
      <TapTarget onTap={toggle}>
        <Checkbox
          size="sm"
          checked={context.selection.has(file.source_path)}
          disabled={!checkable}
          aria-label={file.source_path}
          onCheckedChange={() => toggle?.()}
        />
      </TapTarget>
      <span className="tw:grid tw:min-w-0 tw:flex-1">
        <span
          className={cn("text-caption break-anywhere", checkable ? "tw:cursor-pointer" : "tw:text-muted-foreground")}
          title={file.source_path}
          onClick={toggle}
        >
          {nameOf(file.source_path)}
          <span className="tw:text-muted-foreground"> · {formatBytes(file.size_bytes)}</span>
        </span>
        {caption && <span className="text-caption tw:text-muted-foreground">{caption}</span>}
      </span>
    </div>
  );
}

/** A level's contents: its videos (each with the subtitles that follow it), one line for the files not processed, then its subfolders. */
function Level({ folder, context }: { folder: PickerFolder; context: Context }) {
  const othersId = useId();
  const othersOpen = context.unfoldedOthers.has(folder.path);
  return (
    <ul className="tw:m-0 tw:grid tw:list-none tw:gap-0.5 tw:p-0">
      {folder.videos.map(({ file, subtitles }) => (
        <li key={file.source_path}>
          <FileLine file={file} caption={recognitionText(file, context.route)} context={context} />
          {subtitles.length > 0 && (
            <ul className="tw:m-0 tw:ml-6 tw:grid tw:list-none tw:gap-0.5 tw:p-0 tw:phone:ml-5">
              {subtitles.map((subtitle) => (
                <li key={subtitle.source_path}>
                  <FileLine
                    file={subtitle}
                    caption={fileCheckable(subtitle, context.selection) ? t("oneshot.filePickerFollowsVideo") : t("oneshot.fileSubtitleNeedsVideo")}
                    context={context}
                  />
                </li>
              ))}
            </ul>
          )}
        </li>
      ))}
      {folder.skipped.length > 0 && (
        <li>
          <button
            type="button"
            aria-expanded={othersOpen}
            aria-controls={othersOpen ? othersId : undefined}
            onClick={() => context.onFoldOthers(folder.path)}
            className="text-caption tw:inline-flex tw:cursor-pointer tw:items-center tw:gap-1 tw:rounded-sm tw:border-0 tw:bg-transparent tw:p-0 tw:py-0.5 tw:text-muted-foreground tw:hover:text-foreground tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring"
          >
            <Chevron open={othersOpen} />
            {t("oneshot.filePickerOthers", { n: folder.skipped.length })}
          </button>
          {othersOpen && (
            <ul id={othersId} className="tw:m-0 tw:ml-6 tw:grid tw:list-none tw:gap-0.5 tw:p-0 tw:phone:ml-5">
              {folder.skipped.map((file) => (
                <li key={file.source_path}>
                  <FileLine file={file} caption={skippedReason(file)} context={context} />
                </li>
              ))}
            </ul>
          )}
        </li>
      )}
      {folder.folders.map((child) => (
        <FolderLine key={child.path} folder={child} context={context} />
      ))}
    </ul>
  );
}

function FolderLine({ folder, context }: { folder: PickerFolder; context: Context }) {
  const childrenId = useId();
  const open = context.expanded.has(folder.path);
  const state = directoryState(context.files, context.selection, folder.path);
  const toggle = state === "empty" ? undefined : () => context.onToggleDirectory(folder.path);
  const summary = useMemo(() => folderSummary(folder, context.selection), [folder, context.selection]);
  return (
    <li className="tw:grid tw:gap-0.5">
      <div className="tw:flex tw:items-start tw:gap-1.5 tw:py-0.5">
        <button
          type="button"
          aria-expanded={open}
          aria-controls={open ? childrenId : undefined}
          aria-label={t("oneshot.filePickerFold", { name: folder.path })}
          onClick={() => context.onFold(folder.path)}
          className={foldButton}
        >
          <Chevron open={open} />
        </button>
        <TapTarget onTap={toggle}>
          <Checkbox
            size="sm"
            checked={state === "checked"}
            indeterminate={state === "mixed"}
            disabled={state === "empty"}
            aria-label={t("oneshot.filePickerFolder", { name: folder.path })}
            onCheckedChange={() => toggle?.()}
          />
        </TapTarget>
        <span className="tw:grid tw:min-w-0 tw:flex-1 tw:cursor-pointer" onClick={() => context.onFold(folder.path)}>
          <span className="text-caption break-anywhere tw:flex tw:items-center tw:gap-1.5" title={folder.path}>
            <Folder aria-hidden="true" className="icon-sm tw:flex-none tw:text-muted-foreground" />
            {folder.name}
          </span>
          <span className="text-caption tw:text-muted-foreground">{summaryText(summary)}</span>
        </span>
      </div>
      {open && (
        <div id={childrenId} className="tw:ml-3 tw:border-l tw:border-border tw:pl-3">
          <Level folder={folder} context={context} />
        </div>
      )}
    </li>
  );
}

function toggled(set: ReadonlySet<string>, key: string): Set<string> {
  const next = new Set(set);
  if (!next.delete(key)) next.add(key);
  return next;
}

/**
 * The picker dialog of one seed. Everything inside is local: ticks, folding and restoring the
 * defaults make no request. `onApply` is called once, on a confirm that changed the set, with the
 * new manual choice (undefined = the default files); a confirm without a change only closes.
 */
export function FilePickerDialog({
  seedTitle,
  files,
  choice,
  route,
  onApply,
  onClose,
}: {
  seedTitle: string;
  files: readonly RSSListManifestFile[];
  /** The choice in force: undefined = the default files. */
  choice: string[] | undefined;
  route: UnclassifiedRoute | undefined;
  onApply: (next: string[] | undefined) => void;
  onClose: () => void;
}) {
  const [draft, setDraft] = useState<string[] | undefined>(choice);
  // Folding starts from the data once, when the dialog opens, and is the user's from then on.
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => initialExpandedFolders(files, effectiveFileSelection(files, choice)));
  const [unfoldedOthers, setUnfoldedOthers] = useState<ReadonlySet<string>>(new Set());
  const tree = useMemo(() => buildPickerTree(files), [files]);
  const selection = useMemo(() => effectiveFileSelection(files, draft), [files, draft]);
  const selectable = files.filter((file) => file.selectable).length;
  const hasVideo = selectedVideoCount(files, selection) > 0;

  function confirm() {
    if (!hasVideo) return;
    if (!sameFileChoice(draft, choice)) onApply(draft);
    onClose();
  }

  const context: Context = {
    files,
    selection,
    route,
    expanded,
    unfoldedOthers,
    onToggle: (path) => setDraft((current) => toggleFileSelection(files, current, path)),
    onToggleDirectory: (path) => setDraft((current) => toggleDirectorySelection(files, current, path)),
    onFold: (path) => setExpanded((current) => toggled(current, path)),
    onFoldOthers: (path) => setUnfoldedOthers((current) => toggled(current, path)),
  };

  return (
    <Dialog
      open
      onRequestClose={onClose}
      tier="M"
      fixedHeight
      bodyLayout="fill"
      title={t("oneshot.filePickerTitle")}
      description={
        <span className="tw:block tw:truncate" title={seedTitle}>
          {seedTitle}
        </span>
      }
      footer={
        <>
          {!hasVideo && (
            <p role="status" className="text-caption tw:m-0 tw:w-full tw:text-right tw:text-warning">
              {t("oneshot.filesNoVideo")}
            </p>
          )}
          <Button className="tw:mr-auto" disabled={draft === undefined} onClick={() => setDraft(undefined)}>
            {t("oneshot.filePickerRestore")}
          </Button>
          <Button onClick={onClose}>{t("act.cancel")}</Button>
          <Button variant="primary" disabled={!hasVideo} onClick={confirm}>
            {t("ui.confirm")}
          </Button>
        </>
      }
    >
      <div className="tw:flex tw:flex-none tw:flex-wrap tw:items-baseline tw:justify-between tw:gap-x-4 tw:gap-y-1 tw:border-b tw:border-border tw:px-6 tw:py-2 tw:phone:px-4">
        <p className="text-caption tw:m-0 tw:text-muted-foreground">{t("oneshot.filePickerHint")}</p>
        <p className="text-caption tw:m-0 tw:flex-none tw:text-muted-foreground" aria-live="polite">
          {t("oneshot.filePickerCounter", { n: selection.size, total: selectable })}
        </p>
      </div>
      <div className="tw:min-h-0 tw:flex-1 tw:overflow-y-auto tw:px-6 tw:py-3 tw:phone:px-4">
        <Level folder={tree} context={context} />
      </div>
    </Dialog>
  );
}
