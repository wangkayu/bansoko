import { Alert, Button, Checkbox, Chip, Input, Select, StatePill, StateBlock } from "../../ui/base";
import { useState } from "react";
import { valuesEqual } from "../../lib/draft";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useBoundedRefresh } from "@/lib/events/useBoundedRefresh";
import { Pencil, Zap } from "lucide-react";
import { toast } from "../../ui/toast";
import {
  isVersionConflict,
  listAutomationSchedules,
  listConnections,
  getAutomationRun,
  newUUID,
  patchAutomationSchedule,
  runAutomation,
  toUserMessage,
  type AutomationIntervalUnit,
  type AutomationKind,
  type AutomationState,
  type AutomationSchedule,
  type AutomationSchedulePatch,
} from "../../lib/api/client";
import { formatDateTime } from "../../lib/labels";
import { automationKeys, configKeys, observabilityKeys } from "../../lib/queryKeys";
import { SettingsEditDialog } from "./SettingsEditDialog";
import { t } from "../../lib/i18n";

// Automation tasks: four fixed cards (subgroup discovery / RSS refresh / automatic metadata
//   matching / catalog refresh). Each card shows only enable/disable, interval, run-now, the last
//   outcome and a next-run summary; no long execution-record list exists — routine/automation
//   execution evidence stays internal, WorkItem is not exposed as ordinary user navigation, and the
//   aggregated failure signal is covered by system diagnostics.

const AUTOMATION_KIND_LABEL = (): Record<AutomationKind, string> => ({
  group_discovery: t("nav.automationKind.group_discovery"),
  rss_refresh: t("nav.automationKind.rss_refresh"),
  metadata_match: t("nav.automationKind.metadata_match"),
  catalog_refresh: t("nav.automationKind.catalog_refresh"),
});

// The last schedule dispatch outcome: queued/running are transient in-flight states, completed/partial/failed are terminal.
const lastRunOutcomeLabel = (): Partial<Record<AutomationState, string>> => ({
  queued: t("settings.automation.lastOutcome.queued"),
  running: t("settings.automation.lastOutcome.running"),
  completed: t("settings.automation.lastOutcome.completed"),
  partial: t("settings.automation.lastOutcome.partial"),
  failed: t("settings.automation.lastOutcome.failed"),
});

const lastRunOutcomeColor: Partial<Record<AutomationState, "muted" | "info" | "online" | "warning" | "offline">> = {
  queued: "muted",
  running: "info",
  completed: "online",
  partial: "warning",
  failed: "offline",
};

export function SettingsAutomation() {
  useBoundedRefresh([automationKeys.schedules, configKeys.connections]);
  return (
    <div className="settings-automation">
      <SchedulesCard />
    </div>
  );
}

type ScheduleDraft = {
  enabled: boolean;
  interval_value: string;
  interval_unit: AutomationIntervalUnit;
};

function scheduleDraftFrom(item: AutomationSchedule): ScheduleDraft {
  return {
    enabled: item.enabled,
    interval_value: String(item.interval_value),
    interval_unit: item.interval_unit,
  };
}

function intervalError(kind: AutomationKind, value: number, unit: AutomationIntervalUnit): string | undefined {
  const minutes = value * (unit === "minute" ? 1 : unit === "hour" ? 60 : 1440);
  const minimum = kind === "rss_refresh" ? 5 : 60;
  if (minutes < minimum || minutes > 7 * 24 * 60) {
    return kind === "rss_refresh" ? t("settings.automation.intervalRssRange") : t("settings.automation.intervalOtherRange");
  }
  return undefined;
}

async function waitForAutomationRun(runID: string): Promise<AutomationState> {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    const run = await getAutomationRun(runID);
    if (run.state !== "queued" && run.state !== "running") return run.state;
    await new Promise((resolve) => window.setTimeout(resolve, 500));
  }
  return "running";
}

