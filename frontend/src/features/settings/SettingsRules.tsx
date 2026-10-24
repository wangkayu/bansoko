import { Alert, Button, HelpTip, Input } from "../../ui/base";
import { Fragment, useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil } from "lucide-react";
import { toast } from "../../ui/toast";
import {
  getParserPolicyDefault,
  isVersionConflict,
  listGlobalSections,
  listParserCatalog,
  putGlobalSection,
  saveParserPolicyDefault,
  toUserMessage,
  type GlobalSectionName,
  type GlobalSectionView,
} from "../../lib/api/client";
import { DescriptionList, ConflictBlock, StatePill, TagInput } from "../../ui";
import { configKeys, fileMaintenanceKeys } from "../../lib/queryKeys";
import { NamingTemplatesCard } from "./NamingTemplatesCard";
import { sectionSourceLabel } from "./sectionBadge";
import { TagRowEditor } from "../anime-forms/TagRowEditor";
import { ANIBT_ROW_VOCABULARY, titleRowVocabulary } from "../../lib/tagRowVocabulary";
import { TagRowBlock } from "../anime-forms/TagFilterCard";
import { DEFAULT_TAG_FILTER_RULES, tagRowSummary } from "../../lib/anibtTags";
import { titleTagLabel } from "../../lib/titleTags";
import { getTitleTagVocabulary, type TagFilterRow, type TagFilterRules } from "../../lib/api/client";
import { Select, type SelectOption, StateBlock } from "../../ui/base";
import { SettingsEditDialog } from "./SettingsEditDialog";
import { t } from "../../lib/i18n";
import { joinSentences } from "../../lib/i18n/format";
import { parserKindOptions, parserKindUsesPatterns, type ParserKindOption } from "../../lib/parserKind";
import { valuesEqual } from "../../lib/draft";
import { ParserKindChoice } from "../anime-forms/ParserKindChoice";

// Default rules: the global default sections (parser / release_rules / tag_filters / file_rules).
//   "After download completes" is a global value only, edited in the acquisition section.
//   These values write into global_config_sections and take effect immediately on save for every
//   anime without its own override.
//   Naming rules are the template library (NamingTemplatesCard): the default template is the global
//   naming.

const ruleSections = (): {
  name: GlobalSectionName;
  title: string;
  summary: (value: Record<string, unknown>) => { label: string; value: React.ReactNode }[];
}[] => [
  {
    name: "parser",
    title: t("settings.rules.parser"),
    summary: (v) => [
      { label: t("settings.rules.episodePatterns"), value: <RegexListSummary value={v.episode_patterns} emptyLabel={t("settings.rules.empty")} /> },
    ],
  },
  {
    name: "release_rules",
    title: t("settings.rules.releaseRules"),
    summary: (v) => [
      { label: t("settings.rules.titleMust"), value: <RegexListSummary value={v.must_contain} emptyLabel={t("settings.rules.unlimited")} /> },
      { label: t("settings.rules.titleNot"), value: <RegexListSummary value={v.not_in} emptyLabel={t("settings.rules.unlimited")} /> },
    ],
  },
  {
    name: "tag_filters",
    title: t("tagFilters.title"),
    summary: (v) => [
      { label: t("anibtTags.title"), value: tagRowSummary(tagFilterRules(v).anibt) },
      { label: t("titleTags.title"), value: tagRowSummary(tagFilterRules(v).title, titleTagLabel) },
    ],
  },
  {
    name: "file_rules",
    title: t("settings.rules.fileRules"),
    summary: (v) => [
      { label: t("settings.rules.fileMust"), value: <RegexListSummary value={v.must_contain} emptyLabel={t("settings.rules.unlimited")} /> },
      { label: t("settings.rules.fileNot"), value: <RegexListSummary value={v.not_in} emptyLabel={t("settings.rules.unlimited")} /> },
    ],
  },
];

