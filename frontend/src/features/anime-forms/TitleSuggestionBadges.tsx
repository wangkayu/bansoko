import type { TitleSuggestion } from "../../lib/api/client";
import { t } from "../../lib/i18n";
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
}: {
  suggestions: readonly TitleSuggestion[] | undefined;
  /** The title now in effect; a suggestion equal to it is not offered. */
  current: string;
  onPick: (text: string) => void;
}) {
  const visible = visibleTitleSuggestions(suggestions, current);
  if (visible.length === 0) return null;
  return (
    <div className="tw:flex tw:flex-wrap tw:gap-1.5" role="group" aria-label={t("tabs.metadata.suggestionsAria")}>
      {visible.map((suggestion) => (
        <Button key={suggestion.text} size="sm" variant="secondary" onClick={() => onPick(suggestion.text)}>
          <span className="tw:text-muted-foreground">{t(`tabs.metadata.suggestionSource.${suggestion.source}`)}</span>
          {suggestion.text}
        </Button>
      ))}
    </div>
  );
}
