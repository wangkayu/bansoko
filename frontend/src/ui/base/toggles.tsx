import * as React from "react";
import { Checkbox as BaseCheckbox } from "@base-ui/react/checkbox";
import { Radio as BaseRadio } from "@base-ui/react/radio";
import { RadioGroup as BaseRadioGroup } from "@base-ui/react/radio-group";
import { Switch as BaseSwitch } from "@base-ui/react/switch";
import { Check, Loader2, Minus } from "lucide-react";
import { cn } from "@/lib/utils";
import "./base.css";

// Selection controls: checkbox, switch, radio group and segmented picker.
// Checked state is carried by the brand primary (the same hue as the focus ring
// and active identity); unchecked controls sit on the muted input surface.

const focusRing = "tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring";
// Size and color are set by the caller (Checkbox `size`, className; Radio text-sm) so no two classes compete.
const choiceLabel = "tw:inline-flex tw:items-center tw:gap-2 tw:has-data-disabled:cursor-not-allowed tw:has-data-disabled:opacity-50";

export function Checkbox({
  checked,
  onCheckedChange,
  indeterminate = false,
  disabled,
  children,
  className,
  size = "md",
  title,
  "aria-label": ariaLabel,
}: {
  checked: boolean;
  onCheckedChange: (next: boolean) => void;
  indeterminate?: boolean;
  disabled?: boolean;
  /** Label type size: `md` text-sm (default), `sm` text-xs for dense toolbars and rows. */
  size?: "sm" | "md";
  /** Native tooltip, e.g. why the control is disabled. */
  title?: string;
  /** Visible label; without one, pass aria-label. */
  children?: React.ReactNode;
  className?: string;
  "aria-label"?: string;
}) {
  const labelId = React.useId();
  return (
    <span title={title} className={cn(choiceLabel, size === "sm" ? "tw:text-xs" : "tw:text-sm", className)}>
      <BaseCheckbox.Root
        checked={checked}
        indeterminate={indeterminate}
        disabled={disabled}
        onCheckedChange={(next) => onCheckedChange(next)}
        aria-label={ariaLabel}
        aria-labelledby={children !== undefined && ariaLabel === undefined ? labelId : undefined}
        className={cn(
          "tw:grid tw:size-4 tw:flex-none tw:place-items-center tw:rounded-[4px] tw:border tw:border-border tw:bg-muted tw:text-primary-foreground tw:transition-colors",
          "tw:data-checked:border-primary tw:data-checked:bg-primary tw:data-indeterminate:border-primary tw:data-indeterminate:bg-primary",
          "tw:data-disabled:cursor-not-allowed",
          focusRing,
        )}
      >
        <BaseCheckbox.Indicator className="tw:flex">
          {indeterminate ? <Minus className="tw:size-3" strokeWidth={3} /> : <Check className="tw:size-3" strokeWidth={3} />}
        </BaseCheckbox.Indicator>
      </BaseCheckbox.Root>
      {children !== undefined ? (
        // Not a native <label> (Base UI would then re-label the control from it
        // and override aria-label); clicking the text toggles like a label does.
        <span id={labelId} onClick={() => { if (!disabled) onCheckedChange(!checked); }} className="tw:cursor-pointer tw:select-none">
          {children}
        </span>
      ) : null}
    </span>
  );
}

export function Switch({
  checked,
  onCheckedChange,
  disabled,
  loading,
  "aria-label": ariaLabel,
  className,
}: {
  checked: boolean;
  onCheckedChange: (next: boolean) => void;
  disabled?: boolean;
  /** Pending save: blocks input and shows a spinner in the thumb. */
  loading?: boolean;
  "aria-label": string;
  className?: string;
}) {
  return (
    <BaseSwitch.Root
      checked={checked}
      disabled={disabled || loading}
      onCheckedChange={(next) => onCheckedChange(next)}
      aria-label={ariaLabel}
      aria-busy={loading || undefined}
      className={cn(
        "tw:relative tw:inline-flex tw:h-5 tw:w-9 tw:flex-none tw:items-center tw:rounded-full tw:border tw:border-border tw:bg-muted tw:p-px tw:transition-colors",
        "tw:data-checked:border-primary tw:data-checked:bg-primary tw:data-disabled:cursor-not-allowed tw:data-disabled:opacity-50",
        focusRing,
        className,
      )}
    >
      <BaseSwitch.Thumb className="tw:grid tw:size-4 tw:place-items-center tw:rounded-full tw:bg-muted-foreground tw:shadow-sm tw:transition-[translate,background-color] tw:duration-150 tw:data-checked:translate-x-4 tw:data-checked:bg-primary-foreground">
        {loading ? <Loader2 className="tw:size-3 tw:animate-spin tw:text-primary" aria-hidden="true" /> : null}
      </BaseSwitch.Thumb>
    </BaseSwitch.Root>
  );
}

