import { useId, useState, type ReactNode } from "react";
import { ChevronDown, ChevronRight, FileText } from "lucide-react";
import { formatBytes } from "../lib/labels";
import { t } from "../lib/i18n";
import { labelValue } from "../lib/i18n/format";
import { subtitleFormat } from "../lib/boundSubtitles";
import { Tooltip } from "./base";
import { ReasonText } from "./ReasonText";

// Video ↔ external-subtitle bindings in a file list: each bound subtitle is a
// format badge (uppercase extension, side by side in file-list order) on the
// video row, with an expand button at the end giving per-subtitle details.
//
// The binding itself is the server's conclusion (per-file `bound_video_source_path`);
// this component only groups by it and never re-infers from file names. A subtitle
// whose video is not in the same list is treated as unbound and stays its own row.

/** The facts one external subtitle shows on a video row; each page maps its own file model into this. */
export type BoundSubtitleItem = {
  key: string;
  path: string;
  sizeBytes?: number | null;
  /** Language the subtitle suffix actually uses (sub.lang_tag; legacy data falls back to the detected language). */
  language?: string | null;
  /** Whether the language came from the subtitle file name or the release title (sub.lang_source); hidden when unknown. */
  languageSource?: "file" | "release" | null;
  /** Whether the server lets this subtitle through. */
  included: boolean;
  /** Reason code when not let through (`target_duplicate` = same name not kept; the rest only come from legacy data — show the reason neutrally). */
  reasonCode?: string | null;
  /** The rendered target path. */
  target?: string | null;
  /** Supplementary note under the target path (empty values etc.); the target is still valid. */
  targetNote?: ReactNode;
  /** Explanation when let through but no target path (naming failure, waiting for a match, …). */
  targetProblem?: ReactNode;
};

/**
 * Badge states:
 * - placed: let through and has a target path;
 * - followsVideo: the owning video is excluded, so the subtitle follows it out of the import (neutral);
 * - duplicate: same name as another subtitle of the same video; the first one in file order is kept
 *   (warning colour + strikethrough);
 * - noTarget: let through but the target path did not render (warning colour);
 * - otherReason: the server gave another keep-out reason (file rules in legacy data, …). Bound
 *   subtitles follow their video and are never filtered on their own, so this never says
 *   "filtered" — it just states the reason neutrally.
 */
type BadgeState = "placed" | "followsVideo" | "duplicate" | "noTarget" | "otherReason";

const DUPLICATE_REASON = "target_duplicate";

function badgeState(subtitle: BoundSubtitleItem, videoExcluded: boolean): BadgeState {
  if (videoExcluded) return "followsVideo";
  if (!subtitle.included) return subtitle.reasonCode?.toLowerCase() === DUPLICATE_REASON ? "duplicate" : "otherReason";
  return subtitle.target ? "placed" : "noTarget";
}

/** One-sentence reason on badge hover/focus; placed and follows-video badges have no tip. */
function badgeReason(subtitle: BoundSubtitleItem, state: BadgeState): ReactNode {
  switch (state) {
    case "otherReason":
      return subtitle.reasonCode ? <ReasonText code={subtitle.reasonCode} /> : t("common.unspecifiedReason");
    case "duplicate":
      return t("ui.subtitles.duplicate");
    case "noTarget":
      return subtitle.targetProblem ?? t("ui.subtitles.noTargetDetail");
    default:
      return undefined;
  }
}

const SR_ONLY = "tw:sr-only";

// A format badge belongs to the file-attribute (tag) family: a 4px-radius muted
// square with the same type as seed tags — not a status (dot) and not a category
// capsule. Let-through subtitles use the body colour, warnings use an amber
// ground and amber ink; the reason lives in the tip and the screen-reader text.
const FORMAT_BADGE = {
  placed: "tw:inline-flex tw:items-center tw:rounded tw:bg-muted tw:px-1 tw:text-badge tw:leading-4 tw:tracking-wide tw:font-medium tw:text-foreground",
  neutral: "tw:inline-flex tw:items-center tw:rounded tw:bg-muted tw:px-1 tw:text-badge tw:leading-4 tw:tracking-wide tw:text-muted-foreground",
  warning: "tw:inline-flex tw:items-center tw:rounded tw:bg-warning/12 tw:px-1 tw:text-badge tw:leading-4 tw:tracking-wide tw:font-medium tw:text-foreground",
} as const;

function SubtitleBadge({ subtitle, state }: { subtitle: BoundSubtitleItem; state: BadgeState }) {
  const format = subtitleFormat(subtitle.path);
  const reason = badgeReason(subtitle, state);
  const warning = state === "duplicate" || state === "noTarget";
  const tag = (
    <span className={warning ? FORMAT_BADGE.warning : state === "placed" ? FORMAT_BADGE.placed : FORMAT_BADGE.neutral}>
      {state === "duplicate" ? <s>{format}</s> : format}
    </span>
  );
  if (reason === undefined) return <li className="tw:flex" data-subtitle-badge={state}>{tag}</li>;
  // A badge with a reason is focusable, and the tip's reason is also placed in
  // the badge as screen-reader text (the tip is not the only carrier).
  return (
    <li className="tw:flex" data-subtitle-badge={state}>
      <Tooltip content={reason}>
        <span tabIndex={0} className="tw:inline-flex tw:rounded-[0.25rem] tw:focus-visible:outline-2 tw:focus-visible:outline-offset-1 tw:focus-visible:outline-ring">
          {tag}
          <span className={SR_ONLY}>{reason}</span>
        </span>
      </Tooltip>
    </li>
  );
}

