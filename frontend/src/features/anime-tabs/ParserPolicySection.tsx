import { useEffect, useState } from "react";
import { useSavePart } from "../../lib/saveParts";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  getInstanceParserPolicy,
  isVersionConflict,
  listParserCatalog,
  saveInstanceParserPolicy,
  toUserMessage,
  type ParserPolicyView,
} from "../../lib/api/client";
import { configKeys, fileMaintenanceKeys } from "../../lib/queryKeys";
import { ConflictBlock, StatePill } from "../../ui";
import { Alert, Button } from "../../ui/base";
import { t } from "../../lib/i18n";
import { parserKindOptions, parserKindUsesPatterns } from "../../lib/parserKind";
import { ParserKindChoice } from "../anime-forms/ParserKindChoice";

const skelBar = "tw:block tw:rounded-full tw:bg-muted tw:motion-safe:animate-pulse";

/**
 * Instance-level episode parser policy selector inside the Parsing section card.
 * - Uses `initialPolicy` from the workspace read model when present (0ms first frame, no waterfall).
 * - Waits for both policy and catalog before rendering the Segmented options so there is never a
 *   1-option -> 2-option jump; shows an equal-height single-row skeleton during cold loads.
 * - Registers as the `"parser"` save part of the dialog footer's single Save button.
 */
export function ParserPolicySection(props: {
  animeID: string;
  active?: boolean;
  readOnly?: boolean;
  /** Policy snapshot already loaded by the workspace read model (eliminates the waterfall). */
  initialPolicy?: ParserPolicyView | null;
  /** Number of saved/draft episode regex patterns; shown inline when `reltitle` is active. */
  patternCount?: number;
  /** The displayed parser (the unsaved choice winning, otherwise the saved value). */
  onKindChange?: (kind: string | undefined) => void;
  /** Whether an unsaved choice exists. */
  onDirtyChange?: (dirty: boolean) => void;
  /** Notifies the outer shell after a successful parser policy save. */
  onSaved?: () => void;
  /** "Use defaults everywhere": every seq change fills kind with the unsaved choice. */
  reset?: { kind: string; seq: number };
}) {
  const {
    animeID,
    active = true,
    readOnly = false,
    initialPolicy,
    patternCount = 0,
    onKindChange,
    onDirtyChange,
    onSaved,
    reset,
  } = props;
  const queryClient = useQueryClient();
  const policy = useQuery({
    queryKey: fileMaintenanceKeys.parserPolicy(animeID),
    queryFn: () => getInstanceParserPolicy(animeID),
    initialData: initialPolicy ?? undefined,
    enabled: active && initialPolicy == null,
  });
  const catalog = useQuery({
    queryKey: configKeys.parserCatalog,
    queryFn: listParserCatalog,
    staleTime: Infinity,
  });
  const [draft, setDraft] = useState<string>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();

  useEffect(() => {
    setDraft(undefined);
    setError(undefined);
    setConflict(undefined);
  }, [animeID]);

  const data = policy.data ?? initialPolicy ?? undefined;
  const dirty = !readOnly && data !== undefined && draft !== undefined && draft !== data.parser_kind;
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);
  const resetSeq = reset?.seq;
  const resetKind = reset?.kind;
  useEffect(() => {
    if (resetSeq !== undefined && resetKind !== undefined && !readOnly) setDraft(resetKind);
  }, [resetSeq, resetKind, readOnly]);

  async function handleSave() {
    if (!data || draft === undefined) return { ok: true as const };
    setSaving(true);
    setError(undefined);
    setConflict(undefined);
    try {
      const stored = await saveInstanceParserPolicy(animeID, {
        expected_version: data.version,
        parser_kind: draft as ParserPolicyView["parser_kind"],
      });
      queryClient.setQueryData(fileMaintenanceKeys.parserPolicy(animeID), stored);
      setDraft(stored.parser_kind);
      onSaved?.();
      return { ok: true as const };
    } catch (cause) {
      if (isVersionConflict(cause)) {
        setConflict(cause.currentVersion);
      }
      setError(toUserMessage(cause, t("policy.parser.saveFailed")));
      return { ok: false as const };
    } finally {
      setSaving(false);
    }
  }

  useSavePart("parser", { count: dirty ? 1 : 0, label: t("policy.parser.title"), busy: saving, save: handleSave });

  async function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    const fresh = await queryClient.fetchQuery({
      queryKey: fileMaintenanceKeys.parserPolicy(animeID),
      queryFn: () => getInstanceParserPolicy(animeID),
    });
    setDraft(fresh.parser_kind);
  }

  const shownKind = draft ?? data?.parser_kind;
  useEffect(() => {
    onKindChange?.(shownKind);
  }, [shownKind, onKindChange]);

  const catalogReady = catalog.data !== undefined || catalog.isError;
  const loading = (data === undefined && policy.isLoading) || (data !== undefined && !catalogReady);
  const options = parserKindOptions(catalog.data, data?.parser_kind);
  const usesPatterns = parserKindUsesPatterns(shownKind);
  const hasKeptPatterns = !usesPatterns && patternCount > 0;
  const hasRegexOption = options.some((option) => option.value === "regex");

  return (
    <section className="tw:grid tw:min-w-0 tw:gap-2">
      {loading && (
        <div
          role="status"
          aria-label={t("act.reading")}
          className="tw:flex tw:min-h-[32px] tw:flex-wrap tw:items-center tw:gap-3"
        >
          <span className={`${skelBar} tw:h-8 tw:w-32 tw:rounded-lg`} />
          <span className={`${skelBar} tw:h-3.5 tw:w-52`} />
        </div>
      )}
      {policy.error && !data && <StatePill tone="offline">{toUserMessage(policy.error, t("policy.parser.loadFailed"))}</StatePill>}
      {catalog.error && <StatePill tone="offline">{toUserMessage(catalog.error, t("policy.parser.catalogFailed"))}</StatePill>}
      {data && catalogReady && (
        <div className="tw:grid tw:min-w-0 tw:gap-2">
          <ParserKindChoice
            label={t("policy.parser.kindLabel")}
            value={shownKind ?? data.parser_kind}
            options={options}
            onChange={setDraft}
            readOnly={readOnly || saving}
            noteSuffix={
              hasKeptPatterns ? t("forms.rules.episodePatternsUnusedCount", { n: patternCount }) : undefined
            }
            action={
              hasKeptPatterns && !readOnly && !saving && hasRegexOption ? (
                <Button variant="ghost" size="sm" type="button" onClick={() => setDraft("regex")}>
                  {t("forms.rules.switchToRegexView", { n: patternCount })}
                </Button>
              ) : undefined
            }
          />
          {conflict !== undefined ? (
            <ConflictBlock
              currentVersion={conflict}
              message={error}
              onRefresh={() => void refreshAfterConflict()}
            />
          ) : (
            error && <Alert tone="error" title={error} />
          )}
        </div>
      )}
    </section>
  );
}
