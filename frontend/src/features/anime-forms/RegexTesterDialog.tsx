import { useEffect, useMemo, useState } from "react";
import { ListChecks, Pencil, Wand2 } from "lucide-react";
import { Button, Chip, Dialog, Segmented, StatePill, Textarea } from "../../ui/base";
import { t } from "../../lib/i18n";
import {
  DEFAULT_EPISODE_PATTERNS,
  hasBlockingEpisodePatternError,
  testEpisodePatterns,
} from "../../lib/goRegex";
import { EpisodePatternList } from "./EpisodePatternList";

type ResultFilter = "all" | "matched" | "unmatched";

/**
 * Standalone batch tester dialog (`tier="M"`, stacks cleanly on top of anime
 * config and settings dialogs). Reuses `EpisodePatternList` for live in-dialog
 * tuning and writes changes back only to the caller's draft on explicit Apply.
 */
export function RegexTesterDialog({
  open,
  onClose,
  patterns,
  onApply,
  readOnly = false,
  sampleTitles,
}: {
  open: boolean;
  onClose: () => void;
  patterns: string[];
  onApply?: (next: string[]) => void;
  readOnly?: boolean;
  /** Collected release titles from the current anime when available; falls back to common samples otherwise. */
  sampleTitles?: readonly string[];
}) {
  const initialTitles = useMemo(() => {
    const unique: string[] = [];
    for (const title of sampleTitles ?? []) {
      const trimmed = title.trim();
      if (trimmed && !unique.includes(trimmed)) unique.push(trimmed);
      if (unique.length >= 20) break;
    }
    return unique.length > 0 ? unique : t("forms.rules.patternTester.sampleTitles").split("\n");
  }, [sampleTitles]);
  const hasOwnerSamples = (sampleTitles?.length ?? 0) > 0;

  const [draft, setDraft] = useState<string[]>(patterns);
  const [titlesText, setTitlesText] = useState(() => initialTitles.join("\n"));
  const [editingTitles, setEditingTitles] = useState(false);
  const [filter, setFilter] = useState<ResultFilter>("all");

  useEffect(() => {
    if (open) {
      setDraft(patterns);
      setTitlesText(initialTitles.join("\n"));
      setEditingTitles(false);
      setFilter("all");
    }
  }, [open, patterns, initialTitles]);

  const titles = useMemo(
    () =>
      titlesText
        .split(/\r?\n/)
        .map((line) => line.trim())
        .filter(Boolean),
    [titlesText],
  );

  const evaluations = useMemo(
    () => titles.map((title) => ({ title, result: testEpisodePatterns(title, draft) })),
    [titles, draft],
  );

  const hitCounts = useMemo(() => {
    const counts = new Map<number, number>();
    for (const item of evaluations) {
      if (item.result.matched && !item.result.usedDefaults) {
        counts.set(item.result.ruleIndex, (counts.get(item.result.ruleIndex) ?? 0) + 1);
      }
    }
    return counts;
  }, [evaluations]);

  const matchedCount = evaluations.filter((entry) => entry.result.matched).length;
  const unmatchedCount = evaluations.length - matchedCount;
  const filtered = evaluations.filter((entry) => {
    if (filter === "matched") return entry.result.matched;
    if (filter === "unmatched") return !entry.result.matched;
    return true;
  });

  const changed = !readOnly && onApply !== undefined && JSON.stringify(draft) !== JSON.stringify(patterns);
  const hasBlockingError = hasBlockingEpisodePatternError(draft);

  return (
    <Dialog
      open={open}
      onRequestClose={onClose}
      tier="M"
      title={t("forms.rules.patternTester.title")}
      description={
        hasOwnerSamples
          ? t("forms.rules.patternTester.descriptionOwner")
          : t("forms.rules.patternTester.descriptionDefault")
      }
      footer={
        changed ? (
          <>
            {hasBlockingError && (
              <StatePill tone="offline">{t("forms.rules.patternTester.blockedApply")}</StatePill>
            )}
            <Button variant="ghost" onClick={onClose}>
              {t("forms.rules.patternTester.cancel")}
            </Button>
            <Button
              variant="primary"
              disabled={hasBlockingError}
              onClick={() => {
                onApply(draft);
                onClose();
              }}
            >
              {t("forms.rules.patternTester.apply")}
            </Button>
          </>
        ) : (
          <Button variant="secondary" onClick={onClose}>
            {t("forms.rules.patternTester.close")}
          </Button>
        )
      }
    >
      <div className="tw:grid tw:min-w-0 tw:gap-4">
        <section className="tw:grid tw:min-w-0 tw:gap-2">
          <div className="tw:flex tw:flex-wrap tw:items-center tw:justify-between tw:gap-2">
            <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
              <span className="tw:text-xs tw:font-medium tw:text-foreground">
                {t("forms.rules.patternTester.rulesHeading")}
              </span>
              {draft.length === 0 ? (
                <Chip tone="neutral">{t("forms.rules.patternTester.usingBuiltins")}</Chip>
              ) : (
                <Chip tone="neutral">{t("forms.rules.patternTester.customCount", { n: draft.length })}</Chip>
              )}
              {changed && <Chip tone="accent">{t("forms.rules.patternTester.adjusted")}</Chip>}
            </div>

            <div className="tw:flex tw:items-center tw:gap-1">
              {!readOnly && draft.length === 0 && (
                <Button
                  variant="ghost"
                  size="sm"
                  type="button"
                  onClick={() => setDraft([...DEFAULT_EPISODE_PATTERNS])}
                >
                  <Wand2 className="tw:size-3.5" aria-hidden="true" />
                  {t("forms.rules.patternEditor.fillBuiltins")}
                </Button>
              )}
              {changed && (
                <Button variant="ghost" size="sm" type="button" onClick={() => setDraft(patterns)}>
                  {t("forms.rules.patternTester.restore")}
                </Button>
              )}
            </div>
          </div>

          <EpisodePatternList
            value={draft}
            onChange={readOnly ? undefined : setDraft}
            readOnly={readOnly}
            hitCounts={hitCounts}
            hideHeader
          />
        </section>

        <section className="tw:grid tw:min-w-0 tw:gap-2.5">
          <div className="tw:flex tw:flex-wrap tw:items-center tw:justify-between tw:gap-2">
            <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2.5">
              <span className="tw:text-xs tw:font-medium tw:text-foreground">
                {t("forms.rules.patternTester.resultsHeading")}
              </span>
              {!editingTitles && evaluations.length > 0 && (
                <>
                  <StatePill tone={unmatchedCount === 0 ? "online" : "warning"}>
                    {t("forms.rules.patternTester.summaryMatched", {
                      matched: matchedCount,
                      total: evaluations.length,
                    })}
                  </StatePill>
                  <Segmented<ResultFilter>
                    aria-label={t("forms.rules.patternTester.filterAria")}
                    size="compact"
                    value={filter}
                    onValueChange={setFilter}
                    options={[
                      { value: "all", label: t("forms.rules.patternTester.filterAll"), count: evaluations.length, tone: "quiet" },
                      { value: "matched", label: t("forms.rules.patternTester.filterMatched"), count: matchedCount, tone: "primary" },
                      {
                        value: "unmatched",
                        label: t("forms.rules.patternTester.filterUnmatched"),
                        count: unmatchedCount,
                        tone: unmatchedCount > 0 ? "danger" : "quiet",
                      },
                    ]}
                  />
                </>
              )}
            </div>

            <div className="tw:flex tw:items-center tw:gap-1">
              {editingTitles && (
                <Button
                  variant="ghost"
                  size="sm"
                  type="button"
                  onClick={() => setTitlesText(initialTitles.join("\n"))}
                >
                  {t("forms.rules.patternTester.resetSamples")}
                </Button>
              )}
              <Button
                variant="secondary"
                size="sm"
                type="button"
                onClick={() => setEditingTitles((current) => !current)}
              >
                {editingTitles ? (
                  <>
                    <ListChecks className="tw:size-3.5" aria-hidden="true" />
                    {t("forms.rules.patternTester.viewResults")}
                  </>
                ) : (
                  <>
                    <Pencil className="tw:size-3.5" aria-hidden="true" />
                    {t("forms.rules.patternTester.editTitles")}
                  </>
                )}
              </Button>
            </div>
          </div>

          {editingTitles ? (
            <div className="tw:grid tw:gap-1.5">
              <Textarea
                aria-label={t("forms.rules.patternTester.titlesAria")}
                rows={6}
                value={titlesText}
                onChange={(event) => setTitlesText(event.target.value)}
                placeholder={t("forms.rules.patternTester.titlesPlaceholder")}
                className="tw:font-mono tw:text-xs"
              />
              <span className="tw:text-xs tw:text-muted-foreground">
                {t("forms.rules.patternTester.titlesCount", { n: titles.length })}
              </span>
            </div>
          ) : (
            <div className="tw:grid tw:min-w-0 tw:divide-y tw:divide-border/70 tw:overflow-hidden tw:rounded-control tw:border tw:border-border tw:bg-card">
              {filtered.length === 0 ? (
                <div className="tw:px-3 tw:py-5 tw:text-center tw:text-xs tw:text-muted-foreground">
                  {evaluations.length === 0
                    ? t("forms.rules.patternTester.emptyTitles")
                    : t("forms.rules.patternTester.emptyFiltered")}
                </div>
              ) : (
                filtered.map(({ title, result }, idx) => (
                  <div
                    key={`${idx}:${title}`}
                    className="tw:grid tw:min-w-0 tw:grid-cols-[minmax(0,1fr)] tw:items-center tw:gap-2 tw:px-3 tw:py-2 tw:sm:grid-cols-[7.5rem_minmax(0,1fr)]"
                  >
                    <div className="tw:flex tw:items-center tw:gap-1.5">
                      {result.matched ? (
                        <>
                          <Chip tone="accent" className="tw:tabular-nums">
                            {t("forms.rules.patternTester.episodeChip", { ep: result.episode })}
                          </Chip>
                          <span className="tw:text-badge tw:tabular-nums tw:text-muted-foreground">
                            {result.usedDefaults
                              ? t("forms.rules.patternTester.builtinRule", { n: result.ruleIndex + 1 })
                              : t("forms.rules.patternTester.customRule", { n: result.ruleIndex + 1 })}
                          </span>
                        </>
                      ) : (
                        <StatePill tone="warning">{t("forms.rules.patternTester.unmatched")}</StatePill>
                      )}
                    </div>

                    <div className="tw:min-w-0 tw:font-mono tw:text-xs tw:leading-5 tw:break-all tw:text-muted-foreground">
                      {result.matched ? (
                        <>
                          <span>{title.slice(0, result.matchStart)}</span>
                          <span className="tw:rounded-sm tw:bg-primary/15 tw:px-0.5 tw:text-foreground">
                            <span>{title.slice(result.matchStart, result.captureStart)}</span>
                            <mark className="tw:rounded-sm tw:bg-primary/35 tw:px-0.5 tw:font-semibold tw:text-foreground">
                              {title.slice(result.captureStart, result.captureEnd)}
                            </mark>
                            <span>{title.slice(result.captureEnd, result.matchEnd)}</span>
                          </span>
                          <span>{title.slice(result.matchEnd)}</span>
                        </>
                      ) : (
                        <span className="tw:text-foreground/80">{title}</span>
                      )}
                    </div>
                  </div>
                ))
              )}
            </div>
          )}
        </section>
      </div>
    </Dialog>
  );
}
