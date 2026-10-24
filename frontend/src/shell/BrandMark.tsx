// The product brand: 方案 A「绷带播放键」from the 2026-10-24 brand concepts —
// a tilted bandage whose center pad is a play button. The mark keeps its fixed
// brand colors on light and dark surfaces alike; the wordmark is the lowercase
// name plus an accent dot. The product name is a proper noun, identical in
// every locale, so it is not routed through the copy tables.
export function BrandMark({ size = 24, className }: { size?: number; className?: string }) {
  return (
    <svg viewBox="0 0 64 64" width={size} height={size} className={className} aria-hidden focusable="false">
      <g transform="rotate(-30 32 32)">
        <rect x="7" y="22" width="50" height="20" rx="10" fill="#F4AC8B" stroke="#D97F5B" strokeWidth="2" />
        <g fill="#D97F5B">
          <circle cx="14" cy="29" r="1.6" />
          <circle cx="14" cy="35" r="1.6" />
          <circle cx="19" cy="29" r="1.6" />
          <circle cx="19" cy="35" r="1.6" />
          <circle cx="45" cy="29" r="1.6" />
          <circle cx="45" cy="35" r="1.6" />
          <circle cx="50" cy="29" r="1.6" />
          <circle cx="50" cy="35" r="1.6" />
        </g>
        <rect x="25" y="25" width="14" height="14" rx="3.5" fill="#FBE4D3" stroke="#D97F5B" strokeWidth="1.8" />
        <path d="M30.2 28.6 L30.2 35.4 L36 32 Z" fill="#E4572E" />
      </g>
    </svg>
  );
}

export function BrandWordmark({ className }: { className?: string }) {
  return (
    <span className={className}>
      bansoko<span style={{ color: "#E4572E" }}>.</span>
    </span>
  );
}
