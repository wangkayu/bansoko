import { useState } from "react";
import { disambiguateGroupNames, groupLabel } from "../../lib/groupLabels";
import type {
  GroupAction,
  ProviderGroupDiscoveryGroup,
  SubgroupScoreBreakdown,
  SubgroupScoreEntry,
  SubgroupsSection,
  SubscriptionOrigin,
} from "../../lib/api/client";
import { SegmentedControl, Button, Field, Input } from "../../ui";
import { Checkbox, Dialog, type SegmentedTone } from "../../ui/base";
import { selectionPolicy, type SelectionPolicy } from "../../lib/labels";
import { t } from "../../lib/i18n";
import { reasonView } from "../../lib/i18n/reasons";

// Subgroup selection (a direct row list): one subscription belongs to exactly one source
// provider (mikan/anibt), so the subgroup list is the single list under that provider with no
// provider partition rows. Each row = group name / selection policy (Ranked/Always/Never) / count
// switch; scores are read-only (scoring rules have moved into global settings, and the card does
// not edit provider_defaults / name_keywords — saving still sends the complete section,
// preserving the global configuration's values).
// The group list comes only from discovery snapshots and cannot be added manually; a custom
// subscription takes no part in discovery/scoring and shows its single fixed group. Number inputs
// never silently zero out: invalid input reports inline and keeps the original value.
// Subscription badges and slot counts read only the server's selection facts: no facts means not
// previewed, and the frontend never predicts.

type Provider = SubgroupScoreEntry["provider"];

/**
 * 一个 provider 作用域内的服务端选择事实。订阅前由最近一次 `current_input`
 * 读模型提供，订阅后由 workspace 的 `effective_selection` 提供；两者结构一致，
 * 因此组件只消费这一种形状。
 */
export type ServerSelectionFact = {
  available?: boolean;
  provider: string;
  group_ref: string;
  effective_selected: boolean;
  count_in_limit: boolean;
  /**
   * 后端选择决策给出的原因码（名额已满 / 名单外 / 黑名单内 …）。前端只展示，
   * 不根据这三个布尔值自己推断原因。
   */
  reason_code?: string | null;
  /**
   * 决策自己给出的评分构成（站点默认分、名单效果、命中的名称关键词、手动选择、
   * 最终分）。前端只展示，从不计算评分。
   */
  score_breakdown?: SubgroupScoreBreakdown | null;
};

const siteNames: Record<string, string> = { anibt: "AniBT", mikan: "Mikan" };

function signed(n: number): string {
  return n >= 0 ? `+${n}` : `${n}`;
}

/**
 * 评分构成的一行说明，例如「站点默认 50 · 关键词「简繁」+20 · 合计 70」。
 * 每一项都来自服务端的 score_breakdown；名单排除时只说明排除原因。
 */
function scoreBreakdownLine(provider: string, breakdown: SubgroupScoreBreakdown): string {
  const site = siteNames[provider] ?? provider;
  const parts: string[] = [];
  if (breakdown.entry_override === "always") parts.push(t("anime.sources.breakdownAlways"));
  if (breakdown.entry_override === "never") parts.push(t("anime.sources.breakdownNever"));
  if (breakdown.list_effect === "blacklisted") parts.push(t("anime.sources.breakdownBlacklisted", { site }));
  if (breakdown.list_effect === "not_whitelisted") parts.push(t("anime.sources.breakdownNotWhitelisted", { site }));
  if (breakdown.list_effect === "whitelisted") parts.push(t("anime.sources.breakdownWhitelisted", { site }));
  if (breakdown.final_score !== null && breakdown.final_score !== undefined) {
    if (breakdown.pinned_score !== null && breakdown.pinned_score !== undefined) {
      parts.push(t("anime.sources.breakdownPinned", { n: breakdown.pinned_score }));
    } else {
      parts.push(t("anime.sources.breakdownSiteDefault", { n: breakdown.site_default_score }));
    }
    for (const rule of breakdown.keywords) {
      parts.push(t("anime.sources.breakdownKeyword", { keyword: rule.keyword, n: signed(rule.score) }));
    }
    parts.push(t("anime.sources.breakdownTotal", { n: breakdown.final_score }));
  }
  return parts.join(" · ");
}

