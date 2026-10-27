import type { AnimeDraft, MetadataHold, RSSListGroup, RSSListItem, RSSListView } from "../../lib/api/client";
import { manifestSummary, releaseEpisodeLabel, rssFeedState, type RssExpandModel, type RssGroupModel, type RssItemModel, type RssWorkspaceModel } from "./model";
import { createElement } from "react";
import { Pencil } from "lucide-react";
import { Button } from "../../ui";
import type { StatePillTone } from "../../ui";
import { t } from "../../lib/i18n";
import { emptyFactsNote, knownReasonLabel, namingFailureDetail, namingPendingLabel, reasonLabel } from "../../lib/i18n/reasons";
import { evaluationReasonCode, isReasonImpliedByVerdict, releaseVerdictLabel, releaseVerdictLabelWithHold, releaseVerdictTone } from "../../lib/labels";
import { ReasonText } from "../../ui/ReasonText";
import { TagBadges } from "./TagBadges";
import { tagFilterReasonLabel } from "../../lib/titleTags";
import { disambiguateGroupNames, groupLabel, type GroupNameInput } from "../../lib/groupLabels";

/** Inputs for disambiguating duplicate subgroup names: only the site, the group key and the name; the key itself is never displayed. */
export function groupNameInput(group: RSSListGroup): GroupNameInput {
  return { provider: group.scope.provider, ref: group.scope.provider_group_ref, name: group.display_name };
}

/**
 * 视图组对应的草稿源下标：优先 provider_provenance 精确匹配（provider 条目 ref 三元组），
 * 退回组名匹配——custom 组的 scope ref 就是 trim 后的组名（服务端 customOriginGroups
 * 如此构造），provider 草稿通常也只有一个带来源组名的源。找不到表示该组没有可写
 * 偏移的草稿源（如纯观察组），不渲染偏移入口。
 */
export function matchDraftSourceIndex(draft: AnimeDraft, group: RSSListGroup): number | undefined {
  const scope = group.scope;
  const byProvenance = draft.rss_sources.findIndex(
    (source) =>
      source.provider_provenance &&
      source.provider_provenance.provider === scope.provider &&
      source.provider_provenance.provider_entry_ref === scope.provider_entry_ref &&
      source.provider_provenance.provider_group_ref === scope.provider_group_ref,
  );
  if (byProvenance >= 0) return byProvenance;
  const name = group.display_name.trim();
  if (name === "") return undefined;
  const byName = draft.rss_sources.findIndex((source) => source.group_name.trim() === name);
  return byName >= 0 ? byName : undefined;
}
// The pre-subscription draft's adapter: projects the server-composed RSSListView into the shared
// rendering model.
//
// The server is already the only decision point (`download_action`, `evaluation.verdict`,
// `files[].included`); this layer only maps tags and nodes, re-judging no rule.

/** One seed row's expansion state: the adapter supplies who is expanding / who failed; the panel only renders. */
export type DraftExpandState = { state: "loading" | "failed"; error?: string };

type DraftItemOptions = {
  /** The expand entry and its expansion state in this render; without onStart no entry renders (already-expanded rows). */
  expand?: { onStart: () => void; state?: DraftExpandState };
  /** The adapter requires this row open in this render (the row opens after a successful expand). */
  open?: boolean;
  /** The read model's metadata hold: a waiting seed writes which link it awaits from this. */
  hold?: MetadataHold;
};

function draftExpandModel(source: { onStart: () => void; state?: DraftExpandState }): RssExpandModel {
  const state = source.state?.state ?? "idle";
  return {
    state,
    // A failure keeps the same entry, the button just becomes retry: failures write no cache and a retry simply calls again.
    label: state === "loading" ? t("rss.expandLoading") : state === "failed" ? t("rss.expandRetry") : t("rss.expandSeed"),
    hint: t("rss.expandHint"),
    error: state === "failed" ? source.state?.error : undefined,
    onStart: source.onStart,
  };
}

