import { useMemo, useState } from "react";
import { RotateCcw, Search } from "lucide-react";
import { formatBytes } from "../../lib/labels";
import { t } from "../../lib/i18n";
import { Button, Input, StateBlock } from "../../ui";
import { Checkbox, Dialog } from "../../ui/base";
import { cn } from "../../lib/utils";
import {
  automaticDraft,
  draftMap,
  mergeCheckedOverrides,
  overrideList,
  validDraft,
  type FileEpisodeDraft,
  type FileEpisodeOverride,
  type FileNumberingItem,
} from "./model";

// The shared D editor: filtering and checked rows are local interaction state only. Confirmation
// merges just the checked rows into the baseline captured when this dialog opened; an unchecked
// draft can be kept while another season is edited without being applied accidentally.

export function FileNumberingDialog({
  title,
  items,
  overrides,
  onApply,
  onClose,
}: {
  title: string;
  items: readonly FileNumberingItem[];
  overrides: readonly FileEpisodeOverride[];
  onApply: (next: FileEpisodeOverride[]) => void;
  onClose: () => void;
}) {
  const baseline = useMemo(() => new Map(overrides.map((override) => [override.path, { ...override }])), [overrides]);
  const [drafts, setDrafts] = useState<Map<string, FileEpisodeDraft>>(() => draftMap(baseline));
  const [checked, setChecked] = useState<ReadonlySet<string>>(new Set());
  const [filter, setFilter] = useState("");
  const [bulkSeason, setBulkSeason] = useState("");
  const [sequenceStart, setSequenceStart] = useState("1");
  const [showInvalid, setShowInvalid] = useState(false);

  const visible = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    return items.filter((item) => !needle || item.path.toLowerCase().includes(needle));
  }, [filter, items]);
  const checkedItems = useMemo(() => items.filter((item) => checked.has(item.path)), [checked, items]);
  const invalidChecked = checkedItems.some((item) => drafts.has(item.path) && !validDraft(drafts.get(item.path)));

  function valueOf(item: FileNumberingItem): FileEpisodeDraft {
    return drafts.get(item.path) ?? automaticDraft(item);
  }

  function edit(item: FileNumberingItem, field: keyof FileEpisodeDraft, value: string) {
    setDrafts((current) => {
      const next = new Map(current);
      const draft = { ...(next.get(item.path) ?? automaticDraft(item)) };
      draft[field] = value;
      next.set(item.path, draft);
      return next;
    });
    setShowInvalid(false);
  }

  function toggle(path: string) {
    setChecked((current) => {
      const next = new Set(current);
      if (!next.delete(path)) next.add(path);
      return next;
    });
  }

  function selectVisible(value: boolean) {
    setChecked((current) => {
      const next = new Set(current);
      for (const item of visible) {
        if (value) next.add(item.path);
        else next.delete(item.path);
      }
      return next;
    });
  }

  function changeSeasons() {
    const season = Number(bulkSeason);
    if (checkedItems.length === 0 || bulkSeason.trim() === "" || !Number.isInteger(season) || season < 0) return;
    setDrafts((current) => {
      const next = new Map(current);
      for (const item of checkedItems) {
        next.set(item.path, { ...(next.get(item.path) ?? automaticDraft(item)), season: String(season) });
      }
      return next;
    });
    setShowInvalid(false);
  }

  function fillSequence() {
    const start = Number(sequenceStart);
    const season = Number(bulkSeason);
    if (checkedItems.length === 0 || sequenceStart.trim() === "" || !Number.isInteger(start) || start < 1 || (bulkSeason.trim() !== "" && (!Number.isInteger(season) || season < 0))) return;
    setDrafts((current) => {
      const next = new Map(current);
      checkedItems.forEach((item, index) => {
        const draft = { ...(next.get(item.path) ?? automaticDraft(item)), episode: String(start + index) };
        if (bulkSeason.trim() !== "") draft.season = String(season);
        next.set(item.path, draft);
      });
      return next;
    });
    setShowInvalid(false);
  }

  function restoreChecked() {
    setDrafts((current) => {
      const next = new Map(current);
      for (const item of checkedItems) next.delete(item.path);
      return next;
    });
    setShowInvalid(false);
  }

  function restoreAll() {
    setDrafts(new Map());
    setChecked(new Set(items.map((item) => item.path)));
    setShowInvalid(false);
  }

  function confirm() {
    if (invalidChecked) {
      setShowInvalid(true);
      return;
    }
    const next = mergeCheckedOverrides(baseline, drafts, checked);
    if (!next) {
      setShowInvalid(true);
      return;
    }
    onApply(overrideList(next));
    onClose();
  }

  return (
    <Dialog
      open
      onRequestClose={onClose}
      tier="M"
      fixedHeight
      bodyLayout="fill"
      title={t("fileNumbering.title")}
      description={<span className="tw:block tw:truncate" title={title}>{title}</span>}
      footer={
        <>
          {(showInvalid || invalidChecked) && (
            <p role="alert" className="text-caption tw:m-0 tw:w-full tw:text-left tw:text-warning">{t("fileNumbering.invalid")}</p>
          )}
          <Button className="tw:mr-auto" disabled={drafts.size === 0} onClick={restoreAll}>{t("fileNumbering.restoreAll")}</Button>
          <Button onClick={onClose}>{t("act.cancel")}</Button>
          <Button variant="primary" onClick={confirm}>{t("fileNumbering.confirm", { n: checked.size })}</Button>
        </>
      }
    >
      <div className="tw:grid tw:flex-none tw:gap-2 tw:border-b tw:border-border tw:px-6 tw:py-3 tw:phone:px-4">
        <p className="text-caption tw:m-0 tw:text-muted-foreground">{t("fileNumbering.hint")}</p>
        <p className="text-caption tw:m-0 tw:text-muted-foreground">{t("fileNumbering.seasonHint")}</p>
        <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
          <Input
            controlSize="sm"
            aria-label={t("fileNumbering.filterLabel")}
            placeholder={t("fileNumbering.filterPlaceholder")}
            leadingIcon={<Search aria-hidden="true" />}
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
            className="tw:min-w-48 tw:flex-1"
          />
          <Button size="sm" disabled={visible.length === 0} onClick={() => selectVisible(true)}>{t("fileNumbering.selectFiltered")}</Button>
          <Button size="sm" disabled={visible.length === 0} onClick={() => selectVisible(false)}>{t("fileNumbering.unselectFiltered")}</Button>
        </div>
        <div className="tw:flex tw:flex-wrap tw:items-end tw:gap-2">
          <span className="text-caption tw:mr-auto tw:py-2 tw:text-muted-foreground" aria-live="polite">{t("fileNumbering.checked", { checked: checked.size, total: items.length })}</span>
          <label className="tw:grid tw:gap-0.5">
            <span className="text-caption tw:text-muted-foreground">{t("fileNumbering.bulkSeason")}</span>
            <Input controlSize="sm" fullWidth={false} className="tw:w-20" type="number" min={0} step={1} aria-label={t("fileNumbering.bulkSeason")} placeholder={t("fileNumbering.seasonPlaceholder")} value={bulkSeason} onChange={(event) => setBulkSeason(event.target.value)} />
          </label>
          <Button size="sm" disabled={checked.size === 0 || bulkSeason.trim() === ""} onClick={changeSeasons}>{t("fileNumbering.applySeason")}</Button>
          <label className="tw:grid tw:gap-0.5">
            <span className="text-caption tw:text-muted-foreground">{t("fileNumbering.sequenceStart")}</span>
            <Input controlSize="sm" fullWidth={false} className="tw:w-20" type="number" min={1} step={1} aria-label={t("fileNumbering.sequenceStart")} value={sequenceStart} onChange={(event) => setSequenceStart(event.target.value)} />
          </label>
          <Button size="sm" disabled={checked.size === 0 || sequenceStart.trim() === ""} onClick={fillSequence}>{t("fileNumbering.fillSequence")}</Button>
          <Button size="sm" variant="ghost" disabled={checked.size === 0} onClick={restoreChecked}>{t("fileNumbering.restoreChecked")}</Button>
        </div>
        <p className="text-caption tw:m-0 tw:text-muted-foreground">{t("fileNumbering.showing", { shown: visible.length, total: items.length, overrides: drafts.size })}</p>
      </div>
      <div className="tw:min-h-0 tw:flex-1 tw:overflow-y-auto tw:px-6 tw:py-2 tw:phone:px-4">
        {visible.length === 0 ? (
          <StateBlock kind="empty" title={t("fileNumbering.empty")} />
        ) : (
          <ul className="tw:m-0 tw:grid tw:list-none tw:gap-1 tw:p-0">
            {visible.map((item) => {
              const isChecked = checked.has(item.path);
              const manual = drafts.has(item.path);
              const value = valueOf(item);
              const invalid = isChecked && manual && !validDraft(value);
              return (
                <li key={item.path} data-numbering-row={item.path} className={cn("tw:rounded-control tw:border tw:px-2 tw:py-2", manual ? "tw:border-primary/35 tw:bg-accent/20" : "tw:border-transparent", !isChecked && "tw:bg-muted/15")}>
                  <div className="tw:grid tw:grid-cols-[1.5rem_minmax(0,1fr)_4.5rem_5.5rem] tw:items-center tw:gap-2">
                    <Checkbox size="sm" checked={isChecked} aria-label={t("fileNumbering.checkRow", { path: item.path })} onCheckedChange={() => toggle(item.path)} />
                    <div className="tw:min-w-0">
                      <p className="text-caption break-anywhere tw:m-0" title={item.path}>{item.path}{item.sizeBytes !== undefined && <span className="tw:text-muted-foreground"> · {formatBytes(item.sizeBytes)}</span>}</p>
                      <p className={cn("text-caption tw:m-0 tw:flex tw:flex-wrap tw:items-center tw:gap-x-1.5", manual ? "tw:text-primary" : "tw:text-muted-foreground")}>
                        <span>{manual ? t("fileNumbering.manual") : item.automaticLabel}</span>
                        {item.subtitleCount > 0 && <span>· {t("fileNumbering.subtitles", { n: item.subtitleCount })}</span>}
                        {manual && value.season.trim() === "" && <span>· {t("fileNumbering.inheritedSeason")}</span>}
                        {manual && isChecked && (
                          <button type="button" className="tw:inline-flex tw:cursor-pointer tw:items-center tw:gap-0.5 tw:rounded-sm tw:border-0 tw:bg-transparent tw:p-0 tw:text-muted-foreground tw:underline tw:decoration-dotted tw:underline-offset-2" onClick={() => setDrafts((current) => { const next = new Map(current); next.delete(item.path); return next; })}>
                            <RotateCcw aria-hidden="true" className="tw:size-3" />{t("fileNumbering.restoreRow")}
                          </button>
                        )}
                      </p>
                    </div>
                    <Input controlSize="sm" aria-label={`${t("fileNumbering.season")} ${item.path}`} aria-invalid={invalid || undefined} disabled={!isChecked} type="number" min={0} step={1} placeholder={t("fileNumbering.seasonPlaceholder")} value={value.season} onChange={(event) => edit(item, "season", event.target.value)} />
                    <Input controlSize="sm" aria-label={`${t("fileNumbering.episode")} ${item.path}`} aria-invalid={invalid || undefined} disabled={!isChecked} type="number" min={1} step={1} value={value.episode} onChange={(event) => edit(item, "episode", event.target.value)} />
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </Dialog>
  );
}
