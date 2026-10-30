import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";
import { fetchSeedSearchSnapshot, listConnections, searchProviderCatalog, toUserMessage, type CatalogItem } from "../../lib/api/client";
import { blacklistKey, useBlacklist } from "../../lib/blacklist";
import { getCatalogProvider, providerLabel, searchProviderLabel } from "../../lib/providers/registry";
import { BrowseCard } from "./card";
import { useOpenLiveSubscription } from "./useOpenLiveSubscription";
import { mapCatalogItem } from "../../lib/providers/catalogProvider";
import type { CatalogProvider } from "../../lib/providers/port";
import type { CatalogEntryView, ProviderKey, SearchProviderKey } from "../../lib/providers/viewmodels";
import { catalogKeys } from "./queries";
import { configKeys } from "../../lib/queryKeys";
import { StateBlock } from "../../ui/StateBlock";
import { Dialog, Input, Segmented } from "../../ui/base";
import { Button } from "../../ui/base/controls";
import { loadAnimeConfigDialog } from "../anime-config/loadAnimeConfigDialog";
import { loadOneShotDialog } from "../oneshot/loadOneShotDialog";
import type { SearchSeedBrowse } from "../oneshot/useOneShotSeeds";
import { t } from "../../lib/i18n";

const AnimeConfigDialogEntry = lazy(loadAnimeConfigDialog);
const OneShotDialogEntry = lazy(loadOneShotDialog);

// GlobalSearchDialog — the app's single search entry (the top-bar review: the catalog toolbar
//   no longer has search). One dialog, two modes, program search covers AniBT / Mikan, torrent search also Nyaa:
//   - 番剧 mode reuses the provider keyword-search contract (searchProviderCatalog, a pure read).
//     Results are catalog entry candidates; a card click walks AnimeGrid's result->config path
//     (mapCatalogItem + the same AnimeConfigDialogEntry, where subscribing completes), and the
//     strip's browse icon opens the one-shot download entry. Hidden anime never appear in results.
//   - 种子 mode runs the provider torrent search: one POST persists the keyword result as a
//     short-lived search snapshot. A successful search opens the standard one-shot download flow
//     directly (OneShotDialog with the search snapshot as its seed source; step one lists the
//     snapshot's whole window with select/expand) — no intermediate result list is rendered,
//     because every row of it opened the same dialog. An empty result stays here as the no-match
//     empty state. Torrents found by search are never a subscription source.
//   Both one-shot entries (a candidate's browse icon and a direct search) get a back affordance:
//   back reopens this dialog — the candidate path with its program results intact, the keyword
//   path back at the seed input window (the search runs again from there).
//   Closing the search dialog discards the transient search context; after a successful
//   subscription the catalog query invalidates so the catalog page reflects the latest state.

type SearchMode = "program" | "seed";
// Program search reads catalogs, which only AniBT and Mikan have; torrent search adds Nyaa.
type SearchSite = SearchProviderKey;

const PROGRAM_SEARCH_PROVIDERS = ["anibt", "mikan"] as const;
const SEED_SEARCH_PROVIDERS: readonly SearchSite[] = ["anibt", "mikan", "nyaa"];

type CandidateTarget = {
  providerKey: ProviderKey;
  provider: CatalogProvider;
  entry: CatalogEntryView;
};

