import { useEffect, useMemo, useState } from "react";
import { Search } from "lucide-react";
import { toUserMessage } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import type { MetadataCandidate, MetadataEpisode, MetadataEpisodeLoader } from "../../lib/metadata/types";
import { Button, Input, StateBlock } from "../../ui";
import { Dialog, Select } from "../../ui/base";

// Candidate-level, read-only reference. Its season and filter are deliberately local: neither
// browsing nor closing can select the work or change MetadataMatchDialog's apply-season editor.
export function MetadataEpisodeDialog({
  candidate,
  loader,
  onClose,
}: {
  candidate: MetadataCandidate;
  loader: MetadataEpisodeLoader;
  onClose: () => void;
}) {
  const initialSeason = candidate.suggestedSeason ?? candidate.seasons?.[0]?.seasonNumber ?? 1;
  const [season, setSeason] = useState(String(initialSeason));
  const [filter, setFilter] = useState("");
  const [episodes, setEpisodes] = useState<MetadataEpisode[]>([]);
  const [pending, setPending] = useState(true);
  const [error, setError] = useState<string>();
  const [reloadTick, setReloadTick] = useState(0);
  const isTMDB = candidate.provider === "tmdb";

  useEffect(() => {
    let active = true;
    setPending(true);
    setError(undefined);
    loader(candidate, candidate.provider === "tmdb" ? Number(season) : undefined)
      .then((result) => {
        if (active) setEpisodes(result);
      })
      .catch((cause: unknown) => {
        if (active) setError(toUserMessage(cause, t("forms.episodeLoadFailed")));
      })
      .finally(() => {
        if (active) setPending(false);
      });
    return () => {
      active = false;
    };
  }, [candidate, loader, reloadTick, season]);

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
      title={t("forms.episodeListTitle")}
      description={`${candidate.title} · ${isTMDB ? "TMDB" : "Bangumi"}`}
      footer={
        <>
          <span className="metadata-episode-readonly">{t("forms.episodeReadonly")}</span>
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
                options={(candidate.seasons ?? []).map((item) => ({
                  value: String(item.seasonNumber),
                  label: t("forms.seasonOption", { n: item.seasonNumber, name: item.name || t("forms.unnamed"), count: item.episodeCount }),
                }))}
              />
            </label>
          )}
          <Input
            aria-label={t("forms.episodeFilter")}
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
        <p className="metadata-episode-hint">{isTMDB ? t("forms.episodeBrowseHint") : t("forms.episodeBrowseHintBgm")}</p>
        <div className="metadata-episode-head" aria-hidden="true">
          <span>{t("forms.episodeColumn")}</span><span>{t("forms.episodeTitleColumn")}</span><span>{t("forms.episodeDateColumn")}</span>
        </div>
        <div className="metadata-episode-scroll">
          {pending ? (
            <StateBlock kind="busy" title={t("forms.episodeLoading")} />
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
