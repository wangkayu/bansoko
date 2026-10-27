import type { ReactNode } from "react";
import { Folder } from "lucide-react";
import type { AnimeDraft, RSSListManifestFile } from "../../lib/api/client";
import { formatBytes } from "../../lib/labels";
import { t } from "../../lib/i18n";
import { ReasonText } from "../../ui/ReasonText";
import { Checkbox } from "../../ui/base";
import { cn } from "../../lib/utils";
import { episodesRangeLabel } from "../rss-list/model";
import { buildFileTree, fileCheckable, type FileTreeNode } from "./fileSelection";

// The one-shot file picker: every file of one seed's torrent as a folder tree. Each row is the
//   server's decision (selectable, reason, a video's recognition, the video a subtitle follows);
//   the picker renders it and reports toggles. Nothing here re-computes a rule.

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

/** Why a row cannot be checked right now; undefined when it can. */
function blockedText(file: RSSListManifestFile, checkable: boolean): ReactNode {
  if (checkable) return undefined;
  if (!file.selectable) return <ReasonText code={file.reason_code} />;
  if (file.media_kind === "subtitle") {
    return file.bound_video_source_path === undefined ? <ReasonText code="subtitle_binding_missing" /> : t("oneshot.fileSubtitleNeedsVideo");
  }
  return undefined;
}

function FileLine({
  name,
  file,
  selection,
  route,
  onToggle,
}: {
  name: string;
  file: RSSListManifestFile;
  selection: ReadonlySet<string>;
  route: UnclassifiedRoute | undefined;
  onToggle: (path: string) => void;
}) {
  const checkable = fileCheckable(file, selection);
  const checked = selection.has(file.source_path);
  const blocked = blockedText(file, checkable);
  const recognition = file.selectable && file.media_kind === "video" ? recognitionText(file, route) : undefined;
  return (
    <li className="tw:flex tw:items-start tw:gap-2 tw:py-0.5">
      <Checkbox
        size="sm"
        checked={checked}
        disabled={!checkable}
        aria-label={file.source_path}
        onCheckedChange={() => onToggle(file.source_path)}
      />
      <span className="tw:grid tw:min-w-0 tw:flex-1">
        <span className={cn("text-caption break-anywhere", !checkable && "tw:text-muted-foreground")} title={file.source_path}>
          {name}
          <span className="tw:text-muted-foreground"> · {formatBytes(file.size_bytes)}</span>
        </span>
        {(recognition || blocked) && (
          <span className="text-caption tw:text-muted-foreground">{recognition ?? blocked}</span>
        )}
      </span>
    </li>
  );
}

function TreeLevel({
  nodes,
  selection,
  route,
  onToggle,
}: {
  nodes: FileTreeNode[];
  selection: ReadonlySet<string>;
  route: UnclassifiedRoute | undefined;
  onToggle: (path: string) => void;
}) {
  return (
    <ul className="tw:m-0 tw:grid tw:list-none tw:gap-0.5 tw:p-0">
      {nodes.map((node) =>
        node.kind === "dir" ? (
          <li key={`dir:${node.path}`} className="tw:grid tw:gap-0.5">
            <span className="text-caption break-anywhere tw:flex tw:items-center tw:gap-1 tw:text-muted-foreground" title={node.path}>
              <Folder aria-hidden="true" className="icon-sm tw:flex-none" />
              {node.name}
            </span>
            <div className="tw:border-l tw:border-border tw:pl-3 tw:ml-1.5">
              <TreeLevel nodes={node.children} selection={selection} route={route} onToggle={onToggle} />
            </div>
          </li>
        ) : (
          <FileLine key={node.file.source_path} name={node.name} file={node.file} selection={selection} route={route} onToggle={onToggle} />
        ),
      )}
    </ul>
  );
}

export function FilePicker({
  files,
  selection,
  route,
  onToggle,
}: {
  files: readonly RSSListManifestFile[];
  selection: ReadonlySet<string>;
  /** The draft's unknown-file route: where an unrecognized checked video ends up. */
  route: UnclassifiedRoute | undefined;
  onToggle: (path: string) => void;
}) {
  return (
    <div role="group" aria-label={t("oneshot.filePickerTitle")} className="tw:rounded-md tw:bg-muted/15 tw:px-2.5 tw:py-2">
      <TreeLevel nodes={buildFileTree(files)} selection={selection} route={route} onToggle={onToggle} />
    </div>
  );
}