export function SettingsRules() {
  const queryClient = useQueryClient();
  const sections = useQuery({ queryKey: configKeys.globalSections, queryFn: listGlobalSections });
  // The default parser (the global creation template) and the "episode rules" section share one
//   card: the parser decides whether episode recognition rules matter at all, so they are edited
//   together and saved once (parser first, then the rules).
  const parserDefault = useQuery({ queryKey: fileMaintenanceKeys.parserPolicyDefault, queryFn: getParserPolicyDefault });
  const parserCatalog = useQuery({ queryKey: configKeys.parserCatalog, queryFn: listParserCatalog, staleTime: Infinity });
  const parserOptions = parserKindOptions(parserCatalog.data, parserDefault.data?.parser_kind);
  const [editing, setEditing] = useState<GlobalSectionView>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();

  function invalidate() {
    return queryClient.invalidateQueries({ queryKey: configKeys.globalSections });
  }

  async function save(view: GlobalSectionView, value: Record<string, unknown>, parserKind?: string) {
    setBusy(true);
    setError(undefined);
    setConflict(undefined);
    const current = parserDefault.data;
    const kindChanged = view.section_name === "parser" && parserKind !== undefined && current !== undefined && parserKind !== current.parser_kind;
    const valueChanged = view.section_name !== "parser" || !valuesEqual(value, view.value);
    try {
      if (kindChanged) {
        const stored = await saveParserPolicyDefault({ expected_version: current.version, parser_kind: parserKind });
        queryClient.setQueryData(fileMaintenanceKeys.parserPolicyDefault, stored);
      }
      if (valueChanged) {
        await putGlobalSection(view.section_name, { schema_version: view.schema_version, value }, view.version);
      }
      toast.success(t("settings.rules.saved", { title: sectionTitle(view.section_name) }));
      setEditing(undefined);
      await invalidate();
    } catch (cause) {
      if (isVersionConflict(cause)) {
        setConflict(cause.currentVersion);
      }
      setError(toUserMessage(cause, t("settings.rules.saveFailed")));
    } finally {
      setBusy(false);
    }
  }

  // On a 409 refresh: load the server's latest section (the rules card together with the default parser), discard the stale local form draft, and the user re-edits.
  async function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    const fresh = await queryClient.fetchQuery({ queryKey: configKeys.globalSections, queryFn: listGlobalSections });
    await queryClient.fetchQuery({ queryKey: fileMaintenanceKeys.parserPolicyDefault, queryFn: getParserPolicyDefault });
    setEditing((current) => {
      if (!current) return current;
      return fresh.find((section) => section.section_name === current.section_name) ?? undefined;
    });
  }

  const ruleViews = (sections.data ?? []).filter((s) =>
    ruleSections().some((r) => r.name === s.section_name),
  );

  return (
    <div className="settings-shared-stack">
      <section className="settings-shared-grid">
        <div className="settings-shared-header">
          <h2 className="settings-shared-title text-title">{t("settings.rules.title")}</h2>
          <span className="settings-shared-muted text-caption">{t("settings.rules.description")}</span>
        </div>

        {sections.isLoading && (
          <StateBlock kind="busy" title={t("settings.rules.loading")} />
        )}
        {sections.error && <Alert tone="error" title={toUserMessage(sections.error, t("settings.rules.loadFailed"))} />}

        <ul className="settings-shared-list">
          {ruleSections().map((meta) => {
            const view = ruleViews.find((s) => s.section_name === meta.name);
            if (!view) return null;
            return (
              <Fragment key={meta.name}>
                <li className="settings-shared-card">
                  <div className="settings-shared-header">
                    <h3 className="settings-shared-card-title text-body">{meta.title}</h3>
                    <div className="settings-shared-inline">
                      <span className="settings-shared-muted text-badge">
                        {sectionSourceLabel(view)}
                      </span>
                      <Button variant="ghost" size="sm" onClick={() => { setEditing(view); setError(undefined); setConflict(undefined); }}>
                        <Pencil size={14} />
                        {t("settings.rules.edit")}
                      </Button>
                    </div>
                  </div>
                  <div className="settings-shared-offset">
                    {meta.name === "parser" ? (
                      <ParserSummary
                        value={view.value}
                        kind={parserDefault.data?.parser_kind}
                        options={parserOptions}
                        loading={parserDefault.isLoading}
                        error={parserDefault.error ?? parserCatalog.error}
                      />
                    ) : (
                      <DescriptionList items={meta.summary(view.value)} />
                    )}
                  </div>
                </li>
              </Fragment>
            );
          })}
          <NamingTemplatesCard />
        </ul>
      </section>

      {editing && (
        <SectionEditDialog
          key={`${editing.section_name}:${editing.version}`}
          view={editing}
          busy={busy}
          error={error}
          conflict={conflict}
          onRefreshConflict={() => void refreshAfterConflict()}
          onClose={() => setEditing(undefined)}
          onSave={(value, parserKind) => void save(editing, value, parserKind)}
          parser={{ kind: parserDefault.data?.parser_kind, options: parserOptions }}
        />
      )}
    </div>
  );
}

