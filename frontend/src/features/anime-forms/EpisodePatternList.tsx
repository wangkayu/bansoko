import { useMemo, useState } from "react";
import { ArrowDown, ArrowUp, FlaskConical, Plus, Wand2, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button, Chip, HelpTip, Input, StatePill } from "../../ui/base";
import { t } from "../../lib/i18n";
import { DEFAULT_EPISODE_PATTERNS, MAX_EPISODE_PATTERNS, validateGoRegex } from "../../lib/goRegex";

/**
 * Inline ordered regex editor for `episode_patterns`:
 * - One monospaced row per pattern with priority index (`1, 2, 3`) and in-place editing
 * - Reorder (`↑`/`↓`) and per-row delete (`×`) without accidental Backspace deletion
 * - Persistent bottom add row (Enter to commit, multi-line paste splits into rows, quiet on blur)
 * - Immediate Go RE2 compatibility + capture-group validation
 * - Optional `onOpenTester` opens the standalone `RegexTesterDialog` without bloating the page
 */
export function EpisodePatternList({
  value,
  onChange,
  readOnly = false,
  onOpenTester,
  hitCounts,
  hideHeader = false,
  showHelp = false,
}: {
  value: string[];
  onChange?: (next: string[]) => void;
  readOnly?: boolean;
  /** Opens the standalone batch tester dialog. */
  onOpenTester?: () => void;
  /** Per-row hit counts shown when reused inside `RegexTesterDialog`. */
  hitCounts?: ReadonlyMap<number, number>;
  /** Hide the top header row when the host provides its own (e.g. inside `RegexTesterDialog`). */
  hideHeader?: boolean;
  /** Show the episode-patterns HelpTip beside the header label (useful in settings dialogs without a SectionCard header). */
  showHelp?: boolean;
}) {
  const [newPattern, setNewPattern] = useState("");
  const [newError, setNewError] = useState<string>();

  const diagnostics = useMemo(
    () =>
      value.map((item, idx) => {
        if (!item.trim()) {
          return { severity: "warning" as const, message: t("forms.rules.patternEditor.emptyRowFallback") };
        }
        const base = validateGoRegex(item, { requireCapture: true });
        if (base) return base;
        if (value.findIndex((other) => other.trim() === item.trim()) < idx) {
          return { severity: "warning" as const, message: t("forms.rules.patternEditor.duplicate") };
        }
        return null;
      }),
    [value],
  );

  const errorCount = diagnostics.filter((diag) => diag?.severity === "error").length;

  function updateAt(index: number, nextVal: string) {
    if (!onChange || readOnly) return;
    const next = [...value];
    next[index] = nextVal;
    onChange(next);
  }

  function removeAt(index: number) {
    if (!onChange || readOnly) return;
    onChange(value.filter((_, i) => i !== index));
  }

  function move(index: number, dir: -1 | 1) {
    if (!onChange || readOnly) return;
    const target = index + dir;
    if (target < 0 || target >= value.length) return;
    const next = [...value];
    [next[index], next[target]] = [next[target], next[index]];
    onChange(next);
  }

  function commitNew(raw: string) {
    if (!onChange || readOnly) return;
    const lines = raw
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter(Boolean);
    if (lines.length === 0) return;

    const deduped = lines.filter((line, idx) => {
      if (value.some((existing) => existing.trim() === line)) return false;
      return lines.indexOf(line) === idx;
    });

    if (lines.length === 1) {
      const diag = validateGoRegex(lines[0]);
      if (diag?.severity === "error") {
        setNewError(diag.message);
        return;
      }
      if (deduped.length === 0) {
        setNewError(t("forms.rules.patternEditor.exists"));
        return;
      }
    }

    if (value.length + deduped.length > MAX_EPISODE_PATTERNS) {
      setNewError(t("forms.rules.patternEditor.maxLimit", { max: MAX_EPISODE_PATTERNS }));
      return;
    }

    if (deduped.length > 0) {
      onChange([...value, ...deduped]);
    }
    setNewPattern("");
    setNewError(undefined);
  }

  return (
    <div className="tw:grid tw:min-w-0 tw:gap-2">
      {!hideHeader && (
        <div className="tw:flex tw:flex-wrap tw:items-center tw:justify-between tw:gap-2">
          <div className="tw:inline-flex tw:flex-wrap tw:items-center tw:gap-1.5">
            <span className="tw:text-xs tw:font-medium tw:text-muted-foreground">
              {t("settings.rules.episodePatterns")}
            </span>
            {value.length > 0 && (
              <Chip tone="neutral">{t("forms.rules.patternEditor.countOrdered", { n: value.length })}</Chip>
            )}
            {errorCount > 0 && (
              <StatePill tone="offline">{t("forms.rules.patternEditor.errorCount", { n: errorCount })}</StatePill>
            )}
            {showHelp && (
              <HelpTip label={t("settings.rules.episodePatternsHelpLabel")}>
                {t("settings.rules.episodePatternsHelp")}
              </HelpTip>
            )}
          </div>

          <div className="tw:flex tw:items-center tw:gap-1">
            {!readOnly && onChange && value.length === 0 && (
              <Button
                variant="ghost"
                size="sm"
                type="button"
                onClick={() => onChange([...DEFAULT_EPISODE_PATTERNS])}
              >
                <Wand2 className="tw:size-3.5" aria-hidden="true" />
                {t("forms.rules.patternEditor.fillBuiltins")}
              </Button>
            )}
            {onOpenTester && (
              <Button variant="ghost" size="sm" type="button" onClick={onOpenTester}>
                <FlaskConical className="tw:size-3.5" aria-hidden="true" />
                {t("forms.rules.patternTester.openButton")}
              </Button>
            )}
          </div>
        </div>
      )}

      <div className="tw:grid tw:min-w-0 tw:divide-y tw:divide-border/70 tw:overflow-hidden tw:rounded-control tw:border tw:border-border tw:bg-card">
        {value.length === 0 ? (
          <div className="tw:bg-muted/25 tw:px-3 tw:py-2.5 tw:text-xs tw:text-muted-foreground">
            {t("forms.rules.patternEditor.emptyHint")}
          </div>
        ) : (
          value.map((pattern, index) => {
            const diag = diagnostics[index];
            const hits = hitCounts?.get(index) ?? 0;
            return (
              <div
                key={index}
                className={cn(
                  "tw:grid tw:min-w-0 tw:gap-1 tw:px-2 tw:py-1.5 tw:transition-colors",
                  hits > 0 && "tw:bg-online/6",
                  diag?.severity === "error" && "tw:bg-offline/6",
                )}
              >
                <div className="tw:flex tw:min-w-0 tw:items-center tw:gap-1.5">
                  <span
                    title={t("forms.rules.patternEditor.priorityTitle", { n: index + 1 })}
                    className={cn(
                      "tw:inline-flex tw:h-5 tw:w-5 tw:flex-none tw:items-center tw:justify-center tw:rounded-md tw:text-badge tw:font-medium tw:tabular-nums",
                      hits > 0
                        ? "tw:bg-online/20 tw:text-online"
                        : diag?.severity === "error"
                          ? "tw:bg-offline/15 tw:text-offline"
                          : "tw:bg-muted tw:text-muted-foreground",
                    )}
                  >
                    {index + 1}
                  </span>

                  <Input
                    type="text"
                    controlSize="sm"
                    aria-label={t("forms.rules.patternEditor.rowAria", { n: index + 1 })}
                    value={pattern}
                    readOnly={readOnly}
                    spellCheck={false}
                    autoComplete="off"
                    onChange={(event) => updateAt(index, event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === "Enter") event.preventDefault();
                    }}
                    className={cn(
                      "tw:min-w-0 tw:flex-1 tw:border-transparent tw:bg-transparent tw:font-mono tw:text-xs",
                      !readOnly && "tw:hover:bg-muted/40 tw:focus:border-primary tw:focus:bg-muted/60",
                      readOnly && "tw:cursor-default tw:text-muted-foreground",
                    )}
                  />

                  {hitCounts !== undefined && (
                    <span
                      className={cn(
                        "tw:flex-none tw:rounded-control tw:px-1.5 tw:py-0.5 tw:text-badge tw:tabular-nums",
                        hits > 0
                          ? "tw:bg-online/15 tw:font-medium tw:text-online"
                          : "tw:text-muted-foreground/60",
                      )}
                    >
                      {hits > 0
                        ? t("forms.rules.patternTester.hitCount", { n: hits })
                        : t("forms.rules.patternTester.hitZero")}
                    </span>
                  )}

                  {!readOnly && onChange && (
                    <div className="tw:flex tw:flex-none tw:items-center tw:gap-0.5">
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        aria-label={t("forms.rules.patternEditor.moveUp", { n: index + 1 })}
                        disabled={index === 0}
                        onClick={() => move(index, -1)}
                        className="tw:h-6 tw:w-6 tw:px-0"
                      >
                        <ArrowUp className="tw:size-3.5" aria-hidden="true" />
                      </Button>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        aria-label={t("forms.rules.patternEditor.moveDown", { n: index + 1 })}
                        disabled={index === value.length - 1}
                        onClick={() => move(index, 1)}
                        className="tw:h-6 tw:w-6 tw:px-0"
                      >
                        <ArrowDown className="tw:size-3.5" aria-hidden="true" />
                      </Button>
                      <Button
                        type="button"
                        variant="dangerGhost"
                        size="sm"
                        aria-label={t("ui.tagRemove", { tag: pattern })}
                        onClick={() => removeAt(index)}
                        className="tw:h-6 tw:w-6 tw:px-0"
                      >
                        <X className="tw:size-3.5" aria-hidden="true" />
                      </Button>
                    </div>
                  )}
                </div>

                {diag && (
                  <p
                    role={diag.severity === "error" ? "alert" : "status"}
                    className={cn(
                      "tw:m-0 tw:pl-7 tw:text-xs tw:leading-4",
                      diag.severity === "error" ? "tw:text-offline" : "tw:text-warning",
                    )}
                  >
                    {diag.message}
                  </p>
                )}
              </div>
            );
          })
        )}

        {!readOnly && onChange && (
          <div className="tw:grid tw:gap-1 tw:bg-muted/20 tw:px-2 tw:py-1.5">
            <div className="tw:flex tw:min-w-0 tw:items-center tw:gap-1.5">
              <span className="tw:inline-flex tw:h-5 tw:w-5 tw:flex-none tw:items-center tw:justify-center tw:text-muted-foreground">
                <Plus className="tw:size-3.5" aria-hidden="true" />
              </span>
              <Input
                type="text"
                controlSize="sm"
                aria-label={t("settings.rules.episodePatterns")}
                value={newPattern}
                spellCheck={false}
                autoComplete="off"
                placeholder={t("forms.rules.patternEditor.addPlaceholder")}
                onChange={(event) => {
                  setNewPattern(event.target.value);
                  setNewError(undefined);
                }}
                onPaste={(event) => {
                  const text = event.clipboardData.getData("text");
                  if (text.includes("\n")) {
                    event.preventDefault();
                    commitNew(text);
                  }
                }}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    commitNew(newPattern);
                  }
                }}
                className="tw:min-w-0 tw:flex-1 tw:border-transparent tw:bg-transparent tw:font-mono tw:text-xs tw:placeholder:font-sans"
              />
              {newPattern.trim() !== "" && (
                <Button variant="secondary" size="sm" type="button" onClick={() => commitNew(newPattern)}>
                  {t("forms.rules.patternEditor.addButton")}
                </Button>
              )}
            </div>
            {newError && (
              <p role="alert" className="tw:m-0 tw:pl-7 tw:text-xs tw:text-offline">
                {newError}
              </p>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
