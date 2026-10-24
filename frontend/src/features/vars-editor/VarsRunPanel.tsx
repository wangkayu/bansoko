import { Alert, Checkbox, Input, Select, SpinnerIcon, StatePill, type StatePillTone } from "../../ui/base";
import type {
  NamingVarsDryRunResult,
  NamingVarsDryRunTargetResult,
  NamingVarsReleaseOption,
  NamingVarsSample,
} from "../../lib/api/client";
import { t, tryT } from "../../lib/i18n";
import { problemText, type DryRunSelection } from "./varsEditor";

// The dry-run panel: pick a release to dry-run and see the vars.* variables each release yields.
//   Results come from the same source as what gets frozen when a release is accepted after saving:
//   the same input construction and the same run rules.

export type DryRunView = {
  /** The draft this result corresponds to (normalized). */
  source: string;
  result?: NamingVarsDryRunResult;
  error?: string;
  pending: boolean;
};

const STATUS_TONE: Record<NamingVarsDryRunTargetResult["status"], StatePillTone> = {
  ok: "online",
  error: "offline",
  interrupted: "warning",
  unavailable: "muted",
};

function sampleLabel(ref: string): string {
  return tryT(`varsEditor.run.sample.${ref}`) ?? ref;
}

function targetLabel(target: NamingVarsDryRunTargetResult): string {
  if (target.kind === "sample") return sampleLabel(target.ref);
  if (target.kind === "custom") return t("varsEditor.run.custom");
  return target.title || target.ref;
}

export function VarsRunPanel({
  samples,
  releases,
  releasesFailed,
  selection,
  onSelectionChange,
  view,
  draft,
}: {
  samples: readonly NamingVarsSample[];
  releases: readonly NamingVarsReleaseOption[];
  releasesFailed: boolean;
  selection: DryRunSelection;
  onSelectionChange: (next: DryRunSelection) => void;
  view: DryRunView;
  draft: string;
}) {
  const fresh = view.source === draft;
  const result = view.result;
  const releaseOptions = [
    { value: "", label: t("varsEditor.run.releaseNone") },
    ...releases.map((release) => ({ value: release.id, label: release.anime_title ? `${release.anime_title} · ${release.title}` : release.title })),
  ];
  const nothingSelected = selection.samples.length === 0 && !selection.releaseId && selection.customTitle.trim() === "";
  const staticFailure = fresh && result !== undefined && result.diagnostics.length > 0;

  return (
    <div className="tw:grid tw:min-w-0 tw:gap-3" data-testid="vars-run-panel">
      <p className="naming-muted text-caption">{t("varsEditor.run.hint")}</p>

      <fieldset className="tw:m-0 tw:grid tw:min-w-0 tw:gap-1.5 tw:border-0 tw:p-0">
        <legend className="tw:mb-1 tw:p-0 tw:text-xs tw:text-muted-foreground">{t("varsEditor.run.samples")}</legend>
        {samples.map((sample) => (
          <Checkbox
            key={sample.ref}
            size="sm"
            checked={selection.samples.includes(sample.ref)}
            onCheckedChange={(checked) =>
              onSelectionChange({
                ...selection,
                samples: checked ? [...selection.samples, sample.ref] : selection.samples.filter((ref) => ref !== sample.ref),
              })
            }
          >
            <span title={t("varsEditor.run.sampleDetail", { title: sample.release_title, count: sample.file_count })}>{sampleLabel(sample.ref)}</span>
          </Checkbox>
        ))}
      </fieldset>

      <div className="tw:grid tw:min-w-0 tw:gap-1">
        {releasesFailed ? (
          <span className="tw:text-xs tw:text-destructive">{t("varsEditor.run.releasesFailed")}</span>
        ) : (
          <Select
            label={t("varsEditor.run.releaseLabel")}
            value={selection.releaseId}
            options={releaseOptions}
            onChange={(releaseId) => onSelectionChange({ ...selection, releaseId })}
          />
        )}
      </div>

      <label className="tw:grid tw:min-w-0 tw:gap-1">
        <span className="tw:text-xs tw:text-muted-foreground">{t("varsEditor.run.custom")}</span>
        <Input
          controlSize="sm"
          value={selection.customTitle}
          maxLength={1024}
          placeholder={t("varsEditor.run.customPlaceholder")}
          onChange={(event) => onSelectionChange({ ...selection, customTitle: event.target.value })}
        />
      </label>

      <div aria-live="polite" className="tw:grid tw:min-w-0 tw:gap-2">
        {view.pending && (
          <div role="status" className="tw:flex tw:items-center tw:gap-1.5 tw:text-xs tw:text-muted-foreground">
            <SpinnerIcon />
            {t("varsEditor.run.pending")}
          </div>
        )}
        {view.error && <Alert tone="error" title={view.error} />}
        {nothingSelected && <p className="naming-muted text-caption">{t("varsEditor.run.nothingSelected")}</p>}
        {staticFailure && <p className="naming-muted text-caption">{t("varsEditor.run.draftInvalid")}</p>}
        {fresh && result?.targets.map((target, index) => (
          <TargetCard key={`${target.kind}:${target.ref}:${index}`} target={target} draft={draft} />
        ))}
      </div>
    </div>
  );
}

function TargetCard({ target, draft }: { target: NamingVarsDryRunTargetResult; draft: string }) {
  const groups = Object.entries(target.output).sort(([a], [b]) => a.localeCompare(b));
  const count = groups.reduce((total, [, values]) => total + Object.keys(values).length, 0);
  const diagnostic = target.diagnostics[0];
  return (
    <section className="tw:grid tw:min-w-0 tw:gap-1.5 tw:rounded-control tw:border tw:border-border tw:p-2" data-status={target.status}>
      <header className="tw:flex tw:min-w-0 tw:items-center tw:justify-between tw:gap-2">
        <h4 className="tw:m-0 tw:min-w-0 tw:truncate tw:text-sm tw:font-medium" title={target.title}>{targetLabel(target)}</h4>
        <StatePill tone={STATUS_TONE[target.status]}>{tryT(`varsEditor.run.status.${target.status}`) ?? target.status}</StatePill>
      </header>
      {target.kind !== "sample" && target.title && <p className="tw:m-0 tw:truncate tw:text-xs tw:text-muted-foreground" title={target.title}>{target.title}</p>}
      {target.status === "ok" && (
        <>
          <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("varsEditor.run.outputCount", { n: count })}</p>
          <dl className="tw:m-0 tw:grid tw:gap-0.5 tw:font-mono tw:text-xs">
            {groups.flatMap(([group, values]) =>
              Object.entries(values)
                .sort(([a], [b]) => a.localeCompare(b))
                .map(([key, value]) => (
                  <div key={`${group}.${key}`} className="tw:flex tw:min-w-0 tw:gap-2">
                    <dt className="tw:shrink-0 tw:text-muted-foreground">{`vars.${group}.${key}`}</dt>
                    <dd className="tw:m-0 tw:min-w-0 tw:break-words">{value === "" ? <span className="tw:text-muted-foreground">{t("varsEditor.run.empty")}</span> : value}</dd>
                  </div>
                )),
            )}
          </dl>
        </>
      )}
      {(target.status === "error" || target.status === "interrupted") && diagnostic && (
        <p className="tw:m-0 tw:break-words tw:text-xs tw:text-destructive">{problemText(draft, diagnostic)}</p>
      )}
      {target.status === "unavailable" && target.reason && (
        <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{tryT(`varsEditor.run.reason.${target.reason}`) ?? target.reason}</p>
      )}
    </section>
  );
}
