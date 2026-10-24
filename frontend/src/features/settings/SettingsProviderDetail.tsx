import { Alert, Button, HelpTip, Input, Popover, Segmented, StatePill, StateBlock } from "../../ui/base";
import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { Check, ChevronDown, Plus, X } from "lucide-react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "../../ui/toast";
import {
  deleteSubgroupGroupDictionaryEntry,
  isVersionConflict,
  listSubgroupGroupCandidates,
  readCatalogPreference,
  retrieveSubgroupGroupCandidate,
  setCatalogPreference,
  toUserMessage,
  type GlobalSectionView,
  type NameKeywordRule,
  type ProviderGroupDefault,
  type ProviderGroupDefaults,
  type ProviderNameKeywordRules,
  type SubgroupGroupCandidate,
  type SubgroupScoreEntry,
} from "../../lib/api/client";
import { useBlacklistState } from "../../lib/blacklist";
import { valuesEqual } from "../../lib/draft";
import { catalogKeys, subgroupDictionaryKeys } from "../../lib/queryKeys";
import { useSettingsSection } from "../../lib/settings";
import { useSavePart } from "../../lib/saveParts";
import { SwitchRow } from "../../ui/SaveFooter";
import { currentSeason, seasonLabel } from "../catalog/season";
import { ConflictBlock, ConfirmDialog } from "../../ui";
import { SubgroupScoreBadge } from "../../ui/SubgroupScoreBadge";
import { SettingsEditDialog } from "./SettingsEditDialog";
import { t } from "../../lib/i18n";
import { cn } from "@/lib/utils";

// One provider's configuration page (shared by the anibt and mikan sidebar entries). Carries five
//   blocks of the subgroups section, scoped to this provider:
//   - the subgroup dictionary: a provider-scoped candidate pool (collected + user added) living
//     outside the section as a dictionary resource in its own right. Discover confirms online by
//     subgroup key and adds to the dictionary (immediate effect; the only network entry); each
//     dictionary row edits its pinned score (provider_defaults.<site>.pinned_scores, a page draft
//     saved with the footer; clearing cancels the pin);
//   - provider_defaults: blacklist/whitelist mode + default score + the roster (subgroup scoring);
//   - name_keywords: name keyword scoring (keyword + adjustment, -100..100);
//   - catalog auto-refresh: this/next season refresh toggles (a per-provider owner row);
//   - hidden anime: this provider's catalog suppressions (saved immediately).
//   The roster / name scoring / catalog auto-refresh are all in-place-edited page drafts, committed
//   in order by the settings dialog footer's single save: first the subgroups section with a whole
//   section CAS (the draft starts from the complete section value — max_rss, entries, and the other
//   provider's fields pass through untouched, never wiping what the subscription policy page or the
//   sibling provider page owns), then the catalog refresh preference. The dictionary and hidden list
//   are independent resources that take effect immediately in their own management dialogs.
//   Hard rule: the roster may only reference keys already in the dictionary, and search exists only
//   on the dictionary card — the roster's search box filters dictionary candidates and accepts no
//   free text.

export type ProviderKey = "anibt" | "mikan";

type PolicyMode = "blacklist" | "whitelist";

type ProviderDraft = {
  max_rss: number;
  entries: SubgroupScoreEntry[];
  provider_defaults: Record<ProviderKey, ProviderGroupDefault>;
  name_keywords: Record<ProviderKey, NameKeywordRule[]>;
};

const LABELS: Record<ProviderKey, string> = { anibt: "AniBT", mikan: "Mikan" };

type PinnedGroupScore = NonNullable<ProviderGroupDefault["pinned_scores"]>[number];

function defaultProviderPolicy(): ProviderGroupDefault {
  return { mode: "blacklist", score: 50, groups: [] };
}

// A pinned score appears in the value only when non-empty: an empty array equals an omitted one, sparing the draft and baseline a fake difference.
function normalizePinnedScores(value: unknown): PinnedGroupScore[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  const result: PinnedGroupScore[] = [];
  for (const item of value) {
    if (!item || typeof item !== "object") continue;
    const pin = item as Partial<PinnedGroupScore>;
    if (typeof pin.group_ref !== "string" || typeof pin.score !== "number") continue;
    const ref = pin.group_ref.trim();
    if (ref === "" || seen.has(ref)) continue;
    seen.add(ref);
    result.push({ group_ref: ref, score: pin.score });
  }
  return result;
}

function pinnedScoreOf(policy: ProviderGroupDefault, ref: string): number | undefined {
  return policy.pinned_scores?.find((pin) => pin.group_ref === ref)?.score;
}

// Sets or clears one subgroup's pinned score (an undefined score clears), preserving the existing order.
function withPinnedScore(policy: ProviderGroupDefault, ref: string, score: number | undefined): ProviderGroupDefault {
  const current = policy.pinned_scores ?? [];
  const exists = current.some((pin) => pin.group_ref === ref);
  let next: PinnedGroupScore[];
  if (score === undefined) next = current.filter((pin) => pin.group_ref !== ref);
  else if (exists) next = current.map((pin) => (pin.group_ref === ref ? { group_ref: ref, score } : pin));
  else next = [...current, { group_ref: ref, score }];
  const { pinned_scores: _previous, ...rest } = policy;
  return next.length > 0 ? { ...rest, pinned_scores: next } : rest;
}

function normalizeProviderPolicy(value: unknown): ProviderGroupDefault {
  if (!value || typeof value !== "object") return defaultProviderPolicy();
  const item = value as Partial<ProviderGroupDefault>;
  const groups = Array.isArray(item.groups)
    ? [...new Set(item.groups.filter((group): group is string => typeof group === "string").map((group) => group.trim()).filter(Boolean))]
    : [];
  const pinned = normalizePinnedScores(item.pinned_scores);
  return {
    mode: item.mode === "whitelist" ? "whitelist" : "blacklist",
    score: typeof item.score === "number" && item.score >= 1 && item.score <= 100 ? item.score : 50,
    groups,
    ...(pinned.length > 0 ? { pinned_scores: pinned } : {}),
  };
}

function normalizeKeywordRules(value: unknown): NameKeywordRule[] {
  if (!Array.isArray(value)) return [];
  return value
    .filter((rule): rule is NameKeywordRule => !!rule && typeof rule === "object")
    .map((rule) => ({
      keyword: typeof rule.keyword === "string" ? rule.keyword : "",
      score: typeof rule.score === "number" ? rule.score : 0,
    }));
}

