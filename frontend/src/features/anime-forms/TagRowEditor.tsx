import { useState } from "react";
import { ArrowLeftRight, Ban, Check, X, type LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import type { TagFilterRow } from "../../lib/api/client";
import { Button, Input, Segmented, Select, type SelectOption } from "../../ui/base";
import {
  addRowTag,
  hasTag,
  isPresetTag,
  joinTag,
  removeRowTag,
  rowRoleFull,
  rowTags,
  setRowTagRole,
  splitTag,
  tagKey,
  tagRoleLabel,
  validateTagValue,
  type TagRole,
} from "../../lib/anibtTags";
import type { TagRowVocabulary } from "../../lib/tagRowVocabulary";
import { t } from "../../lib/i18n";
import { withNote } from "../../lib/i18n/format";

// The editor of one tag rule row (shared by the anime rules card and the default rules, and by
// the AniBT row and the title-parsing row). Additive: the user adds tags one by one, each tag
// independently "require" or "exclude".
// - Upper half: the added tags split into two rows by role (require / exclude), told apart by
//   color; each tag can switch role or be removed;
// - Lower half (editable only): "add as" decides the new tag's role; add by clicking a candidate
//   tag or typing. An added tag no longer appears among the candidates, and one tag is never in
//   both lists at once.
// Read-only shows only the added tags. The two rows' differences are described by
// TagRowVocabulary.

const ROLES: readonly TagRole[] = ["require", "exclude"];
const otherRole = (role: TagRole): TagRole => (role === "require" ? "exclude" : "require");

const CHIP_BASE =
  "tw:inline-flex tw:items-center tw:gap-1 tw:rounded-[0.25rem] tw:border tw:text-xs tw:leading-normal tw:transition-colors tw:disabled:cursor-not-allowed tw:disabled:border-border tw:disabled:bg-transparent tw:disabled:text-muted-foreground";
const CHIP_PAD = "tw:px-2 tw:py-0.5";
// An editable added tag carries two icon buttons at its right; no extra vertical padding, so rows do not grow taller.
const CHIP_PAD_ACTIONS = "tw:py-0 tw:pl-2 tw:pr-0.5";
const CHIP_CANDIDATE = "tw:border-border tw:bg-transparent tw:text-muted-foreground tw:enabled:hover:text-foreground";
const CHIP_ROLE: Record<TagRole, string> = {
  require: "tw:border-primary/40 tw:bg-primary/10 tw:text-foreground",
  exclude: "tw:border-destructive/40 tw:bg-destructive/10 tw:text-foreground",
};
// Icons keep the two roles from being told apart by color alone.
const ROLE_ICON: Record<TagRole, LucideIcon> = { require: Check, exclude: Ban };
const ROLE_LABEL: Record<TagRole, string> = {
  require: "tw:text-primary",
  exclude: "tw:text-destructive",
};
const CHIP_ACTION =
  "tw:inline-flex tw:size-5 tw:items-center tw:justify-center tw:rounded-[0.2rem] tw:text-muted-foreground tw:hover:bg-muted tw:hover:text-foreground tw:disabled:pointer-events-none tw:disabled:text-muted-foreground/50 tw:focus-visible:outline-2 tw:focus-visible:outline-ring";
const ROW_GRID = "tw:grid tw:grid-cols-[4.5rem_1fr] tw:items-start tw:gap-2";

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
  const [addDimension, setAddDimension] = useState<string>(vocabulary.defaultAddDimension);
  const [addValue, setAddValue] = useState("");
  const [addError, setAddError] = useState<string>();
  const disabled = readOnly || !onChange;
  const dimension = vocabulary.dimensions.includes(addDimension) ? addDimension : vocabulary.defaultAddDimension;
  const selected = rowTags(value);
  const addChoices = vocabulary.addValues?.(dimension).filter((choice) => !hasTag(selected, joinTag(dimension, choice)));
  const addFull = rowRoleFull(value, addRole);

  function candidates(group: string): string[] {
    const result = vocabulary.presets(group).map((preset) => joinTag(group, preset));
    const observed = extraTags.map(vocabulary.normalizeObserved).filter((tag): tag is string => tag !== undefined);
    for (const tag of observed) {
      if (splitTag(tag)?.dimension === group && !hasTag(result, tag)) result.push(tag);
    }
    return result.filter((tag) => !hasTag(selected, tag));
  }

  function add() {
    if (!onChange) return;
    const chosen = addChoices ? addValue || addChoices[0] || "" : addValue;
    const error = validateTagValue(value, addRole, dimension, chosen);
    setAddError(error);
    if (error) return;
    onChange(addRowTag(value, joinTag(dimension, chosen.trim()), addRole));
    setAddValue("");
  }

  function countNote(tag: string): string | undefined {
    const count = counts?.get(tagKey(tag));
    return count ? t("tagFilters.count", { n: count }) : undefined;
  }

  const groups = disabled ? [] : vocabulary.dimensions.map((group) => ({ group, tags: candidates(group) })).filter((entry) => entry.tags.length > 0);
  const filledRoles = ROLES.filter((role) => value[role].length > 0);

  return (
    <div className="tw:grid tw:gap-3">
      {filledRoles.length === 0 ? (
        <p className="tw:text-xs tw:text-muted-foreground">{t("anibtTags.none")}</p>
      ) : (
        <div className="tw:grid tw:gap-1.5">
          {filledRoles.map((role) => (
            <div key={role} className={ROW_GRID}>
              <span className={cn("tw:pt-0.5 tw:text-xs tw:font-medium", ROLE_LABEL[role])}>{tagRoleLabel(role)}</span>
              <ul aria-label={tagRoleLabel(role)} className="tw:flex tw:flex-wrap tw:gap-1.5">
                {value[role].map((tag) => {
                  const parsed = splitTag(tag);
                  const full = vocabulary.fullLabel(tag);
                  const count = countNote(tag);
                  const target = otherRole(role);
                  return (
                    <li
                      key={tagKey(tag)}
                      data-testid="tag-row-entry"
                      data-role={role}
                      title={[vocabulary.hoverLabel(tag), count].filter(Boolean).join("\n")}
                      className={cn(CHIP_BASE, CHIP_ROLE[role], disabled ? CHIP_PAD : CHIP_PAD_ACTIONS)}
                    >
                      <RoleIcon role={role} />
                      <span>{parsed ? vocabulary.valueLabel(parsed.dimension, parsed.value) : tag}</span>
                      {!disabled && (
                        <>
                          <button
                            type="button"
                            className={CHIP_ACTION}
                            aria-label={t(`anibtTags.switchTo.${target}`, { tag: full })}
                            title={t(`anibtTags.switchTo.${target}`, { tag: full })}
                            disabled={rowRoleFull(value, target)}
                            onClick={() => onChange?.(setRowTagRole(value, tag, target))}
                          >
                            <ArrowLeftRight className="tw:size-3" aria-hidden="true" />
                          </button>
                          <button
                            type="button"
                            className={CHIP_ACTION}
                            aria-label={t("anibtTags.removeAria", { tag: full })}
                            title={t("anibtTags.removeAria", { tag: full })}
                            onClick={() => onChange?.(removeRowTag(value, tag))}
                          >
                            <X className="tw:size-3" aria-hidden="true" />
                          </button>
                        </>
                      )}
                    </li>
                  );
                })}
              </ul>
            </div>
          ))}
        </div>
      )}

      {!disabled && vocabulary.dimensions.length > 0 && (
        <div className="tw:grid tw:gap-2 tw:border-t tw:border-border tw:pt-3">
          <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-x-2 tw:gap-y-1">
            <span className="tw:text-xs tw:text-muted-foreground">{t("anibtTags.addAs")}</span>
            <Segmented<TagRole>
              size="compact"
              aria-label={t("anibtTags.addAs")}
              value={addRole}
              options={ROLES.map((role) => ({ value: role, label: tagRoleLabel(role) }))}
              onValueChange={(next) => {
                setAddRole(next);
                setAddError(undefined);
              }}
            />
            <span className="tw:text-xs tw:text-muted-foreground">{t("anibtTags.roleHint")}</span>
          </div>

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

          <div className="tw:grid tw:gap-1">
            <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
              <div className="tw:w-28">
                <Select<string>
                  label={t("anibtTags.addDimension")}
                  value={dimension}
                  options={vocabulary.dimensions.map((entry): SelectOption<string> => ({ value: entry, label: vocabulary.dimensionLabel(entry) }))}
                  onChange={(next) => {
                    setAddDimension(next);
                    setAddValue("");
                    setAddError(undefined);
                  }}
                />
              </div>
              <div className="tw:w-44">
                {addChoices ? (
                  <Select<string>
                    label={t("anibtTags.addValue")}
                    value={addChoices.includes(addValue) ? addValue : addChoices[0] || ""}
                    disabled={addChoices.length === 0}
                    options={addChoices.map((choice): SelectOption<string> => ({ value: choice, label: vocabulary.valueLabel(dimension, choice) }))}
                    onChange={(next) => {
                      setAddValue(next);
                      setAddError(undefined);
                    }}
                  />
                ) : (
                  <Input
                    aria-label={t("anibtTags.addValue")}
                    aria-invalid={addError ? true : undefined}
                    placeholder={t("anibtTags.addValuePlaceholder")}
                    value={addValue}
                    onChange={(event) => {
                      setAddValue(event.target.value);
                      setAddError(undefined);
                    }}
                    onKeyDown={(event) => {
                      if (event.key === "Enter") {
                        event.preventDefault();
                        add();
                      }
                    }}
                  />
                )}
              </div>
              <Button size="sm" onClick={add} disabled={addChoices?.length === 0}>
                {t("anibtTags.add")}
              </Button>
            </div>
            {addError && <span role="alert" className="tw:text-xs tw:text-destructive">{addError}</span>}
          </div>
        </div>
      )}
    </div>
  );
}

function RoleIcon({ role }: { role: TagRole }) {
  const Icon = ROLE_ICON[role];
  return <Icon className={cn("tw:size-3 tw:flex-none", ROLE_LABEL[role])} aria-hidden="true" />;
}