function sectionTitle(name: string): string {
  return ruleSections().find((s) => s.name === name)?.title ?? name;
}

// --- edit dialog (a different form per section type) -------------------------------

function SectionEditDialog({
  view,
  busy,
  error,
  conflict,
  onRefreshConflict,
  onClose,
  onSave,
  parser,
}: {
  view: GlobalSectionView;
  busy: boolean;
  error?: string;
  conflict?: number;
  onRefreshConflict: () => void;
  onClose: () => void;
  onSave: (value: Record<string, unknown>, parserKind?: string) => void;
  /** The episode rules card: the default parser's current value and options (edited together with the recognition rules). */
  parser: { kind?: string; options: ParserKindOption[] };
}) {
  const title = sectionTitle(view.section_name);
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    setDirty(false);
  }, [view.section_name, view.version]);

  return (
    <SettingsEditDialog
      open
      onOpenChange={(o) => { if (!o) onClose(); }}
      title={t("settings.rules.editTitle", { title })}
      dirty={dirty}
      footer={(requestClose) => (
        <>
          <Button variant="ghost" disabled={busy} onClick={requestClose}>{t("settings.rules.cancel")}</Button>
          <Button variant="primary" aria-label={busy ? t("settings.rules.saving") : t("settings.rules.save")} disabled={busy} form="section-edit-form" type="submit">
            {busy ? t("settings.rules.saving") : t("settings.rules.save")}
          </Button>
        </>
      )}
    >
      {conflict !== undefined ? (
        <ConflictBlock currentVersion={conflict} message={error} onRefresh={onRefreshConflict} />
      ) : (
        error && <Alert tone="error" title={error} />
      )}
      <div onInput={() => setDirty(true)} onChange={() => setDirty(true)}>
        {view.section_name === "parser" && <ParserForm value={view.value} busy={busy} onSave={onSave} parser={parser} onDirty={() => setDirty(true)} />}
        {view.section_name === "release_rules" && <ReleaseRulesForm value={view.value} busy={busy} onSave={onSave} />}
        {view.section_name === "file_rules" && <FileRulesForm value={view.value} busy={busy} onSave={onSave} />}
        {view.section_name === "tag_filters" && <TagFiltersForm value={view.value} busy={busy} onSave={onSave} onDirty={() => setDirty(true)} />}
      </div>
    </SettingsEditDialog>
  );
}

// --- Parser ---

function ParserForm({
  value,
  busy,
  onSave,
  parser,
  onDirty,
}: Omit<FormProps, "onSave"> & {
  onSave: (value: Record<string, unknown>, parserKind?: string) => void;
  parser: { kind?: string; options: ParserKindOption[] };
  onDirty: () => void;
}) {
  const [patterns, setPatterns] = useState(toList(value.episode_patterns));
  const [kind, setKind] = useState(parser.kind);
  // Only the regex parser uses the episode recognition rules; with reltitle the rules are kept but hidden and take no part in parsing.
  const usesPatterns = parserKindUsesPatterns(kind);

  function submit(e: React.FormEvent) {
    e.preventDefault();
    onSave({ episode_patterns: patterns }, kind);
  }

  return (
    <form id="section-edit-form" onSubmit={submit} className="settings-shared-grid">
      {kind !== undefined && (
        <>
          <ParserKindChoice
            label={t("settings.rules.defaultParser.kindLabel")}
            value={kind}
            options={parser.options}
            onChange={(next) => {
              setKind(next);
              onDirty();
            }}
            readOnly={busy}
          />
          <p className="text-caption text-muted">{t("settings.rules.defaultParser.help")}</p>
        </>
      )}
      {usesPatterns ? (
        <Field label={t("settings.rules.episodePatterns")} help={{ label: t("settings.rules.episodePatternsHelpLabel"), body: t("settings.rules.episodePatternsHelp") }}>
          <TagInput ariaLabel={t("settings.rules.episodePatterns")} value={patterns} onChange={setPatterns} validateItem={validateRegex} />
        </Field>
      ) : (
        <p className="text-caption text-muted">{t("forms.rules.episodePatternsUnused")}</p>
      )}
    </form>
  );
}

