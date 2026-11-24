import { useState } from "react";
import { ChevronDown, Plus, X } from "lucide-react";
import { cn } from "@/lib/utils";
import type { TagFilterRow } from "../../lib/api/client";
import { Button, Segmented } from "../../ui/base";
import {
  addRowTag,
  hasTag,
  isPresetTag,
  joinTag,
  removeRowTag,
  rowRoleFull,
  rowTags,
  splitTag,
  tagKey,
  tagRoleLabel,
  type TagRole,
} from "../../lib/anibtTags";
import type { TagRowVocabulary } from "../../lib/tagRowVocabulary";
import { t } from "../../lib/i18n";
import { withNote } from "../../lib/i18n/format";

const ROLES: readonly TagRole[] = ["require", "exclude"];

const CHIP_BASE =
  "tw:inline-flex tw:items-center tw:gap-1 tw:rounded-[0.25rem] tw:border tw:text-xs tw:leading-normal tw:transition-colors tw:disabled:cursor-not-allowed tw:disabled:border-border tw:disabled:bg-transparent tw:disabled:text-muted-foreground";
const CHIP_PAD = "tw:px-2 tw:py-0.5";
const CHIP_CANDIDATE = "tw:border-border tw:bg-transparent tw:text-muted-foreground tw:enabled:hover:text-foreground";
const GROUP_ROLE: Record<TagRole, string> = {
  require: "tw:border-primary/35 tw:bg-primary/5",
  exclude: "tw:border-destructive/35 tw:bg-destructive/5",
};
const DIM_ROLE: Record<TagRole, string> = {
  require: "tw:border-primary/25 tw:text-primary",
  exclude: "tw:border-destructive/25 tw:text-destructive",
};
const ROLE_LABEL: Record<TagRole, string> = {
  require: "tw:text-primary",
  exclude: "tw:text-destructive",
};
const CHIP_ACTION =
  "tw:inline-flex tw:size-4 tw:items-center tw:justify-center tw:rounded-[0.2rem] tw:text-muted-foreground tw:hover:bg-muted tw:hover:text-foreground tw:disabled:pointer-events-none tw:disabled:text-muted-foreground/50 tw:focus-visible:outline-2 tw:focus-visible:outline-ring";
const ROW_GRID = "tw:grid tw:grid-cols-[4.5rem_1fr] tw:items-start tw:gap-2";

function groupTagsByDimension(tags: readonly string[]): { dimension: string; tags: string[] }[] {
  const groups: { dimension: string; tags: string[] }[] = [];
  const byDim = new Map<string, string[]>();
  for (const tag of tags) {
    const dimension = splitTag(tag)?.dimension ?? "";
    let list = byDim.get(dimension);
    if (!list) {
      list = [];
      byDim.set(dimension, list);
      groups.push({ dimension, tags: list });
    }
    list.push(tag);
  }
  return groups;
}

