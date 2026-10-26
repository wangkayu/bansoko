import { useMemo, useState } from "react";
import { disambiguateGroupNames, groupLabel } from "../../lib/groupLabels";
import { toast } from "../../ui/toast";
import { useSavePart } from "../../lib/saveParts";
import {
  discoverAnimeSubgroups,
  isVersionConflict,
  patchAnimeSubgroups,
  toUserMessage,
  type AnimeWorkspace,
  type GroupAction,
} from "../../lib/api/client";
import { subgroupsChangeCount, subgroupsEditFromWorkspace, valuesEqual, type SubgroupsEdit } from "../../lib/draft";
import { SelectionOverrideFields, type ServerSelectionFact } from "../anime-forms/SourcesFields";
import { DraftSubgroupRefresh } from "../anime-config/DraftSubgroupRefresh";
import { StateBlock, Button, ConflictBlock } from "../../ui";
import type { AnimeTabHost, DraftTabHost, OwnerTabHost } from "./host";
import { useOwnerEdit } from "./useOwnerEdit";
import { useSubgroupsSelectionPreview } from "./useSubgroupsSelectionPreview";
import { t } from "../../lib/i18n";
import { reasonLabel } from "../../lib/i18n/reasons";

// The subgroups tab's sole implementation: before subscribing it is read-only discovery merged
// into the draft (no source is created); after subscribing it is owner CAS saving + policy
// switches collecting GroupActions + re-discovery. The subscription/scoring list is the same
// SelectionOverrideFields.
//
// Subscription badges and slot counts read only the server's selection facts and follow unsaved
// edits (effective on save, but never blocking the preview): after edits debounce, the frontend
// requests `subgroups-selection-preview` — with the full draft before subscribing, with the
// unsaved subgroups value after; an owner without unsaved edits uses the workspace's
// `effective_selection` directly. While in flight or failed, the previous numbers stay; the
// frontend never computes a selection.

/** After subscribing: the effective selection the server computed in the workspace (the saved state). */
function ownerSelectionFacts(workspace: AnimeWorkspace): ServerSelectionFact[] {
  return workspace.subgroups.effective_selection.map((selection) => ({
    provider: selection.provider,
    group_ref: selection.group_ref,
    available: selection.available,
    effective_selected: selection.effective_selected,
    reason_code: selection.reason_code,
    score_breakdown: selection.score_breakdown,
  }));
}

export function SubgroupsTab({
  host,
  readOnly = false,
}: {
  host: AnimeTabHost;
  /** A read-only workspace (a retired owner): every edit/save entry is hidden. */
  readOnly?: boolean;
}) {
  if (host.mode === "draft") {
    return <DraftSubgroupsTab host={host} />;
  }
  if (readOnly) {
    return <ReadOnlySubgroupsPanel host={host} />
  }
  return <OwnerSubgroupsTab host={host} />;
}

