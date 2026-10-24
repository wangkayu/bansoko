// SegmentCountLabel — a segmented option's name plus a count (only the name
// until the count arrives). The space between name and count only enters the
// accessible name ("Subscriptions 12"); the visual gap comes from `gap`.
export function SegmentCountLabel({ label, count }: { label: string; count?: number }) {
  return (
    <span className="tw:inline-flex tw:items-baseline tw:gap-1.5">
      <span>{label}</span>{" "}
      {typeof count === "number" ? <span className="tw:tabular-nums tw:opacity-70">{count}</span> : null}
    </span>
  );
}
