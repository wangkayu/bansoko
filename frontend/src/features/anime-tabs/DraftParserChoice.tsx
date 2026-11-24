import { useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { listParserCatalog, toUserMessage, type AnimeDraft, type BusinessSnapshot } from "../../lib/api/client";
import { configKeys } from "../../lib/queryKeys";
import { parserKindOptions, parserKindUsesPatterns } from "../../lib/parserKind";
import { t } from "../../lib/i18n";
import { StatePill } from "../../ui";
import { Button } from "../../ui/base";
import { ParserKindChoice } from "../anime-forms/ParserKindChoice";
import { draftParserKind, isDraftChosenParser, withDraftParserKind } from "../anime-config/businessDraft";

type ParserEnvelope = BusinessSnapshot["parser"];
const skelBar = "tw:block tw:rounded-full tw:bg-muted tw:motion-safe:animate-pulse";

/**
 * 订阅前草稿的解析器选择：与订阅后的实例解析器卡同一组选项（/parser-catalog 里具备
 * release_title 能力的解析器，reltitle 首选），放在规则 tab「解析」段内、集数识别规则之上。
 * 取值是草稿业务快照里的解析器信封：预览按它解析集数，保存草稿时随快照一起冻结，订阅时
 * 复制成实例的解析器。选回载入时的解析器就恢复载入时的信封。
 */
export function DraftParserChoice({
  draft,
  onDraftChange,
  patternCount = 0,
}: {
  draft: AnimeDraft;
  onDraftChange: (next: AnimeDraft) => void;
  patternCount?: number;
}) {
  const catalog = useQuery({ queryKey: configKeys.parserCatalog, queryFn: listParserCatalog, staleTime: Infinity });
  const envelope = draft.business?.parser;
  const loaded = useRef<ParserEnvelope | undefined>(undefined);
  if (envelope && (!loaded.current || !isDraftChosenParser(envelope))) loaded.current = envelope;
  const kind = draftParserKind(draft);
  if (!envelope || kind === undefined) return null;

  const catalogReady = catalog.data !== undefined || catalog.isError;
  const options = parserKindOptions(catalog.data, kind);
  const usesPatterns = parserKindUsesPatterns(kind);
  const hasKeptPatterns = !usesPatterns && patternCount > 0;
  const hasRegexOption = options.some((option) => option.value === "regex");

  function switchKind(next: string) {
    if (next === kind || !loaded.current) return;
    onDraftChange(withDraftParserKind(draft, next, loaded.current, "candidate_draft"));
  }

  return (
    <div className="tw:grid tw:min-w-0 tw:gap-2">
      {catalog.error && <StatePill tone="offline">{toUserMessage(catalog.error, t("policy.parser.catalogFailed"))}</StatePill>}
      {!catalogReady ? (
        <div
          role="status"
          aria-label={t("act.reading")}
          className="tw:flex tw:min-h-[32px] tw:flex-wrap tw:items-center tw:gap-3"
        >
          <span className={`${skelBar} tw:h-8 tw:w-32 tw:rounded-lg`} />
          <span className={`${skelBar} tw:h-3.5 tw:w-52`} />
        </div>
      ) : (
        <ParserKindChoice
          label={t("policy.parser.kindLabel")}
          value={kind}
          options={options}
          onChange={switchKind}
          noteSuffix={
            hasKeptPatterns ? t("forms.rules.episodePatternsUnusedCount", { n: patternCount }) : undefined
          }
          action={
            hasKeptPatterns && hasRegexOption ? (
              <Button variant="ghost" size="sm" type="button" onClick={() => switchKind("regex")}>
                {t("forms.rules.switchToRegexView", { n: patternCount })}
              </Button>
            ) : undefined
          }
        />
      )}
    </div>
  );
}
