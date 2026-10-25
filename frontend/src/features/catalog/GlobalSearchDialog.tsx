import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";
import {
  searchProviderCatalog,
  toUserMessage,
  type CatalogItem,
} from "../../lib/api/client";
import { blacklistKey, useBlacklist } from "../../lib/blacklist";
import { getCatalogProvider, providerLabel } from "../../lib/providers/registry";
import { BrowseCard } from "./card";
import { mapCatalogItem } from "../../lib/providers/catalogProvider";
import type { CatalogProvider } from "../../lib/providers/port";
import type { CatalogEntryView, ProviderKey } from "../../lib/providers/viewmodels";
import { catalogKeys } from "./queries";
import { StateBlock } from "../../ui/StateBlock";
import { Dialog, Input } from "../../ui/base";
import { Button } from "../../ui/base/controls";
import { loadAnimeConfigDialog } from "../anime-config/loadAnimeConfigDialog";
import { loadOneShotDialog } from "../oneshot/loadOneShotDialog";
import { t } from "../../lib/i18n";

const AnimeConfigDialogEntry = lazy(loadAnimeConfigDialog);
const OneShotDialogEntry = lazy(loadOneShotDialog);

// GlobalSearchDialog — the app's single anime search entry (the top-bar review: the catalog toolbar
//   no longer has search). It reuses the provider keyword-search contract (searchProviderCatalog).
//   Only AniBT supports keyword search (Mikan answers 422 SEARCH_NOT_SUPPORTED), so there is no
//   single-option site dropdown — one line under the query field says search within AniBT, going
//   back to a selector once another site joins. Results are catalog entry candidates (not torrents);
//   clicking a candidate walks AnimeGrid's result->config path: mapCatalogItem maps the entry and
//   hands it to the same AnimeConfigDialogEntry (the draft derives from its internal draftFromEntry),
//   with no duplicated config dialog.
//   Results render as browse-area poster cards (BrowseCard): a card click opens the config dialog
//   (where subscribing completes), and the strip's browse icon is the other path — it opens the
//   one-shot download entry (the two-step dialog) without ever entering subscription configuration.
//   Hidden anime never appear in results (matching the catalog columns); they are viewable only in
//   settings' hidden-anime section.
//   Closing the dialog discards the transient search context; after a successful subscription the
//   catalog query invalidates so the catalog page reflects the latest state.

// Providers supporting keyword search (the declared list).
const SEARCH_PROVIDER = "anibt" satisfies ProviderKey;

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
  const [keyword, setKeyword] = useState("");
  const [searching, setSearching] = useState(false);
  const [error, setError] = useState<string>();
  const [items, setItems] = useState<CatalogItem[]>([]);
  // idle (never searched) and empty (searched, no results) are two distinct empty states.
  const [searched, setSearched] = useState(false);
  // The request sequence: a slow response must never overwrite a newer search's results.
  const requestSeq = useRef(0);
  const [configTarget, setConfigTarget] = useState<CandidateTarget | null>(null);
  const hidden = useBlacklist();
  const shownItems = useMemo(() => {
    const hiddenKeys = new Set(hidden.map((entry) => entry.key));
    return items.filter((item) => !hiddenKeys.has(blacklistKey(item.provider, item.provider_entry_ref)));
  }, [hidden, items]);
  const [seedTarget, setSeedTarget] = useState<CandidateTarget | null>(null);

  // Closing discards the transient context (query/results/error); in-flight requests are dropped with it.
  useEffect(() => {
    if (open) return;
    requestSeq.current += 1;
    setKeyword("");
    setItems([]);
    setError(undefined);
    setSearching(false);
    setSearched(false);
  }, [open]);

  async function runSearch() {
    const q = keyword.trim();
    if (!q || searching) return;
    const seq = ++requestSeq.current;
    setSearching(true);
    setError(undefined);
    try {
      const result = await searchProviderCatalog(SEARCH_PROVIDER, q);
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
  }

  function openCandidate(item: CatalogItem) {
    const providerKey = item.provider;
    setConfigTarget({
      providerKey,
      provider: getCatalogProvider(providerKey),
      entry: mapCatalogItem(item, providerKey),
    });
    onOpenChange(false);
  }

  // The same mapping path as a candidate click, but aimed at browse seeds; the search dialog closes
//   too, so dialogs never stack.
  function openSeeds(item: CatalogItem) {
    const providerKey = item.provider;
    setSeedTarget({
      providerKey,
      provider: getCatalogProvider(providerKey),
      entry: mapCatalogItem(item, providerKey),
    });
    onOpenChange(false);
  }

  function handleSubscribed() {
    void queryClient.invalidateQueries({ queryKey: catalogKeys.all });
  }

  return (
    <>
      <Dialog open={open} onRequestClose={() => onOpenChange(false)} tier="M" fixedHeight title={t("search.title")}>
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
                placeholder={t("globalSearch.placeholder")}
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
          <Button variant="primary" type="submit" disabled={searching || !keyword.trim()}>
            {searching ? t("grid.searching") : t("grid.search")}
          </Button>
        </form>
        <p className="tw:mt-2 tw:mb-0 tw:text-xs tw:text-muted-foreground">{t("search.inSite", { site: providerLabel(SEARCH_PROVIDER) })}</p>

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
            onOpenRecords={onOpenRecords}
          />
        </Suspense>
      )}
    </>
  );
}
