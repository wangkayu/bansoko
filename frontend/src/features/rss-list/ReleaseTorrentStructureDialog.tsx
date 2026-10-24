import { useQuery } from "@tanstack/react-query";
import {
  ApiError,
  getReleaseTorrentStructure,
  toUserMessage,
  type ReleaseTorrentStructureView,
} from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { Dialog, StateBlock } from "../../ui/base";
import { displayCells, formatStructureBytes, torrentStructureEntries, type TorrentStructureEntry } from "./torrentStructure";

// ReleaseTorrentStructureDialog — the release row's read-only structure preview: the one-level
//   tree of the release's retained torrent payload (folders and files directly under the torrent
//   root). The server sends paths relative to the torrent root, already sorted, and caps the
//   listing; the frontend only groups them one level and formats sizes.
//
// A release whose payload is not retained (never had one, or past its retention window) answers
//   404: that is a normal state rendered as one line, with no retry and no re-fetch entry.

function entryLabel(entry: TorrentStructureEntry): string {
  return entry.directory ? `${entry.name}/` : entry.name;
}

function entryDetail(entry: TorrentStructureEntry): string {
  const size = formatStructureBytes(entry.sizeBytes);
  return entry.directory ? `${size} · ${t("rss.structure.files", { n: entry.fileCount })}` : size;
}

function StructureListing({ view }: { view: ReleaseTorrentStructureView }) {
  const entries = torrentStructureEntries(view.files);
  const labels = entries.map(entryLabel);
  const width = labels.reduce((max, label) => Math.max(max, displayCells(label)), 0);
  return (
    <div className="tw:grid tw:gap-3">
      <p className="tw:m-0 tw:text-sm">
        {t("rss.structure.total", { size: formatStructureBytes(view.total_size), n: view.file_count })}
      </p>
      <pre className="tw:m-0 tw:overflow-x-auto tw:font-mono tw:text-xs tw:leading-5">
        {entries
          .map((entry, index) => `${labels[index]}${" ".repeat(width - displayCells(labels[index]))}  ${entryDetail(entry)}\n`)
          .join("")}
      </pre>
      {view.truncated && (
        <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">
          {t("rss.structure.truncated", { n: view.files.length })}
        </p>
      )}
    </div>
  );
}

export function ReleaseTorrentStructureDialog({
  releaseID,
  onClose,
}: {
  /** The release to preview; undefined keeps the dialog closed and makes no request. */
  releaseID?: string;
  onClose: () => void;
}) {
  const query = useQuery({
    queryKey: ["release-torrent-structure", releaseID ?? ""],
    queryFn: () => getReleaseTorrentStructure(releaseID ?? ""),
    enabled: releaseID !== undefined,
    retry: false,
  });
  const view = query.data;
  // Any 404 on this endpoint means the payload is out of retention: a state, not a failure.
  const unavailable = query.error instanceof ApiError && query.error.status === 404;
  return (
    <Dialog
      open={releaseID !== undefined}
      onRequestClose={onClose}
      tier="M"
      focusTitle
      title={view?.name ?? t("rss.structure.title")}
    >
      {unavailable ? (
        <p className="tw:m-0 tw:text-sm tw:text-muted-foreground">{t("rss.structure.unavailable")}</p>
      ) : query.error ? (
        <StateBlock kind="error" title={toUserMessage(query.error, t("rss.structure.loadFailed"))} />
      ) : view ? (
        <StructureListing view={view} />
      ) : (
        <StateBlock kind="busy" title={t("rss.structure.loading")} />
      )}
    </Dialog>
  );
}
