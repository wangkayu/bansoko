import { cn } from "@/lib/utils";

// DescriptionList — compact key/value summary (config facts, provenance).

export function DescriptionList({
  items,
  className,
}: {
  items: ReadonlyArray<{ label: React.ReactNode; value: React.ReactNode }>;
  className?: string;
}) {
  return (
    <dl className={cn("description-list", className)}>
      {items.map((item, index) => (
        <div key={index} className="description-list-item">
          <dt className="description-list-label">{item.label}</dt>
          {/* break-words: spaceless values such as long templates or regexes wrap
              mid-word on overflow so they stay fully visible */}
          <dd className="description-list-value">{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}