export function GlobalSearchDialog({
  open,
  onOpenChange,
  onOpenRecords,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Jumps to the execution record from the one-shot download entry; the dialog renders no jump when the host has not wired it. */
  onOpenRecords?: () => void;
}) {
  useEffect(() => {
    void loadAnimeConfigDialog();
    void loadOneShotDialog();
  }, []);

  const queryClient = useQueryClient();
  const { openLiveSubscription, openSubscriptionById } = useOpenLiveSubscription();
  const [keyword, setKeyword] = useState("");
  const [mode, setMode] = useState<SearchMode>("program");
  const [site, setSite] = useState<SearchSite>("anibt");
  const connections = useQuery({ queryKey: configKeys.connections, queryFn: listConnections });
  const jackettAvailable = Boolean(
    connections.data?.some((connection) => connection.kind === "jackett" && connection.enabled && connection.credential_configured),
  );
  const seedProviders = useMemo<readonly SearchSite[]>(
    () => (jackettAvailable ? [...SEED_SEARCH_PROVIDERS, "jackett"] : SEED_SEARCH_PROVIDERS),
    [jackettAvailable],
  );

  // 番剧 mode: idle (never searched) and empty (searched, no results) are two distinct empty states.
  const [searching, setSearching] = useState(false);
  const [error, setError] = useState<string>();
  const [items, setItems] = useState<CatalogItem[]>([]);
  const [searched, setSearched] = useState(false);

  // 种子 mode: one POST per query; a successful search hands the whole snapshot to the standard
  //   one-shot flow, so only the busy/error/no-match states live here.
  const [seedSearching, setSeedSearching] = useState(false);
  const [seedError, setSeedError] = useState<string>();
  const [seedNoMatch, setSeedNoMatch] = useState(false);

  // The request sequence: a slow response must never overwrite a newer search's results.
  const requestSeq = useRef(0);
  const [configTarget, setConfigTarget] = useState<CandidateTarget | null>(null);
  const [seedTarget, setSeedTarget] = useState<CandidateTarget | null>(null);
  const [searchSeedTarget, setSearchSeedTarget] = useState<SearchSeedBrowse | null>(null);
  const hidden = useBlacklist();
  const shownItems = useMemo(() => {
    const hiddenKeys = new Set(hidden.map((entry) => entry.key));
    return items.filter((item) => !hiddenKeys.has(blacklistKey(item.provider, item.provider_entry_ref)));
  }, [hidden, items]);

  // Closing discards the transient context (query/results/error); in-flight requests are dropped
  // with it. A second-level dialog opened from here suppresses the discard: its back button
  // reopens this window with its context intact, and the discard runs once that dialog is gone.
  useEffect(() => {
    if (open || configTarget || seedTarget || searchSeedTarget) return;
    requestSeq.current += 1;
    setKeyword("");
    setItems([]);
    setError(undefined);
    setSearching(false);
    setSearched(false);
    setSeedSearching(false);
    setSeedError(undefined);
    setSeedNoMatch(false);
  }, [open, configTarget, seedTarget, searchSeedTarget]);

  function resetResults() {
    requestSeq.current += 1;
    setItems([]);
    setError(undefined);
    setSearching(false);
    setSearched(false);
    setSeedSearching(false);
    setSeedError(undefined);
    setSeedNoMatch(false);
  }

  // Jackett is a configured source, not a built-in provider. If its last usable connection is
  // disabled or loses its credential, leave it immediately instead of offering a dead search.
  useEffect(() => {
    if (site !== "jackett" || connections.data === undefined || jackettAvailable) return;
    setSite("anibt");
    resetResults();
  }, [connections.data, jackettAvailable, site]);

  async function runSearch() {
    const q = keyword.trim();
    if (!q) return;
    // The busy guard runs before a sequence number is taken: a second submit while a request
    // is in flight is a no-op, never an invalidation that strands the in-flight response
    // (its seq would stop matching and `searching` would never clear).
    if (mode === "program" ? searching : seedSearching) return;
    const seq = ++requestSeq.current;
    if (mode === "program") {
      setSearching(true);
      setError(undefined);
      try {
        const result = await searchProviderCatalog(site === "mikan" ? "mikan" : "anibt", q);
        if (seq !== requestSeq.current) return;
        setItems(result);
        setSearched(true);
      } catch (cause) {
        if (seq !== requestSeq.current) return;
        setItems([]);
        setSearched(true);
        setError(toUserMessage(cause, t("grid.searchFailed")));
      } finally {
        if (seq === requestSeq.current) setSearching(false);
      }
      return;
    }
    // The fetch response's own seed window only decides emptiness: the one-shot flow reads the
    // persisted snapshot (the site's whole search window, at most 100 rows) through the read model.
    setSeedSearching(true);
    setSeedError(undefined);
    setSeedNoMatch(false);
    try {
      const view = await fetchSeedSearchSnapshot(site, { query: q });
      if (seq !== requestSeq.current) return;
      if (view.seeds.length === 0) {
        setSeedNoMatch(true);
        return;
      }
      // No intermediate result list: every row of it opened the same dialog, so a successful
      // search hands the whole snapshot to the standard one-shot flow and closes this window
      // (back from there returns to the seed input window).
      setSearchSeedTarget({ provider: site, query: q, snapshot: view });
      onOpenChange(false);
    } catch (cause) {
      if (seq !== requestSeq.current) return;
      setSeedError(toUserMessage(cause, t("grid.searchFailed")));
    } finally {
      if (seq === requestSeq.current) setSeedSearching(false);
    }
  }

  function openCandidate(item: CatalogItem) {
    const providerKey = item.provider;
    // An entry with a live subscription opens it (the route change closes this dialog); only an
    // entry with none, or only retired ones, opens a candidate draft.
    if (openLiveSubscription(mapCatalogItem(item, providerKey))) return;
    setConfigTarget({
      providerKey,
      provider: getCatalogProvider(providerKey),
      entry: mapCatalogItem(item, providerKey),
    });
    onOpenChange(false);
  }

  // The same mapping path as a candidate click, but aimed at browse seeds; the search dialog closes
  //   too, so dialogs never stack. Back from the one-shot flow reopens this dialog with the
  //   program results intact (the results live on, unlike the keyword path, which has no list).
  function openSeeds(item: CatalogItem) {
    const providerKey = item.provider;
    setSeedTarget({
      providerKey,
      provider: getCatalogProvider(providerKey),
      entry: mapCatalogItem(item, providerKey),
    });
    onOpenChange(false);
  }

  function backFromSeeds() {
    setSeedTarget(null);
    onOpenChange(true);
  }

  // The keyword path has no result list to return to: back lands on the seed input window (mode
  // and site stay as they were; the keyword is cleared for a fresh search).
  function backFromSearchSeeds() {
    setSearchSeedTarget(null);
    setKeyword("");
    setSeedNoMatch(false);
    onOpenChange(true);
  }

  function handleSubscribed() {
    void queryClient.invalidateQueries({ queryKey: catalogKeys.all });
  }

  const busy = mode === "program" ? searching : seedSearching;

  return (
    <>
      <Dialog open={open} onRequestClose={() => onOpenChange(false)} tier="M" fixedHeight title={t("search.title")}>
        <div className="tw:mb-2 tw:flex tw:flex-wrap tw:items-center tw:gap-2">
          <Segmented<SearchMode>
            size="compact"
            aria-label={t("globalSearch.modeAria")}
            value={mode}
            onValueChange={(next) => {
              if (next === mode) return;
              setMode(next);
              // Nyaa and Jackett only exist for torrent search: program search falls back to AniBT.
              if (next === "program" && (site === "nyaa" || site === "jackett")) setSite("anibt");
              resetResults();
            }}
            options={[
              { value: "program", label: t("globalSearch.modeProgram") },
              { value: "seed", label: t("globalSearch.modeSeed") },
            ]}
          />
          <Segmented<SearchSite>
            size="compact"
            aria-label={t("globalSearch.providerAria")}
            value={site}
            onValueChange={(next) => {
              if (next === site) return;
              setSite(next);
              resetResults();
            }}
            options={(mode === "seed" ? seedProviders : PROGRAM_SEARCH_PROVIDERS).map((key) => ({ value: key, label: searchProviderLabel(key) }))}
          />
        </div>
        <form
          className="global-search-form"
          onSubmit={(event) => {
            event.preventDefault();
            void runSearch();
          }}
        >
          <div className="global-search-keyword">
            <div className="tw:relative">
              <Input
                aria-label={t("globalSearch.keywordAria")}
                placeholder={mode === "program" ? t("globalSearch.placeholder") : t("globalSearch.seedPlaceholder")}
                value={keyword}
                onChange={(event) => setKeyword(event.target.value)}
                className="tw:pr-8"
              />
              {keyword ? (
                <button
                  type="button"
                  aria-label={t("globalSearch.clearAria")}
                  onClick={() => setKeyword("")}
                  className="tw:absolute tw:top-1/2 tw:right-2 tw:grid tw:size-5 tw:-translate-y-1/2 tw:place-items-center tw:rounded-full tw:text-muted-foreground tw:hover:text-foreground"
                >
                  <X className="tw:size-3.5" />
                </button>
              ) : null}
            </div>
          </div>
          <Button variant="primary" type="submit" disabled={busy || !keyword.trim()}>
            {busy ? t("grid.searching") : t("grid.search")}
          </Button>
        </form>

        {mode === "program" ? (
          <div className="global-search-results" aria-live="polite">
            {error ? (
              <StateBlock
                kind="error"
                title={error}
                action={
                  <Button size="sm" disabled={searching} onClick={() => void runSearch()}>
                    {t("grid.retry")}
                  </Button>
                }
              />
            ) : searching ? (
              <StateBlock kind="busy" title={t("grid.searchingNow")} />
            ) : !searched ? (
              <StateBlock kind="empty" title={t("globalSearch.start")} />
            ) : shownItems.length === 0 ? (
              <StateBlock kind="empty" title={t("search.noMatch")} description={t("grid.searchRetryHint")} />
            ) : (
              <ul className="global-search-list">
                {shownItems.map((item) => {
                  const entry = mapCatalogItem(item, item.provider);
                  return (
                    <li key={`${item.provider}:${item.provider_entry_ref}`}>
                      <BrowseCard
                        entry={entry}
                        onOpenConfig={() => openCandidate(item)}
                        onBrowseSeeds={() => openSeeds(item)}
                      />
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        ) : (
          <div className="global-search-results" aria-live="polite">
            {seedError ? (
              <StateBlock
                kind="error"
                title={seedError}
                action={
                  <Button size="sm" disabled={seedSearching} onClick={() => void runSearch()}>
                    {t("grid.retry")}
                  </Button>
                }
              />
            ) : seedSearching ? (
              <StateBlock kind="busy" title={t("grid.searchingNow")} />
            ) : seedNoMatch ? (
              <StateBlock kind="empty" title={t("globalSearch.noMatchSeed")} description={t("grid.searchRetryHint")} />
            ) : (
              <StateBlock kind="empty" title={t("globalSearch.start")} description={t("globalSearch.seedHint")} />
            )}
          </div>
        )}
      </Dialog>

      {/* The config dialog after a candidate click: the same AnimeConfigDialogEntry AnimeGrid uses
       *  (the search dialog is already closed by the time the second level opens, so no z-order clash). */}
      {configTarget && (
        <Suspense fallback={null}>
          <AnimeConfigDialogEntry
            open
            onOpenChange={(next) => {
              if (!next) setConfigTarget(null);
            }}
            provider={configTarget.provider}
            entry={configTarget.entry}
            sourceLabel={providerLabel(configTarget.providerKey)}
            onSubscribed={handleSubscribed}
            onCandidateStateChanged={handleSubscribed}
            onOpenExisting={(id) => {
              setConfigTarget(null);
              openSubscriptionById(id);
            }}
          />
        </Suspense>
      )}
      {/* The candidate's browse seeds: another second-level dialog sitting alongside the config one. */}
      {seedTarget && (
        <Suspense fallback={null}>
          <OneShotDialogEntry
            key={`${seedTarget.providerKey}:${seedTarget.entry.providerEntryRef}`}
            open
            entry={seedTarget.entry}
            onClose={() => setSeedTarget(null)}
            onBack={backFromSeeds}
            onOpenRecords={onOpenRecords}
          />
        </Suspense>
      )}
      {/* A successful keyword search's one-shot download: the same two-step dialog, seeded from
       *  the search snapshot; back returns to the seed input window. */}
      {searchSeedTarget && (
        <Suspense fallback={null}>
          <OneShotDialogEntry
            key={`${searchSeedTarget.provider}:${searchSeedTarget.snapshot.snapshot_id}`}
            open
            search={searchSeedTarget}
            onClose={() => setSearchSeedTarget(null)}
            onBack={backFromSearchSeeds}
            onOpenRecords={onOpenRecords}
          />
        </Suspense>
      )}
    </>
  );
}