// A list-exclusion's reason is already stated in the score composition; no separate reason chip repeats it.
const reasonsShownByBreakdown = new Set(["site_blacklisted", "site_not_whitelisted", "policy_always", "policy_never"]);

// Short labels (the compact variant): auto = competing for slots by score ranking
// (selection_mode=auto), require = forced subscription (force_include), exclude = forced
// non-subscription (force_exclude). Auto is the resting state and selected is just a quiet white
// base; require/exclude are colored when selected so a row's state reads at a glance.
const policyOptions = (): ReadonlyArray<{ value: SelectionPolicy; label: string; tone: SegmentedTone }> => [
  { value: "ranked", label: t("forms.sources.policyAuto"), tone: "quiet" },
  { value: "always", label: t("forms.sources.policyAlways"), tone: "primary" },
  { value: "never", label: t("forms.sources.policyNever"), tone: "danger" },
];


function selectionPolicyPatch(
  policy: SelectionPolicy,
): Pick<SubgroupScoreEntry, "selection_mode" | "manual_selected"> {
  if (policy === "ranked") {
    return { selection_mode: "auto", manual_selected: null };
  }
  return { selection_mode: "manual", manual_selected: policy === "always" };
}

export function SelectionOverrideFields({
  value,
  onChange,
  origin,
  fixedGroupName,
  onGroupAction,
  serverSelection,
  selectionPending = false,
  selectionError,
  observed,
}: {
  value: SubgroupsSection;
  onChange: (next: SubgroupsSection) => void;
  /** The subscription's source provider; custom takes no part in discovery/scoring. */
  origin: SubscriptionOrigin;
  /** The custom subscription's fixed group name (taken from the subscription's RSS source). */
  fixedGroupName?: string;
  /** Optional: maps row policy switches to backend GroupActions. */
  onGroupAction?: (action: GroupAction) => void;
  /** The server's selection facts; absent (or missing facts for that group) always displays as not previewed. */
  serverSelection?: readonly ServerSelectionFact[];
  /** A selection preview for unsaved edits is waiting/in flight: the numbers are still the previous result, with only a light notice. */
  selectionPending?: boolean;
  /** A short note of the latest selection preview's failure: the numbers are still the previous result. */
  selectionError?: string;
  /**
   * 草稿发现的组（观察，不是偏好）。尚无 entry 的组显示为「自动·默认积分」观察行，
   * 不写入 entries；用户显式选择必选/排除时才产生 manual entry，改回自动则删除
   * 该 entry，因此发现永远不会制造积分覆盖。
   */
  observed?: readonly ProviderGroupDiscoveryGroup[];
}) {

  const isCustom = origin !== "anibt" && origin !== "mikan";
  // Custom takes no part in discovery/scoring; activeProvider is used only in the non-custom
  // branch, where origin is necessarily anibt or mikan.
  const activeProvider: Provider = origin === "anibt" ? "anibt" : "mikan";
  // Only the current provider's groups are counted; by contract one subscription has no entries of another provider.
  const explicitEntries = value.entries.filter((entry) => entry.provider === activeProvider);
  const explicitRefs = new Set(explicitEntries.map((entry) => entry.group_ref));
  const observedRefs = new Set(
    (observed ?? []).filter((group) => group.provider === activeProvider).map((group) => group.group_ref),
  );
  // Observation-only rows: rendered with the default policy, never persisted.
  const observationRows: SubgroupScoreEntry[] = (observed ?? [])
    .filter((group) => group.provider === activeProvider && !explicitRefs.has(group.group_ref))
    .map((group) => ({
      provider: activeProvider,
      group_ref: group.group_ref,
      display_name: group.display_name,
      selection_mode: "auto",
      manual_selected: null,
      effective_selected: false,
      score: 0,
      count_in_limit: true,
    }));
  const isObservation = (entry: SubgroupScoreEntry) => !explicitRefs.has(entry.group_ref);
  const entries = [...explicitEntries, ...observationRows];

  const [maxRssDialogOpen, setMaxRssDialogOpen] = useState(false);
  const [maxRssInput, setMaxRssInput] = useState("");
  const [filter, setFilter] = useState("");

  // Only same-named subgroups get a site name or short tag appended to the name.
  const displayNames = disambiguateGroupNames(
    entries.map((entry) => ({ provider: entry.provider, ref: entry.group_ref, name: entry.display_name })),
  );
  const nameOf = (entry: SubgroupScoreEntry) => groupLabel(displayNames, entry.provider, entry.group_ref, entry.display_name);
  // Sort: by state first (selected on top, excluded at the bottom, the rest in between), then score descending, then name.
  // A score uses the server's final score when present, otherwise the explicit rating.
  const factOf = (entry: SubgroupScoreEntry) =>
    serverSelection?.find((fact) => fact.provider === activeProvider && fact.group_ref === entry.group_ref);
  const rankScore = (entry: SubgroupScoreEntry) => factOf(entry)?.score_breakdown?.final_score ?? entry.score;
  const statusRank = (entry: SubgroupScoreEntry) => {
    if (!isObservation(entry) && selectionPolicy(entry) === "never") return 2;
    const fact = factOf(entry);
    return fact !== undefined && fact.available !== false && fact.effective_selected ? 0 : 1;
  };
  const sorted = [...entries].sort(
    (a, b) => statusRank(a) - statusRank(b) || rankScore(b) - rankScore(a) || nameOf(a).localeCompare(nameOf(b)),
  );
  const keyword = filter.trim().toLowerCase();
  const visible = keyword
    ? sorted.filter((entry) => entry.display_name.toLowerCase().includes(keyword))
    : sorted;

  // Facts are located by provider + group_ref: a same-named group of another provider does not participate.
  const factsForProvider = serverSelection?.filter((fact) => fact.provider === activeProvider);
  const factByGroupRef = new Map(factsForProvider?.map((fact) => [fact.group_ref, fact] as const) ?? []);
  const selectedFacts = factsForProvider?.filter((fact) => fact.available !== false && fact.effective_selected);
  const countedSelected = selectedFacts?.filter((fact) => fact.count_in_limit).length;
  const notCounted = selectedFacts?.filter((fact) => !fact.count_in_limit).length;


  function openMaxRssDialog() {
    setMaxRssInput(String(value.max_rss));
    setMaxRssDialogOpen(true);
  }

  // max_rss accepts only integers 0-100; invalid input reports inline and leaves the original value untouched (never silently zeroed).
  const maxRssParsed = maxRssInput.trim() === "" ? Number.NaN : Number(maxRssInput);
  const maxRssValid = Number.isInteger(maxRssParsed) && maxRssParsed >= 0 && maxRssParsed <= 100;

  function saveMaxRss() {
    if (!maxRssValid) return;
    onChange({ ...value, max_rss: maxRssParsed });
    setMaxRssDialogOpen(false);
  }

  function updateEntry(target: SubgroupScoreEntry, patch: Partial<SubgroupScoreEntry>) {
    onChange({
      ...value,
      entries: value.entries.map((entry) =>
        entry.provider === target.provider && entry.group_ref === target.group_ref ? { ...entry, ...patch } : entry,
      ),
    });
  }

  // Policy switch → GroupAction mapping mirrors backend applyGroupActions:
  // ranked = restore_auto, always = force_include, never = force_exclude.
  const ACTION_BY_POLICY: Record<SelectionPolicy, GroupAction["action"]> = {
    ranked: "restore_auto",
    always: "force_include",
    never: "force_exclude",
  };

  function changePolicy(entry: SubgroupScoreEntry, nextPolicy: SelectionPolicy) {
    if (isObservation(entry)) {
      // Explicit user intent on an observed group: a manual entry, which the
      // selection policy reads without any score.
      if (nextPolicy !== "ranked") {
        onChange({ ...value, entries: [...value.entries, { ...entry, ...selectionPolicyPatch(nextPolicy) }] });
      }
      return;
    }
    if (nextPolicy === "ranked" && entry.selection_mode === "manual" && observedRefs.has(entry.group_ref)) {
      // Back to automatic for a group whose only entry was the user's manual
      // intent: drop the entry instead of turning it into a score override.
      onChange({
        ...value,
        entries: value.entries.filter(
          (candidate) => !(candidate.provider === entry.provider && candidate.group_ref === entry.group_ref),
        ),
      });
      return;
    }
    updateEntry(entry, selectionPolicyPatch(nextPolicy));
    onGroupAction?.({ provider: entry.provider, group_ref: entry.group_ref, action: ACTION_BY_POLICY[nextPolicy] });
  }

  return (
    <section className="sources-section">
      {/* Only provider subgroups have subscription slots; custom is a single fixed source and shows no meaningless max_rss. */}
      {!isCustom && (
        <div className="sources-header">
          <div className="sources-actions">
            <Button variant="secondary" onClick={openMaxRssDialog}>
              {t("forms.sources.maxButton")}
            </Button>
            <span
              className={`sources-summary text-caption tw:transition-opacity${selectionPending ? " tw:opacity-60" : ""}`}
              aria-busy={selectionPending || undefined}
            >
              {t("forms.sources.summary")} <span className="sources-summary-strong">{countedSelected ?? t("forms.sources.notPreviewed")}</span>
              {countedSelected !== undefined && <>{t("forms.sources.outOf", { n: value.max_rss })}</>}
              {notCounted !== undefined && notCounted > 0 && <span>{t("forms.sources.notCounted", { n: notCounted })}</span>}
              {selectionPending && <span className="tw:ml-2">{t("forms.sources.selectionUpdating")}</span>}
            </span>
            {!selectionPending && selectionError && (
              <span className="text-caption tw:text-destructive" role="status">
                {selectionError}
              </span>
            )}
          </div>
        </div>
      )}

      {isCustom ? (
        <p className="sources-fixed text-caption">
          {fixedGroupName ? (
            <>
              {t("forms.sources.fixedGroup")} <span className="sources-summary-strong">{fixedGroupName}</span>
              <span className="sources-note-block">{t("forms.sources.fixedNote")}</span>
            </>
          ) : (
            t("forms.sources.fixedGeneric")
          )}
        </p>
      ) : entries.length === 0 ? (
        <p className="sources-empty text-caption">
          {t("forms.sources.noGroups")}
        </p>
      ) : (
        <div className="sources-policy-stack">
          {entries.length > 6 && (
            <Input
              aria-label={t("forms.sources.filterAria")}
              placeholder={t("forms.sources.filterPlaceholder")}
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
              className="sources-filter-input"
            />
          )}
          {visible.length === 0 && (
            <p className="sources-empty text-caption">{t("forms.sources.noMatch")}</p>
          )}
          <ul className="sources-list" aria-label={t("forms.sources.listAria")}>
            {visible.map((entry) => {
              const policy = selectionPolicy(entry);
              const selection = factByGroupRef.get(entry.group_ref);
              // Reasons come only from the server's conclusion: selected rows explain nothing,
              // unselected/unavailable rows translate the backend's reason code into copy, and no
              // reason code means no tip (the frontend never guesses).
              const selectionReason =
                selection !== undefined && (selection.available === false || !selection.effective_selected)
                  ? reasonView(selection.reason_code)
                  : undefined;
              const breakdown = selection?.score_breakdown ?? undefined;
              const finalScore = breakdown?.final_score;
              const breakdownText = breakdown === undefined ? "" : scoreBreakdownLine(entry.provider, breakdown);
              const reasonText =
                selection?.reason_code && !reasonsShownByBreakdown.has(selection.reason_code.trim().toLowerCase())
                  ? reasonView(selection.reason_code)?.text
                  : undefined;
              const detail = [breakdownText, reasonText].filter((part) => part !== undefined && part !== "").join(" · ");
              return (
                <li
                  key={`${entry.provider}:${entry.group_ref}`}
                  className="sources-row"
                >
                  <div className="sources-main">
                    <span className="sources-name">
                      <span className="sources-name-text text-body" title={nameOf(entry)}>
                        {nameOf(entry)}
                      </span>
                      <span className="sources-badge text-badge">
                        {finalScore !== null && finalScore !== undefined
                          ? t("anime.sources.finalScore", { n: finalScore })
                          : breakdown !== undefined
                            ? t("anime.sources.noFinalScore")
                            : isObservation(entry)
                              ? t("anime.sources.defaultScore")
                              : t("anime.sources.score", { n: entry.score })}
                      </span>
                      {(policy !== "never" || selection?.available === false) && (
                        <span
                          className={
                            selection?.available !== false && selection?.effective_selected
                              ? "sources-policy-badge sources-policy-badge-selected text-badge"
                              : "sources-policy-badge text-badge"
                          }
                          title={selectionReason === undefined ? undefined : (selectionReason.title ?? selectionReason.text)}
                        >
                          {selection === undefined ? t("forms.sources.notPreviewed") : selection.available === false ? t("forms.sources.unavailable") : selection.effective_selected ? t("anime.sources.selected") : t("anime.sources.unselected")}
                        </span>
                      )}
                    </span>
                    {detail !== "" && <span className="sources-breakdown text-caption">{detail}</span>}
                  </div>
                  <div className="sources-actions">
                    <Checkbox
                      size="sm"
                      aria-label={t("forms.sources.countAria", { name: nameOf(entry) })}
                      title={isObservation(entry) ? t("forms.sources.countTip") : undefined}
                      disabled={isObservation(entry)}
                      checked={entry.count_in_limit}
                      onCheckedChange={(checked) => updateEntry(entry, { count_in_limit: checked })}
                    >
                      {t("forms.sources.count")}
                    </Checkbox>
                    <SegmentedControl
                      compact
                      ariaLabel={t("forms.sources.policyAria", { name: nameOf(entry) })}
                      title={t("anime.sources.policyHint")}
                      value={policy}
                      onChange={(nextPolicy) => changePolicy(entry, nextPolicy)}
                      options={policyOptions()}
                    />
                  </div>
                </li>
              );
            })}
          </ul>
        </div>
      )}

      <Dialog
        open={maxRssDialogOpen}
        onRequestClose={() => setMaxRssDialogOpen(false)}
        tier="S"
        title={t("forms.sources.maxDialogTitle")}
        footer={
          <>
            <Button onClick={() => setMaxRssDialogOpen(false)}>{t("act.cancel")}</Button>
            <Button variant="primary" disabled={!maxRssValid} onClick={saveMaxRss}>
              {t("forms.sources.maxApply")}
            </Button>
          </>
        }
      >
        <div className="sources-note text-body">
          <p className="sources-note">{t("anime.sources.countHint")}</p>
          <Field label={t("forms.sources.maxField")}>
            <Input
              type="number"
              min={0}
              max={100}
              value={maxRssInput}
              aria-invalid={!maxRssValid}
              onChange={(event) => setMaxRssInput(event.target.value)}
            />
          </Field>
          {maxRssInput === "0" && (
            <p className="sources-empty text-caption">
              {t("forms.sources.maxZero")}
            </p>
          )}
          {!maxRssValid && (
            <p role="alert" className="sources-error text-caption">
              {t("forms.sources.maxInvalid")}
            </p>
          )}
        </div>
      </Dialog>
    </section>
  );
}
