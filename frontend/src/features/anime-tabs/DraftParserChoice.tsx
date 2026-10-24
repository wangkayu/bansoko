import { useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { listParserCatalog, toUserMessage, type AnimeDraft, type BusinessSnapshot } from "../../lib/api/client";
import { configKeys } from "../../lib/queryKeys";
import { parserKindOptions } from "../../lib/parserKind";
import { t } from "../../lib/i18n";
import { StatePill } from "../../ui";
import { ParserKindChoice } from "../anime-forms/ParserKindChoice";
import { draftParserKind, isDraftChosenParser, withDraftParserKind } from "../anime-config/businessDraft";

type ParserEnvelope = BusinessSnapshot["parser"];

/**
 * 订阅前草稿的解析器选择：与订阅后的实例解析器卡同一组选项（/parser-catalog 里具备
 * release_title 能力的解析器），放在规则 tab「解析」段内、集数识别规则之上。取值是
 * 草稿业务快照里的解析器信封：预览按它解析集数，保存草稿时随快照一起冻结，订阅时
 * 复制成实例的解析器。选回载入时的解析器就恢复载入时的信封。
 */
export function DraftParserChoice({ draft, onDraftChange }: { draft: AnimeDraft; onDraftChange: (next: AnimeDraft) => void }) {
  const catalog = useQuery({ queryKey: configKeys.parserCatalog, queryFn: listParserCatalog, staleTime: Infinity });
  const envelope = draft.business?.parser;
  // The loaded envelope: the one seen at first load and at every reload after ("load the latest
  // default settings"). An envelope the draft swapped in never overwrites it, so switching back
  // to the original parser restores it as it was.
  const loaded = useRef<ParserEnvelope | undefined>(undefined);
  if (envelope && (!loaded.current || !isDraftChosenParser(envelope))) loaded.current = envelope;
  const kind = draftParserKind(draft);
  if (!envelope || kind === undefined) return null;
  const options = parserKindOptions(catalog.data, kind);
  return (
    <div className="tw:grid tw:min-w-0 tw:gap-2">
      {catalog.error && <StatePill tone="offline">{toUserMessage(catalog.error, t("policy.parser.catalogFailed"))}</StatePill>}
      <ParserKindChoice
        label={t("policy.parser.kindLabel")}
        value={kind}
        options={options}
        onChange={(next) => {
          if (next === kind || !loaded.current) return;
          onDraftChange(withDraftParserKind(draft, next, loaded.current, "candidate_draft"));
        }}
      />
    </div>
  );
}
