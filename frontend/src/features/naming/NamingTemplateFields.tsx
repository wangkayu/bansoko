import { Dialog, HelpTip, Select } from "../../ui/base";
import { t, tryT } from "../../lib/i18n";
import { emptyFactsNote } from "../../lib/i18n/reasons";
import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  getNamingTemplateCatalog,
  previewNamingTemplate,
  toUserMessage,
  type NamingCatalogField,
  type NamingCatalogFunction,
  type NamingCatalogSample,
  type NamingResolvedValue,
  type NamingRules,
  type NamingTemplate,
  type NamingTemplateCatalog,
  type NamingTemplateKind,
  type NamingTemplatePreview,
} from "../../lib/api/client";
import { Chip, SegmentedControl, StatePill, Field, Input } from "../../ui";
import { templateCatalogKeys } from "../../lib/queryKeys";
import { FactCatalog, PreviewText } from "../templateFacts/FactCatalog";
import { namingTemplateBody, sameNamingTemplates } from "./templateBody";
import { factDescription, factLabel, factRootLabel, groupFacts } from "../templateFacts/facts";

// The naming template editor: the js-naming-1 language (JavaScript template literals, unified
//   variable names). The catalog is edited separately per the two TV-series / movie variants, and
//   subtitles expose only the suffix template (the directory/name/extension follow the bound
//   video). What the fields edit is the template body (the content inside the braces); the server
//   adds the outer backticks at render time. A full literal saved by older versions is normalized
//   to the body on display and becomes the body once saved.
// The variable and function allowlist comes from the closed catalog at /template-catalogs/naming;
//   the frontend never hardcodes sample refs. The preview is a pure read endpoint and writes no
//   server state.
//
// The globally-set template library (NamingTemplatesCard) and the per-anime override
//   (RulesSectionsFields) share this component so the variable hints cannot drift apart between the
//   two places. Sample picking appears only in global settings (samplePicker); a per-anime override
//   receives the library (templates), "choose template" copies its body into the fields, and no link
//   to the template remains afterwards.

const PREVIEW_DEBOUNCE_MS = 400;

type NamingTab = "tv" | "movie" | "unknown" | "subtitle";


const tabOptions = (): ReadonlyArray<{ value: NamingTab; label: string }> => [
  { value: "tv", label: t("naming.tabs.tv") },
  { value: "movie", label: t("naming.tabs.movie") },
  { value: "unknown", label: t("naming.tabs.unknown") },
  { value: "subtitle", label: t("naming.tabs.subtitle") },
];

// Default preview-sample choice: matched dynamically from the catalog's samples by tab keywords,
//   never a hardcoded ref; subtitle samples prefer refs containing subtitle, TV/movie excludes
//   them.
function defaultSampleRef(tab: NamingTab, samples: NamingCatalogSample[]): string | undefined {
  const match = samples.find((sample) => {
    const ref = sample.ref.toLowerCase();
    if (tab === "subtitle") return ref.includes("subtitle");
    return ref.includes(tab) && !ref.includes("subtitle");
  });
  return match?.ref;
}

export type NamingTemplateValue = NamingRules;

type ActiveField = { tab: NamingTab; kind: NamingTemplateKind };

/** What the fields read and write is the template body; the server supplies the outer backticks. */
function fieldValue(field: ActiveField, value: NamingTemplateValue): string {
  if (field.tab === "subtitle") return value.subtitle_suffix_template;
  return field.kind === "directory"
    ? value[field.tab].directory_template
    : value[field.tab].video_filename_template;
}

function withField(field: ActiveField, value: NamingTemplateValue, next: string): NamingTemplateValue {
  if (field.tab === "subtitle") return { ...value, subtitle_suffix_template: next };
  const variant = value[field.tab];
  return {
    ...value,
    [field.tab]:
      field.kind === "directory"
        ? { ...variant, directory_template: next }
        : { ...variant, video_filename_template: next },
  };
}