export type ChoiceOption<V extends string> = {
  value: V;
  label: React.ReactNode;
  disabled?: boolean;
  /** RadioGroup only: one muted line under the label. */
  description?: React.ReactNode;
  /**
   * Segmented only: how the option looks while checked. `solid` (default) is
   * the primary fill; `quiet` is a plain raised chip for a default/no-op choice;
   * `primary` and `danger` are soft tints that mark an override at a glance.
   */
  tone?: SegmentedTone;
};

export type SegmentedTone = "solid" | "quiet" | "primary" | "danger";

/**
 * Radio list; use Segmented for a compact inline picker over a few options.
 * `variant="card"`: each option is a bordered row (label + description) that is
 * clickable as a whole and highlights when checked — for a choice between a few
 * modes that each need one line of explanation.
 */
export function RadioGroup<V extends string>({
  value,
  onValueChange,
  options,
  orientation = "horizontal",
  variant = "plain",
  disabled,
  "aria-label": ariaLabel,
  className,
}: {
  value: V;
  onValueChange: (next: V) => void;
  options: ReadonlyArray<ChoiceOption<V>>;
  orientation?: "horizontal" | "vertical";
  variant?: "plain" | "card";
  disabled?: boolean;
  "aria-label": string;
  className?: string;
}) {
  return (
    <BaseRadioGroup
      value={value}
      onValueChange={(next) => onValueChange(next as V)}
      disabled={disabled}
      aria-label={ariaLabel}
      className={cn(
        "tw:flex",
        orientation === "vertical" ? "tw:flex-col tw:gap-2" : "tw:flex-wrap tw:gap-x-4 tw:gap-y-2",
        className,
      )}
    >
      {options.map((option) => (
        <RadioItem key={option.value} option={option} variant={variant} />
      ))}
    </BaseRadioGroup>
  );
}

const radioItemFrame = {
  plain: cn(choiceLabel, "tw:text-sm"),
  card: cn(
    "tw:flex tw:min-w-0 tw:cursor-pointer tw:items-start tw:gap-2 tw:rounded-control tw:border tw:border-border tw:bg-card tw:px-3 tw:py-2 tw:text-sm tw:transition-colors",
    "tw:hover:not-has-data-checked:bg-muted/40 tw:has-data-checked:border-primary tw:has-data-checked:bg-primary/5",
    // Read-only: no hover affordance; the checked row stays readable.
    "tw:has-data-disabled:pointer-events-none tw:has-data-disabled:opacity-70",
  ),
} as const;

function RadioItem<V extends string>({ option, variant }: { option: ChoiceOption<V>; variant: "plain" | "card" }) {
  const labelId = React.useId();
  const descriptionId = React.useId();
  // Card rows are a <label> (the hidden radio input inside makes the whole row
  // clickable); plain rows keep the radio itself as the only target.
  const Frame = variant === "card" ? "label" : "span";
  return (
    <Frame className={radioItemFrame[variant]}>
      <BaseRadio.Root
        value={option.value}
        disabled={option.disabled}
        aria-labelledby={labelId}
        aria-describedby={option.description ? descriptionId : undefined}
        className={cn(
          "tw:grid tw:size-4 tw:flex-none tw:place-items-center tw:rounded-full tw:border tw:border-border tw:bg-muted tw:transition-colors",
          "tw:data-checked:border-primary tw:data-disabled:cursor-not-allowed",
          variant === "card" && "tw:mt-0.5",
          focusRing,
        )}
      >
        <BaseRadio.Indicator className="tw:size-2 tw:rounded-full tw:bg-primary" />
      </BaseRadio.Root>
      <span className="tw:grid tw:min-w-0 tw:gap-0.5">
        <span id={labelId} className={variant === "card" ? "tw:font-medium tw:text-foreground" : "tw:cursor-default"}>
          {option.label}
        </span>
        {option.description ? (
          <span id={descriptionId} className="tw:text-xs tw:text-muted-foreground">
            {option.description}
          </span>
        ) : null}
      </span>
    </Frame>
  );
}

