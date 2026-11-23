import { useState, type DragEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ArrowDown,
  ArrowUp,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  GripVertical,
  Plus,
  X,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { getReplacementCompareVocabulary, type QualityCompareKey, type QualityProfile } from "../../lib/api/client";
import { fileMaintenanceKeys } from "../../lib/queryKeys";
import { t } from "../../lib/i18n";
import { Button, Input, Popover, Segmented } from "../../ui/base";
import { useAnimeTabNavigation } from "../anime-tabs/tabNavigation";
import {
  COMPARE_NAMESPACES,
  addEntry,
  addItem,
  candidateValues,
  comboValues,
  entryLabel,
  itemDef,
  moveEntry,
  moveItem,
  parseEntry,
  removeEntry,
  removeItem,
  setKey,
  setSizeDirection,
  sizeDirection,
  splitCandidates,
  unusedItems,
  type CompareNamespace,
  type EntryProblem,
  type InsertPosition,
  type SizeDirection,
} from "./qualityProfile";
import {
  ITEM_COPY,
  ITEM_DESC_COPY,
  NAMESPACE_COPY,
  PROBLEM_COPY,
  compareItemName,
  compareValueMeaning,
} from "./compareCopy";

// "What counts as better": the re-fetch comparison-terms editor. Both the global
//   replacement policy and the anime config use this one component.
//   Terms are compared top to bottom, item by item; sort values run best to worst.
//   Each item collapses to a compact priority summary row by default so the full
//   pipeline stays visible on one screen, and expands on click to edit its ranking
//   with a unified chip picker (single quick-pick or multi-value combination builder).

const headerIconButton =
  "tw:inline-flex tw:size-7 tw:phone:size-6 tw:items-center tw:justify-center tw:rounded-control tw:text-muted-foreground tw:transition-colors tw:enabled:hover:bg-muted tw:enabled:hover:text-foreground tw:disabled:cursor-not-allowed tw:disabled:text-muted-foreground/40 tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring";

const rankIconButton =
  "tw:inline-flex tw:size-5 tw:items-center tw:justify-center tw:rounded tw:text-muted-foreground tw:transition-colors tw:enabled:hover:bg-muted tw:enabled:hover:text-foreground tw:disabled:cursor-not-allowed tw:disabled:text-muted-foreground/40 tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring";

const NAMESPACE_BADGE: Record<CompareNamespace, string> = {
  subgroup: "tw:bg-online/12 tw:text-online",
  anibt: "tw:bg-accent tw:text-accent-foreground",
  rt: "tw:bg-muted tw:text-muted-foreground",
  file: "tw:bg-warning/15 tw:text-warning",
};

type DimDragState = {
  from: number;
  over?: number;
  before?: boolean;
};

type RankDragState = {
  from: number;
  over?: number;
  left?: boolean;
};

type PickerMode = "single" | "combo";

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
  const [expandedKeys, setExpandedKeys] = useState<Set<QualityCompareKey>>(() => new Set());
  const [addOpen, setAddOpen] = useState(false);
  const [dimDrag, setDimDrag] = useState<DimDragState>();

  const count = value.items.length;
  const unused = unusedItems(value);
  const allExpanded = count > 0 && value.items.every((item) => expandedKeys.has(item.key));
  const anyExpanded = value.items.some((item) => expandedKeys.has(item.key));

  function toggleExpand(key: QualityCompareKey) {
    setExpandedKeys((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  function setAllExpanded(expand: boolean) {
    setExpandedKeys(expand ? new Set(value.items.map((item) => item.key)) : new Set());
  }

  function handleAddItem(key: QualityCompareKey) {
    onChange(addItem(value, key));
    setExpandedKeys((current) => new Set(current).add(key));
    setAddOpen(false);
  }

  function handleRemoveItem(index: number, key: QualityCompareKey) {
    onChange(removeItem(value, index));
    setExpandedKeys((current) => {
      if (!current.has(key)) return current;
      const next = new Set(current);
      next.delete(key);
      return next;
    });
  }

  function handleDimDragStart(event: DragEvent<HTMLSpanElement>, index: number) {
    setDimDrag({ from: index });
    event.dataTransfer.effectAllowed = "move";
    const card = event.currentTarget.closest("li");
    if (card && typeof event.dataTransfer.setDragImage === "function") {
      event.dataTransfer.setDragImage(card, 24, 24);
    }
  }

  function handleDimDragOver(event: DragEvent<HTMLLIElement>, index: number) {
    if (!dimDrag) return;
    event.preventDefault();
    const bounds = event.currentTarget.getBoundingClientRect();
    const before = event.clientY < bounds.top + bounds.height / 2;
    if (dimDrag.over !== index || dimDrag.before !== before) {
      setDimDrag({ from: dimDrag.from, over: index, before });
    }
  }

  function handleDimDrop(event: DragEvent<HTMLLIElement>, index: number) {
    if (!dimDrag) return;
    event.preventDefault();
    const bounds = event.currentTarget.getBoundingClientRect();
    const before = event.clientY < bounds.top + bounds.height / 2;
    let target = before ? index : index + 1;
    if (dimDrag.from < target) target -= 1;
    if (target !== dimDrag.from) {
      onChange(moveItem(value, dimDrag.from, target));
    }
    setDimDrag(undefined);
  }

  // "Keep all" compares nothing: the editor collapses to a one-line note. The profile itself is
  //   untouched (the caller keeps it), so switching back restores the edited terms.
  if (admissionMode === "all") {
    return (
      <div className="tw:grid tw:min-w-0 tw:grid-cols-[minmax(0,1fr)] tw:gap-1" role="group" aria-label={t("qualityCompare.title")}>
        <h3 className="tw:m-0 tw:text-sm tw:font-semibold tw:text-foreground">{t("qualityCompare.title")}</h3>
        <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("qualityCompare.allModeNote")}</p>
      </div>
    );
  }

  return (
    <div className="tw:grid tw:min-w-0 tw:grid-cols-[minmax(0,1fr)] tw:gap-3" role="group" aria-label={t("qualityCompare.title")}>
      <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-start tw:justify-between tw:gap-2">
        <div className="tw:grid tw:min-w-0 tw:flex-1 tw:gap-1">
          <h3 className="tw:m-0 tw:text-sm tw:font-semibold tw:text-foreground">{t("qualityCompare.title")}</h3>
          <p className="tw:m-0 tw:text-xs tw:leading-relaxed tw:text-muted-foreground">
            {t("qualityCompare.description")} {t("qualityCompare.sourceHint")}
          </p>
        </div>
        {count > 0 && (
          <div className="tw:flex tw:flex-none tw:items-center tw:gap-1">
            <Button size="sm" variant="ghost" disabled={allExpanded} onClick={() => setAllExpanded(true)}>
              {t("qualityCompare.expandAll")}
            </Button>
            <Button size="sm" variant="ghost" disabled={!anyExpanded} onClick={() => setAllExpanded(false)}>
              {t("qualityCompare.collapseAll")}
            </Button>
          </div>
        )}
      </div>

      <ol className="tw:m-0 tw:grid tw:min-w-0 tw:grid-cols-[minmax(0,1fr)] tw:list-none tw:gap-2 tw:p-0" aria-label={t("qualityCompare.listAria")}>
        {value.items.map((item, index) => {
          const def = itemDef(item.key);
          const name = compareItemName(item.key);
          const expanded = expandedKeys.has(item.key);
          const isDragging = dimDrag?.from === index;
          const dropBefore = dimDrag !== undefined && dimDrag.from !== index && dimDrag.over === index && dimDrag.before === true;
          const dropAfter = dimDrag !== undefined && dimDrag.from !== index && dimDrag.over === index && dimDrag.before === false;
          return (
            <li
              key={item.key}
              data-compare-item={item.key}
              onDragOver={(event) => handleDimDragOver(event, index)}
              onDrop={(event) => handleDimDrop(event, index)}
              className={cn(
                "tw:relative tw:min-w-0 tw:rounded-control tw:border tw:bg-card tw:transition-colors",
                expanded ? "tw:border-primary/45 tw:shadow-1" : "tw:border-border",
                isDragging && "tw:border-dashed tw:opacity-45",
                dropBefore && "tw:before:pointer-events-none tw:before:absolute tw:before:inset-x-2.5 tw:before:-top-1.5 tw:before:z-10 tw:before:h-0.5 tw:before:rounded-full tw:before:bg-primary tw:before:content-['']",
                dropAfter && "tw:after:pointer-events-none tw:after:absolute tw:after:inset-x-2.5 tw:after:-bottom-1.5 tw:after:z-10 tw:after:h-0.5 tw:after:rounded-full tw:after:bg-primary tw:after:content-['']",
              )}
            >
              <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-x-2 tw:gap-y-1.5 tw:px-2.5 tw:py-2 tw:phone:gap-x-1 tw:phone:px-2">
                <span
                  data-dim-drag-handle=""
                  draggable={!disabled}
                  onDragStart={(event) => handleDimDragStart(event, index)}
                  onDragEnd={() => setDimDrag(undefined)}
                  title={t("qualityCompare.dragHandle")}
                  aria-hidden="true"
                  className={cn(
                    "tw:inline-flex tw:h-7 tw:w-5 tw:flex-none tw:items-center tw:justify-center tw:rounded tw:text-muted-foreground tw:transition-colors",
                    disabled ? "tw:opacity-40" : "tw:cursor-grab tw:hover:bg-muted tw:hover:text-foreground tw:active:cursor-grabbing",
                  )}
                >
                  <GripVertical className="tw:size-4" />
                </span>
                <span
                  className={cn(
                    "tw:inline-flex tw:size-5.5 tw:flex-none tw:items-center tw:justify-center tw:rounded-md tw:text-xs tw:font-semibold tw:tabular-nums",
                    expanded ? "tw:bg-accent tw:text-accent-foreground" : "tw:bg-muted tw:text-muted-foreground",
                  )}
                >
                  {index + 1}
                </span>
                <button
                  type="button"
                  aria-expanded={expanded}
                  onClick={() => toggleExpand(item.key)}
                  className={cn(
                    "tw:inline-flex tw:min-w-0 tw:items-center tw:gap-1.5 tw:rounded-md tw:px-1 tw:py-0.5 tw:text-left tw:text-sm tw:transition-colors tw:hover:text-primary",
                    expanded ? "tw:flex-1" : "tw:flex-none tw:phone:flex-1",
                  )}
                >
                  <span className={cn("tw:flex-none tw:rounded tw:px-1.5 tw:py-0.5 tw:text-xs tw:font-semibold", NAMESPACE_BADGE[def.namespace])}>
                    {t(NAMESPACE_COPY[def.namespace])}
                  </span>
                  <span className="tw:truncate tw:font-semibold tw:text-foreground">{t(ITEM_COPY[item.key])}</span>
                </button>

                {!expanded && (
                  <div
                    role="presentation"
                    onClick={() => toggleExpand(item.key)}
                    className="tw:flex tw:min-w-0 tw:flex-1 tw:cursor-pointer tw:flex-wrap tw:items-center tw:gap-1 tw:rounded-md tw:px-1 tw:py-0.5 tw:text-left tw:transition-colors tw:hover:bg-muted/60 tw:phone:order-10 tw:phone:basis-full tw:phone:pl-7"
                  >
                    <ItemSummary itemKey={item.key} ranks={item.ranks} admissionMode={admissionMode} />
                  </div>
                )}

                <div className="tw:ml-auto tw:flex tw:flex-none tw:items-center tw:gap-0.5">
                  <button
                    type="button"
                    className={headerIconButton}
                    aria-label={t("qualityCompare.moveUp", { item: name })}
                    disabled={disabled || index === 0}
                    onClick={() => onChange(moveItem(value, index, index - 1))}
                  >
                    <ArrowUp size={14} aria-hidden="true" />
                  </button>
                  <button
                    type="button"
                    className={headerIconButton}
                    aria-label={t("qualityCompare.moveDown", { item: name })}
                    disabled={disabled || index === count - 1}
                    onClick={() => onChange(moveItem(value, index, index + 1))}
                  >
                    <ArrowDown size={14} aria-hidden="true" />
                  </button>
                  <span aria-hidden="true" className="tw:mx-0.5 tw:h-4 tw:w-px tw:bg-border" />
                  <button
                    type="button"
                    className={headerIconButton}
                    aria-label={expanded ? t("qualityCompare.collapseItem", { item: name }) : t("qualityCompare.expandItem", { item: name })}
                    aria-expanded={expanded}
                    onClick={() => toggleExpand(item.key)}
                  >
                    <ChevronDown
                      size={14}
                      aria-hidden="true"
                      className={cn("tw:transition-transform tw:duration-150", expanded && "tw:rotate-180")}
                    />
                  </button>
                  <button
                    type="button"
                    className={cn(headerIconButton, "tw:enabled:hover:bg-destructive/12 tw:enabled:hover:text-destructive")}
                    aria-label={t("qualityCompare.removeItem", { item: name })}
                    disabled={disabled}
                    onClick={() => handleRemoveItem(index, item.key)}
                  >
                    <X size={14} aria-hidden="true" />
                  </button>
                </div>
              </div>

              {expanded && (
                <div className="tw:grid tw:min-w-0 tw:grid-cols-[minmax(0,1fr)] tw:gap-3 tw:border-t tw:border-border tw:bg-muted/25 tw:px-3.5 tw:py-3 tw:phone:px-2.5">
                  {def.kind === "score" && <ScoreBody admissionMode={admissionMode} />}
                  {def.kind === "revision" && (
                    <p className="tw:m-0 tw:text-xs tw:leading-relaxed tw:text-muted-foreground">{t("qualityCompare.revisionHint")}</p>
                  )}
                  {def.kind === "size" && (
                    <SizeBody
                      direction={sizeDirection(item.ranks)}
                      disabled={disabled}
                      onChange={(next) => onChange(setSizeDirection(value, next))}
                    />
                  )}
                  {(def.kind === "single" || def.kind === "multi") && (
                    <RankedBody
                      profile={value}
                      itemKey={item.key}
                      ranks={item.ranks}
                      vocabulary={candidateValues(item.key, vocabulary.get(item.key))}
                      onChange={onChange}
                      disabled={disabled}
                    />
                  )}
                </div>
              )}
            </li>
          );
        })}
      </ol>

      <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:justify-between tw:gap-2">
        {unused.length > 0 && !disabled ? (
          <Popover
            open={addOpen}
            onOpenChange={setAddOpen}
            side="bottom"
            align="start"
            className="tw:grid tw:max-h-96 tw:w-[min(360px,calc(100vw-2rem))] tw:gap-2.5 tw:overflow-y-auto tw:p-2.5"
            trigger={
              <Button size="sm">
                <Plus size={14} aria-hidden="true" />
                {t("qualityCompare.addItem")}
              </Button>
            }
          >
            {COMPARE_NAMESPACES.map((namespace) => {
              const items = unused.filter((item) => item.namespace === namespace);
              if (items.length === 0) return null;
              return (
                <div key={namespace} className="tw:grid tw:gap-1" role="group" aria-label={t(NAMESPACE_COPY[namespace])}>
                  <div className="tw:px-1.5 tw:pt-0.5">
                    <span className={cn("tw:inline-flex tw:rounded tw:px-1.5 tw:py-0.5 tw:text-xs tw:font-semibold", NAMESPACE_BADGE[namespace])}>
                      {t(NAMESPACE_COPY[namespace])}
                    </span>
                  </div>
                  {items.map((item) => (
                    <button
                      key={item.key}
                      type="button"
                      onClick={() => handleAddItem(item.key)}
                      className="tw:flex tw:w-full tw:items-start tw:justify-between tw:gap-2 tw:rounded-md tw:px-2 tw:py-1.5 tw:text-left tw:transition-colors tw:hover:bg-muted"
                    >
                      <span className="tw:grid tw:min-w-0 tw:gap-0.5">
                        <span className="tw:text-sm tw:font-medium tw:text-foreground">{t(ITEM_COPY[item.key])}</span>
                        <span className="tw:text-xs tw:leading-snug tw:text-muted-foreground">{t(ITEM_DESC_COPY[item.key])}</span>
                      </span>
                      <span className="tw:mt-0.5 tw:flex-none tw:text-xs tw:font-semibold tw:text-primary">
                        + {t("qualityCompare.addItemAction")}
                      </span>
                    </button>
                  ))}
                </div>
              );
            })}
          </Popover>
        ) : <span />}
        {!disabled && count > 0 && (
          <span className="tw:text-xs tw:text-muted-foreground">{t("qualityCompare.dragTip")}</span>
        )}
      </div>
    </div>
  );
}

function ItemSummary({
  itemKey,
  ranks,
  admissionMode,
}: {
  itemKey: QualityCompareKey;
  ranks: string[][];
  admissionMode: "all" | "per_group" | "single_copy";
}) {
  const def = itemDef(itemKey);
  if (def.kind === "score") {
    if (admissionMode === "per_group") {
      return (
        <span className="tw:inline-flex tw:max-w-full tw:rounded tw:bg-warning/15 tw:px-1.5 tw:py-0.5 tw:text-xs tw:font-medium tw:text-warning">
          {t("qualityCompare.summary.scoreInactive")}
        </span>
      );
    }
    return (
      <span className="tw:inline-flex tw:rounded tw:bg-primary/12 tw:px-1.5 tw:py-0.5 tw:text-xs tw:font-semibold tw:text-primary">
        {t("qualityCompare.summary.scoreHigh")}
      </span>
    );
  }
  if (def.kind === "revision") {
    return (
      <span className="tw:inline-flex tw:rounded tw:bg-primary/12 tw:px-1.5 tw:py-0.5 tw:text-xs tw:font-semibold tw:text-primary">
        {t("qualityCompare.summary.revision")}
      </span>
    );
  }
  if (def.kind === "size") {
    const dir = sizeDirection(ranks);
    return (
      <span className="tw:inline-flex tw:rounded tw:bg-primary/12 tw:px-1.5 tw:py-0.5 tw:text-xs tw:font-semibold tw:text-primary">
        {dir === "smaller" ? t("qualityCompare.summary.sizeSmaller") : t("qualityCompare.summary.sizeLarger")}
      </span>
    );
  }
  if (ranks.length === 0) {
    return (
      <span className="tw:inline-flex tw:max-w-full tw:rounded tw:bg-warning/15 tw:px-1.5 tw:py-0.5 tw:text-xs tw:font-medium tw:text-warning">
        {t("qualityCompare.summary.empty")}
      </span>
    );
  }
  const shown = ranks.slice(0, 4);
  const rest = ranks.length - shown.length;
  return (
    <>
      {shown.map((entry, idx) => (
        <span key={entryLabel(entry)} className="tw:inline-flex tw:items-center tw:gap-1">
          {idx > 0 && <span aria-hidden="true" className="tw:text-xs tw:text-muted-foreground/60">&gt;</span>}
          <span
            className={cn(
              "tw:inline-flex tw:rounded tw:px-1.5 tw:py-0.5 tw:text-xs tw:font-medium",
              idx === 0 ? "tw:bg-primary/12 tw:font-semibold tw:text-primary" : "tw:bg-muted tw:text-foreground",
            )}
          >
            {entryLabel(entry)}
          </span>
        </span>
      ))}
      {rest > 0 && (
        <span className="tw:text-xs tw:text-muted-foreground">{t("qualityCompare.summary.more", { n: rest })}</span>
      )}
    </>
  );
}

function SizeBody({
  direction,
  disabled,
  onChange,
}: {
  direction: SizeDirection;
  disabled?: boolean;
  onChange: (next: SizeDirection) => void;
}) {
  return (
    <div className="tw:grid tw:min-w-0 tw:grid-cols-[minmax(0,1fr)] tw:gap-2">
      <p className="tw:m-0 tw:text-xs tw:leading-relaxed tw:text-muted-foreground">{t("qualityCompare.sizeHint")}</p>
      <Segmented<SizeDirection>
        size="default"
        aria-label={t("qualityCompare.sizeAria")}
        value={direction}
        disabled={disabled}
        onValueChange={onChange}
        className="tw:phone:w-full"
        options={[
          { value: "larger", label: t("qualityCompare.sizeLarger"), tone: "primary" },
          { value: "smaller", label: t("qualityCompare.sizeSmaller"), tone: "primary" },
        ]}
      />
    </div>
  );
}

function ScoreBody({ admissionMode }: { admissionMode: string }) {
  const navigate = useAnimeTabNavigation();
  return (
    <div className="tw:grid tw:min-w-0 tw:grid-cols-[minmax(0,1fr)] tw:gap-2">
      <p className="tw:m-0 tw:text-xs tw:leading-relaxed tw:text-muted-foreground">{t("qualityCompare.scoreHint")}</p>
      {admissionMode === "per_group" && (
        <div className="tw:max-w-full tw:rounded-md tw:border tw:border-warning/30 tw:bg-warning/12 tw:px-2.5 tw:py-2 tw:text-xs tw:leading-relaxed tw:font-medium tw:text-warning tw:wrap-anywhere">
          {t("qualityCompare.scorePerGroup")}
        </div>
      )}
      {navigate && (
        <div>
          <Button size="sm" variant="link" onClick={() => navigate("subgroups")}>
            {t("qualityCompare.scoreLink")}
          </Button>
        </div>
      )}
    </div>
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
  const isMulti = def.kind === "multi";
  const name = compareItemName(itemKey);
  const [text, setText] = useState("");
  const [pickerMode, setPickerMode] = useState<PickerMode>("single");
  const [combo, setCombo] = useState<string[]>([]);
  const [insertPos, setInsertPos] = useState<InsertPosition>("auto");
  const [showAllCandidates, setShowAllCandidates] = useState(false);
  const [problem, setProblem] = useState<EntryProblem>();
  const [lastAddedKey, setLastAddedKey] = useState<string>();
  const [insertNote, setInsertNote] = useState<string>();
  const [rankDrag, setRankDrag] = useState<RankDragState>();

  function add(values: string[], position: InsertPosition = "auto"): boolean {
    const result = addEntry(profile, itemKey, values, position);
    if (result.problem) {
      setProblem(result.problem);
      return false;
    }
    setProblem(undefined);
    const addedKey = setKey(values);
    setLastAddedKey(addedKey);
    const nextLen = ranks.length + 1;
    setInsertNote(
      result.insertedAt < nextLen - 1
        ? t("qualityCompare.picker.insertedAt", { n: result.insertedAt + 1 })
        : undefined,
    );
    onChange(result.profile);
    return true;
  }

  function addTyped() {
    if (!text.trim()) return;
    const parsed = parseEntry(text, itemKey);
    const pos: InsertPosition = parsed.length > 1 ? insertPos : "end";
    if (add(parsed, pos)) setText("");
  }

  function toggleComboValue(choice: string) {
    setProblem(undefined);
    setInsertNote(undefined);
    const fold = choice.toLowerCase();
    setCombo((current) =>
      current.some((v) => v.toLowerCase() === fold)
        ? current.filter((v) => v.toLowerCase() !== fold)
        : [...current, choice],
    );
  }

  function loadComboIntoComposer(entry: readonly string[]) {
    if (disabled || !isMulti || entry.length < 2) return;
    setPickerMode("combo");
    setCombo([...entry]);
    setProblem(undefined);
    setInsertNote(undefined);
  }

  function handleRankDragStart(event: DragEvent<HTMLSpanElement>, index: number) {
    event.stopPropagation();
    setRankDrag({ from: index });
    event.dataTransfer.effectAllowed = "move";
    const pill = event.currentTarget.closest("[data-entry]");
    if (pill && typeof event.dataTransfer.setDragImage === "function") {
      event.dataTransfer.setDragImage(pill, 16, 14);
    }
  }

  function handleRankDragOver(event: DragEvent<HTMLLIElement>, index: number) {
    if (!rankDrag) return;
    event.preventDefault();
    event.stopPropagation();
    const bounds = event.currentTarget.getBoundingClientRect();
    const left = event.clientX < bounds.left + bounds.width / 2;
    if (rankDrag.over !== index || rankDrag.left !== left) {
      setRankDrag({ from: rankDrag.from, over: index, left });
    }
  }

  function handleRankDrop(event: DragEvent<HTMLLIElement>, index: number) {
    if (!rankDrag) return;
    event.preventDefault();
    event.stopPropagation();
    const bounds = event.currentTarget.getBoundingClientRect();
    const left = event.clientX < bounds.left + bounds.width / 2;
    let target = left ? index : index + 1;
    if (rankDrag.from < target) target -= 1;
    if (target !== rankDrag.from) {
      onChange(moveEntry(profile, itemKey, rankDrag.from, target));
      setLastAddedKey(undefined);
      setInsertNote(undefined);
    }
    setRankDrag(undefined);
  }

  const allCandidates = isMulti ? comboValues(ranks, vocabulary) : (vocabulary ?? []);
  const { common, overflow } = splitCandidates(itemKey, allCandidates);
  const singleListed = new Set(ranks.filter((entry) => entry.length === 1).map((entry) => setKey(entry)));
  const comboFolded = new Set(combo.map((v) => v.toLowerCase()));

  const query = text.trim().toLowerCase();
  const visiblePool = query
    ? allCandidates.filter((choice) => {
        const meaning = compareValueMeaning(itemKey, choice)?.toLowerCase() ?? "";
        return choice.toLowerCase().includes(query) || meaning.includes(query);
      })
    : showAllCandidates
      ? allCandidates
      : common;

  const exactMatch = query !== "" && allCandidates.some((choice) => choice.toLowerCase() === query);
  const showCustomChip = query !== "" && !exactMatch;

  const itemSpecificHints: string[] = [];
  if (itemKey === "rt.sub_flag") itemSpecificHints.push(t("qualityCompare.subFlagHint"));
  if (itemKey === "rt.content_rating") itemSpecificHints.push(t("qualityCompare.contentRatingHint"));
  if (isMulti) itemSpecificHints.push(t("qualityCompare.multiHint"));
  else if (def.namespace === "anibt") itemSpecificHints.push(t("qualityCompare.anibtHint"));

  return (
    <div className="tw:grid tw:min-w-0 tw:grid-cols-[minmax(0,1fr)] tw:gap-3">
      {itemSpecificHints.length > 0 && (
        <p className="tw:m-0 tw:text-xs tw:leading-relaxed tw:text-muted-foreground">{itemSpecificHints.join(" ")}</p>
      )}

      {/* Ranked Order Strip */}
      <div className="tw:grid tw:min-w-0 tw:grid-cols-[minmax(0,1fr)] tw:gap-1.5">
        <div className="tw:flex tw:flex-wrap tw:items-center tw:justify-between tw:gap-2 tw:text-xs tw:font-medium tw:text-muted-foreground">
          <span>{t("qualityCompare.ranksHeader")}</span>
          <span className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
            {insertNote && (
              <span role="status" className="tw:font-semibold tw:text-primary">
                {insertNote}
              </span>
            )}
            <span>
              {t("qualityCompare.ranksCount", { n: ranks.length })}
              {!disabled && isMulti && ranks.some((r) => r.length > 1) ? ` · ${t("qualityCompare.ranksComboTip")}` : ""}
            </span>
          </span>
        </div>

        <ol
          className="tw:m-0 tw:flex tw:min-h-9 tw:min-w-0 tw:list-none tw:flex-wrap tw:items-center tw:gap-1.5 tw:rounded-control tw:border tw:border-border tw:bg-card tw:px-2 tw:py-1.5"
          aria-label={t("qualityCompare.ranksAria", { item: name })}
        >
          {ranks.map((entry, index) => {
            const label = entryLabel(entry);
            const entryKey = setKey(entry);
            const isCombo = entry.length > 1;
            const meaning = !isCombo ? compareValueMeaning(itemKey, entry[0]!) : undefined;
            const isDragging = rankDrag?.from === index;
            const dropLeft = rankDrag !== undefined && rankDrag.from !== index && rankDrag.over === index && rankDrag.left === true;
            const dropRight = rankDrag !== undefined && rankDrag.from !== index && rankDrag.over === index && rankDrag.left === false;
            return (
              <li
                key={label}
                data-entry=""
                onDragOver={(event) => handleRankDragOver(event, index)}
                onDrop={(event) => handleRankDrop(event, index)}
                className={cn(
                  "tw:relative tw:inline-flex tw:max-w-full tw:items-center tw:gap-1 tw:rounded-lg tw:border tw:py-0.5 tw:pr-1 tw:pl-1.5 tw:text-xs tw:leading-tight tw:transition-colors",
                  index === 0
                    ? "tw:border-primary/45 tw:bg-accent/55"
                    : "tw:border-border tw:bg-muted/55 tw:hover:border-primary/35 tw:hover:bg-card",
                  lastAddedKey === entryKey && "tw:ring-2 tw:ring-primary/50",
                  isDragging && "tw:border-dashed tw:opacity-40",
                  dropLeft && "tw:before:pointer-events-none tw:before:absolute tw:before:inset-y-0 tw:before:-left-1.5 tw:before:z-10 tw:before:w-0.5 tw:before:rounded-full tw:before:bg-primary tw:before:content-['']",
                  dropRight && "tw:after:pointer-events-none tw:after:absolute tw:after:inset-y-0 tw:after:-right-1.5 tw:after:z-10 tw:after:w-0.5 tw:after:rounded-full tw:after:bg-primary tw:after:content-['']",
                )}
              >
                {!disabled && (
                  <span
                    data-entry-handle=""
                    draggable
                    onDragStart={(event) => handleRankDragStart(event, index)}
                    onDragEnd={() => setRankDrag(undefined)}
                    title={t("qualityCompare.dragHandle")}
                    aria-hidden="true"
                    className="tw:inline-flex tw:flex-none tw:cursor-grab tw:items-center tw:text-muted-foreground/75 tw:hover:text-foreground tw:active:cursor-grabbing"
                  >
                    <GripVertical className="tw:size-3" />
                  </span>
                )}
                <span
                  className={cn(
                    "tw:min-w-3.5 tw:flex-none tw:text-center tw:text-badge tw:font-semibold tw:tabular-nums",
                    index === 0 ? "tw:text-primary" : "tw:text-muted-foreground",
                  )}
                >
                  {index + 1}
                </span>

                {isCombo ? (
                  <button
                    type="button"
                    disabled={disabled}
                    title={disabled ? undefined : t("qualityCompare.picker.comboEditTitle")}
                    onClick={() => loadComboIntoComposer(entry)}
                    className="tw:inline-flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-0.5 tw:font-semibold tw:text-foreground tw:enabled:cursor-pointer"
                  >
                    {entry.map((token, tokenIndex) => (
                      <span key={token} className="tw:inline-flex tw:items-center tw:gap-0.5">
                        {tokenIndex > 0 && <span className="tw:text-badge tw:text-muted-foreground">+</span>}
                        <span className="tw:rounded tw:border tw:border-border tw:bg-card tw:px-1 tw:py-px tw:text-xs">{token}</span>
                      </span>
                    ))}
                  </button>
                ) : (
                  <span className="tw:inline-flex tw:items-baseline tw:gap-1 tw:whitespace-nowrap tw:font-semibold tw:text-foreground">
                    <span>{label}</span>
                    {meaning && <span className="tw:text-badge tw:font-normal tw:text-muted-foreground">{meaning}</span>}
                  </span>
                )}

                {!disabled && (
                  <span className="tw:ml-0.5 tw:inline-flex tw:flex-none tw:items-center tw:gap-0.5 tw:border-l tw:border-border/75 tw:pl-1">
                    <button
                      type="button"
                      className={rankIconButton}
                      aria-label={t("qualityCompare.entryEarlier", { value: label })}
                      disabled={index === 0}
                      onClick={() => onChange(moveEntry(profile, itemKey, index, index - 1))}
                    >
                      <ChevronLeft size={12} aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      className={rankIconButton}
                      aria-label={t("qualityCompare.entryLater", { value: label })}
                      disabled={index === ranks.length - 1}
                      onClick={() => onChange(moveEntry(profile, itemKey, index, index + 1))}
                    >
                      <ChevronRight size={12} aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      className={cn(rankIconButton, "tw:ml-0.5 tw:enabled:hover:bg-destructive/12 tw:enabled:hover:text-destructive")}
                      aria-label={t("qualityCompare.entryRemove", { value: label })}
                      onClick={() => onChange(removeEntry(profile, itemKey, index))}
                    >
                      <X size={12} aria-hidden="true" />
                    </button>
                  </span>
                )}
              </li>
            );
          })}
          {ranks.length === 0 ? (
            <li className="tw:px-1 tw:text-xs tw:text-muted-foreground">{t("qualityCompare.empty")}</li>
          ) : (
            <li className="tw:rounded-lg tw:border tw:border-dashed tw:border-border tw:px-2 tw:py-0.5 tw:text-xs tw:text-muted-foreground">
              {t("qualityCompare.others")}
            </li>
          )}
        </ol>
      </div>

      {/* Unified Value Picker */}
      {!disabled && (
        <div className="tw:grid tw:min-w-0 tw:grid-cols-[minmax(0,1fr)] tw:gap-2.5 tw:rounded-control tw:border tw:border-border tw:bg-card tw:p-3 tw:phone:p-2.5">
          <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:justify-between tw:gap-2">
            {isMulti ? (
              <Segmented<PickerMode>
                size="compact"
                aria-label={t("qualityCompare.picker.modeAria", { item: name })}
                value={pickerMode}
                onValueChange={(next) => {
                  setPickerMode(next);
                  setProblem(undefined);
                }}
                options={[
                  { value: "single", label: t("qualityCompare.picker.modeSingle"), tone: "quiet" },
                  { value: "combo", label: t("qualityCompare.picker.modeCombo"), tone: "primary" },
                ]}
              />
            ) : (
              <span className="tw:text-xs tw:font-medium tw:text-muted-foreground">{t("qualityCompare.picker.singleHint")}</span>
            )}

            <div className="tw:flex tw:min-w-0 tw:flex-1 tw:items-center tw:gap-1.5 tw:sm:max-w-72">
              <Input
                controlSize="sm"
                className="tw:min-w-0 tw:flex-1"
                aria-label={t("qualityCompare.valueAria", { item: name })}
                placeholder={isMulti ? t("qualityCompare.picker.filterPlaceholderMulti") : t("qualityCompare.picker.filterPlaceholder")}
                value={text}
                onChange={(event) => {
                  setText(event.target.value);
                  setProblem(undefined);
                }}
              />
              <Button
                size="sm"
                aria-label={t("qualityCompare.addValueAria", { item: name })}
                disabled={!text.trim()}
                onClick={addTyped}
              >
                {t("qualityCompare.addValue")}
              </Button>
            </div>
          </div>

          {isMulti && pickerMode === "combo" && (
            <div
              role="group"
              aria-label={t("qualityCompare.comboLabel")}
              className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:justify-between tw:gap-2 tw:rounded-lg tw:border tw:border-primary/35 tw:bg-accent/45 tw:px-2.5 tw:py-2"
            >
              <div className="tw:flex tw:min-w-0 tw:flex-1 tw:flex-wrap tw:items-center tw:gap-1">
                {combo.length === 0 ? (
                  <span className="tw:text-xs tw:text-muted-foreground">{t("qualityCompare.picker.comboPrompt")}</span>
                ) : (
                  combo.map((token, idx) => (
                    <span key={token} className="tw:inline-flex tw:items-center tw:gap-1">
                      {idx > 0 && <span className="tw:text-xs tw:font-semibold tw:text-muted-foreground">+</span>}
                      <span className="tw:inline-flex tw:items-center tw:gap-1 tw:rounded-md tw:border tw:border-primary/40 tw:bg-card tw:py-0.5 tw:pr-1 tw:pl-2 tw:text-xs tw:font-semibold tw:text-foreground">
                        {token}
                        <button
                          type="button"
                          aria-label={t("qualityCompare.picker.comboTokenRemove", { value: token })}
                          onClick={() => toggleComboValue(token)}
                          className="tw:inline-flex tw:items-center tw:text-muted-foreground tw:hover:text-destructive"
                        >
                          <X size={11} aria-hidden="true" />
                        </button>
                      </span>
                    </span>
                  ))
                )}
              </div>

              <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
                <Segmented<InsertPosition>
                  size="compact"
                  aria-label={t("qualityCompare.picker.insertAria")}
                  value={insertPos}
                  onValueChange={setInsertPos}
                  options={[
                    { value: "auto", label: t("qualityCompare.picker.insertAuto"), tone: "quiet" },
                    { value: "end", label: t("qualityCompare.picker.insertEnd"), tone: "quiet" },
                  ]}
                />
                <Button
                  size="sm"
                  variant="primary"
                  disabled={combo.length < 2}
                  onClick={() => {
                    if (add(combo, insertPos)) setCombo([]);
                  }}
                >
                  {t("qualityCompare.comboAdd")}
                </Button>
              </div>
            </div>
          )}

          {(visiblePool.length > 0 || overflow.length > 0 || showCustomChip) && (
            <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-1.5">
              {visiblePool.map((choice) => {
                const meaning = compareValueMeaning(itemKey, choice);
                const inCombo = isMulti && pickerMode === "combo" && comboFolded.has(choice.toLowerCase());
                const alreadySingle = (!isMulti || pickerMode === "single") && singleListed.has(setKey([choice]));
                return (
                  <button
                    key={choice}
                    type="button"
                    disabled={alreadySingle}
                    aria-pressed={isMulti && pickerMode === "combo" ? inCombo : undefined}
                    title={alreadySingle ? t("qualityCompare.picker.alreadyListed") : meaning}
                    onClick={() => {
                      if (isMulti && pickerMode === "combo") toggleComboValue(choice);
                      else add([choice], "end");
                    }}
                    className={cn(
                      "tw:inline-flex tw:items-center tw:gap-1 tw:rounded-lg tw:border tw:px-2 tw:py-1 tw:text-xs tw:font-medium tw:transition-colors",
                      inCombo
                        ? "tw:border-primary tw:bg-primary tw:text-primary-foreground"
                        : alreadySingle
                          ? "tw:cursor-not-allowed tw:border-dashed tw:border-border tw:bg-transparent tw:text-muted-foreground tw:opacity-60"
                          : "tw:border-border tw:bg-muted/40 tw:text-foreground tw:hover:border-primary tw:hover:bg-accent/55 tw:hover:text-accent-foreground",
                    )}
                  >
                    {inCombo || alreadySingle ? (
                      <Check size={12} aria-hidden="true" />
                    ) : (
                      <Plus size={12} aria-hidden="true" className="tw:text-muted-foreground" />
                    )}
                    <span>{choice}</span>
                    {meaning && (
                      <span className={cn("tw:text-badge tw:font-normal", inCombo ? "tw:text-primary-foreground/85" : "tw:text-muted-foreground")}>
                        {meaning}
                      </span>
                    )}
                  </button>
                );
              })}

              {!query && overflow.length > 0 && (
                <button
                  type="button"
                  onClick={() => setShowAllCandidates((open) => !open)}
                  className="tw:rounded-lg tw:px-2 tw:py-1 tw:text-xs tw:font-medium tw:text-primary tw:transition-colors tw:hover:bg-accent"
                >
                  {showAllCandidates
                    ? t("qualityCompare.picker.lessCandidates")
                    : t("qualityCompare.picker.moreCandidates", { n: overflow.length })}
                </button>
              )}

              {showCustomChip && (
                <button
                  type="button"
                  onClick={addTyped}
                  className="tw:inline-flex tw:items-center tw:gap-1 tw:rounded-lg tw:border tw:border-dashed tw:border-primary tw:bg-accent/35 tw:px-2 tw:py-1 tw:text-xs tw:font-medium tw:text-accent-foreground tw:transition-colors tw:hover:bg-accent"
                >
                  <Plus size={12} aria-hidden="true" />
                  {t("qualityCompare.picker.addCustom", { value: text.trim() })}
                </button>
              )}
            </div>
          )}

          {problem && (
            <span role="alert" className="tw:text-xs tw:font-medium tw:text-destructive">
              {t(PROBLEM_COPY[problem])}
            </span>
          )}
        </div>
      )}
    </div>
  );
}
