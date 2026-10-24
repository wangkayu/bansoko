// Class maps for the shared controls' visual variants.
// Kept apart from controls.tsx so tests can check them against the theme tokens
// (tokens.test.ts) and the component file only exports components.

export type ButtonVariant = "primary" | "secondary" | "ghost" | "link" | "danger" | "dangerOutline" | "dangerGhost";
// Button hierarchy: one filled primary per region; refresh, retry and run-now
// are secondary; --accent is never a button fill.
export const buttonVariant: Record<ButtonVariant, string> = {
  primary: "tw:border tw:border-primary tw:bg-primary tw:text-primary-foreground tw:hover:brightness-105",
  // aria-pressed: a toggle button (filter chip, show-blocked) shows its on state as a primary tint.
  secondary:
    "tw:border tw:border-border tw:bg-card tw:text-foreground tw:hover:border-primary tw:hover:text-accent-foreground " +
    "tw:aria-pressed:border-primary/50 tw:aria-pressed:bg-primary/10",
  ghost: "tw:border tw:border-transparent tw:bg-transparent tw:text-foreground tw:hover:bg-muted",
  link: "tw:border tw:border-transparent tw:bg-transparent tw:text-accent-foreground tw:underline-offset-2 tw:hover:underline",
  danger: "tw:border tw:border-destructive tw:bg-destructive tw:text-card tw:hover:brightness-105",
  // Destructive actions that are not the primary call to action: red text/stroke only.
  dangerOutline: "tw:border tw:border-destructive/60 tw:bg-card tw:text-destructive tw:hover:border-destructive",
  dangerGhost: "tw:border tw:border-transparent tw:bg-transparent tw:text-destructive tw:hover:bg-muted",
};
// Disabled (and not loading) replaces the variant classes instead of dimming
// them: opacity alone makes a disabled primary read like an accent-tinted
// enabled button. Filled variants turn into a muted ground
// with muted ink; outline and text variants keep their frame with muted ink.
// No hover classes, so a disabled button never reacts to the pointer.
export const buttonDisabled: Record<ButtonVariant, string> = {
  primary: "tw:border tw:border-muted tw:bg-muted tw:text-muted-foreground",
  secondary: "tw:border tw:border-border tw:bg-card tw:text-muted-foreground",
  ghost: "tw:border tw:border-transparent tw:bg-transparent tw:text-muted-foreground",
  link: "tw:border tw:border-transparent tw:bg-transparent tw:text-muted-foreground",
  danger: "tw:border tw:border-muted tw:bg-muted tw:text-muted-foreground",
  dangerOutline: "tw:border tw:border-border tw:bg-card tw:text-muted-foreground",
  dangerGhost: "tw:border tw:border-transparent tw:bg-transparent tw:text-muted-foreground",
};

export type StatePillTone = "online" | "warning" | "offline" | "info" | "muted";
// Status (StatePill): a round dot + label,
// no box. Calm tones are muted inline text; attention tones (warning, offline)
// add a soft 12% tint of their own hue behind medium-weight ink (tone-coloured
// ink drops below 4.5:1 on a tinted muted row in the light theme). Dots and
// icons reach 3:1 non-text contrast and every label 4.5:1, on card, popover,
// background and muted grounds (and on the tint) in both themes
// (tokens.test.ts). --accent is never a status colour.
// Items are start-aligned and the dot/icon is nudged onto the first line's
// centre, so a long label (an error message) wraps under itself.
export const statusText: Record<StatePillTone, string> = {
  online: "tw:inline-flex tw:max-w-full tw:items-start tw:gap-1.5 tw:text-xs tw:leading-5 tw:wrap-anywhere tw:font-normal tw:text-muted-foreground",
  info: "tw:inline-flex tw:max-w-full tw:items-start tw:gap-1.5 tw:text-xs tw:leading-5 tw:wrap-anywhere tw:font-normal tw:text-muted-foreground",
  muted: "tw:inline-flex tw:max-w-full tw:items-start tw:gap-1.5 tw:text-xs tw:leading-5 tw:wrap-anywhere tw:font-normal tw:text-muted-foreground",
  warning:
    "tw:inline-flex tw:max-w-full tw:items-start tw:gap-1.5 tw:text-xs tw:leading-5 tw:wrap-anywhere tw:rounded-control tw:px-2 tw:py-0.5 tw:font-medium tw:bg-warning/12 tw:text-foreground",
  offline:
    "tw:inline-flex tw:max-w-full tw:items-start tw:gap-1.5 tw:text-xs tw:leading-5 tw:wrap-anywhere tw:rounded-control tw:px-2 tw:py-0.5 tw:font-medium tw:bg-offline/12 tw:text-foreground",
};
// muted is a hollow ring (inactive / not applicable); every other tone is filled.
export const statusDot: Record<StatePillTone, string> = {
  online: "tw:mt-1.75 tw:size-1.5 tw:shrink-0 tw:rounded-full tw:bg-online",
  warning: "tw:mt-1.75 tw:size-1.5 tw:shrink-0 tw:rounded-full tw:bg-warning",
  offline: "tw:mt-1.75 tw:size-1.5 tw:shrink-0 tw:rounded-full tw:bg-offline",
  info: "tw:mt-1.75 tw:size-1.5 tw:shrink-0 tw:rounded-full tw:bg-muted-foreground",
  muted: "tw:mt-1.75 tw:size-1.5 tw:shrink-0 tw:rounded-full tw:border-[1.5px] tw:border-muted-foreground",
};
/** In progress (loading, refreshing): a filled neutral dot that pulses unless reduced motion is requested. */
export const statusBusyDot = "tw:mt-1.75 tw:size-1.5 tw:shrink-0 tw:rounded-full tw:bg-muted-foreground tw:motion-safe:animate-pulse";
export const statusIcon: Record<StatePillTone, string> = {
  online: "tw:mt-0.75 tw:size-3.5 tw:shrink-0 tw:text-online",
  warning: "tw:mt-0.75 tw:size-3.5 tw:shrink-0 tw:text-warning",
  offline: "tw:mt-0.75 tw:size-3.5 tw:shrink-0 tw:text-offline",
  info: "tw:mt-0.75 tw:size-3.5 tw:shrink-0 tw:text-muted-foreground",
  muted: "tw:mt-0.75 tw:size-3.5 tw:shrink-0 tw:text-muted-foreground",
};

export type ChipTone = "neutral" | "accent";
// Category / marker labels (Chip): a soft filled capsule with no outline and no
// dot, so it reads apart from a status (dot + label) and from a seed tag (4px
// muted rectangle). neutral = kind / version / origin / trigger labels;
// accent marks the one item that is the current default or active choice
// (--accent as a tinted surface).
export const chipTone: Record<ChipTone, string> = {
  neutral: "tw:inline-flex tw:max-w-full tw:items-center tw:gap-1 tw:rounded-control tw:px-2 tw:py-0.5 tw:text-xs tw:leading-5 tw:font-medium tw:wrap-anywhere tw:bg-muted tw:text-foreground",
  accent: "tw:inline-flex tw:max-w-full tw:items-center tw:gap-1 tw:rounded-control tw:px-2 tw:py-0.5 tw:text-xs tw:leading-5 tw:font-medium tw:wrap-anywhere tw:bg-accent tw:text-accent-foreground",
};