/**
 * Segmented picker for a small finite option set (same look as the
 * SegmentedControl default variant). Radio semantics with arrow-key movement.
 */
// Per-size geometry: each size owns its frame and option box outright.
const segmentedFrame = {
  default: "tw:gap-0.5 tw:rounded-lg tw:border tw:border-border tw:bg-card/60 tw:p-1",
  compact: "tw:gap-0.5 tw:rounded-md tw:border tw:border-border/60 tw:bg-muted/30 tw:p-0.5",
  // 32px overall, the height the dense toolbar's other controls share. A soft, borderless
  // tinted block — the filter row's segments read as one group, not as boxed controls.
  toolbar: "tw:gap-0.5 tw:rounded-control tw:bg-muted tw:p-1",
} as const;
const segmentedOption = {
  default: "tw:rounded-md tw:px-3 tw:py-1 tw:text-xs/[1.5]",
  compact: "tw:rounded-[0.25rem] tw:px-1.5 tw:py-0.5 tw:text-badge",
  toolbar: "tw:h-6 tw:rounded-lg tw:px-2 tw:text-xs/[1.5]",
} as const;

// Checked look per option tone; each tone owns its full checked class set.
const segmentedChecked: Record<SegmentedTone, string> = {
  solid: "tw:data-checked:bg-primary tw:data-checked:text-primary-foreground",
  quiet: "tw:data-checked:bg-card tw:data-checked:text-foreground tw:data-checked:shadow-sm",
  primary: "tw:data-checked:bg-primary/15 tw:data-checked:text-accent-foreground",
  danger: "tw:data-checked:bg-destructive/12 tw:data-checked:text-destructive",
};

export function Segmented<V extends string>({
  value,
  onValueChange,
  options,
  disabled,
  size = "default",
  title,
  "aria-label": ariaLabel,
  className,
}: {
  value: V;
  onValueChange: (next: V) => void;
  options: ReadonlyArray<ChoiceOption<V>>;
  disabled?: boolean;
  /** `compact`: small, light-stroke variant for dense rows (one picker per list row). `toolbar`: 32px, matching the other dense-toolbar controls. */
  size?: "default" | "compact" | "toolbar";
  title?: string;
  "aria-label": string;
  className?: string;
}) {
  return (
    <BaseRadioGroup
      value={value}
      onValueChange={(next) => onValueChange(next as V)}
      disabled={disabled}
      aria-label={ariaLabel}
      title={title}
      className={cn("tw:inline-flex tw:w-fit tw:items-center", segmentedFrame[size], className)}
    >
      {options.map((option) => (
        <BaseRadio.Root
          key={option.value}
          value={option.value}
          disabled={option.disabled}
          className={cn(
            // Center the label in the option's box: the fixed-height `toolbar` size would
            // otherwise park its line box at the top and read a few pixels high.
            "tw:inline-flex tw:cursor-pointer tw:items-center tw:justify-center tw:text-center tw:font-medium tw:text-muted-foreground tw:transition-colors tw:duration-150",
            segmentedOption[size],
            "tw:hover:not-data-checked:bg-muted tw:hover:not-data-checked:text-foreground",
            segmentedChecked[option.tone ?? "solid"],
            "tw:data-disabled:cursor-not-allowed tw:data-disabled:opacity-50",
            focusRing,
          )}
        >
          {option.label}
        </BaseRadio.Root>
      ))}
    </BaseRadioGroup>
  );
}
