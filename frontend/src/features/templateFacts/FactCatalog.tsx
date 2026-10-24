import { useId, useState } from "react";
import { t } from "../../lib/i18n";
import { Input } from "../../ui/base";
import { Chip } from "../../ui";
import { factLabel, factRootLabel, factTitle, filterFacts, groupFacts, placeholderText, splitPlaceholders, type CatalogFact } from "./facts";

// The template-variable catalog: grouped by root, searchable, click-to-insert. Shared by the
//   naming and notification editors; the token decides the spelling of what inserts (${path} or
//   {{path}}).
export function FactCatalog({
  facts,
  token,
  onInsert,
}: {
  facts: readonly CatalogFact[];
  token: (path: string) => string;
  onInsert: (token: string) => void;
}) {
  const [query, setQuery] = useState("");
  const searchId = useId();
  const groups = groupFacts(filterFacts(facts, query));
  return (
    <div className="tw:grid tw:gap-2">
      <label htmlFor={searchId} className="tw:sr-only">{t("templateFacts.searchLabel")}</label>
      <Input
        id={searchId}
        controlSize="sm"
        type="search"
        value={query}
        placeholder={t("templateFacts.searchPlaceholder")}
        onChange={(e) => setQuery(e.target.value)}
      />
      <p className="naming-muted text-caption">{t("templateFacts.insertHint")}</p>
      {groups.length === 0 && <p className="naming-muted text-caption">{t("templateFacts.noMatch")}</p>}
      {groups.map(([root, items]) => (
        <div key={root} className="naming-palette-group">
          <span className="naming-palette-label text-caption">{factRootLabel(root)}</span>
          <div className="naming-palette-items">
            {items.map((fact) => (
              <button
                key={fact.path}
                type="button"
                title={factTitle(fact)}
                className="naming-palette-item text-caption"
                onClick={() => onInsert(token(fact.path))}
              >
                {factLabel(fact)}
                <span className="naming-palette-detail">{fact.path}</span>
                {fact.phase === "execution" && (
                  <Chip className="tw:ml-1">{t("templateFacts.phaseExecution")}</Chip>
                )}
                {fact.nullable && <Chip className="tw:ml-1">{t("templateFacts.nullable")}</Chip>}
                {fact.sample_value != null && fact.phase !== "execution" && (
                  <span className="naming-palette-example">{t("templateFacts.example", { value: fact.sample_value })}</span>
                )}
              </button>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

/** Preview text: values that exist only after download render as bright placeholders and do not count as empty. */
export function PreviewText({ text }: { text: string }) {
  return (
    <>
      {splitPlaceholders(text).map((part, index) =>
        part.placeholder ? (
          <mark key={index} className="tw:rounded tw:bg-primary/15 tw:px-0.5 tw:font-medium tw:text-primary" title={t("templateFacts.placeholderHint", { path: part.placeholder })}>
            {placeholderText(part.placeholder)}
          </mark>
        ) : (
          <span key={index}>{part.text}</span>
        ),
      )}
    </>
  );
}