// The episode rules card's summary: parser first, with the recognition rules listed only under the regex parser.
function ParserSummary({
  value,
  kind,
  options,
  loading,
  error,
}: {
  value: Record<string, unknown>;
  kind?: string;
  options: ParserKindOption[];
  loading: boolean;
  error: unknown;
}) {
  const kindLabel = kind === undefined ? undefined : (options.find((option) => option.value === kind)?.label ?? kind);
  const items: { label: string; value: React.ReactNode }[] = [];
  if (kindLabel !== undefined) items.push({ label: t("settings.rules.defaultParser.kindLabel"), value: kindLabel });
  items.push({
    label: t("settings.rules.episodePatterns"),
    value: parserKindUsesPatterns(kind)
      ? <RegexListSummary value={value.episode_patterns} emptyLabel={t("settings.rules.empty")} />
      : t("forms.rules.episodePatternsUnused"),
  });
  return (
    <>
      {loading && <StatePill busy>{t("settings.rules.loading")}</StatePill>}
      {error != null && <StatePill tone="offline">{toUserMessage(error, t("settings.rules.defaultParser.loadFailed"))}</StatePill>}
      <DescriptionList items={items} />
      {kind !== undefined && <p className="text-caption text-muted">{t("settings.rules.defaultParser.help")}</p>}
    </>
  );
}

// Each episode regex is validated as it is typed (the regex validation hook).
function validateRegex(tag: string): string | null {
  try {
    new RegExp(tag);
    return null;
  } catch {
    return t("settings.rules.invalidRegex");
  }
}

// --- Release filter rules ---

function RegexRulesForm({ value, onSave, kind }: FormProps & { kind: "title" | "file" }) {
  const [must, setMust] = useState(String(value.must_contain ?? ""));
  const [exclude, setExclude] = useState(String(value.not_in ?? ""));
  const mustLabel = t(kind === "file" ? "settings.rules.fileMust" : "settings.rules.titleMust");
  const notLabel = t(kind === "file" ? "settings.rules.fileNot" : "settings.rules.titleNot");
  const help = {
    label: t("settings.rules.regexHelpLabel"),
    body: kind === "file" ? joinSentences(t("settings.rules.regexHelp"), t("settings.rules.fileRegexHelp")) : t("settings.rules.regexHelp"),
  };
  return <form id="section-edit-form" onSubmit={(e) => { e.preventDefault(); onSave({ must_contain: must, not_in: exclude }); }} className="settings-shared-grid">
    <Field label={mustLabel} help={help}><Input aria-label={mustLabel} value={must} onChange={(e) => setMust(e.target.value)} /></Field>
    <Field label={notLabel}><Input aria-label={notLabel} value={exclude} onChange={(e) => setExclude(e.target.value)} /></Field>
  </form>;
}
const ReleaseRulesForm = (props: FormProps) => <RegexRulesForm {...props} kind="title" />;
const FileRulesForm = (props: FormProps) => <RegexRulesForm {...props} kind="file" />;

// --- Tag filters (AniBT row + title-parse row) ---

