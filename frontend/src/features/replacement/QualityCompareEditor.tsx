import { useId, useState, type DragEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, ChevronLeft, ChevronRight, GripVertical, Plus, X } from "lucide-react";
import { getReplacementCompareVocabulary, type QualityCompareKey, type QualityProfile } from "../../lib/api/client";
import { fileMaintenanceKeys } from "../../lib/queryKeys";
import { t } from "../../lib/i18n";
import { Button, Checkbox, DropdownMenu, Input, type MenuEntry } from "../../ui/base";
import { useAnimeTabNavigation } from "../anime-tabs/tabNavigation";
import {
  COMPARE_NAMESPACES,
  addEntry,
  addItem,
  comboValues,
  entryLabel,
  itemDef,
  moveEntry,
  moveItem,
  parseEntry,
  removeEntry,
  removeItem,
  suggestions,
  unusedItems,
  type EntryProblem,
} from "./qualityProfile";
import { ITEM_COPY, NAMESPACE_COPY, PROBLEM_COPY, compareItemName } from "./compareCopy";

// "What counts as better": the re-fetch comparison-terms editor. Both the global
//   replacement policy and the anime config use this one component (the "try it" affordance is not here).
//   Terms are compared from top to bottom, item by item; sort values run from best to worst.

const iconButton = "tw:size-7 tw:px-0";

export function QualityCompareEditor({
  value,
  onChange,
  admissionMode,
  managedAnimeID,
  disabled,
}: {
  value: QualityProfile;
  onChange: (next: QualityProfile) => void;
  admissionMode: "all" | "per_group" | "single_copy";
  /** Scopes AniBT value suggestions to one anime; absent = all releases. */
  managedAnimeID?: string;
  disabled?: boolean;
}) {
  const vocabularyQuery = useQuery({
    queryKey: fileMaintenanceKeys.compareVocabulary(managedAnimeID ?? ""),
    queryFn: () => getReplacementCompareVocabulary(managedAnimeID),
    staleTime: 5 * 60_000,
    retry: false,
    enabled: admissionMode !== "all",
  });
  const vocabulary = new Map((vocabularyQuery.data?.items ?? []).map((item) => [item.key, item.values]));
  const [dragFrom, setDragFrom] = useState<number>();
  const count = value.items.length;
  const unused = unusedItems(value);

  const addMenu: MenuEntry[] = [];
  for (const namespace of COMPARE_NAMESPACES) {
    const items = unused.filter((item) => item.namespace === namespace);
    if (items.length === 0) continue;
    if (addMenu.length > 0) addMenu.push({ type: "divider", key: `divider-${namespace}` });
    for (const item of items) addMenu.push({ key: item.key, label: compareItemName(item.key), onSelect: () => onChange(addItem(value, item.key)) });
  }

  function dropOn(index: number, event: DragEvent) {
    if (dragFrom === undefined) return;
    event.preventDefault();
    onChange(moveItem(value, dragFrom, index));
    setDragFrom(undefined);
  }

  // "Keep all" compares nothing: the editor collapses to a one-line note. The profile itself is
  //   untouched (the caller keeps it), so switching back restores the edited terms.
  if (admissionMode === "all") {
    return (
      <div className="tw:grid tw:gap-1" role="group" aria-label={t("qualityCompare.title")}>
        <h3 className="settings-policy-card-title text-body">{t("qualityCompare.title")}</h3>
        <p className="settings-policy-card-description text-caption">{t("qualityCompare.allModeNote")}</p>
      </div>
    );
  }

  return (
    <div className="tw:grid tw:gap-3" role="group" aria-label={t("qualityCompare.title")}>
      <div className="tw:grid tw:gap-1">
        <h3 className="settings-policy-card-title text-body">{t("qualityCompare.title")}</h3>
        <p className="settings-policy-card-description text-caption">{t("qualityCompare.description")}</p>
        <p className="settings-policy-card-description text-caption">{t("qualityCompare.sourceHint")}</p>
      </div>
      <ol className="tw:m-0 tw:grid tw:list-none tw:gap-2 tw:p-0" aria-label={t("qualityCompare.listAria")}>
        {value.items.map((item, index) => {
          const def = itemDef(item.key);
          const name = compareItemName(item.key);
          return (
            <li
              key={item.key}
              draggable={!disabled}
              onDragStart={(event) => {
                if ((event.target as HTMLElement).closest("[data-entry]")) return;
                setDragFrom(index);
                event.dataTransfer.effectAllowed = "move";
              }}
              onDragEnd={() => setDragFrom(undefined)}
              onDragOver={(event) => { if (dragFrom !== undefined) event.preventDefault(); }}
              onDrop={(event) => dropOn(index, event)}
              className="tw:rounded-control tw:border tw:border-border tw:bg-card"
            >
              <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2 tw:px-2.5 tw:py-2">
                <span title={t("qualityCompare.dragHandle")} aria-hidden="true" className="tw:cursor-grab tw:text-muted-foreground">
                  <GripVertical className="tw:size-4" />
                </span>
                <span className="tw:w-5 tw:text-center tw:text-xs tw:text-muted-foreground tw:tabular-nums">{index + 1}</span>
                <span className="tw:flex tw:min-w-32 tw:flex-1 tw:flex-wrap tw:items-center tw:gap-1.5 text-body">
                  <span className="tw:rounded tw:bg-accent tw:px-1.5 tw:text-xs tw:text-accent-foreground">{t(NAMESPACE_COPY[def.namespace])}</span>
                  <span className="tw:font-semibold">{t(ITEM_COPY[item.key])}</span>
                  {def.namespace === "rt" && <span className="tw:text-xs tw:text-muted-foreground">{t("qualityCompare.rtSource")}</span>}
                </span>
                <Button size="sm" variant="ghost" className={iconButton} aria-label={t("qualityCompare.moveUp", { item: name })} disabled={disabled || index === 0} onClick={() => onChange(moveItem(value, index, index - 1))}>
                  <ArrowUp size={14} aria-hidden="true" />
                </Button>
                <Button size="sm" variant="ghost" className={iconButton} aria-label={t("qualityCompare.moveDown", { item: name })} disabled={disabled || index === count - 1} onClick={() => onChange(moveItem(value, index, index + 1))}>
                  <ArrowDown size={14} aria-hidden="true" />
                </Button>
                <Button size="sm" variant="ghost" className={iconButton} aria-label={t("qualityCompare.removeItem", { item: name })} disabled={disabled} onClick={() => onChange(removeItem(value, index))}>
                  <X size={14} aria-hidden="true" />
                </Button>
              </div>
              <div className="tw:grid tw:gap-2 tw:px-3 tw:pb-3 tw:sm:pl-11">
                {def.kind === "score" && <ScoreBody admissionMode={admissionMode} />}
                {def.kind === "revision" && <p className="business-hint text-caption">{t("qualityCompare.revisionHint")}</p>}
                {(def.kind === "single" || def.kind === "multi") && (
                  <RankedBody
                    profile={value}
                    itemKey={item.key}
                    ranks={item.ranks}
                    vocabulary={vocabulary.get(item.key)}
                    onChange={onChange}
                    disabled={disabled}
                  />
                )}
              </div>
            </li>
          );
        })}
      </ol>
      {addMenu.length > 0 && !disabled && (
        <div>
          <DropdownMenu
            align="start"
            trigger={<Button size="sm"><Plus size={14} aria-hidden="true" />{t("qualityCompare.addItem")}</Button>}
            items={addMenu}
          />
        </div>
      )}
    </div>
  );
}

