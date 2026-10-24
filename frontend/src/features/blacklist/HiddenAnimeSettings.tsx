import { useMemo, useState } from "react";
import { Plus, RotateCcw } from "lucide-react";
import { toast } from "../../ui/toast";
import { Alert, Button, Input, Segmented } from "../../ui/base";
import { StateBlock } from "../../ui/StateBlock";
import { toUserMessage } from "../../lib/api/client";
import {
  useBlacklistActions,
  useBlacklistState,
  type BlacklistEntry,
  type SuppressibleProvider,
} from "../../lib/blacklist";
import { t } from "../../lib/i18n";
import { formatLocaleDate } from "../../lib/i18n/format";

// The only management surface for hidden anime (catalog suppressions): the "hidden anime"
//   section of settings (not in the account menu and no ?tool= route dialog). Grouped by site,
//   with a title filter; the inline "unhide" IS unsuppress. Two entry points exist: the hide action
//   on an anime card, or entering it directly here by site + site anime id (title optional,
//   defaulting to an ID placeholder). Data goes through the
//   listCatalogSuppressions / suppressCatalogEntry / unsuppressCatalogEntry contract API
//   (lib/blacklist.ts); localStorage is never involved.

const PROVIDER_ORDER = ["mikan", "anibt"] as const;
const PROVIDER_LABELS: Record<string, string> = { mikan: "Mikan", anibt: "AniBT" };

export function HiddenAnimeSettings() {
  const { entries, isLoading, error } = useBlacklistState();
  const { add, remove } = useBlacklistActions();
  const [filter, setFilter] = useState("");
  const [addProvider, setAddProvider] = useState<SuppressibleProvider>("mikan");
  const [addId, setAddId] = useState("");
  const [addTitle, setAddTitle] = useState("");
  const [addError, setAddError] = useState<string>();

  const grouped = useMemo(() => {
    const keyword = filter.trim().toLowerCase();
    const visible = keyword
      ? entries.filter((entry) => entry.title.toLowerCase().includes(keyword))
      : entries;
    // Only sites holding entries are listed: the empty state appears exactly once for the whole section (per the top-bar review).
    return PROVIDER_ORDER.map((provider) => ({
      provider,
      label: PROVIDER_LABELS[provider] ?? provider,
      items: visible.filter((entry) => entry.source === provider),
    })).filter((group) => group.items.length > 0);
  }, [entries, filter]);

  function onRestore(entry: BlacklistEntry) {
    void remove(entry)
      .then(() => toast.success(t("blacklist.restored", { title: entry.title })))
      .catch((cause: unknown) => toast.error(toUserMessage(cause, t("blacklist.restoreFailed"))));
  }

  // Direct entry by provider + program id: title optional, left empty it defaults to an ID
  //   placeholder; the server is idempotent — a repeated entry only refreshes the title snapshot.
  function onAddById() {
    const id = addId.trim();
    if (id === "") {
      setAddError(t("blacklist.idRequired"));
      return;
    }
    setAddError(undefined);
    const title = addTitle.trim() || id;
    void add({ source: addProvider, providerEntryRef: id, title })
      .then(() => {
        toast.success(t("blacklist.added", { title }));
        setAddId("");
        setAddTitle("");
      })
      .catch((cause: unknown) => toast.error(toUserMessage(cause, t("blacklist.addFailed"))));
  }

  return (
    <section className="settings-updates">
      <div className="settings-updates-header">
        <h2 className="settings-updates-title text-title">{t("blacklist.title")}</h2>
      </div>
      <p className="settings-updates-description text-caption">{t("blacklist.description")}</p>
      <div className="tw:flex tw:min-w-0 tw:flex-col tw:gap-3">
        {error ? (
          <Alert tone="error" title={toUserMessage(error, t("blacklist.loadFailed"))} />
        ) : (
          <>
            {/* Entry by ID: site + site anime id, title optional */}
            <div className="tw:grid tw:gap-2 tw:rounded-md tw:border tw:border-border tw:bg-muted/20 tw:p-2">
              <div className="tw:flex tw:w-full tw:flex-wrap tw:items-center tw:gap-2">
                <Segmented<SuppressibleProvider>
                  aria-label={t("blacklist.siteLabel")}
                  value={addProvider}
                  onValueChange={setAddProvider}
                  options={PROVIDER_ORDER.map((provider) => ({ value: provider, label: PROVIDER_LABELS[provider] }))}
                />
                <div className="tw:w-36">
                  <Input
                    aria-label={t("blacklist.idLabel")}
                    placeholder={t("blacklist.idPlaceholder")}
                    value={addId}
                    onChange={(e) => {
                      setAddId(e.target.value);
                      setAddError(undefined);
                    }}
                  />
                </div>
                <div className="tw:min-w-40 tw:flex-1">
                  <Input
                    aria-label={t("blacklist.titleLabel")}
                    placeholder={t("blacklist.titlePlaceholder")}
                    value={addTitle}
                    onChange={(e) => setAddTitle(e.target.value)}
                  />
                </div>
                <Button variant="primary" onClick={onAddById}>
                  <Plus size={14} />
                  {t("blacklist.add")}
                </Button>
              </div>
              {addError && <Alert tone="error" title={addError} />}
            </div>

            {isLoading ? (
              <StateBlock kind="busy" title={t("act.reading")} />
            ) : entries.length === 0 ? (
              <StateBlock kind="empty" title={t("blacklist.empty")} />
            ) : (
              <>
                <Input
                  aria-label={t("blacklist.filterLabel")}
                  placeholder={t("blacklist.filterPlaceholder")}
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                />
                {grouped.length === 0 ? (
                  <StateBlock kind="empty" title={t("search.noMatch")} />
                ) : (
                  <div className="tw:grid tw:grid-cols-[minmax(0,1fr)] tw:gap-4">
                    {grouped.map(({ provider, label, items }) => (
                      <section key={provider} className="tw:grid tw:grid-cols-[minmax(0,1fr)] tw:gap-2">
                        <h3 className="tw:m-0 tw:text-sm tw:font-semibold">
                          {label}
                          <span className="tw:ml-2 tw:text-sm tw:font-normal tw:text-muted-foreground">{t("blacklist.count", { n: items.length })}</span>
                        </h3>
                        <ul className="tw:m-0 tw:grid tw:list-none tw:grid-cols-[minmax(0,1fr)] tw:gap-1.5 tw:p-0">
                          {items.map((entry) => (
                            <li
                              key={entry.key}
                              className="tw:flex tw:items-center tw:gap-2 tw:rounded-md tw:border tw:border-border tw:bg-card/50 tw:px-3 tw:py-2"
                            >
                              {/* The truncation constraint is carried by min-w-0/flex-1; the full title goes in the title attribute */}
                              <span className="tw:min-w-0 tw:flex-1 tw:truncate" title={entry.title}>
                                {entry.title}
                              </span>
                              <span className="tw:flex-none tw:text-xs tw:text-muted-foreground">
                                {formatLocaleDate(new Date(entry.addedAt))}
                              </span>
                              <Button
                                size="sm"
                                variant="ghost"
                                aria-label={t("blacklist.restoreAria", { title: entry.title })}
                                onClick={() => onRestore(entry)}
                              >
                                <RotateCcw size={14} />
                                {t("blacklist.restore")}
                              </Button>
                            </li>
                          ))}
                        </ul>
                      </section>
                    ))}
                  </div>
                )}
              </>
            )}
          </>
        )}
      </div>
    </section>
  );
}
