import { useEffect, useMemo, useState, type RefObject } from "react";
import { Search } from "lucide-react";
import { listTMDBWorkSeasons, toUserMessage, type TMDBSeasonList } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import type { MetadataCandidate, MetadataEpisode, MetadataEpisodeLoader, MetadataEpisodeWork } from "../../lib/metadata/types";
import { Button, Input, StateBlock } from "../../ui";
import { Dialog, Select } from "../../ui/base";

export type MetadataEpisodeBinding = Pick<MetadataEpisodeWork, "workId" | "title"> & (
  { provider: "bgm" } | { provider: "tmdb"; season: number }
);

type EpisodeDialogProps = {
  loader: MetadataEpisodeLoader;
  onClose: () => void;
  returnFocus?: RefObject<HTMLButtonElement | null>;
} & ({ candidate: MetadataCandidate; binding?: never } | { binding: MetadataEpisodeBinding; candidate?: never });

// Browsing keeps the selected season and filter local, independent of matching and saved settings.
export function MetadataEpisodeDialog({ candidate, binding, loader, onClose, returnFocus }: EpisodeDialogProps) {
  const work = binding ?? candidate;
  const boundSeason = binding?.provider === "tmdb" ? binding.season : undefined;
  const boundTMDBWorkID = binding?.provider === "tmdb" ? binding.workId : undefined;
  const initialSeason = boundSeason ?? candidate?.suggestedSeason ?? candidate?.seasons?.[0]?.seasonNumber ?? 1;
  const [season, setSeason] = useState(String(initialSeason));
  const [filter, setFilter] = useState("");
  const [episodes, setEpisodes] = useState<MetadataEpisode[]>([]);
  const [pending, setPending] = useState(true);
  const [error, setError] = useState<string>();
  const [reloadTick, setReloadTick] = useState(0);
  const isTMDB = work.provider === "tmdb";
  const [catalogue, setCatalogue] = useState<TMDBSeasonList["seasons"]>([]);
  const [catalogueFailed, setCatalogueFailed] = useState(false);
  const [catalogueRetry, setCatalogueRetry] = useState(0);

  useEffect(() => {
    if (boundTMDBWorkID === undefined) return;
    let active = true;
    setCatalogueFailed(false);
    listTMDBWorkSeasons(boundTMDBWorkID).then(
      (result) => { if (active) setCatalogue(result.seasons); },
      () => { if (active) setCatalogueFailed(true); },
    );
    return () => { active = false; };
  }, [boundTMDBWorkID, catalogueRetry]);

  const seasonOptions = boundSeason === undefined
    ? (candidate?.seasons ?? []).map((item) => ({
        value: String(item.seasonNumber),
        label: t("forms.seasonOption", { n: item.seasonNumber, name: item.name || t("forms.unnamed"), count: item.episodeCount }),
      }))
    : [...new Set([boundSeason, ...catalogue.map((item) => item.season)])].sort((a, b) => a - b).map((number) => {
        const name = number === 0 ? t("forms.seasonSpecials")
          : catalogue.find((item) => item.season === number)?.name || t("forms.seasonN", { n: number });
        return { value: String(number), label: number === boundSeason ? t("forms.boundSeasonOption", { season: name }) : name };
      });

  useEffect(() => {
    let active = true;
    setPending(true);
    setEpisodes([]);
    setError(undefined);
    loader(work, isTMDB ? Number(season) : undefined)
      .then((result) => {
        if (active) setEpisodes(result);
      })
      .catch((cause: unknown) => {
        if (active) setError(toUserMessage(cause, t(binding ? "forms.boundEpisodeLoadFailed" : "forms.episodeLoadFailed")));
      })
      .finally(() => {
        if (active) setPending(false);
      });
    return () => {
      active = false;
    };
  }, [binding, isTMDB, loader, reloadTick, season, work]);

  const shown = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    return episodes.filter((episode) =>
      !needle || String(episode.episode).includes(needle) || (episode.title ?? "").toLowerCase().includes(needle),
    );
  }, [episodes, filter]);
  const sections = [
    { id: "main" as const, label: t("forms.episodeMainSection"), items: shown.filter((episode) => episode.section !== "special") },
    { id: "special" as const, label: t("forms.episodeSpecialSection"), items: shown.filter((episode) => episode.section === "special") },
  ].filter((section) => section.items.length > 0);

  function renderEpisode(episode: MetadataEpisode) {
    return (
      <li className="metadata-episode-row" key={`${episode.section ?? "main"}:${episode.episode}`}>
        <span className="metadata-episode-number">{t("forms.episodeNumber", { n: episode.episode })}</span>
        <span className="metadata-episode-title">{episode.title || t("forms.unnamed")}</span>
        <time className="metadata-episode-date">{episode.airDate || "—"}</time>
      </li>
    );
  }

  return (
    <Dialog
      open
      onRequestClose={onClose}
      tier="M"
      reader
      fixedHeight
      bodyLayout="fill"
      focusTitle
      returnFocus={returnFocus}
      title={t(binding ? "forms.boundEpisodeTitle" : "forms.episodeListTitle")}
      description={[work.title, isTMDB ? "TMDB" : "Bangumi"].filter(Boolean).join(" · ")}
      footer={
        <>
          <span className="metadata-episode-readonly">{t(binding ? "forms.boundEpisodeReadonly" : "forms.episodeReadonly")}</span>
          <Button onClick={onClose}>{t("ui.close")}</Button>
        </>
      }
    >
      <div className="metadata-episode-browser">
        <div className="metadata-episode-tools">
          {isTMDB && (
            <label className="metadata-episode-season">
              {t("forms.browseSeason")}
              <Select
                label={t("forms.browseSeason")}
                value={season}
                onChange={setSeason}
                options={seasonOptions}
              />
            </label>
          )}
          <Input
            aria-label={t(binding ? "forms.boundEpisodeFilter" : "forms.episodeFilter")}
            placeholder={t("forms.episodeFilterPlaceholder")}
            leadingIcon={<Search aria-hidden="true" />}
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
            controlSize="sm"
            className="metadata-episode-filter"
          />
          <span className="metadata-episode-count" aria-live="polite">
            {t("forms.episodeShown", { shown: shown.length, total: episodes.length })}
          </span>
        </div>
        {catalogueFailed && (
          <div className="metadata-episode-catalogue-error">
            <span role="status">{t("forms.seasonCatalogueLoadFailed")}</span>
            <Button size="sm" variant="ghost" onClick={() => setCatalogueRetry((value) => value + 1)}>{t("act.retry")}</Button>
          </div>
        )}
        <p className="metadata-episode-hint">
          {binding
            ? t(isTMDB ? "forms.boundEpisodeHintTmdb" : "forms.boundEpisodeHintBgm")
            : t(isTMDB ? "forms.episodeBrowseHint" : "forms.episodeBrowseHintBgm")}
        </p>
        <div className="metadata-episode-head" aria-hidden="true">
          <span>{t("forms.episodeColumn")}</span><span>{t("forms.episodeTitleColumn")}</span><span>{t("forms.episodeDateColumn")}</span>
        </div>
        <div className="metadata-episode-scroll">
          {pending ? (
            <StateBlock kind="busy" skeleton="table" title={t("forms.episodeLoading")} />
          ) : error ? (
            <StateBlock kind="error" title={error} action={<Button size="sm" onClick={() => setReloadTick((value) => value + 1)}>{t("act.retry")}</Button>} />
          ) : shown.length === 0 ? (
            <StateBlock kind="empty" title={t("forms.episodeEmpty")} />
          ) : isTMDB ? (
            <ol className="metadata-episode-list">
              {shown.map(renderEpisode)}
            </ol>
          ) : (
            <div className="metadata-episode-groups">
              {sections.map((section) => (
                <section key={section.id}>
                  <h3 className="metadata-episode-group-title">{section.label}<span>{t("forms.episodesCount", { n: section.items.length })}</span></h3>
                  <ol className="metadata-episode-list">{section.items.map(renderEpisode)}</ol>
                </section>
              ))}
            </div>
          )}
        </div>
      </div>
    </Dialog>
  );
}