function draftFromSection(section?: GlobalSectionView): ProviderDraft {
  const value = section?.value ?? {};
  const defaults = (value.provider_defaults ?? {}) as ProviderGroupDefaults;
  const keywords = (value.name_keywords ?? {}) as ProviderNameKeywordRules;
  return {
    max_rss: typeof value.max_rss === "number" ? value.max_rss : 100,
    entries: Array.isArray(value.entries) ? (value.entries as SubgroupScoreEntry[]) : [],
    provider_defaults: {
      anibt: normalizeProviderPolicy(defaults.anibt),
      mikan: normalizeProviderPolicy(defaults.mikan),
    },
    name_keywords: {
      anibt: normalizeKeywordRules(keywords.anibt),
      mikan: normalizeKeywordRules(keywords.mikan),
    },
  };
}

export function SettingsProviderDetail({
  provider,
  onDirtyChange,
}: {
  provider: ProviderKey;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const label = LABELS[provider];
  const {
    section: subgroups,
    sectionsQuery: sections,
    draft,
    changed,
    saving,
    error,
    conflict,
    updateDraft,
    submit,
    refreshAfterConflict,
  } = useSettingsSection<ProviderDraft>({
    sectionName: "subgroups",
    initialDraft: () => draftFromSection(),
    draftFromSection,
    successMessage: t("settings.provider.saved", { label }),
    failureFallback: t("settings.provider.saveFailed", { label }),
    onDirtyChange,
  });
  const [dictionaryOpen, setDictionaryOpen] = useState(false);
  const [discoverOpen, setDiscoverOpen] = useState(false);

  const policy = draft.provider_defaults[provider];
  const rules = draft.name_keywords[provider];
  const updatePolicy = (next: ProviderGroupDefault) =>
    updateDraft((current) => ({
      ...current,
      provider_defaults: { ...current.provider_defaults, [provider]: next },
    }));
  const baseline = useMemo(() => draftFromSection(subgroups), [subgroups]);
  // The unsaved-parts count: subgroup scoring (pinned scores included) and name scoring each count as one (this site's fields only).
  const count = !changed
    ? 0
    : Math.max(
        (valuesEqual(baseline.provider_defaults[provider], policy) ? 0 : 1) +
          (valuesEqual(baseline.name_keywords[provider], rules) ? 0 : 1),
        1,
      );
  useSavePart("section", { count, label: t("settings.provider.title", { label }), busy: saving, save: submit });

  return (
    <div className="settings-provider">
      <section className="settings-provider-section">
        <div className="settings-provider-header">
          <div>
            <h2 className="settings-provider-title text-title">{t("settings.provider.title", { label })}</h2>
            <p className="settings-provider-muted text-caption">
              {t("settings.provider.description")}
            </p>
          </div>
        </div>
        {conflict !== undefined ? (
          <ConflictBlock currentVersion={conflict} message={error} onRefresh={refreshAfterConflict} />
        ) : (
          error && <Alert tone="error" title={error} />
        )}
        {sections.isLoading && (
          <StateBlock kind="busy" title={t("settings.provider.loading", { label })} />
        )}
        {sections.error && <Alert tone="error" title={toUserMessage(sections.error, t("settings.provider.loadFailed", { label }))} />}

        {/* Card A: the subgroup dictionary (an independently owned pool saved immediately; pinned scores go into the page draft) */}
        <SubgroupDictionaryCard
          provider={provider}
          policy={policy}
          onChange={updatePolicy}
          onManage={() => setDictionaryOpen(true)}
          onDiscover={() => setDiscoverOpen(true)}
        />

        {/* Card B: subgroup scoring (mode + base score + roster, edited in the draft in place) */}
        <ProviderGroupPolicyCard
          provider={provider}
          policy={policy}
          onChange={updatePolicy}
          onOpenDictionary={() => setDictionaryOpen(true)}
        />

        {/* Rules area: name scoring (draft) */}
        <NameKeywordRulesCard
          provider={provider}
          rules={rules}
          mode={policy.mode}
          onChange={(next) =>
            updateDraft((current) => ({
              ...current,
              name_keywords: { ...current.name_keywords, [provider]: next },
            }))
          }
        />

        {/* Rules area: catalog auto-refresh (a per-provider owner row, in the draft, committed in order with the footer save) */}
        <CatalogRefreshPreferenceRow provider={provider} />

        {/* Rules area: hidden anime (statistics filtered to the current provider; the manage button switches to settings' hidden-anime section) */}
        <BlacklistCard provider={provider} />

        {/* The dictionary dialog: deletions take effect immediately; a row's pinned score goes into the page draft */}
        {dictionaryOpen ? (
          <SubgroupDictionaryDialog
            open
            onOpenChange={setDictionaryOpen}
            provider={provider}
            policy={policy}
            onChange={updatePolicy}
          />
        ) : null}

        {/* The discover dialog: looks up and confirms a subgroup online, adding it to the dictionary on confirm (immediate effect, never the page draft) */}
        {discoverOpen ? <SubgroupDiscoverDialog open onOpenChange={setDiscoverOpen} provider={provider} /> : null}
      </section>
    </div>
  );
}

const NO_CANDIDATES: SubgroupGroupCandidate[] = [];

// The dictionary dialog's page size. Browsing (unfiltered) takes the cap in one page so the whole
//   dictionary (the Mikan snapshot is about 370 usable ids) fits after hiding retired entries;
//   filtered by name/key, 100 per page.
const DICTIONARY_BROWSE_LIMIT = 500;
const DICTIONARY_FILTER_LIMIT = 100;

// A dictionary key's display name: candidates the caller already holds (search results, the user
//   layer list) fill first, and each remaining unknown key queries the dictionary once for the entry
//   whose group_ref is exactly equal. Not found or a failed request only remembers "no name" (an
//   empty string) — no error, no loading state. Cached within the component, each key queried at
//   most once.
function useDictionaryNames(provider: ProviderKey, keys: string[], known: SubgroupGroupCandidate[] = NO_CANDIDATES) {
  const [names, setNames] = useState<Map<string, string>>(() => new Map());
  const resolving = useRef<Set<string>>(new Set());

  useEffect(() => {
    if (known.length === 0) return;
    setNames((prev) => {
      let next: Map<string, string> | undefined;
      for (const candidate of known) {
        if (prev.get(candidate.group_ref) === candidate.display_name) continue;
        next ??= new Map(prev);
        next.set(candidate.group_ref, candidate.display_name);
      }
      return next ?? prev;
    });
  }, [known]);

  useEffect(() => {
    const unknown = keys.filter((key) => !names.has(key) && !resolving.current.has(key));
    for (const key of unknown) {
      resolving.current.add(key);
      void listSubgroupGroupCandidates(provider, { q: key })
        .then((list) => {
          const match = list.candidates.find((candidate) => candidate.group_ref === key);
          setNames((prev) => (prev.has(key) ? prev : new Map(prev).set(key, match?.display_name ?? "")));
        })
        .catch(() => setNames((prev) => (prev.has(key) ? prev : new Map(prev).set(key, ""))))
        .finally(() => {
          resolving.current.delete(key);
        });
    }
  }, [provider, keys, names]);

  return names;
}

// The user-added layer's (source=user) reads: the dictionary card's summary count, pinned-score
//   names, and the discover dialog's already-in-dictionary judgment share one local read. A
//   discover/delete invalidates by provider prefix.
function useUserDictionary(provider: ProviderKey) {
  return useQuery({
    queryKey: subgroupDictionaryKeys.user(provider),
    queryFn: () => listSubgroupGroupCandidates(provider, { source: "user" }),
  });
}

// The inline pinned score: an integer 1..100 writes the draft, and clearing cancels the pin; an
//   illegal input stays in the field with a hint in place while the draft keeps the last legal
//   value. The score feeds the server's decisions — the browser never computes the final score.
//   The pinned score's quick tiers: 100 is "always selected and takes no subscription slot", 80 is
//   "preferred but competing normally". Everything else in 1..99 competes by score, so custom input
//   remains.
const PIN_ALWAYS = 100;
const PIN_PREFER = 80;

// The pinned-score editor: the two quick tiers, the custom field and the effect line. Shared by the
//   dictionary row's trigger popover and the pinned-score badge's popover; what it writes is still
//   the page draft (provider_defaults.<site>.pinned_scores), saved once by the footer.
function PinScoreEditor({
  name,
  value,
  onChange,
  onDone,
}: {
  name: string;
  value: number | undefined;
  onChange: (score: number | undefined) => void;
  onDone?: () => void;
}) {
  // The quick tiers need not reappear in the custom field; any other value is carried out as-is for continued editing.
  const [customText, setCustomText] = useState(
    value === undefined || value === PIN_ALWAYS || value === PIN_PREFER ? "" : String(value),
  );
  const [customInvalid, setCustomInvalid] = useState(false);

  function apply(score: number) {
    onChange(score);
    onDone?.();
  }

  function applyCustom(raw: string) {
    setCustomText(raw);
    const trimmed = raw.trim();
    if (trimmed === "") {
      // Clearing is synonymous with unpinning: an empty score simply is no pin.
      setCustomInvalid(false);
      onChange(undefined);
      return;
    }
    const parsed = Number(trimmed);
    if (Number.isInteger(parsed) && parsed >= 1 && parsed <= 100) {
      setCustomInvalid(false);
      onChange(parsed);
      return;
    }
    setCustomInvalid(true);
  }

  const presetClass = (active: boolean) =>
    cn(
      "tw:flex tw:w-full tw:items-start tw:justify-between tw:gap-2 tw:rounded-control tw:border tw:px-3 tw:py-2 tw:text-left tw:text-sm tw:transition-colors",
      active ? "tw:border-primary tw:bg-primary/5" : "tw:border-border tw:bg-card tw:hover:bg-muted/40",
    );

  return (
    <div className="tw:grid tw:gap-2">
      <div className="tw:grid tw:gap-2">
        <button
          type="button"
          className={presetClass(value === PIN_ALWAYS)}
          aria-label={t("settings.provider.dictionary.pinPresetAlways")}
          aria-pressed={value === PIN_ALWAYS}
          onClick={() => apply(PIN_ALWAYS)}
        >
          <span className="tw:flex tw:items-center tw:gap-2 tw:font-medium">
            {t("settings.provider.dictionary.pinPresetAlways")}
            {value === PIN_ALWAYS ? <Check size={14} aria-hidden /> : null}
          </span>
          <span className="tw:text-xs tw:text-muted-foreground">
            {t("settings.provider.dictionary.pinPresetAlwaysHint")}
          </span>
        </button>
        <button
          type="button"
          className={presetClass(value === PIN_PREFER)}
          aria-label={t("settings.provider.dictionary.pinPresetPrefer")}
          aria-pressed={value === PIN_PREFER}
          onClick={() => apply(PIN_PREFER)}
        >
          <span className="tw:flex tw:items-center tw:gap-2 tw:font-medium">
            {t("settings.provider.dictionary.pinPresetPrefer")}
            {value === PIN_PREFER ? <Check size={14} aria-hidden /> : null}
          </span>
          <span className="tw:text-xs tw:text-muted-foreground">
            {t("settings.provider.dictionary.pinPresetPreferHint")}
          </span>
        </button>
      </div>
      <label className="tw:grid tw:gap-1">
        <span className="tw:text-xs tw:text-muted-foreground">{t("settings.provider.dictionary.pinCustom")}</span>
        <Input
          inputMode="numeric"
          value={customText}
          placeholder={t("settings.provider.dictionary.pinPlaceholder")}
          aria-label={t("settings.provider.dictionary.pinAria", { name })}
          aria-invalid={customInvalid ? true : undefined}
          onChange={(event) => applyCustom(event.target.value)}
        />
      </label>
      {customInvalid ? (
        <p role="alert" className="settings-provider-error text-caption">
          {t("settings.provider.dictionary.pinInvalid")}
        </p>
      ) : null}
      <p className="settings-provider-description text-caption">
        {value === undefined
          ? t("settings.provider.dictionary.pinEffectUnset")
          : value === PIN_ALWAYS
            ? t("settings.provider.dictionary.pinEffectAlways")
            : t("settings.provider.dictionary.pinEffectRanked", { score: value })}
      </p>
      {value !== undefined ? (
        <Button
          size="sm"
          variant="ghost"
          onClick={() => {
            onChange(undefined);
            onDone?.();
          }}
        >
          {t("settings.provider.dictionary.pinClear")}
        </Button>
      ) : null}
    </div>
  );
}

// The pinned-score control. Unpinned it shows only "set pinned score"; pinned it shows the compact
//   "pinned N", opening a popover to edit. The popover holds the shared editor; the badge surface
//   mounts that same editor directly inside its own popover.
function PinScoreControl({
  name,
  value,
  onChange,
}: {
  name: string;
  value: number | undefined;
  onChange: (score: number | undefined) => void;
}) {
  const [open, setOpen] = useState(false);
  // Each open remounts the editor so its custom field restarts from the current value.
  const [editorKey, setEditorKey] = useState(0);
  const trigger =
    value === undefined ? (
      <Button size="sm" variant="ghost" aria-label={t("settings.provider.dictionary.pinSetAria", { name })}>
        <Plus size={14} aria-hidden />
        {t("settings.provider.dictionary.pinSet")}
      </Button>
    ) : (
      <Button
        size="sm"
        variant="secondary"
        aria-label={t("settings.provider.dictionary.pinChipAria", { name, score: value })}
      >
        {t("settings.provider.dictionary.pinChip", { score: value })}
        <ChevronDown size={14} aria-hidden />
      </Button>
    );
  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) setEditorKey((current) => current + 1);
      }}
      align="end"
      className="tw:w-64"
      trigger={trigger}
      title={t("settings.provider.dictionary.pinTitle", { name })}
    >
      <PinScoreEditor key={editorKey} name={name} value={value} onChange={onChange} onDone={() => setOpen(false)} />
    </Popover>
  );
}

