import type { TagFilterRow, TagFilterRules } from "../../lib/api/client";
import { hasTag, tagFullLabel, tagLabel } from "../../lib/anibtTags";
import { titleTagFullLabel, titleTagLabel } from "../../lib/titleTags";
import { t } from "../../lib/i18n";

// A seed row's tags: AniBT tags and title-parsed tags each take one row by source, with the
// source named at the row's head; every tag shows, no cross-source dedupe, none collapsed. In the
// effective tag filter rules, tags hit by that source's "require" row are emphasized and tags hit
// by "exclude" get the warning color; hovering spells out the kind.

const badgeClass = {
  require: "chain-file-badge chain-file-badge-strong tw:font-medium text-badge",
  exclude: "chain-file-badge chain-file-badge-excluded text-badge",
  none: "chain-file-badge text-badge",
} as const;

function roleOf(tag: string, row: TagFilterRow | undefined): keyof typeof badgeClass {
  if (!row) return "none";
  // Matches the server: trimmed of surrounding whitespace, compared case-insensitively.
  if (hasTag(row.require, tag)) return "require";
  if (hasTag(row.exclude, tag)) return "exclude";
  return "none";
}

function SourceRow({ tags, source, aria, label, fullLabel, row }: {
  tags: readonly string[];
  source: string;
  aria: string;
  label: (tag: string) => string;
  fullLabel: (tag: string) => string;
  row: TagFilterRow | undefined;
}) {
  return (
    <span className="chain-file-badges" aria-label={aria}>
      <span className="text-caption text-muted">{source}</span>
      {tags.map((tag) => (
        <span key={tag} className={badgeClass[roleOf(tag, row)]} title={fullLabel(tag)}>
          {label(tag)}
        </span>
      ))}
    </span>
  );
}

/** Renders nothing without tags. rules are the effective tag filter rules, used to mark tags hit by require/exclude. */
export function TagBadges({
  anibt,
  title,
  rules,
}: {
  anibt?: readonly string[] | null;
  title?: readonly string[] | null;
  rules?: TagFilterRules;
}) {
  const hasAniBT = !!anibt && anibt.length > 0;
  const hasTitle = !!title && title.length > 0;
  if (!hasAniBT && !hasTitle) return null;
  return (
    <span className="chain-file-badge-groups" aria-label={t("tagFilters.seedRowAria")}>
      {hasAniBT && (
        <SourceRow tags={anibt} source={t("tagFilters.sourceAniBT")} aria={t("anibtTags.rowAria")} label={tagLabel} fullLabel={tagFullLabel} row={rules?.anibt} />
      )}
      {hasTitle && (
        <SourceRow tags={title} source={t("tagFilters.sourceTitle")} aria={t("titleTags.rowAria")} label={titleTagLabel} fullLabel={titleTagFullLabel} row={rules?.title} />
      )}
    </span>
  );
}