function TagFiltersForm({ value, onSave, onDirty }: FormProps & { onDirty: () => void }) {
  const current = tagFilterRules(value);
  const [anibt, setAniBT] = useState<TagFilterRow>(current.anibt);
  const [title, setTitle] = useState<TagFilterRow>(current.title);
  const vocabulary = useQuery({ queryKey: configKeys.titleTagVocabulary, queryFn: getTitleTagVocabulary, staleTime: Infinity });
  return (
    <form id="section-edit-form" onSubmit={(e) => { e.preventDefault(); onSave({ anibt, title }); }} className="settings-shared-grid">
      <p className="tw:text-xs tw:text-muted-foreground">{t("tagFilters.hint")}</p>
      <TagRowBlock heading={t("anibtTags.title")} hint={t("anibtTags.hint")}>
        <TagRowEditor vocabulary={ANIBT_ROW_VOCABULARY} value={anibt} onChange={(next) => { setAniBT(next); onDirty(); }} />
      </TagRowBlock>
      <TagRowBlock heading={t("titleTags.title")} hint={t("titleTags.hint")}>
        {vocabulary.isError ? (
          <p className="tw:text-xs tw:text-destructive">{t("titleTags.unavailable")}</p>
        ) : vocabulary.data ? (
          <TagRowEditor vocabulary={titleRowVocabulary(vocabulary.data)} value={title} onChange={(next) => { setTitle(next); onDirty(); }} />
        ) : (
          <p className="tw:text-xs tw:text-muted-foreground">{t("settings.rules.loading")}</p>
        )}
      </TagRowBlock>
    </form>
  );
}

function tagFilterRules(value: Record<string, unknown>): TagFilterRules {
  const rules = value as Partial<TagFilterRules>;
  return {
    anibt: rules.anibt ?? DEFAULT_TAG_FILTER_RULES.anibt,
    title: rules.title ?? DEFAULT_TAG_FILTER_RULES.title,
  };
}

// --- Shared ---

function Field({
  label,
  help,
  children,
}: {
  label: React.ReactNode;
  help?: { label: string; body: string };
  children: React.ReactNode;
}) {
  return (
    <div className="settings-shared-field text-caption">
      <span className="tw:inline-flex tw:items-center tw:gap-1">
        {label}
        {help && <HelpTip label={help.label}>{help.body}</HelpTip>}
      </span>
      {children}
    </div>
  );
}

type FormProps = {
  value: Record<string, unknown>;
  busy: boolean;
  onSave: (value: Record<string, unknown>) => void;
};

function nestedString(v: Record<string, unknown>, path: string[]): string {
  let current: unknown = v;
  for (const key of path) {
    if (current == null || typeof current !== "object") return "";
    current = (current as Record<string, unknown>)[key];
  }
  return typeof current === "string" ? current : "";
}

function toList(v: unknown): string[] {
  if (!Array.isArray(v)) return [];
  return v.filter((item): item is string => typeof item === "string");
}

// --- regex list summary -------------------------------------------------------------
//
// The rules are regexes; comma-joining them into one line is unreadable and collides with commas
//   inside the patterns themselves: one per line, monospace; beyond three, first show "n rules"
//   and list them all once expanded.
const REGEX_PREVIEW_LIMIT = 3;

function RegexListSummary({ value, emptyLabel }: { value: unknown; emptyLabel: string }) {
  // A single regex stores as a string (must-include / exclude lists); multiple rules store as an array (the episode recognition rules).
  const list = typeof value === "string" ? (value.trim() === "" ? [] : [value]) : toList(value);
  const [expanded, setExpanded] = useState(false);
  if (list.length === 0) return <>{emptyLabel}</>;
  const collapsed = list.length > REGEX_PREVIEW_LIMIT && !expanded;
  return (
    <div className="tw:grid tw:min-w-0 tw:gap-1">
      {collapsed ? null : (
        <ul className="tw:m-0 tw:grid tw:list-none tw:gap-0.5 tw:p-0">
          {list.map((pattern, index) => (
            <li key={`${index}:${pattern}`} className="tw:font-mono tw:text-xs tw:break-all">
              {pattern}
            </li>
          ))}
        </ul>
      )}
      {list.length > REGEX_PREVIEW_LIMIT && (
        <span className="tw:inline-flex tw:items-center tw:gap-2">
          {collapsed && <span>{t("settings.rules.patternCount", { n: list.length })}</span>}
          <Button variant="ghost" size="sm" aria-expanded={expanded} onClick={() => setExpanded((current) => !current)}>
            {expanded ? t("settings.rules.patternCollapse") : t("settings.rules.patternExpand")}
          </Button>
        </span>
      )}
    </div>
  );
}
