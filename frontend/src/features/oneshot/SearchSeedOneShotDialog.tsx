import { useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "../../ui/toast";
import {
  ApiError,
  downloadSnapshotSeed,
  expandSnapshotSeed,
  newUUID,
  toUserMessage,
  type AnimeDraft,
  type SeedSearchSnapshotSeed,
  type SeedSearchSnapshotView,
  type SnapshotSeedDownloadInput,
  type TorrentManifestView,
} from "../../lib/api/client";
import { createEmptyDraft } from "../../lib/draft";
import { withGlobalBusiness } from "../anime-config/businessDraft";
import { formatBytes } from "../../lib/labels";
import { t } from "../../lib/i18n";
import { Button, Dialog, Field, Input, StateBlock } from "../../ui/base";

// One-shot acquisition of a single torrent found by keyword search. The search
// snapshot (kind=search) supplies the whole identity: scope_ref/query_ref plus
// snapshot_id/snapshot_version/content_hash are fed back verbatim, the server
// resolves the locator from the snapshot and the torrent bytes never pass
// through the frontend. The draft carries only the full Global business
// snapshot plus title/instance_label — rss_sources stays empty (a search seed
// is not an RSS source and no locator is ever fabricated). The episode offset
// is SnapshotSeedDownloadInput's own top-level field, never a draft field.

const OFFSET_MIN = -1000;
const OFFSET_MAX = 1000;

export type SearchSeedOneShotDialogProps = {
  provider: "anibt" | "mikan";
  snapshot: SeedSearchSnapshotView;
  seed: SeedSearchSnapshotSeed;
  onClose: () => void;
  /** The one-shot was admitted; the parent keeps its own UI state. */
  onSuccess: () => void;
  /** The snapshot identity was rejected as stale; the parent must re-search. */
  onStale: () => void;
};

function isStaleSearchFailure(cause: unknown): boolean {
  return (
    cause instanceof ApiError &&
    (cause.status === 409 ||
      cause.code === "SEED_PREVIEW_STALE" ||
      cause.code === "SEED_SNAPSHOT_RETIRED" ||
      cause.code === "SEED_SNAPSHOT_RETENTION_EXPIRED")
  );
}

export function SearchSeedOneShotDialog({
  provider,
  snapshot,
  seed,
  onClose,
  onSuccess,
  onStale,
}: SearchSeedOneShotDialogProps) {
  const queryClient = useQueryClient();
  const identity = useMemo(
    () => ({
      provider,
      provider_entry_ref: snapshot.scope_ref,
      provider_group_ref: snapshot.query_ref,
      snapshot_version: snapshot.snapshot_version,
      content_hash: snapshot.content_hash,
    }),
    [provider, snapshot],
  );
  const [manifest, setManifest] = useState<TorrentManifestView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [retryToken, setRetryToken] = useState(0);
  const [title, setTitle] = useState(seed.title);
  const [offsetText, setOffsetText] = useState("");
  const [submitting, setSubmitting] = useState(false);

  // Callbacks are taken from a ref to stay current: a parent re-render hands new function
  // identities, and they must never re-fire the expand effect below.
  const onStaleRef = useRef(onStale);
  onStaleRef.current = onStale;

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const result = await expandSnapshotSeed(snapshot.snapshot_id, seed.stable_seed_key, identity);
        if (cancelled) return;
        const expanded = result.state === "available" ? (result.torrent.manifest ?? null) : null;
        if (!expanded) {
          setError(t("oneshot.expandFailedTitle"));
          return;
        }
        setManifest(expanded);
        setError(null);
      } catch (cause) {
        if (cancelled) return;
        if (isStaleSearchFailure(cause)) {
          onStaleRef.current();
          return;
        }
        setError(toUserMessage(cause, t("oneshot.expandFailedTitle")));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [snapshot.snapshot_id, seed.stable_seed_key, identity, retryToken]);

  // A search seed has no baseline (no entry, no preset), so the offset input starts empty and
  // empty means 0: no correction, nothing sent. A non-empty value must be an integer in
  // [-1000, 1000]; anything else blocks submission with rss.offsetInvalid.
  const trimmedOffset = offsetText.trim();
  const offsetValue = trimmedOffset === "" ? 0 : Number(trimmedOffset);
  const offsetValid = Number.isInteger(offsetValue) && offsetValue >= OFFSET_MIN && offsetValue <= OFFSET_MAX;

  // One idempotency key per request body: a retry of the same request (e.g. after a network
  // failure) reuses the key and the server dedups by it; a changed request (title/offset edited)
  // takes a new key, or the server rejects same-key different-body.
  const idempotency = useRef<{ key: string; body: string } | null>(null);
  function idempotencyKeyFor(input: SnapshotSeedDownloadInput): string {
    const body = JSON.stringify([snapshot.snapshot_id, seed.stable_seed_key, input]);
    const known = idempotency.current;
    if (known && known.body === body) return known.key;
    const key = newUUID();
    idempotency.current = { key, body };
    return key;
  }

  async function submit() {
    const name = title.trim();
    if (!name || !offsetValid || submitting) return;
    setSubmitting(true);
    setError(null);
    try {
      const draft: AnimeDraft = await withGlobalBusiness({
        ...createEmptyDraft(),
        origin: "oneshot",
        title: name,
        instance_label: name,
        rss_sources: [],
      });
      const input: SnapshotSeedDownloadInput = {
        ...identity,
        input_source: "current_input",
        draft,
        ...(offsetValue !== 0 ? { episode_offset: offsetValue } : {}),
      };
      await downloadSnapshotSeed(snapshot.snapshot_id, seed.stable_seed_key, input, idempotencyKeyFor(input));
      void queryClient.invalidateQueries({ queryKey: ["acquisition-records"] });
      toast.success(t("oneshot.resultOk"));
      onSuccess();
    } catch (cause) {
      if (isStaleSearchFailure(cause)) {
        onStaleRef.current();
        return;
      }
      setError(toUserMessage(cause, t("oneshot.createdNone")));
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open
      onRequestClose={() => {
        if (!submitting) onClose();
      }}
      tier="M"
      fixedHeight
      title={t("oneshot.searchTitle")}
    >
      {error && !manifest ? (
        <StateBlock
          kind="error"
          title={error}
          action={
            <Button type="button" onClick={() => setRetryToken((value) => value + 1)}>
              {t("grid.retry")}
            </Button>
          }
        />
      ) : !manifest ? (
        <StateBlock kind="busy" title={t("oneshot.expanding")} />
      ) : (
        <div className="tw:grid tw:gap-4">
          <Field label={t("field.title")}>
            <Input value={title} onChange={(event) => setTitle(event.target.value)} disabled={submitting} />
          </Field>
          <div className="tw:grid tw:gap-1.5">
            <Field label={t("oneshot.offset")}>
              <Input
                aria-label={t("oneshot.offset")}
                inputMode="numeric"
                type="number"
                min={OFFSET_MIN}
                max={OFFSET_MAX}
                step="1"
                placeholder="0"
                value={offsetText}
                onChange={(event) => setOffsetText(event.target.value)}
                disabled={submitting}
              />
            </Field>
            {!offsetValid && <p className="text-danger text-caption">{t("rss.offsetInvalid")}</p>}
          </div>
          <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">
            {t("oneshot.searchFiles", { count: manifest.file_count, size: formatBytes(manifest.total_size_bytes) })}
          </p>
          {error ? <p className="tw:m-0 tw:text-sm tw:text-destructive">{error}</p> : null}
          <div className="tw:flex tw:justify-end tw:gap-2">
            <Button type="button" onClick={onClose} disabled={submitting}>
              {t("act.cancel")}
            </Button>
            <Button
              type="button"
              variant="primary"
              loading={submitting}
              disabled={!title.trim() || !offsetValid}
              onClick={() => void submit()}
            >
              {t("oneshot.downloadOne")}
            </Button>
          </div>
        </div>
      )}
    </Dialog>
  );
}