export function TagRowEditor({
  vocabulary,
  value,
  onChange,
  readOnly = false,
  extraTags = [],
  counts,
}: {
  vocabulary: TagRowVocabulary;
  value: TagFilterRow;
  onChange?: (next: TagFilterRow) => void;
  readOnly?: boolean;
  /** Tags observed for this anime ("kind:value"). */
  extraTags?: readonly string[];
  /** How often each tag occurs in this anime's seeds (by comparison key). */
  counts?: ReadonlyMap<string, number>;
}) {
  const [addRole, setAddRole] = useState<TagRole>("require");
  const [editorOpen, setEditorOpen] = useState(false);
  const disabled = readOnly || !onChange;
  const selected = rowTags(value);
  const addFull = rowRoleFull(value, addRole);

  function candidates(group: string): string[] {
    const result = vocabulary.presets(group).map((preset) => joinTag(group, preset));
    const observed = extraTags.map(vocabulary.normalizeObserved).filter((tag): tag is string => tag !== undefined);
    for (const tag of observed) {
      if (splitTag(tag)?.dimension === group && !hasTag(result, tag)) result.push(tag);
    }
    const available = result.filter((tag) => !hasTag(selected, tag));
    if (!counts || counts.size === 0) return available;
    return available.sort((a, b) => (counts.get(tagKey(b)) ?? 0) - (counts.get(tagKey(a)) ?? 0));
  }

  function countNote(tag: string): string | undefined {
    const count = counts?.get(tagKey(tag));
    return count ? t("tagFilters.count", { n: count }) : undefined;
  }

  function renderGroupPill(group: { dimension: string; tags: string[] }, role: TagRole) {
    return (
      <li
        key={group.dimension || group.tags[0]}
        className={cn(
          "tw:inline-flex tw:flex-wrap tw:items-center tw:gap-1 tw:rounded-[0.375rem] tw:border tw:py-0.5 tw:pr-1",
          GROUP_ROLE[role],
        )}
      >
        {group.dimension && (
          <span className={cn("tw:border-r tw:px-1.5 tw:text-badge tw:font-medium tw:leading-snug", DIM_ROLE[role])}>
            {vocabulary.dimensionLabel(group.dimension)}
          </span>
        )}
        {group.tags.map((tag) => {
          const parsed = splitTag(tag);
          const full = vocabulary.fullLabel(tag);
          const count = countNote(tag);
          return (
            <span
              key={tagKey(tag)}
              data-testid="tag-row-entry"
              data-role={role}
              title={[vocabulary.hoverLabel(tag), count].filter(Boolean).join("\n")}
              className={cn(
                "tw:inline-flex tw:items-center tw:gap-0.5 tw:rounded-[0.25rem] tw:border tw:border-border/70 tw:bg-card tw:text-xs tw:leading-normal tw:text-foreground",
                disabled ? "tw:px-1.5" : "tw:pl-1.5 tw:pr-0.5",
              )}
            >
              <span>{parsed ? vocabulary.valueLabel(parsed.dimension, parsed.value) : tag}</span>
              {!disabled && (
                <button
                  type="button"
                  className={CHIP_ACTION}
                  aria-label={t("anibtTags.removeAria", { tag: full })}
                  title={t("anibtTags.removeAria", { tag: full })}
                  onClick={() => onChange?.(removeRowTag(value, tag))}
                >
                  <X className="tw:size-3" aria-hidden="true" />
                </button>
              )}
            </span>
          );
        })}
      </li>
    );
  }

  const groups = !disabled && editorOpen
    ? vocabulary.dimensions.map((group) => ({ group, tags: candidates(group) })).filter((entry) => entry.tags.length > 0)
    : [];
  const filledRoles = ROLES.filter((role) => value[role].length > 0);

  return (
    <div className="tw:grid tw:gap-3">
      {filledRoles.length === 0 ? (
        <p className="tw:text-xs tw:text-muted-foreground">{t("anibtTags.none")}</p>
      ) : (
        <div className="tw:grid tw:gap-1.5">
          {filledRoles.map((role) => (
            <div key={role} className={ROW_GRID}>
              <span className={cn("tw:pt-1 tw:text-xs tw:font-medium", ROLE_LABEL[role])}>{tagRoleLabel(role)}</span>
              <ul aria-label={tagRoleLabel(role)} className="tw:flex tw:flex-wrap tw:items-center tw:gap-1.5">
                {groupTagsByDimension(value[role]).map((group) => renderGroupPill(group, role))}
              </ul>
            </div>
          ))}
        </div>
      )}

      {!disabled && vocabulary.dimensions.length > 0 && (
        <div className="tw:grid tw:gap-2 tw:border-t tw:border-border tw:pt-2.5">
          <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
            <Button
              type="button"
              size="sm"
              variant="ghost"
              aria-expanded={editorOpen}
              onClick={() => setEditorOpen((open) => !open)}
            >
              {editorOpen ? (
                <>
                  <ChevronDown className="tw:size-3" aria-hidden="true" />
                  {t("anibtTags.closeEditor")}
                </>
              ) : (
                <>
                  <Plus className="tw:size-3" aria-hidden="true" />
                  {t("anibtTags.openEditor")}
                </>
              )}
            </Button>

            {editorOpen && (
              <>
                <span className="tw:text-xs tw:text-muted-foreground">{t("anibtTags.addAs")}</span>
                <Segmented<TagRole>
                  size="compact"
                  aria-label={t("anibtTags.addAs")}
                  value={addRole}
                  options={ROLES.map((role) => ({ value: role, label: tagRoleLabel(role) }))}
                  onValueChange={setAddRole}
                />
              </>
            )}
          </div>

          {editorOpen && groups.length > 0 && (
            <div className="tw:grid tw:gap-1.5">
              {groups.map(({ group, tags }) => (
                <div key={group} className={ROW_GRID}>
                  <span className="tw:pt-0.5 tw:text-xs tw:text-muted-foreground">{vocabulary.dimensionLabel(group)}</span>
                  <div role="group" aria-label={vocabulary.groupAria(vocabulary.dimensionLabel(group))} className="tw:flex tw:flex-wrap tw:gap-1.5">
                    {tags.map((tag) => {
                      const parsed = splitTag(tag)!;
                      const fresh = vocabulary.markFresh && !isPresetTag(tag);
                      const count = counts?.get(tagKey(tag));
                      const title = [fresh ? withNote(vocabulary.hoverLabel(tag), t("anibtTags.newValue")) : vocabulary.hoverLabel(tag), countNote(tag)]
                        .filter(Boolean)
                        .join("\n");
                      return (
                        <button
                          key={tag}
                          type="button"
                          title={title}
                          disabled={addFull}
                          onClick={() => onChange?.(addRowTag(value, tag, addRole))}
                          className={cn(CHIP_BASE, CHIP_PAD, CHIP_CANDIDATE)}
                        >
                          {vocabulary.valueLabel(parsed.dimension, parsed.value)}
                          {count ? <span aria-hidden="true" className="tw:text-badge tw:text-muted-foreground">{count}</span> : null}
                          {fresh && <span className="tw:text-badge tw:text-muted-foreground">{t("anibtTags.newValue")}</span>}
                        </button>
                      );
                    })}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