function SchedulesCard() {
  const queryClient = useQueryClient();
  const schedules = useQuery({ queryKey: automationKeys.schedules, queryFn: listAutomationSchedules });
  // Automatic metadata matching depends on the TMDB connection's API Key; reuse the connections card's query cache, showing a read-only hint with no action.
  const connections = useQuery({ queryKey: configKeys.connections, queryFn: listConnections });
  const tmdbCredentialMissing =
    connections.data !== undefined &&
    !connections.data.some((item) => item.kind === "tmdb" && item.credential_configured);
  const [editing, setEditing] = useState<AutomationSchedule>();
  const [draft, setDraft] = useState<ScheduleDraft>();
  const [savedDraft, setSavedDraft] = useState<ScheduleDraft>();
  const [busy, setBusy] = useState(false);
  const [running, setRunning] = useState<Set<AutomationKind>>(new Set());
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();

  function invalidateSchedules() {
    return queryClient.invalidateQueries({ queryKey: automationKeys.schedules });
  }

  async function save(item: AutomationSchedule) {
    if (!draft) return;
    const value = Number.parseInt(draft.interval_value, 10);
    if (!Number.isFinite(value) || value < 1) {
      setError(t("settings.automation.intervalInvalid"));
      return;
    }
    const boundsError = intervalError(item.kind, value, draft.interval_unit);
    if (boundsError) {
      setError(boundsError);
      return;
    }
    const patch: AutomationSchedulePatch = {
      expected_version: item.version,
      enabled: draft.enabled,
      interval_value: value,
      interval_unit: draft.interval_unit,
    };
    setBusy(true);
    setError(undefined);
    setConflict(undefined);
    try {
      await patchAutomationSchedule(item.kind, patch);
      toast.success(t("settings.automation.saved"));
      setEditing(undefined);
      setDraft(undefined);
      setSavedDraft(undefined);
      await invalidateSchedules();
    } catch (cause) {
      if (isVersionConflict(cause)) {
        setConflict(cause.currentVersion);
      }
      setError(toUserMessage(cause, t("settings.automation.saveFailed")));
    } finally {
      setBusy(false);
    }
  }

  // On a 409 refresh: load the server's latest schedule, discard the stale local form draft, and the user re-edits.
  async function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    const fresh = await queryClient.fetchQuery({ queryKey: automationKeys.schedules, queryFn: listAutomationSchedules });
    setEditing((current) => {
      if (!current) return current;
      const latest = fresh.find((schedule) => schedule.kind === current.kind);
      if (latest) {
        const latestDraft = scheduleDraftFrom(latest);
        setDraft(latestDraft);
        setSavedDraft(latestDraft);
      }
      return latest;
    });
  }

  async function runNow(kind: AutomationKind) {
    setRunning((prev) => new Set(prev).add(kind));
    try {
      const accepted = await runAutomation(kind, newUUID());
      const state = await waitForAutomationRun(accepted.run_id);
      const name = AUTOMATION_KIND_LABEL()[kind] ?? kind;
      if (state === "completed") toast.success(t("settings.automation.runDone", { name }));
      else if (state === "partial") toast.warning(t("settings.automation.runPartial", { name }));
      else if (state === "failed") toast.error(t("settings.automation.runFailed", { name }));
      else toast.info(t("settings.automation.triggered"));
      await invalidateSchedules();
      await queryClient.invalidateQueries({ queryKey: observabilityKeys.records });
      await queryClient.invalidateQueries({ queryKey: observabilityKeys.diagnostics });
    } catch (cause) {
      toast.error(toUserMessage(cause, t("settings.automation.runNowFailed")));
    } finally {
      setRunning((prev) => {
        const next = new Set(prev);
        next.delete(kind);
        return next;
      });
    }
  }

  return (
    <section className="settings-automation-section">
      <div className="settings-automation-header">
        <h2 className="settings-automation-title text-title">{t("settings.automation.title")}</h2>
        <span className="settings-automation-muted text-caption">{t("settings.automation.subtitle")}</span>
      </div>

      {schedules.isLoading && (
        <StateBlock kind="busy" title={t("settings.automation.loading")} />
      )}
      {schedules.error && <Alert tone="error" title={toUserMessage(schedules.error, t("settings.automation.loadFailed"))} />}
      {schedules.data && schedules.data.length === 0 && <StateBlock kind="empty" title={t("settings.automation.empty")} />}

      <ul className="settings-automation-list">
        {(schedules.data ?? []).map((item) => (
          <li key={item.kind} className="settings-automation-item">
            {/* Three parts: title and labels, time info, right-aligned actions; on desktop the actions column spans two rows at the right, on phones it lands on the last row, right-aligned. */}
            <div className="tw:grid tw:grid-cols-[minmax(0,1fr)_auto] tw:items-center tw:gap-x-3 tw:gap-y-1.5 tw:phone:grid-cols-1">
              <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-2">
                <span className="settings-automation-item-title text-body">{AUTOMATION_KIND_LABEL()[item.kind] ?? item.kind}</span>
                <StatePill tone={item.enabled ? "online" : "muted"}>{item.enabled ? t("common.enabled") : t("common.disabled")}</StatePill>
                {item.kind === "metadata_match" && tmdbCredentialMissing && <StatePill tone="warning">{t("settings.automation.missingTag")}</StatePill>}
                <Chip>
                  {t("settings.automation.every", { n: item.interval_value, unit: t(`settings.automation.unit.${item.interval_unit}`) })}
                </Chip>
                {item.last_run_outcome && (
                  <StatePill tone={lastRunOutcomeColor[item.last_run_outcome.state]}>
                    {lastRunOutcomeLabel()[item.last_run_outcome.state] ?? item.last_run_outcome.state}
                  </StatePill>
                )}
              </div>
              <div className="tw:row-span-2 tw:flex tw:items-center tw:justify-end tw:gap-2 tw:phone:row-span-1 tw:phone:row-start-3">
                <Button variant="ghost" size="sm" onClick={() => {
                    const next = scheduleDraftFrom(item);
                    setEditing(item);
                    setDraft(next);
                    setSavedDraft(next);
                    setError(undefined);
                    setConflict(undefined);
                  }}>
                  <Pencil size={14} />
                  {t("settings.automation.edit")}
                </Button>
                <Button size="sm" loading={running.has(item.kind)} onClick={() => void runNow(item.kind)}>
                  <Zap size={14} />
                  {running.has(item.kind) ? t("settings.automation.running") : t("settings.automation.runNow")}
                </Button>
              </div>
              {((item.kind === "metadata_match" && tmdbCredentialMissing) || item.next_run_at || item.last_dispatch_at) && (
                <p className="settings-automation-muted tw:m-0 tw:flex tw:min-w-0 tw:flex-wrap tw:gap-x-3 tw:gap-y-0.5 text-caption">
                  {item.kind === "metadata_match" && tmdbCredentialMissing && <span>{t("settings.automation.missingTmdbKey")}</span>}
                  {item.next_run_at && <span>{t("settings.automation.next", { time: formatDateTime(item.next_run_at) })}</span>}
                  {item.last_dispatch_at && <span>{t("settings.automation.last", { time: formatDateTime(item.last_dispatch_at) })}</span>}
                </p>
              )}
            </div>
          </li>
        ))}
      </ul>

      {editing && draft && (
        <SettingsEditDialog
          open
          onOpenChange={(o) => { if (!o) { setEditing(undefined); setDraft(undefined); setSavedDraft(undefined); } }}
          title={t("settings.automation.editTitle", { name: AUTOMATION_KIND_LABEL()[editing.kind] ?? editing.kind })}
          description={t("settings.automation.editDescription")}
          dirty={() => !!draft && !valuesEqual(draft, savedDraft)}
          footer={(requestClose) => (
            <>
              <Button variant="ghost" disabled={busy} onClick={requestClose}>
                {t("settings.automation.cancel")}
              </Button>
              <Button variant="primary" loading={busy} onClick={() => void save(editing)}>
                {busy ? t("settings.automation.saving") : t("settings.automation.save")}
              </Button>
            </>
          )}
        >
          {conflict !== undefined ? (
            <Alert tone="error" title={t("common.versionConflict")} description={
                <>
                  {error && <div>{error}</div>}
                  <div>{t("common.conflictHint")}</div>
                </>
              } action={<Button size="sm" onClick={() => void refreshAfterConflict()}>{t("common.refreshLatest")}</Button>} />
          ) : (
            error && <Alert tone="error" title={error} />
          )}
          <div className="settings-automation-edit-toggle">
            <Checkbox checked={draft.enabled} onCheckedChange={(checked) => setDraft({ ...draft, enabled: checked })}>
              {t("settings.automation.enable")}
            </Checkbox>
          </div>
          <div className="settings-automation-edit-fields">
            <label className="settings-automation-edit-field text-caption">
              <span>{t("settings.automation.intervalField")}</span>
              <Input type="number" min={1} value={draft.interval_value} onChange={(e) => setDraft({ ...draft, interval_value: e.target.value })} />
            </label>
            <label className="settings-automation-edit-field text-caption">
              <span>{t("settings.automation.unitField")}</span>
              <Select label={t("settings.automation.unitField")} value={draft.interval_unit} options={[
                  { value: "minute", label: t("settings.automation.unit.minute") },
                  { value: "hour", label: t("settings.automation.unit.hour") },
                  { value: "day", label: t("settings.automation.unit.day") },
                ]} onChange={(value) => setDraft({ ...draft, interval_unit: value as AutomationIntervalUnit })} />
            </label>
          </div>
        </SettingsEditDialog>
      )}
    </section>
  );
}