export function NamingTemplateFields({
  value,
  onChange,
  disabled = false,
  samplePicker = false,
  templates,
}: {
  value: NamingTemplateValue;
  onChange: (next: NamingTemplateValue) => void;
  disabled?: boolean;
  /** Shows the preview-sample picker (global settings only). */
  samplePicker?: boolean;
  /** The template library: when passed, shows "choose template". */
  templates?: NamingTemplate[];
}) {
  const catalog = useQuery({
    queryKey: templateCatalogKeys.naming(value.language),
    queryFn: () => getNamingTemplateCatalog(value.language),
    staleTime: Infinity,
  });

  const [tab, setTab] = useState<NamingTab>("tv");
  // The last focused input decides where a variable inserts; by default it goes to the current tab's directory template.
  const [activeField, setActiveField] = useState<ActiveField>({ tab: "tv", kind: "directory" });
  // Per-tab user choice of preview sample; with no choice, auto-matched from catalog samples by tab keywords.
  const [sampleChoice, setSampleChoice] = useState<Partial<Record<NamingTab, string>>>({});
  const [helpOpen, setHelpOpen] = useState(false);
  const directoryRef = useRef<HTMLInputElement>(null);
  const videoRef = useRef<HTMLInputElement>(null);
  const subtitleRef = useRef<HTMLInputElement>(null);

  // When the current value equals some template's body, that template is shown; otherwise
  //   "custom". Choosing only copies — later edits to fields or templates never affect each
  //   other.
  const matchedTemplate = templates?.find((item) => sameNamingTemplates(item.rules, value));

  function focusField(field: ActiveField) {
    setActiveField(field);
  }

  function insertToken(token: string) {
    // The insert target follows the last focused input first; if that box is not in the current tab
    //   (unmounted), fall back to the current tab's first input.
    const target: ActiveField =
      activeField.tab === tab ? activeField : { tab, kind: tab === "subtitle" ? "subtitle_suffix" : "directory" };
    const ref = target.kind === "directory" ? directoryRef : target.kind === "video_filename" ? videoRef : subtitleRef;
    const input = ref.current;
    const current = fieldValue(target, value);
    if (!input) {
      onChange(withField(target, value, current + token));
      return;
    }
    const start = input.selectionStart ?? current.length;
    const end = input.selectionEnd ?? current.length;
    const next = current.slice(0, start) + token + current.slice(end);
    onChange(withField(target, value, next));
    // After inserting, the cursor lands after the token to make consecutive inserts easy.
    requestAnimationFrame(() => {
      input.focus();
      const caret = start + token.length;
      input.setSelectionRange(caret, caret);
    });
  }

  const samples = catalog.data?.samples ?? [];
  const sampleRef = (samplePicker ? sampleChoice[tab] : undefined) ?? defaultSampleRef(tab, samples);

  return (
    <div className="naming-stack">
      {templates && templates.length > 0 && !disabled && (
        <Field label={t("naming.templateField")} hint={t("naming.templateHint")}>
          <Select
            label={t("naming.templateField")}
            value={matchedTemplate?.key ?? ""}
            options={[
              { value: "", label: t("naming.custom") },
              ...templates.map((item) => ({ value: item.key, label: item.name })),
            ]}
            onChange={(key) => {
              const chosen = templates.find((item) => item.key === key);
              if (chosen) onChange(namingTemplateBody(chosen.rules));
            }}
          />
        </Field>
      )}

      <SegmentedControl options={tabOptions()} value={tab} onChange={setTab} ariaLabel={t("naming.tabsAria")} />

      {tab !== "subtitle" && (
        <>
          <Field label={t("naming.directory")}>
            <Input
              ref={directoryRef}
              value={value[tab].directory_template}
              readOnly={disabled}
              disabled={disabled}
              placeholder={t("naming.placeholder")}
              onFocus={() => focusField({ tab, kind: "directory" })}
              onChange={(e) => onChange({ ...value, [tab]: { ...value[tab], directory_template: e.target.value } })}
            />
          </Field>
          <p className="naming-muted text-caption">
            {t("naming.dirHint")}
            <HelpTip label={t("naming.dirHelpLabel")} className="tw:ml-1 tw:align-middle">{t("naming.dirHelp")}</HelpTip>
          </p>
          <Field label={t("naming.filename")}>
            <Input
              ref={videoRef}
              value={value[tab].video_filename_template}
              readOnly={disabled}
              disabled={disabled}
              placeholder={t("naming.placeholder")}
              onFocus={() => focusField({ tab, kind: "video_filename" })}
              onChange={(e) => onChange({ ...value, [tab]: { ...value[tab], video_filename_template: e.target.value } })}
            />
          </Field>
        </>
      )}

      {tab === "subtitle" && (
        <>
          <Field label={t("naming.subtitleSuffix")}>
            <Input
              ref={subtitleRef}
              value={value.subtitle_suffix_template}
              readOnly={disabled}
              disabled={disabled}
              placeholder={t("naming.placeholder")}
              onFocus={() => focusField({ tab: "subtitle", kind: "subtitle_suffix" })}
              onChange={(e) => onChange({ ...value, subtitle_suffix_template: e.target.value })}
            />
          </Field>
          <p className="naming-muted text-caption">
            {t("naming.subtitleHint")}
            <HelpTip label={t("naming.subtitleHelpLabel")} className="tw:ml-1 tw:align-middle">{t("naming.subtitleHelp")}</HelpTip>
          </p>
        </>
      )}

      {!disabled && (
        <>
          <VariablePalette
            catalog={catalog.data}
            loading={catalog.isPending}
            error={catalog.error}
            onInsert={insertToken}
          />
          {catalog.data && (
            <div className="naming-field-row">
              {samplePicker && samples.length > 0 && (
                <label className="naming-field-label text-caption">
                  {t("naming.sample")}
                  <Select
                    label={t("naming.sample")}
                    value={sampleRef ?? ""}
                    options={[
                      { value: "", label: t("naming.auto") },
                      ...samples.map((sample) => ({ value: sample.ref, label: sample.label })),
                    ]}
                    onChange={(next) => setSampleChoice((prev) => ({ ...prev, [tab]: next || undefined }))}
                  />
                </label>
              )}
              <button
                type="button"
                className="naming-field-link text-caption"
                onClick={() => setHelpOpen(true)}
              >
                {t("naming.help")}
              </button>
            </div>
          )}
          {catalog.data && tab !== "subtitle" && (
            <div className="naming-section">
              <TemplatePreview
                kind="directory"
                label={t("naming.previewDir")}
                source={value[tab].directory_template}
                sampleRef={sampleRef}
                language={value.language}
              />
              <TemplatePreview
                kind="video_filename"
                label={t("naming.previewFile")}
                source={value[tab].video_filename_template}
                sampleRef={sampleRef}
                language={value.language}
              />
            </div>
          )}
          {catalog.data && tab === "subtitle" && (
            <TemplatePreview
              kind="subtitle_suffix"
              label={t("naming.previewSuffix")}
              source={value.subtitle_suffix_template}
              sampleRef={sampleRef}
              language={value.language}
            />
          )}
          {catalog.data && (
            <NamingHelpDialog catalog={catalog.data} open={helpOpen} onOpenChange={setHelpOpen} />
          )}
        </>
      )}
    </div>
  );
}