function draftItem(item: RSSListItem, options: DraftItemOptions = {}): RssItemModel {
  const { expand, open, hold } = options;
  const manifest = item.torrent.manifest;
  // A seed the server judged unexpanded carries only RSS facts and no file list. It is not a
//   failure and waits for the user to expand — unless the server already filtered it by those RSS
//   facts (tag rows, keyword filter): then the row shows that verdict and follows the
//   show-filtered switch, while its torrent stays pending and can still be expanded.
  const pending = item.torrent.state === "pending";
  const evaluated = item.evaluation.state === "evaluated" ? item.evaluation.verdict : undefined;
  const awaitingExpansion = pending && evaluated !== "filtered";
  const reasonCode = evaluationReasonCode(item.evaluation.reason_code, item.evaluation.reason_detail);
  const filteredWithReason = evaluated === "filtered" && !!reasonCode;
  // A not-yet-started subscription's bench: with "fetch new seeds only" on, the server marks rows
//   published before subscription creation as start_baseline — after starting they bypass rule
//   judgment, and this displays the state they will get.
  if (item.download_action.reason_code === "start_baseline") {
    return {
      ...draftItem({ ...item, download_action: { ...item.download_action, reason_code: undefined } }, options),
      status: { label: releaseVerdictLabel("start_baseline"), tone: releaseVerdictTone("start_baseline") },
      filtered: false,
      statusDetail: undefined,
    };
  }
  return {
    key: `${item.snapshot_id}:${item.stable_seed_key}`,
    title: item.seed.title,
    leadingMeta: item.seed.anibt_tags?.length || item.seed.title_tags?.length ? createElement(TagBadges, { anibt: item.seed.anibt_tags, title: item.seed.title_tags }) : undefined,
    publishedAt: item.seed.published_at ?? undefined,
    sizeBytes: item.seed.size_bytes,
    episodeLabel: releaseEpisodeLabel(item.evaluation.parsed_facts),
    status:
      awaitingExpansion
        ? { label: t("rss.notExpanded"), tone: "muted" as StatePillTone }
        : evaluated
          ? { label: releaseVerdictLabelWithHold(evaluated, hold, filteredWithReason ? tagFilterReasonLabel(item.evaluation.reason_code, item.evaluation.reason_detail) ?? reasonLabel(reasonCode) : undefined), tone: releaseVerdictTone(evaluated) }
          : { label: t("verdict.unparsed"), tone: "muted" as StatePillTone },
    filtered: evaluated === "filtered",
    statusDetail: pending || filteredWithReason || isReasonImpliedByVerdict(evaluated, reasonCode) ? undefined : createElement(ReasonText, { code: reasonCode }),
    manifestSummary: pending || !manifest ? undefined : manifestSummary(item),
    // Structure-unavailable is a "could not fetch" verdict; unexpanded has no verdict yet and never reaches this branch.
    manifestUnavailable: !pending && item.torrent.state === "unavailable" ? knownReasonLabel(item.torrent.error_code) : undefined,
    expand: expand ? draftExpandModel(expand) : undefined,
    open,
    files: item.files.map((file) => ({
      key: file.file_ref,
      path: file.source_path,
      sizeBytes: file.size_bytes,
      mediaKind: file.media_kind,
      language: file.sub_lang_tag ?? file.language,
      languageSource: file.sub_lang_tag ? file.sub_lang_source : undefined,
      boundVideoPath: file.bound_video_source_path,
      included: file.included,
      reasonCode: file.reason_code,
      reason: file.reason_code ? createElement(ReasonText, { code: file.reason_code }) : undefined,
      target: file.rendered_target,
      targetNote: file.rendered_target ? emptyFactsNote(file.empty_facts) : undefined,
      targetError: file.render_error && !namingPendingLabel(file.reason_code) ? namingFailureDetail(file.reason_code, file.empty_facts) ?? file.render_error : undefined,
      targetPending: file.render_error ? namingPendingLabel(file.reason_code) : undefined,
    })),
  };
}