// Card A: the dictionary (the pool). Discover confirms online by subgroup id / slug and adds to the
//   dictionary (the only network entry); manage dictionary opens the whole dictionary's local list
//   (filtering, inline pinned scores, deleting user-added entries). The card lists pinned subgroups
//   directly: finding them again in a several-hundred-row dictionary list is unnecessary when pinned
//   scores are editable without opening the dialog, so the dialog keeps no separate "pinned"
//   section.
function SubgroupDictionaryCard({
  provider,
  policy,
  onChange,
  onManage,
  onDiscover,
}: {
  provider: ProviderKey;
  policy: ProviderGroupDefault;
  onChange: (policy: ProviderGroupDefault) => void;
  onManage: () => void;
  onDiscover: () => void;
}) {
  const label = LABELS[provider];
  const user = useUserDictionary(provider);
  const pins = useMemo(() => policy.pinned_scores ?? [], [policy.pinned_scores]);
  const pinnedKeys = useMemo(() => pins.map((pin) => pin.group_ref), [pins]);
  const names = useDictionaryNames(provider, pinnedKeys, user.data?.candidates ?? NO_CANDIDATES);
  return (
    <div className="settings-provider-card">
      <div className="settings-provider-header">
        <h3 className="settings-provider-card-title text-body">{t("settings.provider.dictionary.title")}</h3>
        <div className="settings-provider-actions">
          <Button
            variant="ghost"
            size="sm"
            aria-label={t("settings.provider.dictionary.discoverAria", { label })}
            onClick={onDiscover}
          >
            {t("settings.provider.dictionary.discover")}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            aria-label={t("settings.provider.dictionary.manageAria", { label })}
            onClick={onManage}
          >
            {t("settings.provider.dictionary.manage")}
          </Button>
        </div>
      </div>
      <p className="settings-provider-description text-caption">
        {t("settings.provider.dictionary.summary", { n: user.data?.candidates.length ?? 0 })}
      </p>
      <p className="settings-provider-description text-caption">{t("settings.provider.dictionary.description")}</p>
      {user.error && <Alert tone="error" title={toUserMessage(user.error, t("settings.provider.dictionary.loadFailed"))} />}
      <div className="settings-provider-form-section">
        <h4 className="settings-provider-card-title text-caption">
          {t("settings.provider.dictionary.pinnedTitle")}
          <HelpTip label={t("settings.provider.dictionary.pinnedHelpLabel")} className="tw:ml-1 tw:align-middle">
            {t("settings.provider.dictionary.pinnedHelp")}
          </HelpTip>
        </h4>
        {pins.length === 0 ? (
          <p className="settings-provider-description text-caption">{t("settings.provider.dictionary.pinnedEmpty")}</p>
        ) : (
          <ul
            className="tw:m-0 tw:flex tw:list-none tw:flex-wrap tw:items-start tw:gap-1.5 tw:p-0"
            aria-label={t("settings.provider.dictionary.pinnedTitle")}
          >
            {pins.map((pin) => {
              const name = names.get(pin.group_ref);
              const primary = name ? name : pin.group_ref;
              return (
                <li key={pin.group_ref}>
                  <SubgroupScoreBadge
                    name={primary}
                    score={t("settings.provider.dictionary.pinChip", { score: pin.score })}
                    ariaLabel={t("settings.provider.dictionary.pinChipAria", { name: primary, score: pin.score })}
                    detail={name ? { text: pin.group_ref, reasonTitle: pin.group_ref } : undefined}
                  >
                    <PinScoreEditor
                      name={primary}
                      value={pin.score}
                      onChange={(score) => onChange(withPinnedScore(policy, pin.group_ref, score))}
                    />
                  </SubgroupScoreBadge>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </div>
  );
}

// The dictionary modal: a local list of the whole dictionary (collected + user added), no network.
//   With an empty filter every usable entry lists (retired collected entries excluded); typing
//   filters both layers by name/key (retired included). Paging continues via "load more". Each row
//   accepts a pinned score (into the page draft; clearing cancels), and user-added entries can be
//   deleted (confirmed twice, immediate effect). New subgroups join only through discover.
function SubgroupDictionaryDialog({
  open,
  onOpenChange,
  provider,
  policy,
  onChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  provider: ProviderKey;
  policy: ProviderGroupDefault;
  onChange: (policy: ProviderGroupDefault) => void;
}) {
  const label = LABELS[provider];
  const queryClient = useQueryClient();
  const [filter, setFilter] = useState("");
  const [deleteError, setDeleteError] = useState<string>();
  const [pendingDelete, setPendingDelete] = useState<SubgroupGroupCandidate>();

  useEffect(() => {
    if (!open) return;
    setFilter("");
    setDeleteError(undefined);
    setPendingDelete(undefined);
  }, [open]);

  const query = filter.trim();
  const filtering = query !== "";
  // All local reads (GET), no network. An empty filter means the whole dictionary with retired
//   collected entries excluded — enough that one page holds every usable entry (about 370 for
//   Mikan); typing filters both layers by name/key, retired entries findable too. The next page
//   comes from "load more".
  const list = useInfiniteQuery({
    queryKey: subgroupDictionaryKeys.list(provider, query),
    queryFn: ({ pageParam }) =>
      listSubgroupGroupCandidates(provider, {
        q: query,
        hide_retired: !filtering,
        limit: filtering ? DICTIONARY_FILTER_LIMIT : DICTIONARY_BROWSE_LIMIT,
        offset: pageParam,
      }),
    initialPageParam: 0,
    getNextPageParam: (lastPage) => lastPage.next_offset ?? undefined,
    enabled: open,
  });
  const rows = useMemo(() => list.data?.pages.flatMap((page) => page.candidates) ?? [], [list.data]);
  const total = list.data?.pages[0]?.total ?? 0;
  const listError = list.error;
  const loading = list.isFetching && !list.isFetchingNextPage;

  async function removeEntry(candidate: SubgroupGroupCandidate) {
    const name = candidate.display_name || candidate.group_ref;
    setDeleteError(undefined);
    try {
      await deleteSubgroupGroupDictionaryEntry(provider, candidate.group_ref);
      toast.success(t("settings.provider.dictionary.removed", { name }));
      await queryClient.invalidateQueries({ queryKey: subgroupDictionaryKeys.provider(provider) });
    } catch (cause) {
      setDeleteError(toUserMessage(cause, t("settings.provider.dictionary.deleteFailed")));
    }
  }

  const pendingName = pendingDelete ? pendingDelete.display_name || pendingDelete.group_ref : "";

  return (
    <SettingsEditDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t("settings.provider.dictionary.dialogTitle", { label })}
      description={t("settings.provider.dictionary.dialogHint")}
    >
      <div className="settings-provider-form">
        <div className="settings-provider-form-section">
          <Input
            value={filter}
            aria-label={t("settings.provider.dictionary.filterAria", { label })}
            placeholder={t("settings.provider.dictionary.filterPlaceholder")}
            onChange={(event) => setFilter(event.target.value)}
          />
        </div>

        <div className="settings-provider-form-section">
          <h4 className="settings-provider-card-title text-caption">
            {filtering
              ? t("settings.provider.dictionary.filterTitle", { n: total })
              : t("settings.provider.dictionary.listTitle", { n: total })}
          </h4>
          {!filtering && (
            <p className="settings-provider-description text-caption">{t("settings.provider.dictionary.retiredHidden")}</p>
          )}
          {listError ? (
            <Alert tone="error" title={toUserMessage(listError, t("settings.provider.dictionary.loadFailed"))} />
          ) : loading && rows.length === 0 ? null : rows.length === 0 ? (
            filtering ? (
              <p className="settings-provider-description text-caption">{t("settings.provider.dictionary.filterEmpty")}</p>
            ) : (
              <StateBlock kind="empty" title={t("settings.provider.dictionary.listEmpty")} />
            )
          ) : (
            <ul className="settings-provider-list">
              {rows.map((candidate) => {
                const name = candidate.display_name || candidate.group_ref;
                return (
                  <li key={candidate.group_ref} className="settings-provider-list-item">
                    <div className="tw:flex tw:min-w-0 tw:flex-1 tw:items-baseline tw:gap-2">
                      <span className="settings-provider-truncate text-body" title={name}>{name}</span>
                      {candidate.display_name ? (
                        <span className="settings-provider-muted text-caption" title={candidate.group_ref}>{candidate.group_ref}</span>
                      ) : null}
                      {!candidate.available ? (
                        <StatePill tone="offline">{t("settings.provider.dictionary.statusRetired")}</StatePill>
                      ) : candidate.source === "collected" ? (
                        <StatePill tone="info">{t("settings.provider.dictionary.statusCollected")}</StatePill>
                      ) : null}
                    </div>
                    <PinScoreControl
                      name={name}
                      value={pinnedScoreOf(policy, candidate.group_ref)}
                      onChange={(score) => onChange(withPinnedScore(policy, candidate.group_ref, score))}
                    />
                    {candidate.source === "user" ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={t("settings.provider.dictionary.remove", { name })}
                        onClick={() => setPendingDelete(candidate)}
                      >
                        <X size={14} />
                      </Button>
                    ) : null}
                  </li>
                );
              })}
            </ul>
          )}
          {list.hasNextPage && !listError ? (
            <Button
              size="sm"
              variant="ghost"
              className="tw:self-center"
              loading={list.isFetchingNextPage}
              onClick={() => void list.fetchNextPage()}
            >
              {t("settings.provider.dictionary.loadMore")}
            </Button>
          ) : null}
          {deleteError && <Alert tone="error" title={deleteError} />}
          <p className="settings-provider-description text-caption">{t("settings.provider.dictionary.collectedNote")}</p>
        </div>
      </div>

      {pendingDelete && (
        <ConfirmDialog
          open
          onOpenChange={(next) => {
            if (!next) setPendingDelete(undefined);
          }}
          title={t("settings.provider.dictionary.removeConfirmTitle")}
          danger
          confirmLabel={t("settings.provider.dictionary.removeConfirm")}
          onConfirm={() => void removeEntry(pendingDelete)}
          description={
            <>
              <p>{t("settings.provider.dictionary.removeConfirmLine1", { name: pendingName })}</p>
              <p>{t("settings.provider.dictionary.removeConfirmLine2")}</p>
            </>
          }
        />
      )}
    </SettingsEditDialog>
  );
}

// The discover modal: the only network entry. It accepts only a subgroup key (a Mikan id / AniBT
//   slug / group page link) and rejects any name (SUBGROUP_DISCOVER_KEY_REQUIRED). Keys already in
//   the dictionary cost zero requests; only absent ones confirm online, and a confirmed subgroup
//   writes into the dictionary's user layer. An empty result is a normal outcome, not an error.
//   It touches only the dictionary — never a new RSS source, never any anime's group selection.
function SubgroupDiscoverDialog({
  open,
  onOpenChange,
  provider,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  provider: ProviderKey;
}) {
  const label = LABELS[provider];
  const queryClient = useQueryClient();
  const user = useUserDictionary(provider);
  const [query, setQuery] = useState("");
  const [searching, setSearching] = useState(false);
  const [results, setResults] = useState<SubgroupGroupCandidate[]>();
  const [searchError, setSearchError] = useState<string>();
  // Keys already in the user layer before discovery. The response carries no was-just-written
//   marker, so the pre-discovery snapshot distinguishes "newly added to the dictionary" from
//   "already in the dictionary".
  const knownRef = useRef<Set<string>>(new Set());

  useEffect(() => {
    if (!open) return;
    setQuery("");
    setResults(undefined);
    setSearchError(undefined);
  }, [open]);

  async function runSearch() {
    const text = query.trim();
    if (text === "") return;
    knownRef.current = new Set((user.data?.candidates ?? []).map((candidate) => candidate.group_ref));
    setSearching(true);
    setSearchError(undefined);
    setResults(undefined);
    try {
      const list = await retrieveSubgroupGroupCandidate(provider, text);
      setResults(list.candidates);
      await queryClient.invalidateQueries({ queryKey: subgroupDictionaryKeys.provider(provider) });
    } catch (cause) {
      setSearchError(toUserMessage(cause, t("settings.provider.dictionary.searchFailed")));
    } finally {
      setSearching(false);
    }
  }

  function statusOf(candidate: SubgroupGroupCandidate): { label: string; tone: "offline" | "info" | "online" | "muted" } {
    if (!candidate.available) return { label: t("settings.provider.dictionary.statusRetired"), tone: "offline" };
    if (candidate.source === "collected") return { label: t("settings.provider.dictionary.statusCollected"), tone: "info" };
    return knownRef.current.has(candidate.group_ref)
      ? { label: t("settings.provider.dictionary.statusExisting"), tone: "muted" }
      : { label: t("settings.provider.dictionary.statusAdded"), tone: "online" };
  }

  return (
    <SettingsEditDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t("settings.provider.dictionary.discoverTitle", { label })}
      description={t("settings.provider.dictionary.discoverHint")}
    >
      <div className="settings-provider-form">
        <div className="settings-provider-form-section">
          <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
            <div className="tw:min-w-[12rem] tw:flex-1">
              <Input
                value={query}
                aria-label={t("settings.provider.dictionary.searchAria", { label })}
                placeholder={t(provider === "mikan" ? "settings.provider.dictionary.searchPlaceholderMikan" : "settings.provider.dictionary.searchPlaceholderAnibt")}
                onChange={(event) => {
                  setQuery(event.target.value);
                  setSearchError(undefined);
                }}
                onKeyDown={(event) => {
                  if (event.key !== "Enter") return;
                  event.preventDefault();
                  void runSearch();
                }}
              />
            </div>
            <Button variant="primary" size="sm" disabled={searching || query.trim() === ""} onClick={() => void runSearch()}>
              {searching ? t("settings.provider.dictionary.searching") : t("settings.provider.dictionary.search")}
            </Button>
          </div>
          {searchError && <Alert tone="error" title={searchError} />}
        </div>

        {results !== undefined && (
          <div className="settings-provider-form-section">
            <h4 className="settings-provider-card-title text-caption">{t("settings.provider.dictionary.resultsTitle")}</h4>
            {results.length === 0 ? (
              <p className="settings-provider-description text-caption">{t("settings.provider.dictionary.searchEmpty")}</p>
            ) : (
              <ul className="settings-provider-list">
                {results.map((candidate) => {
                  const name = candidate.display_name || candidate.group_ref;
                  const status = statusOf(candidate);
                  return (
                    <li key={candidate.group_ref} className="settings-provider-list-item">
                      <div className="tw:flex tw:min-w-0 tw:flex-1 tw:items-baseline tw:gap-2">
                        <span className="settings-provider-truncate text-body" title={name}>{name}</span>
                        {candidate.display_name ? (
                          <span className="settings-provider-muted text-caption" title={candidate.group_ref}>{candidate.group_ref}</span>
                        ) : null}
                      </div>
                      <StatePill tone={status.tone}>{status.label}</StatePill>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        )}
      </div>
    </SettingsEditDialog>
  );
}

// The name scoring card: edits keywords plus adjustments (-100..100) inline, into the page draft,
//   saved by the settings dialog footer's single section-wide CAS. Row validation aligns with the
//   backend's ValidateNameKeywordRules: non-empty keywords, integer scores of -100..100,
//   case-insensitive keyword dedup; an illegal row is flagged inline, never silently dropped. Its
//   scope follows the roster mode, read here once as policy.mode.
function NameKeywordRulesCard({
  provider,
  rules,
  mode,
  onChange,
}: {
  provider: ProviderKey;
  rules: NameKeywordRule[];
  mode: PolicyMode;
  onChange: (rules: NameKeywordRule[]) => void;
}) {
  const label = LABELS[provider];

  function updateRule(index: number, patch: Partial<NameKeywordRule>) {
    onChange(rules.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)));
  }

  function removeRule(index: number) {
    onChange(rules.filter((_, i) => i !== index));
  }

  function addRule() {
    onChange([...rules, { keyword: "", score: 0 }]);
  }

  const seen = new Set<string>();
  const rowErrors = rules.map((rule) => {
    const keyword = rule.keyword.trim();
    if (keyword === "") return t("settings.provider.rules.keywordEmpty");
    if (keyword.length > 256) return t("settings.provider.rules.keywordTooLong");
    if (!Number.isInteger(rule.score) || rule.score < -100 || rule.score > 100)
      return t("settings.site.scoreRange");
    const key = keyword.toLowerCase();
    if (seen.has(key)) return t("settings.provider.rules.keywordDuplicate");
    seen.add(key);
    return undefined;
  });

  return (
    <div className="settings-provider-card">
      <div className="settings-provider-header">
        <h3 className="settings-provider-card-title text-body">{t("settings.provider.rules.title")}</h3>
        <Button variant="ghost" size="sm" aria-label={t("settings.provider.rules.addAria", { label })} onClick={addRule}>
          <Plus size={14} />
          {t("settings.provider.rules.add")}
        </Button>
      </div>
      <p className="settings-provider-description text-caption">
        {t("settings.provider.rules.description")}
        <HelpTip label={t("settings.provider.rules.helpLabel")} className="tw:ml-1 tw:align-middle">{t("settings.provider.rules.help")}</HelpTip>
      </p>
      <p className="settings-provider-description text-caption">
        {t(mode === "blacklist" ? "settings.provider.rules.scopeBlacklist" : "settings.provider.rules.scopeWhitelist")}
      </p>
      {rules.length === 0 ? (
        <StateBlock kind="empty" title={t("settings.provider.rules.empty")} />
      ) : (
        <div className="settings-provider-rule-list">
          {rules.map((rule, index) => (
            <div key={index} className="settings-provider-rule-row">
              <div className="settings-provider-rule-controls">
                <div className="settings-provider-rule-keyword">
                  <Input
                    aria-label={t("settings.provider.rules.keywordAria", { label, index: index + 1 })}
                    placeholder={t("settings.provider.rules.keywordPlaceholder")}
                    value={rule.keyword}
                    onChange={(event) => updateRule(index, { keyword: event.target.value })}
                  />
                </div>
                <div className="settings-provider-rule-score">
                  <Input
                    aria-label={t("settings.provider.rules.scoreAria", { label, index: index + 1 })}
                    type="number"
                    min={-100}
                    max={100}
                    step={1}
                    value={Number.isNaN(rule.score) ? "" : String(rule.score)}
                    onChange={(event) =>
                      updateRule(index, {
                        score: event.target.value.trim() === "" ? Number.NaN : Number(event.target.value),
                      })
                    }
                  />
                </div>
                <Button variant="ghost" size="sm" aria-label={t("settings.provider.rules.removeAria", { label, index: index + 1 })} onClick={() => removeRule(index)}>
                  <X size={14} />
                </Button>
              </div>
              {rowErrors[index] && (
                <p role="alert" className="settings-provider-rule-error text-caption">
                  {rowErrors[index]}
                </p>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// The subgroup scoring card (edited in place): mode + base score + roster all write the page draft
//   directly, saved by the settings dialog footer's single CAS.
//   The roster may only be picked from the dictionary: the search box filters dictionary candidates
//   only (list…?q=, a local read), and clicking a result adds its group_ref to the draft — no free
//   text entry and no "add" button (search lives on the dictionary card).
//   A roster entry's display name resolves through useDictionaryNames; when unresolvable only the
//   key shows, with no error and no spinner.
function ProviderGroupPolicyCard({
  provider,
  policy,
  onChange,
  onOpenDictionary,
}: {
  provider: ProviderKey;
  policy: ProviderGroupDefault;
  onChange: (policy: ProviderGroupDefault) => void;
  onOpenDictionary: () => void;
}) {
  const label = LABELS[provider];
  const [search, setSearch] = useState("");
  // A just-typed illegal base score; undefined means the input reflects the draft value directly.
  const [pendingScore, setPendingScore] = useState<string>();
  const scoreInput = pendingScore ?? String(policy.score);
  const scoreValid = pendingScore === undefined;

  const query = search.trim();
  const searchResult = useQuery({
    queryKey: subgroupDictionaryKeys.search(provider, query),
    queryFn: () => listSubgroupGroupCandidates(provider, { q: query }),
    enabled: query !== "",
  });
  const candidates = useMemo(() => searchResult.data?.candidates ?? [], [searchResult.data]);
  const nameCache = useDictionaryNames(provider, policy.groups, candidates);

  function addGroup(key: string) {
    if (!policy.groups.includes(key)) onChange({ ...policy, groups: [...policy.groups, key] });
  }

  function removeGroup(key: string) {
    onChange({ ...policy, groups: policy.groups.filter((group) => group !== key) });
  }

  return (
    <div className="settings-provider-card" aria-label={t("settings.provider.groups.dialogTitle", { label })} role="group">
      <div className="settings-provider-header">
        <h3 className="settings-provider-card-title text-body">{t("settings.provider.groups.title")}</h3>
      </div>
      <div className="settings-provider-form">
        <div className="settings-provider-form-section">
          <Segmented<PolicyMode>
            aria-label={t("settings.provider.groups.modeAria")}
            value={policy.mode}
            onValueChange={(mode) => onChange({ ...policy, mode })}
            options={[
              { value: "blacklist", label: t("settings.provider.groups.modeBlacklist") },
              { value: "whitelist", label: t("settings.provider.groups.modeWhitelist") },
            ]}
          />
          <p className="settings-provider-description text-caption">
            {t(policy.mode === "blacklist" ? "settings.provider.groups.blacklistHelp" : "settings.provider.groups.whitelistHelp")}
          </p>
        </div>
        <label className="settings-provider-field text-caption">
          <span>{t("settings.site.defaultScoreRange")}</span>
          <Input
            inputMode="numeric"
            value={scoreInput}
            fullWidth={false}
            className="tw:w-24"
            aria-invalid={!scoreValid ? true : undefined}
            onChange={(event) => {
              const text = event.target.value;
              const parsed = text.trim() === "" ? Number.NaN : Number(text);
              if (Number.isInteger(parsed) && parsed >= 1 && parsed <= 100) {
                setPendingScore(undefined);
                onChange({ ...policy, score: parsed });
              } else {
                setPendingScore(text);
              }
            }}
          />
        </label>
        {!scoreValid && <p role="alert" className="settings-provider-error text-caption">{t("settings.site.defaultScoreInvalid")}</p>}

        <div className="settings-provider-form-section">
          <label className="settings-provider-field text-caption">
            <span>{t("settings.provider.groups.listTitle")}</span>
            <Input
              value={search}
              placeholder={t("settings.provider.groups.searchPlaceholder")}
              aria-label={t("settings.provider.groups.searchAria", { label })}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          {query !== "" &&
            (candidates.length > 0 ? (
              <ul className="settings-provider-list">
                {candidates.map((candidate) => {
                  const name = candidate.display_name || candidate.group_ref;
                  const inList = policy.groups.includes(candidate.group_ref);
                  return (
                    <li key={candidate.group_ref} className="settings-provider-list-item">
                      <Button
                        variant="ghost"
                        size="sm"
                        className="tw:flex tw:min-w-0 tw:flex-1 tw:items-center tw:justify-start tw:gap-2 tw:text-left"
                        aria-label={t("settings.provider.groups.resultAria", { name })}
                        disabled={inList}
                        onClick={() => addGroup(candidate.group_ref)}
                      >
                        <span className="settings-provider-truncate text-body" title={name}>{name}</span>
                        <span className="settings-provider-muted text-caption" title={candidate.group_ref}>{candidate.group_ref}</span>
                        {inList ? <span className="settings-provider-muted text-caption">{t("settings.provider.groups.inList")}</span> : null}
                      </Button>
                    </li>
                  );
                })}
              </ul>
            ) : searchResult.isFetching ? null : (
              <div className="settings-provider-form-section">
                <p className="settings-provider-description text-caption">{t("settings.provider.groups.searchEmpty")}</p>
                <div>
                  <Button variant="secondary" size="sm" onClick={onOpenDictionary}>
                    {t("settings.provider.groups.goToDictionary")}
                  </Button>
                </div>
              </div>
            ))}
        </div>

        {policy.groups.length > 0 ? (
          // The roster is the set of excluded/allowed groups, not a per-item editing table: rendered as
//           wrap-around badges, one per group, with X removing it from the draft. Out of a dictionary
//           of several hundred groups only a few are picked, so a horizontal layout saves space and
//           shows the roster's length at a glance.
          <ul
            className="tw:m-0 tw:flex tw:list-none tw:flex-wrap tw:items-center tw:gap-1.5 tw:p-0"
            aria-label={t("settings.provider.groups.listTitle")}
          >
            {policy.groups.map((key) => {
              const name = nameCache.get(key);
              const primary = name ? name : key;
              return (
                <li
                  key={key}
                  className="tw:inline-flex tw:max-w-full tw:items-center tw:gap-1 tw:rounded-[0.25rem] tw:border tw:border-border tw:bg-muted/60 tw:px-2 tw:py-0.5 tw:text-xs tw:font-medium tw:leading-normal"
                >
                  <span className="tw:truncate" title={primary}>{primary}</span>
                  {name ? <span className="tw:shrink-0 tw:text-muted-foreground" title={key}>{key}</span> : null}
                  <button
                    type="button"
                    aria-label={t("settings.provider.groups.remove", { name: primary })}
                    onClick={() => removeGroup(key)}
                    className="tw:grid tw:shrink-0 tw:place-items-center tw:rounded-[2px] tw:text-muted-foreground tw:hover:text-foreground tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring"
                  >
                    <X size={12} aria-hidden />
                  </button>
                </li>
              );
            })}
          </ul>
        ) : (
          <p className="settings-provider-description text-caption">{t("settings.provider.groups.listEmpty")}</p>
        )}
      </div>
    </div>
  );
}

// Catalog auto-refresh: a single provider's toggles (PUT
//   /catalog/providers/:provider/seasons/:season/preference, If-Match CAS), part of the page draft
//   (part "catalog"), committed by the section footer's single save; a 409 goes through the shared
//   ConflictBlock. The toggles cover both this and next season; the refresh interval is decided only
//   by automation's catalog refresh plan, and the preference's schedule is preserved as-is.
function CatalogRefreshPreferenceRow({ provider }: { provider: ProviderKey }) {
  const label = LABELS[provider];
  const queryClient = useQueryClient();
  const location = useLocation();
  const season = currentSeason();
  const preferenceKey = catalogKeys.preference(provider, season);
  const preference = useQuery({
    queryKey: preferenceKey,
    queryFn: () => readCatalogPreference(provider, season),
  });
  const [draft, setDraft] = useState<boolean>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();
  const savedEnabled = preference.data?.refresh_enabled ?? false;
  const dirty = preference.data !== undefined && draft !== undefined && draft !== savedEnabled;

  async function save() {
    const current = preference.data;
    if (!current || draft === undefined) return { ok: true as const };
    setSaving(true);
    setError(undefined);
    setConflict(undefined);
    try {
      const stored = await setCatalogPreference(
        provider,
        season,
        { season, refresh_enabled: draft, schedule: current.schedule ?? null },
        current.version,
      );
      queryClient.setQueryData(preferenceKey, stored);
      void queryClient.invalidateQueries({ queryKey: preferenceKey });
      return { ok: true as const, message: t("settings.provider.refresh.saved", { label }) };
    } catch (cause) {
      if (isVersionConflict(cause)) {
        setConflict(cause.currentVersion);
      }
      setError(toUserMessage(cause, t("settings.provider.refresh.saveFailed")));
      return { ok: false as const };
    } finally {
      setSaving(false);
    }
  }
  useSavePart("catalog", { count: dirty ? 1 : 0, label: t("settings.provider.refresh.title"), busy: saving, save });

  // On a 409 refresh: refetch the preference (version taken from the query data) while the local choice is kept, then save once more.
  function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    void queryClient.invalidateQueries({ queryKey: preferenceKey });
  }

  return (
    <div className="settings-provider-card">
      <div className="settings-provider-header">
        <h3 className="settings-provider-card-title text-body">{t("settings.provider.refresh.title")}</h3>
        <span className="settings-provider-muted text-caption">{seasonLabel(season)}</span>
      </div>
      <p className="settings-provider-description text-caption">
        {t("settings.provider.refresh.description")}
        {" · "}
        <Link to="/settings/automation" replace state={location.state} className="tw:text-primary tw:underline-offset-2 tw:hover:underline">
          {t("settings.provider.refresh.scheduleLink")}
        </Link>
      </p>
      {conflict !== undefined ? (
        <ConflictBlock currentVersion={conflict} message={error} onRefresh={refreshAfterConflict} />
      ) : (
        error && <Alert tone="error" title={error} />
      )}
      {preference.isLoading && (
        <StateBlock kind="busy" title={t("settings.provider.refresh.loading")} />
      )}
      {preference.error && <Alert tone="error" title={toUserMessage(preference.error, t("settings.provider.refresh.loadFailed"))} />}
      {preference.data && (
        <SwitchRow
          label={t("settings.provider.refresh.toggleAria", { label })}
          checked={draft ?? savedEnabled}
          disabled={saving}
          onCheckedChange={setDraft}
        />
      )}
    </div>
  );
}

// The hidden-anime card: statistics filter to the current provider (useBlacklistState entries
//   filtered to source === provider); manage switches to settings' hidden-anime section (which
//   groups by provider itself). The hidden list is an independently owned immediate-save resource,
//   never the page draft; unsaved changes here are caught by SettingsDialog's departing
//   confirmation.
function BlacklistCard({ provider }: { provider: ProviderKey }) {
  const label = LABELS[provider];
  const { entries, error } = useBlacklistState();
  const navigate = useNavigate();
  const location = useLocation();
  const count = entries.filter((entry) => entry.source === provider).length;

  return (
    <div className="settings-provider-card">
      <div className="settings-provider-header">
        <h3 className="settings-provider-card-title text-body">{t("settings.provider.blacklist.title")}</h3>
        <div className="settings-provider-actions">
          <span className="settings-provider-muted text-caption">{t("settings.provider.blacklist.count", { label, n: count })}</span>
          <Button variant="ghost" size="sm" onClick={() => navigate("/settings/hidden", { replace: true, state: location.state })}>
            {t("settings.provider.blacklist.manage")}
          </Button>
        </div>
      </div>
      <p className="settings-provider-description text-caption">
        {t("settings.provider.blacklist.description")}
        <HelpTip label={t("settings.provider.blacklist.helpLabel")} className="tw:ml-1 tw:align-middle">{t("settings.provider.blacklist.help")}</HelpTip>
      </p>
      {error ? <Alert tone="error" title={toUserMessage(error, t("settings.provider.blacklist.loadFailed"))} /> : null}
    </div>
  );
}
