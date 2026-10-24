import * as React from "react";
import { Select as BaseSelect } from "@base-ui/react/select";
import { Tabs as BaseTabs } from "@base-ui/react/tabs";
import { Check, ChevronsUpDown, Eye, EyeOff, Loader2, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { usePopupZIndex } from "./layers";
import { t } from "../../lib/i18n";
import { buttonDisabled, buttonVariant, type ButtonVariant } from "./tones";
import "./base.css";

// Form and display controls on Base UI + Tailwind. Shared visual parameters:
// 36px control height, --ring focus ring, muted input surface, lucide icons.
// Status and category labels are StatePill / Chip.

const focusRing = "tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring";
// Surface only: height and width are owned by each control (see controlBox) so
// a caller-visible size option never emits two classes for one property.
// controlFrame has no inline padding so a variant with a leading icon can own it.
const controlFrame =
  "tw:rounded-control tw:border tw:border-border tw:bg-muted tw:text-sm tw:text-foreground " +
  "tw:transition-colors tw:hover:border-primary/60 tw:data-popup-open:border-primary tw:aria-invalid:border-destructive " + focusRing;
const controlSurface = controlFrame + " tw:px-3";
const controlBox = "tw:h-9 tw:w-full";

type ControlSize = "sm" | "md";

export const Button = React.forwardRef<
  HTMLButtonElement,
  React.ButtonHTMLAttributes<HTMLButtonElement> & {
    variant?: ButtonVariant;
    /** `icon`: 36px square for an icon-only button (pass aria-label); same height as `md`. */
    size?: "sm" | "md" | "icon";
    /** Busy: keeps the variant look with a spinner and blocks clicks (aria-busy). */
    loading?: boolean;
  }
>(({ className, variant = "secondary", size = "md", loading, disabled, children, type = "button", ...props }, ref) => (
  <button
    ref={ref}
    type={type}
    disabled={disabled || loading}
    aria-busy={loading || undefined}
    className={cn(
      "tw:inline-flex tw:items-center tw:justify-center tw:gap-1.5 tw:whitespace-nowrap tw:rounded-control tw:font-medium tw:transition",
      loading ? "tw:cursor-progress" : disabled ? "tw:cursor-not-allowed" : "",
      // cn() is plain clsx (no tailwind-merge): two utilities for the same
      // property resolve by Tailwind's stylesheet order, not class order, so
      // variants must never emit competing padding/height classes.
      size === "sm" ? "tw:h-7 tw:text-xs" : size === "icon" ? "tw:size-9 tw:text-sm" : "tw:h-9 tw:text-sm",
      variant === "link" || size === "icon" ? "" : size === "sm" ? "tw:px-2.5" : "tw:px-3.5",
      disabled && !loading ? buttonDisabled[variant] : buttonVariant[variant],
      focusRing,
      className,
    )}
    {...props}
  >
    {loading ? <Loader2 className="tw:size-3.5 tw:animate-spin" aria-hidden="true" /> : null}
    {children}
  </button>
));
Button.displayName = "Button";

export type InputProps = React.InputHTMLAttributes<HTMLInputElement> & {
  /** `sm` (32px) for dense inline rows; default `md` (36px). */
  controlSize?: ControlSize;
  /**
   * Default true: the input fills its container. Pass false and a width
   * utility (e.g. `tw:w-24`) for a fixed-width input, so the two never compete.
   */
  fullWidth?: boolean;
  /**
   * Icon inside the field's leading edge (a lucide icon sized `tw:size-4`).
   * With an icon, `className` and the width go to the wrapper; the input fills it.
   */
  leadingIcon?: React.ReactNode;
};

export const Input = React.forwardRef<HTMLInputElement, InputProps>(
  ({ className, controlSize = "md", fullWidth = true, leadingIcon, ...props }, ref) =>
    leadingIcon ? (
      <span className={cn("tw:relative tw:inline-flex tw:min-w-0 tw:items-center", fullWidth ? "tw:w-full" : "", className)}>
        <span aria-hidden="true" className="tw:pointer-events-none tw:absolute tw:left-3 tw:flex tw:text-muted-foreground">
          {leadingIcon}
        </span>
        <input
          ref={ref}
          className={cn(
            controlFrame,
            "tw:pr-3 tw:pl-9",
            controlSize === "sm" ? "tw:h-8" : "tw:h-9",
            "tw:w-full tw:min-w-0 tw:placeholder:text-muted-foreground tw:disabled:cursor-not-allowed tw:disabled:opacity-60",
          )}
          {...props}
        />
      </span>
    ) : (
    <input
      ref={ref}
      className={cn(
        controlSurface,
        controlSize === "sm" ? "tw:h-8" : "tw:h-9",
        fullWidth ? "tw:w-full" : "",
        "tw:min-w-0 tw:placeholder:text-muted-foreground tw:disabled:cursor-not-allowed tw:disabled:opacity-60",
        className,
      )}
      {...props}
    />
  ),
);
Input.displayName = "Input";

export const Textarea = React.forwardRef<HTMLTextAreaElement, React.TextareaHTMLAttributes<HTMLTextAreaElement>>(
  ({ className, rows = 3, ...props }, ref) => (
    <textarea ref={ref} rows={rows} className={cn(controlSurface, "tw:w-full tw:min-w-0 tw:py-2 tw:leading-5", className)} {...props} />
  ),
);
Textarea.displayName = "Textarea";

/** Secret input with a reveal toggle. */
export const PasswordInput = React.forwardRef<HTMLInputElement, Omit<React.InputHTMLAttributes<HTMLInputElement>, "type">>(
  ({ className, ...props }, ref) => {
    const [revealed, setRevealed] = React.useState(false);
    return (
      <span className="tw:relative tw:block tw:min-w-0">
        <input
          ref={ref}
          type={revealed ? "text" : "password"}
          className={cn(controlSurface, controlBox, "tw:min-w-0 tw:pr-9", className)}
          {...props}
        />
        <button
          type="button"
          aria-label={revealed ? t("ui.hide") : t("ui.show")}
          aria-pressed={revealed}
          onClick={() => setRevealed((current) => !current)}
          className={cn("tw:absolute tw:inset-y-0 tw:right-0 tw:grid tw:w-9 tw:place-items-center tw:rounded-control tw:text-muted-foreground tw:hover:text-foreground", focusRing)}
        >
          {revealed ? <EyeOff className="tw:size-4" aria-hidden="true" /> : <Eye className="tw:size-4" aria-hidden="true" />}
        </button>
      </span>
    );
  },
);
PasswordInput.displayName = "PasswordInput";

export function Field({
  label,
  hint,
  children,
  className,
}: {
  label: React.ReactNode;
  /** Secondary line under the control (format rules, inherited value). */
  hint?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <label className={cn("tw:grid tw:min-w-0 tw:gap-1", className)}>
      <span className="tw:text-xs tw:text-muted-foreground">{label}</span>
      {children}
      {hint ? <span className="tw:text-xs tw:text-muted-foreground/80">{hint}</span> : null}
    </label>
  );
}

export type SelectOption<V extends string> = {
  value: V;
  label: string;
  disabled?: boolean;
  /** Heading to list the option under; options sharing a heading must be adjacent. */
  group?: string;
};

function groupOptions<V extends string>(options: SelectOption<V>[]): { group?: string; options: SelectOption<V>[] }[] {
  const sections: { group?: string; options: SelectOption<V>[] }[] = [];
  for (const option of options) {
    const last = sections[sections.length - 1];
    if (last && last.group === option.group) last.options.push(option);
    else sections.push({ group: option.group, options: [option] });
  }
  return sections;
}

/** Single-value select. `label` doubles as the trigger's accessible name. */
export function Select<V extends string>({
  label,
  value,
  options,
  onChange,
  disabled,
  className,
}: {
  label: string;
  value: V;
  options: SelectOption<V>[];
  onChange: (next: V) => void;
  disabled?: boolean;
  className?: string;
}) {
  const zIndex = usePopupZIndex();
  return (
    <BaseSelect.Root items={options} value={value} disabled={disabled} onValueChange={(next) => onChange(next as V)}>
      <BaseSelect.Trigger
        aria-label={label}
        className={cn(controlSurface, controlBox, "tw:flex tw:min-w-0 tw:items-center tw:justify-between tw:gap-2 tw:text-left tw:data-disabled:cursor-not-allowed tw:data-disabled:opacity-50", className)}
      >
        {/* min-w-0 (here and on the trigger) lets a long value truncate instead of pushing past its column. */}
        <BaseSelect.Value className="tw:min-w-0 tw:truncate" />
        <BaseSelect.Icon className="tw:shrink-0 tw:text-muted-foreground">
          <ChevronsUpDown className="tw:size-3.5" />
        </BaseSelect.Icon>
      </BaseSelect.Trigger>
      <BaseSelect.Portal>
        <BaseSelect.Positioner style={{ zIndex }} className="tw:outline-none" sideOffset={4} alignItemWithTrigger={false}>
          <BaseSelect.Popup className="tw:min-w-(--anchor-width) tw:origin-(--transform-origin) tw:rounded-control tw:border tw:border-border tw:bg-popover tw:p-1 tw:text-sm tw:text-popover-foreground tw:shadow-2 tw:outline-none tw:transition-[opacity,scale] tw:duration-100 tw:data-starting-style:opacity-0 tw:data-ending-style:opacity-0">
            <BaseSelect.List className="tw:max-h-(--available-height) tw:overflow-y-auto">
              {groupOptions(options).map((section, index) => {
                const items = section.options.map((option) => (
                  <BaseSelect.Item
                    key={option.value}
                    value={option.value}
                    disabled={option.disabled}
                    className="tw:grid tw:cursor-default tw:grid-cols-[1rem_1fr] tw:items-center tw:gap-2 tw:rounded-md tw:py-1.5 tw:pr-3 tw:pl-2 tw:outline-none tw:select-none tw:data-highlighted:bg-accent tw:data-highlighted:text-accent-foreground"
                  >
                    <BaseSelect.ItemIndicator className="tw:col-start-1">
                      <Check className="tw:size-3.5" />
                    </BaseSelect.ItemIndicator>
                    <BaseSelect.ItemText className="tw:col-start-2">{option.label}</BaseSelect.ItemText>
                  </BaseSelect.Item>
                ));
                if (section.group === undefined) return items;
                return (
                  <BaseSelect.Group key={`${index}:${section.group}`}>
                    <BaseSelect.GroupLabel className="tw:px-2 tw:py-1 tw:text-xs tw:text-muted-foreground">{section.group}</BaseSelect.GroupLabel>
                    {items}
                  </BaseSelect.Group>
                );
              })}
            </BaseSelect.List>
          </BaseSelect.Popup>
        </BaseSelect.Positioner>
      </BaseSelect.Portal>
    </BaseSelect.Root>
  );
}

const tabClass = cn(
  "tw:-mb-px tw:border-b-2 tw:border-transparent tw:px-3 tw:py-2 tw:text-sm tw:text-muted-foreground tw:transition-colors",
  "tw:hover:text-foreground tw:data-active:border-primary tw:data-active:font-semibold tw:data-active:text-foreground",
  focusRing,
);

export function Tabs({
  value,
  onChange,
  items,
  className,
}: {
  value: string;
  onChange: (next: string) => void;
  items: { key: string; label: string; children: React.ReactNode }[];
  className?: string;
}) {
  return (
    <TabsRoot value={value} onChange={onChange} className={className}>
      <TabsList items={items} />
      {items.map((item) => (
        <TabsPanel key={item.key} value={item.key} className="tw:min-h-0 tw:pt-3">
          {item.children}
        </TabsPanel>
      ))}
    </TabsRoot>
  );
}

// Composable parts for hosts that place the tab strip and the panels in
// different regions (e.g. a fixed strip above a scrolling body), or that keep
// inactive panels mounted so unsaved edits survive a tab switch.

export function TabsRoot({
  value,
  onChange,
  className,
  children,
}: {
  value: string;
  onChange: (next: string) => void;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <BaseTabs.Root value={value} onValueChange={(next) => onChange(String(next))} className={cn("tw:flex tw:min-h-0 tw:flex-col", className)}>
      {children}
    </BaseTabs.Root>
  );
}

export function TabsList({
  items,
  id,
  className,
}: {
  items: { key: string; label: React.ReactNode }[];
  id?: string;
  className?: string;
}) {
  return (
    <BaseTabs.List id={id} className={cn("tw:relative tw:flex tw:flex-none tw:gap-1 tw:border-b tw:border-border", className)}>
      {items.map((item) => (
        <BaseTabs.Tab key={item.key} value={item.key} className={tabClass}>
          {item.label}
        </BaseTabs.Tab>
      ))}
    </BaseTabs.List>
  );
}

export function TabsPanel({
  value,
  keepMounted = false,
  className,
  children,
  ...rest
}: {
  value: string;
  /** Keep the panel mounted (hidden) while inactive. */
  keepMounted?: boolean;
  className?: string;
  children: React.ReactNode;
} & Omit<React.HTMLAttributes<HTMLDivElement>, "children" | "className">) {
  return (
    <BaseTabs.Panel value={value} keepMounted={keepMounted} className={cn("tw:outline-none", className)} {...rest}>
      {children}
    </BaseTabs.Panel>
  );
}

/** Icon-only spinner for inline use next to existing status text. */
export function SpinnerIcon({ className }: { className?: string }) {
  return <Loader2 className={cn("tw:size-4 tw:animate-spin", className)} aria-hidden="true" />;
}

const chip = "tw:inline-flex tw:items-center tw:gap-1 tw:rounded-[0.25rem] tw:border tw:border-border tw:px-2 tw:py-0.5 tw:text-xs tw:font-medium tw:leading-normal tw:text-foreground";

/**
 * Keyword list input (chips). Enter or blur commits the typed text as a chip;
 * each chip has its own remove button; Backspace on an empty input removes the
 * last chip; duplicates are ignored; `validateItem` can reject an item with an
 * inline message. Read-only renders the chips only, plus an optional inherited
 * value hint.
 */
export function TagInput({
  value,
  onChange,
  placeholder = t("ui.tagPlaceholder"),
  validateItem,
  readOnly = false,
  globalHint,
  ariaLabel,
}: {
  value: string[];
  onChange?: (next: string[]) => void;
  placeholder?: string;
  /** Per-item validation: return a message to block the commit and show it inline. */
  validateItem?: (tag: string) => string | null;
  readOnly?: boolean;
  /** Inherited value hint (e.g. "Global: Movie, OVA"), shown in read-only mode. */
  globalHint?: string;
  ariaLabel?: string;
}) {
  const [inputValue, setInputValue] = React.useState("");
  const [error, setError] = React.useState<string>();

  function commit(raw: string) {
    const tag = raw.trim();
    if (!tag) return;
    if (value.includes(tag)) {
      setInputValue("");
      return;
    }
    if (validateItem) {
      const message = validateItem(tag);
      if (message) {
        setError(message);
        return;
      }
    }
    setError(undefined);
    onChange?.([...value, tag]);
    setInputValue("");
  }

  function remove(tag: string) {
    onChange?.(value.filter((item) => item !== tag));
  }

  if (readOnly) {
    return (
      <div>
        {value.length === 0 ? (
          <span className="tw:text-xs tw:text-muted-foreground/60">{t("ui.tagEmpty")}</span>
        ) : (
          <span className="tw:flex tw:flex-wrap tw:gap-1.5">
            {value.map((tag) => (
              <span key={tag} className={chip}>
                {tag}
              </span>
            ))}
          </span>
        )}
        {globalHint ? (
          <p className="tw:m-0 tw:mt-1 tw:text-xs tw:whitespace-pre-line tw:wrap-anywhere tw:text-muted-foreground/70">{globalHint}</p>
        ) : null}
      </div>
    );
  }

  return (
    <div>
      <div className="tw:flex tw:min-h-9 tw:flex-wrap tw:items-center tw:gap-1.5 tw:rounded-control tw:border tw:border-border tw:bg-muted tw:px-2 tw:py-1 tw:transition-colors tw:hover:border-primary/60 tw:focus-within:border-primary">
        {value.map((tag) => (
          <span key={tag} className={chip}>
            {tag}
            <button
              type="button"
              aria-label={t("ui.tagRemove", { tag })}
              onClick={() => remove(tag)}
              className={cn("tw:grid tw:place-items-center tw:rounded-[2px] tw:text-muted-foreground tw:hover:text-foreground", focusRing)}
            >
              <X className="tw:size-3" aria-hidden="true" />
            </button>
          </span>
        ))}
        <input
          aria-label={ariaLabel}
          className="tw:min-w-[120px] tw:flex-1 tw:border-0 tw:bg-transparent tw:px-1 tw:py-0.5 tw:text-sm tw:text-foreground tw:outline-none tw:placeholder:text-muted-foreground"
          value={inputValue}
          placeholder={value.length === 0 ? placeholder : ""}
          onChange={(event) => {
            setInputValue(event.target.value);
            setError(undefined);
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              commit(inputValue);
            } else if (event.key === "Backspace" && inputValue === "" && value.length > 0) {
              remove(value[value.length - 1]);
            }
          }}
          onBlur={() => commit(inputValue)}
        />
      </div>
      {error ? (
        <p role="alert" className="tw:m-0 tw:mt-1 tw:text-xs tw:text-offline">
          {error}
        </p>
      ) : null}
    </div>
  );
}