export function draftWorkspaceModel(
  view: RSSListView,
  handlers: {
    onRefreshGroup?: (group: RSSListGroup) => void;
    refreshingGroup?: string;
    stale?: boolean;
    /** Returns the group's current offset when it has a matching draft source (reading the draft live, no re-read wait); otherwise no offset entry renders. */
    draftOffsetFor?: (group: RSSListGroup) => number | undefined;
    /** Opens the group's draft offset editing (the dialog is held by the caller). */
    onEditOffset?: (group: RSSListGroup) => void;
    /** The entry to expand this seed (the panel never calls APIs directly); without it no expand entry renders. */
    onExpand?: (item: RSSListItem) => void;
    /** Expansion state keyed by `${snapshot_id}:${stable_seed_key}`, used only to render loading / failed. */
    expandStates?: Map<string, DraftExpandState>;
    /** The row required open after an action (opens following a successful expand). */
    openedSeedKey?: string;
  },
): RssWorkspaceModel {
  const names = disambiguateGroupNames(view.groups.map(groupNameInput));
  return {
    groups: view.groups.map((group) => {
      const key = `${group.scope.provider}:${group.scope.provider_entry_ref}:${group.scope.provider_group_ref}`;
      // The bench preserves subscription semantics: the server reads only selected subgroups' seed
//   lists — an unselected group is not read even when it carries a snapshot identity
//   (unused_feed_identities). An unselected group's state is "not read", not "stale", and it gets
//   no refresh entry — the list read model a refresh fetches would not use it either.
      const selected = group.selection.effective_selected;
      const refreshBusy = handlers.refreshingGroup === key;
      const selectedLabel = `${selected ? t("rss.selected") : t("rss.notSelected")}${
        group.selection.source === "observed_default" ? t("rss.defaultGroup") : ""
      }`;
      return {
        key,
        displayName: groupLabel(names, group.scope.provider, group.scope.provider_group_ref, group.display_name),
        scoreLabel: group.score ? t("rss.score", { n: group.score }) : undefined,
        feed: !selected
          ? { label: t("rss.feed.notSelected"), tone: "muted" as StatePillTone }
          : refreshBusy
            ? { label: t("rss.feed.refreshing"), tone: "muted" as StatePillTone, busy: true }
            : rssFeedState(group.feed.state),
        feedDetail: selected
          ? t("rss.groupSummary", { selected: selectedLabel, offset: group.offset.value, n: group.items.length })
          : t("rss.groupSummaryNotRead", { selected: selectedLabel, offset: group.offset.value }),
        // The draft-side offset entry: shaped like the owner side's pencil + current value; writes only the in-memory draft.
        actions: (() => {
          const offset = handlers.draftOffsetFor?.(group);
          if (offset === undefined || !handlers.onEditOffset) return undefined;
          const name = groupLabel(names, group.scope.provider, group.scope.provider_group_ref, group.display_name);
          return createElement(
            "span",
            { className: "chain-source-actions" },
            createElement(
              Button,
              {
                size: "sm",
                variant: "ghost",
                "aria-label": t("rss.adjustOffset", { name }),
                title: t("rss.adjustOffset", { name }),
                onClick: () => handlers.onEditOffset?.(group),
              },
              createElement(Pencil, { className: "icon-sm" }),
              ` ${t("rss.offset", { n: offset })}`,
            ),
          );
        })(),
        onRefresh: selected && handlers.onRefreshGroup ? () => handlers.onRefreshGroup?.(group) : undefined,
        refreshBusy,
        items: group.items.map((item) => {
          const key = `${item.snapshot_id}:${item.stable_seed_key}`;
          return draftItem(item, {
            // Only seeds the server judged unexpanded get an expand entry; expanded rows go straight to the file list.
            expand:
              item.torrent.state === "pending" && handlers.onExpand
                ? { onStart: () => handlers.onExpand?.(item), state: handlers.expandStates?.get(key) }
                : undefined,
            open: handlers.openedSeedKey === key,
            hold: view.metadata_hold,
          });
        }),
        emptyText: selected ? t("rss.emptyGroup") : t("rss.emptyNotSelected"),
      };
    }),
    emptyText: t("rss.emptyNoGroups"),
  };
}
