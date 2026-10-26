import { cn } from "@/lib/utils";
import { RadioGroup } from "./base/toggles";
import { Field, Input } from "./base/controls";
import { t, type CopyKey } from "../lib/i18n";

// SecretField — the three-state control for secrets. Edit mode shows
// keep/replace/remove radios with a consequence note; create mode renders only
// the required input, no radios. A password field never echoes the stored value.
// A single-input caller passes inputLabel/value/onChange to use the built-in
// password input; a caller where several fields share one three-state choice
// (e.g. notification targets) uses only the radio + note part.

export type SecretMode = "keep" | "replace" | "remove";

const SECRET_MODE_OPTIONS: ReadonlyArray<{ value: SecretMode; labelKey: CopyKey }> = [
  { value: "keep", labelKey: "secret.keep" },
  { value: "replace", labelKey: "secret.replace" },
  { value: "remove", labelKey: "secret.remove" },
];

export function SecretField({
  label = t("secret.title"),
  editing = false,
  mode = "keep",
  onModeChange,
  subject,
  keepHint = t("secret.keepHint"),
  replaceHint = t("secret.replaceHint"),
  removeHint = t("secret.removeHint"),
  inputLabel,
  value,
  onChange,
  placeholder,
  inputError,
  inputMaxLength,
  allowRemove = true,
  className,
}: {
  /** Section title. */
  label?: string;
  /** Edit mode (an object is already stored) shows the three-state radio; create mode renders only the input area. */
  editing?: boolean;
  mode?: SecretMode;
  onModeChange?: (mode: SecretMode) => void;
  /** Prefix of the consequence note (the credential field name, e.g. "bot_token"). */
  subject?: string;
  /** Consequence note per state, overridable. */
  keepHint?: string;
  replaceHint?: string;
  removeHint?: string;
  /** Label of the built-in password input; passing inputLabel enables single-input mode. */
  inputLabel?: string;
  value?: string;
  onChange?: (next: string) => void;
  placeholder?: string;
  /** Validation message shown under the built-in password input. */
  inputError?: string;
  inputMaxLength?: number;
  /** Offer the remove state; off for credentials the target cannot run without. */
  allowRemove?: boolean;
  className?: string;
}) {
  // Conditional input: always shown (required) when creating; in edit mode only
  // under the "replace with a new value" state.
  const showInput = inputLabel !== undefined && (!editing || mode === "replace");
  return (
    <div className={cn("secret-field", className)}>
      <span className="secret-field-title text-caption">{label}</span>
      {editing && (
        <RadioGroup<SecretMode>
          aria-label={label}
          value={mode}
          onValueChange={(next) => onModeChange?.(next)}
          options={SECRET_MODE_OPTIONS.filter((o) => allowRemove || o.value !== "remove").map((o) => ({ value: o.value, label: t(o.labelKey) }))}
        />
      )}
      {editing && (
        <p className="secret-field-hint text-caption">
          {subject}
          {mode === "keep" ? keepHint : mode === "remove" ? removeHint : replaceHint}
        </p>
      )}
      {showInput && (
        <Field label={inputLabel} hint={inputError ? <span className="tw:text-destructive">{inputError}</span> : undefined}>
          <Input
            type="password"
            aria-label={inputLabel}
            maxLength={inputMaxLength}
            value={value ?? ""}
            placeholder={placeholder}
            onChange={(e) => onChange?.(e.target.value)}
          />
        </Field>
      )}
    </div>
  );
}