const DETAIL_META = "text-caption tw:m-0 tw:text-muted-foreground";

function SubtitleDetail({ subtitle, state }: { subtitle: BoundSubtitleItem; state: BadgeState }) {
  const size = subtitle.sizeBytes !== undefined && subtitle.sizeBytes !== null && subtitle.sizeBytes > 0 ? formatBytes(subtitle.sizeBytes) : undefined;
  return (
    <li className="tw:grid tw:min-w-0 tw:gap-0.5">
      <p className="text-caption break-anywhere tw:m-0 tw:flex tw:min-w-0 tw:items-start tw:gap-1.5" title={subtitle.path}>
        <FileText className="icon-sm-fixed tw:mt-0.5 tw:text-muted-foreground" aria-hidden="true" />
        <span className="tw:min-w-0">
          {subtitle.path}
          {size && <span className="tw:text-muted-foreground"> · {size}</span>}
        </span>
      </p>
      <p className={DETAIL_META}>
        {subtitle.language ? labelValue(t("ui.subtitles.language"), subtitle.language) : t("ui.subtitles.noLanguage")}
        {subtitle.language && subtitle.languageSource && (
          <> · {t(subtitle.languageSource === "file" ? "ui.subtitles.languageFromFile" : "ui.subtitles.languageFromRelease")}</>
        )}
      </p>
      {state === "placed" && (
        <>
          <p className="text-caption break-anywhere tw:m-0" title={subtitle.target ?? undefined}>→ {subtitle.target}</p>
          {subtitle.targetNote && <p className={DETAIL_META}>{subtitle.targetNote}</p>}
        </>
      )}
      {state === "followsVideo" && (
        <p className={DETAIL_META}><ReasonText code="subtitle_video_excluded" /></p>
      )}
      {(state === "duplicate" || state === "noTarget") && (
        <p className="text-caption tw:m-0 tw:text-warning">{badgeReason(subtitle, state)}</p>
      )}
      {state === "otherReason" && <p className={DETAIL_META}>{badgeReason(subtitle, state)}</p>}
    </li>
  );
}

/**
 * External subtitles under a video row: one row of format badges (in file-list
 * order) plus an expand button. A same-name-not-kept (strikethrough) or
 * missing-target subtitle gets a warning badge with the reason on hover/focus;
 * when the video is excluded the subtitles follow it and the badges are neutral.
 * Expanded, each entry shows the source file and size, the language, and the
 * full target path or the reason.
 */
export function BoundSubtitles({
  videoName,
  subtitles,
  videoExcluded = false,
}: {
  videoName: string;
  subtitles: readonly BoundSubtitleItem[];
  /** Owning video is excluded: the subtitles follow it and are not shown as filtered on their own. */
  videoExcluded?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const panelID = useId();
  if (subtitles.length === 0) return null;
  const count = subtitles.length;
  const states = subtitles.map((subtitle) => badgeState(subtitle, videoExcluded));
  return (
    <div className="tw:grid tw:min-w-0 tw:gap-1" data-bound-subtitles="">
      <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-1">
        <ul className="tw:m-0 tw:flex tw:min-w-0 tw:list-none tw:flex-wrap tw:items-center tw:gap-1 tw:p-0" aria-label={t("ui.subtitles.formats", { n: count })}>
          {subtitles.map((subtitle, index) => <SubtitleBadge key={subtitle.key} subtitle={subtitle} state={states[index]!} />)}
        </ul>
        <button
          type="button"
          className="tw:inline-flex tw:size-6 tw:cursor-pointer tw:items-center tw:justify-center tw:rounded-[0.25rem] tw:border-0 tw:bg-transparent tw:p-0 tw:text-muted-foreground tw:hover:bg-muted tw:hover:text-foreground tw:focus-visible:outline-2 tw:focus-visible:outline-offset-1 tw:focus-visible:outline-ring"
          aria-expanded={open}
          aria-controls={panelID}
          aria-label={open ? t("ui.subtitles.collapse", { name: videoName, n: count }) : t("ui.subtitles.expand", { name: videoName, n: count })}
          onClick={() => setOpen((value) => !value)}
        >
          {open ? <ChevronDown className="icon-sm-fixed" aria-hidden="true" /> : <ChevronRight className="icon-sm-fixed" aria-hidden="true" />}
        </button>
      </div>
      {open && (
        <ul id={panelID} className="tw:m-0 tw:ml-1.5 tw:grid tw:min-w-0 tw:list-none tw:gap-1.5 tw:border-l tw:border-border tw:py-0.5 tw:pl-3 tw:pr-0">
          {subtitles.map((subtitle, index) => <SubtitleDetail key={subtitle.key} subtitle={subtitle} state={states[index]!} />)}
        </ul>
      )}
    </div>
  );
}
