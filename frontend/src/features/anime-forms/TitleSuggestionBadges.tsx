import type { TitleSuggestion } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { cn } from "@/lib/utils";
import { Button } from "../../ui";

/**
 * The suggestions the badges show: empty ones and the current title dropped, then one per text,
 * keeping the first source in the server's order.
 */
function visibleTitleSuggestions(
  suggestions: readonly TitleSuggestion[] | undefined,
  current: string,
): TitleSuggestion[] {
  const seen = new Set([current.trim()]);
  const result: TitleSuggestion[] = [];
  for (const suggestion of suggestions ?? []) {
    const text = suggestion.text.trim();
    if (text === "" || seen.has(text)) continue;
    seen.add(text);
    result.push({ source: suggestion.source, text });
  }
  return result;
}

/** Title candidates as one-click badges; a click hands the text to the caller, which saves it like a typed title. */
export function TitleSuggestionBadges({
  suggestions,
  current,
  onPick,
  className,
}: {
  suggestions: readonly TitleSuggestion[] | undefined;
  /** The title now in effect; a suggestion equal to it is not offered. */
  current: string;
  onPick: (text: string) => void;
  /** Spacing from the host row; applied only when there are badges to show. */
  className?: string;
}) {
  const visible = visibleTitleSuggestions(suggestions, current);
  if (visible.length === 0) return null;
  return (
    <div className={cn("tw:flex tw:flex-wrap tw:gap-1.5", className)} role="group" aria-label={t("tabs.metadata.suggestionsAria")}>
      {visible.map((suggestion) => (
        // A long name truncates inside the badge (full text on hover) instead of widening the host column.
        <Button
          key={suggestion.text}
          size="sm"
          variant="secondary"
          className="tw:min-w-0 tw:max-w-full"
          title={suggestion.text}
          onClick={() => onPick(suggestion.text)}
        >
          <span className="tw:shrink-0 tw:text-muted-foreground">{t(`tabs.metadata.suggestionSource.${suggestion.source}`)}</span>
          <span className="tw:min-w-0 tw:truncate">{suggestion.text}</span>
        </Button>
      ))}
    </div>
  );
}