// The read-only subgroups panel: a retired instance's subgroup configuration is no longer
// editable, but the effective selection stays visible (part of the history facts). No mutation
// callback is consumed here, so save/discovery buttons cannot leak out.
function ReadOnlySubgroupsPanel({ host }: { host: OwnerTabHost }) {
  const entries = host.workspace.subgroups.effective_selection.filter((entry) => entry.available && entry.effective_selected).map((entry) => ({ ...entry, score: entry.effective_score }));
  const displayNames = disambiguateGroupNames(entries.map((entry) => ({ provider: entry.provider, ref: entry.group_ref, name: entry.display_name })));
  return (
    <div className="subgroups-stack">
      <StateBlock
        kind="empty"
        title={t("tabs.subgroupsReadOnly")}
        description={t("anime.subgroups.endedHint")}
      />
      {entries.length > 0 && (
        <ul className="sources-list" aria-label={t("tabs.effectiveSubgroupsAria")}>
          {entries.map((entry) => (
            <li key={`${entry.provider}:${entry.group_ref}`} className="sources-row">
              <span className="sources-name-text text-body">{groupLabel(displayNames, entry.provider, entry.group_ref, entry.display_name)}</span>
              <span className="sources-badge text-badge">{t("tabs.score", { n: entry.score })}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// --- Draft: discoveries are observed rows; only an explicit require/exclude writes draft entries ---

function DraftSubgroupsTab({ host }: { host: DraftTabHost }) {
  const { draft, onDraftChange } = host;
  // Only provider drafts have subgroup selection; while the draft is not ready to read, no
  // request goes out (displayed as not previewed). The identity takes only the inputs that decide
  // the selection, so edits in other tabs never trigger a request.
  const previewable = host.previewable && (draft.origin === "anibt" || draft.origin === "mikan");
  const selection = useSubgroupsSelectionPreview(
    previewable ? { input_source: "current_input", draft, observation: host.observation ?? null } : undefined,
    previewable
      ? JSON.stringify([draft.origin, draft.origin_entry_ref, draft.subgroups, host.observation ?? null])
      : undefined,
  );
  return (
    <div className="subgroups-stack">
      {/* Provider entries: the group list can be refreshed proactively even unsubscribed (a
          read-only discovery, the snapshot stays on the server, no source is created); custom has
          no provider entry to discover and shows nothing. Discovered groups display as observed
          rows, never written into entries; seed viewing belongs to the RSS preview tab and is not
          duplicated here. */}
      {host.subgroupDiscovery && (
        <DraftSubgroupRefresh
          provider={host.subgroupDiscovery.provider}
          providerEntryRef={host.subgroupDiscovery.providerEntryRef}
          onDiscovered={host.onDiscovered}
        />
      )}
      <SelectionOverrideFields
        value={draft.subgroups}
        onChange={(subgroups) => onDraftChange({ ...draft, subgroups })}
        origin={draft.origin}
        fixedGroupName={draft.rss_sources[0]?.group_name}
        serverSelection={selection.facts}
        selectionPending={selection.pending}
        selectionError={selection.error}
        observed={host.observedGroups}
      />
    </div>
  );
}

// --- Owner: draft + GroupActions, footer saves; re-discovery is an immediate action ----------

function OwnerSubgroupsTab({ host }: { host: OwnerTabHost }) {
  const { animeID, workspace, onChanged } = host;
  const saved = useMemo(() => subgroupsEditFromWorkspace(workspace.subgroups), [workspace.subgroups]);
  const { edit, setEdit, baseline, markSaved } = useOwnerEdit<SubgroupsEdit>(saved, valuesEqual);
  const [busy, setBusy] = useState(false);
  const [discovering, setDiscovering] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();
  // Pending GroupActions collected from policy switches since last load.
  // Sent with the subgroups PATCH (backend applyGroupActions), then cleared.
  // The entries themselves already carry the same end state, so actions are
  // idempotent with respect to the section payload; an empty array keeps the
  // old CAS behavior.
  const [pendingActions, setPendingActions] = useState<GroupAction[]>([]);
  // Between a save/discovery success and the re-read, the CAS version follows the server's response.
  const [localVersion, setLocalVersion] = useState<{ base: number; next: number }>();
  const subgroupsVersion =
    localVersion && localVersion.base === workspace.subgroups.version ? localVersion.next : workspace.subgroups.version;
  const setSubgroupsVersion = (next: number) => setLocalVersion({ base: workspace.subgroups.version, next });

  const count = edit && baseline ? subgroupsChangeCount(baseline.subgroups, edit.subgroups) : 0;
  // Unsaved edits are previewed by the server; without unsaved edits this is the workspace's
  // saved state. While the preview has not returned (or failed), the previous server numbers keep
  // showing.
  const unsaved = edit !== undefined && baseline !== undefined && !valuesEqual(baseline.subgroups, edit.subgroups);
  const selection = useSubgroupsSelectionPreview(
    unsaved && edit ? { input_source: "owner_draft", managed_anime_id: animeID, subgroups: edit.subgroups } : undefined,
    unsaved && edit ? JSON.stringify([animeID, edit.subgroups]) : undefined,
  );
  const savedSelection = ownerSelectionFacts(workspace);

  async function save() {
    if (!edit) return { ok: true as const };
    setBusy(true);
    setError(undefined);
    setConflict(undefined);
    try {
      const result = await patchAnimeSubgroups(animeID, {
        expected_version: subgroupsVersion,
        subgroups: edit.subgroups,
        group_actions: pendingActions,
      });
      setSubgroupsVersion(result.version);
      setPendingActions([]);
      markSaved(edit);
      onChanged();
      return { ok: true as const, message: t("tabs.subgroups.saved") };
    } catch (cause) {
      if (isVersionConflict(cause)) {
        setConflict(cause.currentVersion);
      }
      setError(toUserMessage(cause, t("tabs.subgroups.saveFailed")));
      return { ok: false as const };
    } finally {
      setBusy(false);
    }
  }

  useSavePart("subgroups", { count, label: t("tabs.names.subgroups"), busy, save });

  async function discover() {
    setDiscovering(true);
    setError(undefined);
    setConflict(undefined);
    try {
      const result = await discoverAnimeSubgroups(animeID, {
        expected_version: subgroupsVersion,
      });
      if (result.outcome === "discovered") {
        // The discovery response carries the complete SubgroupsOwnerResult: it is injected into
        // the edit state directly so the list reflects the newly discovered groups at once. Relying
        // only on onChanged's refetch would not do — the useOwnerEdit keep-alive hook would keep
        // the old edit (unequal to the new saved), the list would not refresh and a false dirty
        // would appear, after which saving would overwrite the just-discovered groups with the old
        // entries.
        markSaved(subgroupsEditFromWorkspace(result.subgroups));
        setSubgroupsVersion(result.subgroups.version);
        setPendingActions([]);
        toast.success(t("config.discovery.found", { n: result.subgroups.subgroups.entries.length }));
      } else if (result.outcome === "no_groups") {
        toast.info(t("config.discovery.none"));
      } else {
        toast.error(result.reason ? (reasonLabel(result.reason) ?? t("config.discovery.failed")) : t("config.discovery.failed"));
      }
      onChanged();
    } catch (cause) {
      if (isVersionConflict(cause)) {
        setConflict(cause.currentVersion);
      }
      setError(toUserMessage(cause, t("config.discovery.failed")));
    } finally {
      setDiscovering(false);
    }
  }

  // A 409 refresh: re-read the workspace (along onChanged's refetch path), keeping local edits.
  function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    onChanged();
  }

  if (!edit) return <StateBlock kind="busy" title={t("tabs.subgroups.reading")} />;
  return (
    <div className="subgroups-stack">
      {conflict !== undefined && (
        <ConflictBlock currentVersion={conflict} message={error} onRefresh={refreshAfterConflict} />
      )}
      {conflict === undefined && error && <StateBlock kind="error" title={error} />}
      <SelectionOverrideFields
        value={edit.subgroups}
        onChange={(subgroups) => setEdit({ ...edit, subgroups })}
        origin={workspace.subscription.origin}
        serverSelection={unsaved ? (selection.facts ?? savedSelection) : savedSelection}
        selectionPending={unsaved && selection.pending}
        selectionError={unsaved ? selection.error : undefined}
        fixedGroupName={workspace.rss_sources[0]?.group_name}
        onGroupAction={(action) =>
          setPendingActions((current) => [
            // Latest switch for the same group wins; order across groups is preserved.
            ...current.filter(
              (item) => !(item.provider === action.provider && item.group_ref === action.group_ref),
            ),
            action,
          ])
        }
      />
      {workspace.subscription.origin !== "custom" && (
        <div className="subgroups-discover">
          {/* Discovering subgroups is a configuration action, not automatic scheduling: stopped
              is allowed too (the one-shot task case); the backend's DiscoverAnimeSubgroups admits
              both active and stopped. */}
          <Button
            variant="secondary"
            size="sm"
            disabled={busy || discovering}
            onClick={() => void discover()}
          >
            {discovering ? t("act.refreshing") : t("config.discovery.refresh")}
          </Button>
        </div>
      )}
    </div>
  );
}
