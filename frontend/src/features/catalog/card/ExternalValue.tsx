import { ExternalLink } from "lucide-react";

/** Allows only http(s) external links: the href may come from provider data, so any other scheme (javascript: and friends) renders as plain text. */
function safeExternalHref(href: string): string | undefined {
  try {
    const parsed = new URL(href);
    return parsed.protocol === "https:" || parsed.protocol === "http:" ? parsed.href : undefined;
  } catch {
    return undefined;
  }
}

/** A quiet external-value link: accent color + a small external icon, with clicks never bubbling to the card. */
export function ExternalValue({ href, children }: { href: string; children: React.ReactNode }) {
  const safe = safeExternalHref(href);
  if (!safe) return <span className="catalog-external-value">{children}</span>;
  return (
    <a
      href={safe}
      target="_blank"
      rel="noopener noreferrer"
      className="catalog-external-value"
      onClick={(e) => e.stopPropagation()}
    >
      {children}
      <ExternalLink className="catalog-external-icon" />
    </a>
  );
}