function ScoreBody({ admissionMode }: { admissionMode: string }) {
  const navigate = useAnimeTabNavigation();
  return (
    <>
      <p className="business-hint text-caption">{t("qualityCompare.scoreHint")}</p>
      {admissionMode === "per_group" && <p className="business-hint text-caption">{t("qualityCompare.scorePerGroup")}</p>}
      {navigate && (
        <div>
          <Button size="sm" variant="link" onClick={() => navigate("subgroups")}>{t("qualityCompare.scoreLink")}</Button>
        </div>
      )}
    </>
  );
}

function RankedBody({
  profile,
  itemKey,
  ranks,
  vocabulary,
  onChange,
  disabled,
}: {
  profile: QualityProfile;
  itemKey: QualityCompareKey;
  ranks: string[][];
  vocabulary: string[] | undefined;
  onChange: (next: QualityProfile) => void;
  disabled?: boolean;
}) {
  const def = itemDef(itemKey);
  const name = compareItemName(itemKey);
  const listID = useId();
  const [text, setText] = useState("");
  const [combo, setCombo] = useState<string[]>([]);
  const [problem, setProblem] = useState<EntryProblem>();
  const [dragFrom, setDragFrom] = useState<number>();

  function add(values: string[]): boolean {
    const result = addEntry(profile, itemKey, values);
    if (result.problem) {
      setProblem(result.problem);
      return false;
    }
    setProblem(undefined);
    onChange(result.profile);
    return true;
  }

  function addTyped() {
    if (!text.trim()) return;
    if (add(parseEntry(text, itemKey))) setText("");
  }

  const offered = suggestions(ranks, vocabulary);
  const comboChoices = def.kind === "multi" ? comboValues(ranks, vocabulary) : [];
  const hints = [t("qualityCompare.rankHint")];
  if (def.kind === "multi") hints.push(t("qualityCompare.multiHint"));
  if (def.namespace === "anibt") hints.push(t("qualityCompare.anibtHint"));

  return (
    <>
      <p className="business-hint text-caption">{hints.join(" ")}</p>
      <ol className="tw:m-0 tw:flex tw:list-none tw:flex-wrap tw:items-center tw:gap-1.5 tw:p-0" aria-label={t("qualityCompare.ranksAria", { item: name })}>
        {ranks.map((entry, index) => {
          const label = entryLabel(entry);
          return (
            <li
              key={label}
              data-entry=""
              draggable={!disabled}
              onDragStart={(event) => { event.stopPropagation(); setDragFrom(index); event.dataTransfer.effectAllowed = "move"; }}
              onDragEnd={() => setDragFrom(undefined)}
              onDragOver={(event) => { if (dragFrom !== undefined) { event.preventDefault(); event.stopPropagation(); } }}
              onDrop={(event) => {
                if (dragFrom === undefined) return;
                event.preventDefault();
                event.stopPropagation();
                onChange(moveEntry(profile, itemKey, dragFrom, index));
                setDragFrom(undefined);
              }}
              className="tw:inline-flex tw:items-center tw:gap-0.5 tw:rounded-full tw:border tw:border-border tw:bg-muted tw:py-0.5 tw:pr-1 tw:pl-2.5 tw:text-sm"
            >
              <span className="tw:mr-1 tw:text-xs tw:text-muted-foreground tw:tabular-nums">{index + 1}</span>
              <span>{label}</span>
              <Button size="sm" variant="ghost" className="tw:h-5 tw:px-1" aria-label={t("qualityCompare.entryEarlier", { value: label })} disabled={disabled || index === 0} onClick={() => onChange(moveEntry(profile, itemKey, index, index - 1))}>
                <ChevronLeft size={12} aria-hidden="true" />
              </Button>
              <Button size="sm" variant="ghost" className="tw:h-5 tw:px-1" aria-label={t("qualityCompare.entryLater", { value: label })} disabled={disabled || index === ranks.length - 1} onClick={() => onChange(moveEntry(profile, itemKey, index, index + 1))}>
                <ChevronRight size={12} aria-hidden="true" />
              </Button>
              <Button size="sm" variant="ghost" className="tw:h-5 tw:px-1" aria-label={t("qualityCompare.entryRemove", { value: label })} disabled={disabled} onClick={() => onChange(removeEntry(profile, itemKey, index))}>
                <X size={12} aria-hidden="true" />
              </Button>
            </li>
          );
        })}
        {ranks.length === 0 ? (
          <li className="business-hint text-caption">{t("qualityCompare.empty")}</li>
        ) : (
          <li className="tw:rounded-full tw:border tw:border-dashed tw:border-border tw:px-2.5 tw:py-0.5 tw:text-xs tw:text-muted-foreground">{t("qualityCompare.others")}</li>
        )}
      </ol>
      {!disabled && (
        <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
          <Input
            controlSize="sm"
            className="tw:max-w-56"
            list={listID}
            aria-label={t("qualityCompare.valueAria", { item: name })}
            placeholder={def.kind === "multi" ? t("qualityCompare.valuePlaceholderMulti") : t("qualityCompare.valuePlaceholder")}
            value={text}
            onChange={(event) => { setText(event.target.value); setProblem(undefined); }}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.nativeEvent.isComposing) {
                event.preventDefault();
                addTyped();
              }
            }}
          />
          <datalist id={listID}>
            {offered.map((option) => <option key={option} value={option} />)}
          </datalist>
          <Button size="sm" aria-label={t("qualityCompare.addValueAria", { item: name })} disabled={!text.trim()} onClick={addTyped}>
            {t("qualityCompare.addValue")}
          </Button>
        </div>
      )}
      {!disabled && def.kind === "multi" && comboChoices.length > 1 && (
        <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-x-3 tw:gap-y-1" role="group" aria-label={t("qualityCompare.comboLabel")}>
          <span className="business-hint text-caption">{t("qualityCompare.comboLabel")}</span>
          {comboChoices.map((choice) => (
            <Checkbox
              key={choice}
              size="sm"
              aria-label={t("qualityCompare.comboValueAria", { value: choice })}
              checked={combo.includes(choice)}
              onCheckedChange={(checked) => {
                setProblem(undefined);
                setCombo((current) => (checked ? [...current, choice] : current.filter((value) => value !== choice)));
              }}
            >
              {choice}
            </Checkbox>
          ))}
          <Button size="sm" disabled={combo.length < 2} onClick={() => { if (add(combo)) setCombo([]); }}>
            {t("qualityCompare.comboAdd")}
          </Button>
        </div>
      )}
      {problem && <span role="alert" className="settings-policy-error text-caption">{t(PROBLEM_COPY[problem])}</span>}
    </>
  );
}
