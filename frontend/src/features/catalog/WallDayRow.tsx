import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { Chip } from "../../ui/Chip";
import { t } from "../../lib/i18n";
import {
  WALL_ROW_BODY_CLASS,
  WALL_ROW_CLASS,
  WALL_ROW_EXPAND_BTN_CLASS,
  WALL_ROW_GRID_CLASS,
  WALL_ROW_HEAD_CLASS,
  WALL_ROW_LABEL_CLASS,
  WALL_ROW_NAV_BACK_CLASS,
  WALL_ROW_NAV_FORWARD_CLASS,
  WALL_ROW_NAV_ICON_CLASS,
  WALL_ROW_TITLE_WRAP_CLASS,
  WALL_ROW_TRACK_CLASS,
} from "./wallRowClasses";

// One row of the subscribed wall (the design language's Subscribed wall): the row label pinned at
//   the line start with the cards in a horizontal scrolling track by default, and an in-place
//   expand/collapse toggle when the row overflows (switching to a 2-column grid on phones and an
//   auto-fill grid on desktop).

function prefersReducedMotion(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

/** Whether either side of the track still holds clipped content (a 1px tolerance avoids subpixel jitter). */
function useScrollEdges(itemCount: number, expanded: boolean) {
  const ref = useRef<HTMLUListElement>(null);
  const [edges, setEdges] = useState({ start: false, end: false });
  const measure = useCallback(() => {
    if (expanded) return;
    const node = ref.current;
    if (!node) return;
    const start = node.scrollLeft > 1;
    const end = node.scrollLeft + node.clientWidth < node.scrollWidth - 1;
    setEdges((current) => (current.start === start && current.end === end ? current : { start, end }));
  }, [expanded]);
  useEffect(() => {
    const node = ref.current;
    if (!node || expanded) return;
    measure();
    node.addEventListener("scroll", measure, { passive: true });
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(node);
    return () => {
      node.removeEventListener("scroll", measure);
      observer?.disconnect();
    };
  }, [measure, itemCount, expanded]);
  return { ref, edges };
}

export function WallDayRow({
  label,
  children,
  itemCount,
  isToday = false,
}: {
  label: string;
  children: ReactNode;
  itemCount: number;
  isToday?: boolean;
}) {
  const headingId = useId();
  const [expanded, setExpanded] = useState(false);
  const { ref, edges } = useScrollEdges(itemCount, expanded);
  const canExpand = expanded || edges.start || edges.end || itemCount >= 6;

  // Page one screen: rounded to whole card steps so after paging the left edge stays aligned on a card.
  const page = (direction: 1 | -1) => {
    const node = ref.current;
    if (!node) return;
    const first = node.firstElementChild as HTMLElement | null;
    const gap = parseFloat(getComputedStyle(node).columnGap) || 0;
    const step = first ? first.offsetWidth + gap : node.clientWidth;
    const cards = Math.max(1, Math.floor((node.clientWidth + gap) / step));
    node.scrollBy({ left: direction * cards * step, behavior: prefersReducedMotion() ? "auto" : "smooth" });
  };

  return (
    <section
      aria-labelledby={headingId}
      className={WALL_ROW_CLASS}
      data-wall-row=""
      data-today={isToday || undefined}
      data-wall-expanded={expanded || undefined}
    >
      <div className={WALL_ROW_HEAD_CLASS}>
        <div className={WALL_ROW_TITLE_WRAP_CLASS}>
          <h3 id={headingId} className={WALL_ROW_LABEL_CLASS}>
            {label}
          </h3>
          {isToday ? <Chip tone="accent">{t("grid.wallRow.today")}</Chip> : null}
        </div>
        {canExpand ? (
          <button
            type="button"
            aria-expanded={expanded}
            className={WALL_ROW_EXPAND_BTN_CLASS}
            onClick={() => setExpanded((prev) => !prev)}
          >
            {expanded ? t("grid.wallRow.collapse") : t("grid.wallRow.expand")}
          </button>
        ) : null}
      </div>
      <div
        className={WALL_ROW_BODY_CLASS}
        data-fade-start={(!expanded && edges.start) || undefined}
        data-fade-end={(!expanded && edges.end) || undefined}
      >
        <ul ref={ref} className={expanded ? WALL_ROW_GRID_CLASS : WALL_ROW_TRACK_CLASS}>
          {children}
        </ul>
        {!expanded && edges.start && (
          <button type="button" tabIndex={-1} className={WALL_ROW_NAV_BACK_CLASS} aria-label={t("grid.wallRow.scrollBack")} onClick={() => page(-1)}>
            <ChevronLeft aria-hidden className={WALL_ROW_NAV_ICON_CLASS} />
          </button>
        )}
        {!expanded && edges.end && (
          <button type="button" tabIndex={-1} className={WALL_ROW_NAV_FORWARD_CLASS} aria-label={t("grid.wallRow.scrollForward")} onClick={() => page(1)}>
            <ChevronRight aria-hidden className={WALL_ROW_NAV_ICON_CLASS} />
          </button>
        )}
      </div>
    </section>
  );
}
