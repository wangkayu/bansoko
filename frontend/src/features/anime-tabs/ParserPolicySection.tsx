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
import { Alert } from "../../ui/base";
import { t } from "../../lib/i18n";
import { parserKindOptions } from "../../lib/parserKind";
import { ParserKindChoice } from "../anime-forms/ParserKindChoice";

// The episode parser card: the sole UI of the instance-level parser policy (exposed by prior
// ruling). The options come from /parser-catalog (parsers with the release_title capability:
// regex / reltitle), displayed as an either/or; read-only disables the whole group. The choice is
// the rules tab's draft, committed by the dialog footer's single save (expected-version CAS; on
// 409 ConflictBlock asks for a refresh). The displayed value (the unsaved choice winning) is
// reported to the rules tab through onKindChange, deciding whether the episode recognition rules
// show. The rules tab's "use defaults everywhere" fills the default parser with the unsaved
// choice through reset, submitted with the same save.
// No outer frame: the card sits inside the "parsing" section right below "parse episode numbers
// from RSS titles", without another border.
const card = "tw:grid tw:min-w-0 tw:gap-2";
const cardBody = "tw:grid tw:min-w-0 tw:gap-3";
const header = "tw:flex tw:min-w-0 tw:items-center tw:justify-between tw:gap-2";
const note = "tw:m-0 tw:text-xs tw:text-muted-foreground";

export function ParserPolicySection(props: {
  animeID: string;
  active?: boolean;
  readOnly?: boolean;
  /** The displayed parser (the unsaved choice winning, otherwise the saved value). */
  onKindChange?: (kind: string | undefined) => void;
  /** Whether an unsaved choice exists. */
  onDirtyChange?: (dirty: boolean) => void;
  /** "Use defaults everywhere": every seq change fills kind with the unsaved choice. */
  reset?: { kind: string; seq: number };
}) {
  const { animeID, active = true, readOnly = false, onKindChange, onDirtyChange, reset } = props;
  const queryClient = useQueryClient();
  const policy = useQuery({
    queryKey: fileMaintenanceKeys.parserPolicy(animeID),
    queryFn: () => getInstanceParserPolicy(animeID),
    enabled: active,
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

  const dirty = !readOnly && policy.data !== undefined && draft !== undefined && draft !== policy.data.parser_kind;
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);
  const resetSeq = reset?.seq;
  const resetKind = reset?.kind;
  useEffect(() => {
    if (resetSeq !== undefined && resetKind !== undefined && !readOnly) setDraft(resetKind);
  }, [resetSeq, resetKind, readOnly]);
  async function handleSave() {
    const data = policy.data;
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
      void queryClient.invalidateQueries({ queryKey: fileMaintenanceKeys.parserPolicy(animeID) });
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

  // A 409 refresh: loads the server's latest value and drops the stale local choice; the user picks again.
  async function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    const fresh = await queryClient.fetchQuery({
      queryKey: fileMaintenanceKeys.parserPolicy(animeID),
      queryFn: () => getInstanceParserPolicy(animeID),
    });
    setDraft(fresh.parser_kind);
  }

  const data = policy.data;
  const options = parserKindOptions(catalog.data, data?.parser_kind);
  const shownKind = draft ?? data?.parser_kind;
  useEffect(() => {
    onKindChange?.(shownKind);
  }, [shownKind, onKindChange]);

  return (
    <section className={card}>
      <div className={header}>
        <h3 className="tw:m-0 tw:text-sm tw:font-medium tw:text-foreground">{t("policy.parser.title")}</h3>
      </div>
      {policy.isLoading && <StatePill busy>{t("act.reading")}</StatePill>}
      {policy.error && <StatePill tone="offline">{toUserMessage(policy.error, t("policy.parser.loadFailed"))}</StatePill>}
      {catalog.error && <StatePill tone="offline">{toUserMessage(catalog.error, t("policy.parser.catalogFailed"))}</StatePill>}
      {data && (
        <div className={cardBody}>
          <ParserKindChoice
            label={t("policy.parser.kindLabel")}
            value={draft ?? data.parser_kind}
            options={options}
            onChange={setDraft}
            readOnly={readOnly || saving}
          />
          <p className={note}>{t("policy.parser.hint")}</p>
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