function VariablePalette({
  catalog,
  loading,
  error,
  onInsert,
}: {
  catalog?: NamingTemplateCatalog;
  loading: boolean;
  error: unknown;
  onInsert: (token: string) => void;
}) {
  const [open, setOpen] = useState(false);

  if (loading) {
    return <p className="naming-muted text-caption">{t("naming.catalogLoading")}</p>;
  }
  if (error) {
    return (
      <p className="naming-palette-error text-caption" role="alert">
        {toUserMessage(error, t("naming.catalogFailed"))}
      </p>
    );
  }
  if (!catalog) return null;

  const fields = catalog.fields ?? [];
  const functions = catalog.functions ?? [];
  const shorthands = catalog.shorthands ?? [];

  return (
    <div className="naming-palette">
      <button
        type="button"
        className="naming-palette-toggle text-caption"
        aria-expanded={open}
        onClick={() => setOpen((prev) => !prev)}
      >
        <span>
          {t("naming.paletteSummary", { fields: fields.length, functions: functions.length })}
        </span>
        <span>{open ? t("naming.collapse") : t("naming.expand")}</span>
      </button>

      {open && (
        <div className="naming-palette-body">
          {shorthands.length > 0 && (
            <div className="naming-palette-group">
              <span className="naming-palette-label text-caption">{t("naming.shorthandsLabel")}</span>
              <div className="naming-palette-items">
                {shorthands.map((shorthand) => (
                  <button
                    key={shorthand.name}
                    type="button"
                    title={`${shorthand.expansion}\n${t("naming.exampleTitle", { value: shorthand.example })}`}
                    className="naming-palette-item text-caption"
                    onClick={() => onInsert(`\${${shorthand.name}}`)}
                  >
                    {t("naming.shorthandPair", { token: `\${${shorthand.name}}`, example: shorthand.example })}
                  </button>
                ))}
              </div>
              {/* Two non-obvious points: a season-bearing shorthand expands to nothing when there is
                  no season number (owner-ruled), and a shorthand is mere input convenience — the full
                  expression stays legal. */}
              <span className="naming-palette-label text-caption">{t("naming.shorthandsHint")}</span>
            </div>
          )}
          <FactCatalog facts={fields} token={(path) => `\${${path}}`} onInsert={onInsert} />
          <div className="naming-palette-group">
            <span className="naming-palette-label text-caption">{t("naming.functionsLabel")}</span>
            <div className="naming-palette-items">
              {functions.map((fn) => (
                <button
                  key={fn.name}
                  type="button"
                  title={fn.example ? `${fn.signature}\n${t("naming.exampleTitle", { value: fn.example })}` : fn.signature}
                  className="naming-palette-item text-caption"
                  onClick={() => onInsert(fn.example ?? fn.signature)}
                >
                  {fn.signature}
                </button>
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

// The fully browsable syntax reference: the field tree (top-level objects grouped),
//   nullability/example values/types, function signatures with examples, and the fixed redacted
//   sample list. Read-only help; insertion goes through the variable panel.
function NamingHelpDialog({
  catalog,
  open,
  onOpenChange,
}: {
  catalog: NamingTemplateCatalog;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const fields = catalog.fields ?? [];
  const functions = catalog.functions ?? [];
  const samples = catalog.samples ?? [];
  const grouped = groupFacts(fields);
  return (
    <Dialog open={open} onRequestClose={() => onOpenChange(false)} tier="M" title={t("naming.helpTitle")}>
      {/* Custom variables (vars.*) are hidden in the current version: the help covers syntax only and
          omits vars_keys. When reintroducing them, switch back to
          t("naming.helpBody", { keys: catalog.vars_keys.join(", ") || t("naming.none") }).
          The field and variable panels come from the backend catalog, which already excludes vars.*;
          no filtering needed here. */}
      <p className="naming-muted text-caption">{t("naming.helpSyntax")}</p>
      <div className="naming-help">
        <section className="naming-help-section">
          <h3 className="naming-help-title text-body">{t("naming.fields")}</h3>
          {grouped.map(([group, groupFields]) => (
            <div key={group} className="naming-help-group">
              <span className="naming-help-group-label text-caption">
                {factRootLabel(group)}
              </span>
              <ul className="naming-help-list">
                {groupFields.map((field) => (
                  <HelpFieldRow key={field.path} field={field} />
                ))}
              </ul>
            </div>
          ))}
        </section>

        <section className="naming-help-section">
          <h3 className="naming-help-title text-body">{t("naming.functions")}</h3>
          <ul className="naming-help-list">
            {functions.map((fn) => (
              <FunctionHelpRow key={fn.name} fn={fn} />
            ))}
          </ul>
        </section>

        {samples.length > 0 && (
          <section className="naming-help-section">
            <h3 className="naming-help-title text-body">{t("naming.samples")}</h3>
            <ul className="naming-help-list">
              {samples.map((sample) => (
                <li key={sample.ref} className="naming-help-row">
                  <span className="naming-help-sample-label text-caption">{sample.label}</span>
                  <code className="naming-help-sample-ref text-caption">{sample.ref}</code>
                  {sample.description && (
                    <p className="naming-help-description text-caption">{sample.description}</p>
                  )}
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
    </Dialog>
  );
}

function HelpFieldRow({ field }: { field: NamingCatalogField }) {
  const label = factLabel(field);
  const description = factDescription(field);
  return (
    <li className="naming-help-item">
      <div className="naming-help-row">
        <code className="naming-help-code text-caption">{"${"}{field.path}{"}"}</code>
        {label !== field.path && <span className="naming-help-field-label text-caption">{label}</span>}
        {field.phase === "execution" && <Chip>{t("templateFacts.phaseExecution")}</Chip>}
        {field.nullable && <Chip>{t("templateFacts.nullable")}</Chip>}
      </div>
      {(description || field.sample_value != null) && (
        <p className="naming-help-description text-caption">
          {description}
          {field.sample_value != null && `${description ? " " : ""}${t("naming.sampleValue", { value: field.sample_value })}`}
        </p>
      )}
    </li>
  );
}

function FunctionHelpRow({ fn }: { fn: NamingCatalogFunction }) {
  return (
    <li className="naming-help-row">
      <div className="naming-help-row">
        <code className="text-caption naming-help-function-signature">{fn.signature}</code>
        <span className="text-caption naming-help-function-arguments">{t("naming.arguments", { value: fn.arguments.join(", ") || t("naming.none") })}</span>
      </div>
      <p className="naming-help-description text-caption">{fn.description}</p>
      {fn.example && <code className="naming-help-function-example text-caption">{t("naming.functionExample", { value: fn.example })}</code>}
    </li>
  );
}

function TemplatePreview({
  kind,
  label,
  source,
  sampleRef,
  language,
}: {
  kind: NamingTemplateKind;
  label: string;
  source: string;
  sampleRef?: string;
  language: NamingTemplateValue["language"];
}) {
  const [preview, setPreview] = useState<NamingTemplatePreview>();
  const [error, setError] = useState<string>();
  const [pending, setPending] = useState(false);

  useEffect(() => {
    if (source.trim() === "") {
      setPreview(undefined);
      setError(undefined);
      setPending(false);
      return;
    }
    let cancelled = false;
    setPending(true);
    const timer = setTimeout(() => {
      previewNamingTemplate({
        template_kind: kind,
        template_source: source,
        sample_ref: sampleRef,
        language,
      })
        .then((result) => {
          if (cancelled) return;
          setPreview(result);
          setError(undefined);
        })
        .catch((cause: unknown) => {
          if (cancelled) return;
          setPreview(undefined);
          setError(toUserMessage(cause, t("naming.previewFailed")));
        })
        .finally(() => {
          if (!cancelled) setPending(false);
        });
    }, PREVIEW_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [kind, source, sampleRef, language]);

  if (source.trim() === "") return null;

  const locked = preview && (preview.locked_directory || preview.locked_basename || preview.locked_extension);
  const emptyFacts = emptyFactsNote(preview?.empty_facts);

  return (
    <div className="naming-preview">
      <div className="naming-preview-header">
        <span className="naming-preview-muted text-caption">{t("naming.previewOf", { label })}</span>
        {pending && <span className="naming-preview-muted text-caption">{t("naming.rendering")}</span>}
        {!pending && preview && (
          <StatePill tone={preview.valid ? "online" : "offline"}>
            {preview.valid ? t("naming.valid") : t("naming.invalid")}
          </StatePill>
        )}
      </div>

      {error && (
        <p className="naming-preview-error text-caption" role="alert">
          {error}
        </p>
      )}

      {preview && (
        <div className="naming-preview-body">
          {preview.valid && (
            <p className="naming-preview-value text-caption"><PreviewText text={preview.rendered} /></p>
          )}
          {emptyFacts && <p className="naming-preview-muted text-caption">{emptyFacts}</p>}
          {preview.valid && preview.composed_target && (
            <p className="naming-preview-muted text-caption">
              {t("naming.composedLabel")}
              <PreviewText text={preview.composed_target} />
            </p>
          )}
          {locked && (
            <div className="naming-preview-locked">
              <span className="naming-preview-locked-label text-caption">{t("naming.lockedNote")}</span>
              {preview.locked_directory && (
                <p className="naming-preview-muted text-caption">{t("naming.lockedDir", { value: preview.locked_directory })}</p>
              )}
              {preview.locked_basename && (
                <p className="naming-preview-muted text-caption">
                  {t("naming.lockedBaseLabel")}
                  <PreviewText text={preview.locked_basename} />
                </p>
              )}
              {preview.locked_extension && (
                <p className="naming-preview-muted text-caption">{t("naming.lockedExt", { value: preview.locked_extension })}</p>
              )}
            </div>
          )}
          {preview.errors.map((item, index) => (
            <p key={`e${index}`} className="naming-preview-error text-caption" role="alert">
              {namingPreviewErrorText(item.code, item.message)}
            </p>
          ))}
          {preview.warnings.map((item, index) => (
            <p key={`w${index}`} className="naming-preview-warning text-caption">
              {item.variable ? `${item.variable}: ` : ""}
              {item.message}
            </p>
          ))}
          <ResolvedValuesBlock preview={preview} />
        </div>
      )}
    </div>
  );
}

// Naming-result failures (NAMING_*) use their registered explanations, with empty values listed on their own line; syntax and other compile errors keep the server's original text (positions included).
function namingPreviewErrorText(code: string, message: string): string {
  return (code.startsWith("NAMING_") ? tryT(`error.${code}`) : undefined) ?? message;
}

// Echoes of resolver-resolved values: a secondary visual tier, collapsed by default. Same
//   degradation policy as locked_* — with resolved_values missing the section is not rendered at
//   all, never left as an empty shell.
function ResolvedValuesBlock({ preview }: { preview: NamingTemplatePreview }) {
  const [open, setOpen] = useState(false);
  const values = preview.resolved_values ?? [];

  if (values.length === 0) return null;

  return (
    <div className="naming-resolved">
      <button
        type="button"
        className="naming-resolved-toggle text-caption"
        aria-expanded={open}
        onClick={() => setOpen((prev) => !prev)}
      >
        <span>{t("naming.resolved", { n: values.length })}</span>
        <span>{open ? t("naming.collapse") : t("naming.expand")}</span>
      </button>

      {open && (
        <ul className="naming-resolved-list">
          {values.map((item) => (
            <ResolvedValueRow key={item.key} item={item} />
          ))}
        </ul>
      )}
    </div>
  );
}

const resolvedStateMeta = (): Record<string, { tone: "online" | "muted" | "warning"; label: string }> => ({
  value: { tone: "online", label: t("naming.resolvedState.value") },
  conflict: { tone: "warning", label: t("naming.resolvedState.conflict") },
});

function ResolvedValueRow({ item }: { item: NamingResolvedValue }) {
  const meta =
    item.state === null
      ? { tone: "muted" as const, label: t("naming.resolvedState.empty") }
      : (resolvedStateMeta()[item.state] ?? { tone: "muted" as const, label: t("naming.resolvedState.unknown") });
  const detail = [item.source && t("naming.source", { value: item.source }), item.evidence].filter(Boolean).join(" · ");

  return (
    <li className="naming-resolved-row" title={detail || undefined}>
      <code className="text-caption naming-resolved-key">{item.key}</code>
      <StatePill tone={meta.tone}>{meta.label}</StatePill>
      {item.value != null && <span className="naming-resolved-value text-caption">{item.value}</span>}
      {detail && <span className="naming-resolved-detail text-caption">{detail}</span>}
    </li>
  );
}

