import { Fragment, useEffect, useState, type RefObject } from "react";
import { t, type CopyKey } from "../../lib/i18n";
import { namingFactLabel } from "../../lib/i18n/reasons";
import { cn } from "../../lib/utils";
import { REVEAL_MS } from "../../ui/base";
import { factField, type FactField } from "../oneshot/seedRows";

/** The brief outline when jumping to an input: a marker only, no motion. */
const JUMP_FLASH = ["tw:outline-2", "tw:outline-offset-2", "tw:outline-warning"];
const JUMP_FLASH_MS = 1600;
/** Typeable fields; hidden inputs that come with dropdown selects are excluded. */
const TEXT_INPUT = "input:not([disabled]):not([type='hidden']):not([aria-hidden='true']), textarea:not([disabled])";
const SPLIT = "\u0000";

/** Text buttons in the body (empties, summary): inherit the surrounding row's color and font size. */
export const inlineFactLinkClass =
  "tw:cursor-pointer tw:rounded-sm tw:border-0 tw:bg-transparent tw:p-0 tw:text-inherit tw:underline tw:decoration-dotted tw:underline-offset-2 tw:hover:decoration-solid tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring";

/** Dedup empties (several paths may share a name, e.g. multiple episode kinds), keeping the first occurrence's path for jumps. */
export function factLabels(facts: readonly string[]): { path: string; label: string }[] {
  const seen = new Map<string, string>();
  for (const path of facts) {
    const label = namingFactLabel(path);
    if (!seen.has(label)) seen.set(label, path);
  }
  return [...seen].map(([label, path]) => ({ path, label }));
}

/** Summary and empty notes jump to the directly editable field first (year, TMDB…), falling back to naming only when none exists. */
export function firstFactField(facts: readonly string[]): FactField {
  return facts.map(factField).find((field) => field !== "naming") ?? "naming";
}

/** Clickable empty/missing facts note shared by OneShot and file-manager recognition. */
export function MissingFactsNote({
  facts,
  onJump,
  copyKey = "oneshot.missingFacts",
  tone = "warning",
}: {
  facts: readonly string[];
  onJump: (field: FactField) => void;
  copyKey?: Extract<CopyKey, "oneshot.missingFacts" | "fileManager.recognize.preview.emptyFacts" | "fileManager.recognize.preview.blocker.facts_missing">;
  tone?: "warning" | "error";
}) {
  const labels = factLabels(facts);
  if (labels.length === 0) return null;
  const [before = "", after = ""] = t(copyKey, { facts: SPLIT }).split(SPLIT);
  return (
    <p className={cn("text-caption tw:m-0", tone === "error" ? "tw:text-destructive" : "tw:text-warning")}>
      {before}
      {labels.map((fact, index) => (
        <Fragment key={fact.path}>
          {index > 0 && t("common.format.listSeparator")}
          <button type="button" className={inlineFactLinkClass} onClick={() => onJump(factField(fact.path))}>
            {fact.label}
          </button>
        </Fragment>
      ))}
      {after}
    </p>
  );
}

/**
 * Scrolls to, focuses, and briefly highlights the right-column field corresponding
 * to a missing/empty template fact; automatically expands the advanced section when
 * targeting the naming rules block.
 */
export function useFactJump({
  idPrefix,
  settingsRef,
  namingRef,
  onOpenAdvanced,
}: {
  idPrefix: string;
  settingsRef: RefObject<HTMLElement | null>;
  namingRef: RefObject<HTMLElement | null>;
  onOpenAdvanced: () => void;
}) {
  const [jump, setJump] = useState<{ field: FactField; seq: number }>();

  function jumpTo(field: FactField) {
    if (field === "naming") onOpenAdvanced();
    setJump((current) => ({ field, seq: (current?.seq ?? 0) + 1 }));
  }

  useEffect(() => {
    if (!jump) return;
    let frame = 0;
    let timer = 0;
    let flashTimer = 0;
    let tries = 0;
    const reduced = typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const findNode = (field: FactField): HTMLElement | null => {
      const settings = settingsRef.current;
      if (!settings) return null;
      if (field === "naming") return namingRef.current;
      if (field === "title") return settings.querySelector<HTMLElement>(`#${idPrefix}-title`);
      const labelId = {
        year: `${idPrefix}-year-label`,
        season: `${idPrefix}-season-label`,
        bgm: `${idPrefix}-bgm-label`,
        tmdb: `${idPrefix}-tmdb-label`,
        kind: `${idPrefix}-media-kind-label`,
      }[field];
      return settings.querySelector<HTMLElement>(`#${labelId}`)?.closest<HTMLElement>("section") ?? null;
    };
    const run = () => {
      const node = findNode(jump.field);
      // A just-expanded advanced section mounts on the next frame.
      if (!node) {
        if (tries++ < 10) frame = window.requestAnimationFrame(run);
        return;
      }
      node.scrollIntoView?.({ block: "center", behavior: reduced ? "auto" : "smooth" });
      const focusable = node.matches("input, button")
        ? node
        : (node.querySelector<HTMLElement>(TEXT_INPUT) ?? node.querySelector<HTMLElement>("button:not([disabled])"));
      focusable?.focus({ preventScroll: true });
      node.classList.add(...JUMP_FLASH);
      flashTimer = window.setTimeout(() => node.classList.remove(...JUMP_FLASH), JUMP_FLASH_MS);
    };
    if (jump.field === "naming") timer = window.setTimeout(run, REVEAL_MS + 20);
    else run();
    return () => {
      window.cancelAnimationFrame(frame);
      window.clearTimeout(timer);
      window.clearTimeout(flashTimer);
    };
  }, [idPrefix, jump, namingRef, settingsRef]);

  return jumpTo;
}
