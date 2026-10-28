import { Button } from "../../ui/base";
import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpCircle, Package, RefreshCw } from "lucide-react";
import { toast } from "../../ui/toast";
import {
  admitUpdate,
  ApiError,
  checkUpdate,
  getUpdateStatus,
  newUUID,
  toUserMessage,
  type EmbeddedUpdateStatus,
  type UpdateCheck,
} from "../../lib/api/client";
import { formatDateTime } from "../../lib/labels";
import { getLocale, t, tryT, useLocale } from "../../lib/i18n";
import { updateKeys } from "../../lib/queryKeys";
import { Chip, DescriptionList, StateBlock, StatePill, ConfirmDialog } from "../../ui";
import { RestrictedMarkdown } from "../../ui/RestrictedMarkdown";

const PENDING_UPDATE_OPERATION_KEY = "pending_update_operation_id";
const ACTIVE_STEPS: Record<string, true> = {
  // Admission is an in-memory server phase, not a durable checkpoint. A
  // successful status GET can still report it while the payload is fetched.
  accepted: true,
  pending_backup: true,
  pending_migration: true,
  migration_succeeded: true,
  promoted: true,
  readiness_failed: true,
  rollback_started: true,
  database_restored: true,
  fence_closure_pending: true,
};
const HISTORICAL_TERMINAL_STEPS = new Set(["succeeded", "failed", "rolled_back"]);
const BLOCKING_STEP = "rollback_blocked";

type UpdateCandidate = NonNullable<UpdateCheck["candidate"]>;

type ReleaseNotesEntry = NonNullable<UpdateCandidate["release_notes"]>[number];

type CheckPhase = "idle" | "checking" | "complete" | "failed";

function stepLabel(step: string): string {
  return tryT(`settings.updates.step.${step}`) ?? t("settings.updates.step.unknown");
}

function isHistoricalStatus(status: EmbeddedUpdateStatus | null | undefined): status is EmbeddedUpdateStatus {
  return Boolean(status && HISTORICAL_TERMINAL_STEPS.has(status.step));
}

function isBlockedStatus(status: EmbeddedUpdateStatus | null | undefined): status is EmbeddedUpdateStatus {
  return status?.step === BLOCKING_STEP;
}

function isActiveStatus(status: EmbeddedUpdateStatus | null | undefined): status is EmbeddedUpdateStatus {
  return Boolean(status && ACTIVE_STEPS[status.step]);
}

function stepTone(step: string): "online" | "warning" | "offline" | "info" | "muted" {
  switch (step) {
    case "succeeded":
      return "online";
    case "rolled_back":
    case "readiness_failed":
      return "warning";
    case "failed":
    case "rollback_blocked":
      return "offline";
    default:
      return "info";
  }
}

function markPendingReceipt(operationID: string): void {
  try {
    window.sessionStorage.setItem(PENDING_UPDATE_OPERATION_KEY, operationID);
  } catch {
    // Storage can be unavailable in a restricted browser context. The mounted
    // component still detects the active-to-terminal edge without it.
  }
}

function consumePendingReceipt(operationID: string): boolean {
  try {
    const pendingOperationID = window.sessionStorage.getItem(PENDING_UPDATE_OPERATION_KEY);
    if (pendingOperationID !== operationID) return false;
    window.sessionStorage.removeItem(PENDING_UPDATE_OPERATION_KEY);
    return true;
  } catch {
    return false;
  }
}

export function SettingsUpdates() {
  const queryClient = useQueryClient();
  const [lastActiveStatus, setLastActiveStatus] = useState<EmbeddedUpdateStatus>();
  const {
    data: recoveredStatus,
    error: statusError,
    isLoading: statusLoading,
    isFetching: statusFetching,
    isRefetchError: statusRefetchError,
    refetch: refetchStatus,
  } = useQuery({
    queryKey: updateKeys.status,
    queryFn: getUpdateStatus,
    retry: false,
    // The explicit listeners below make focus/reconnect behavior deterministic
    // and avoid duplicate refetches from Query's global browser listeners.
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    refetchInterval: (query) => {
      const current = query.state.data;
      return isActiveStatus(current) || (!current && isActiveStatus(lastActiveStatus)) ? 1500 : false;
    },
  });
  const [statusReconnectError, setStatusReconnectError] = useState(false);
  const [receipt, setReceipt] = useState<EmbeddedUpdateStatus>();
  const [checkPhase, setCheckPhase] = useState<CheckPhase>("idle");
  const [checkResult, setCheckResult] = useState<UpdateCheck>();
  const [checkError, setCheckError] = useState<unknown>();
  const [pendingCandidate, setPendingCandidate] = useState<UpdateCandidate>();
  const [pendingKey, setPendingKey] = useState<string>();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string>();
  const previousStepRef = useRef<string>();
  const previousOperationRef = useRef<string>();

  useEffect(() => {
    const next = recoveredStatus;
    if (!next) {
      // The bootstrap health-only responder returns 404 during handoff. It
      // must not erase a known active operation or stop the polling loop.
      if (lastActiveStatus) setStatusReconnectError(true);
      return;
    }

    setStatusReconnectError(false);
    if (isActiveStatus(next)) {
      markPendingReceipt(next.operation_id);
      setLastActiveStatus(next);
      setReceipt((current) => (current?.operation_id === next.operation_id ? current : undefined));
    } else {
      setLastActiveStatus(undefined);
    }

    const sameOperation = previousOperationRef.current === next.operation_id;
    const crossedActiveBoundary = sameOperation && isActiveStep(previousStepRef.current);
    const consumedPendingReceipt = isHistoricalStatus(next) && consumePendingReceipt(next.operation_id);
    if (isHistoricalStatus(next) && (crossedActiveBoundary || consumedPendingReceipt)) {
      setReceipt(next);
      // A check made before the operation describes the version it replaced:
      // drop it so the panel asks for a fresh check instead of offering it again.
      setCheckResult(undefined);
      setCheckError(undefined);
      setCheckPhase("idle");
      setSubmitError(undefined);
    }

    previousStepRef.current = next.step;
    previousOperationRef.current = next.operation_id;
  }, [recoveredStatus, lastActiveStatus]);

  useEffect(() => {
    const refetchStatusOnResume = () => {
      if (queryClient.isFetching({ queryKey: updateKeys.status }) > 0) return;
      void refetchStatus().then(({ error }) => setStatusReconnectError(Boolean(error)));
    };

    window.addEventListener("online", refetchStatusOnResume);
    window.addEventListener("focus", refetchStatusOnResume);
    return () => {
      window.removeEventListener("online", refetchStatusOnResume);
      window.removeEventListener("focus", refetchStatusOnResume);
    };
  }, [queryClient, refetchStatus]);

  async function handleCheck() {
    if (checkPhase === "checking") return;

    setCheckPhase("checking");
    setCheckError(undefined);
    setCheckResult(undefined);
    setSubmitError(undefined);
    setPendingCandidate(undefined);
    setPendingKey(undefined);
    try {
      const result = await checkUpdate();
      setCheckResult(result);
      setCheckPhase("complete");
      if (result.active_operation) {
        markPendingReceipt(result.active_operation.operation_id);
        queryClient.setQueryData(updateKeys.status, result.active_operation);
      }
    } catch (cause) {
      setCheckError(cause);
      setCheckPhase("failed");
    }
  }

  function requestUpdate(candidate: UpdateCandidate) {
    const currentStatus = queryClient.getQueryData<EmbeddedUpdateStatus | null>(updateKeys.status);
    if (
      statusError ||
      statusFetching ||
      statusRefetchError ||
      statusReconnectError ||
      isActiveStatus(currentStatus) ||
      isBlockedStatus(currentStatus)
    )
      return;
    setPendingCandidate(candidate);
    // Keep this key when the same candidate is reopened after a transient
    // admission failure. The server can therefore safely deduplicate retries.
    setPendingKey((current) => current ?? newUUID());
    setSubmitError(undefined);
    setConfirmOpen(true);
  }

  async function reconcileAdmissionFailure(cause: unknown) {
    // A lost response and UPDATE_LOCKED both require status reconciliation before
    // the user can decide whether the original admission took effect.
    const shouldReadStatus =
      cause instanceof ApiError &&
      (cause.code === "UPDATE_LOCKED" || cause.status === 0 || cause.status >= 500);

    if (shouldReadStatus) {
      try {
        const recovered = await getUpdateStatus();
        queryClient.setQueryData(updateKeys.status, recovered);
        if (isActiveStatus(recovered) || isBlockedStatus(recovered)) {
          setConfirmOpen(false);
          setPendingCandidate(undefined);
          setPendingKey(undefined);
          setSubmitError(undefined);
          return;
        }
      } catch {
        // Keep the original admission identity; the visible error remains
        // actionable and a later explicit retry performs the same reconciliation.
      }
    }

    if (cause instanceof ApiError && cause.code === "UPDATE_CANDIDATE_STALE") {
      setPendingCandidate(undefined);
      setPendingKey(undefined);
      setConfirmOpen(false);
      setCheckResult(undefined);
      setCheckError(cause);
      setCheckPhase("failed");
      return;
    }

    const message = toUserMessage(cause, t("settings.updates.submitFailed"));
    setSubmitError(message);
    toast.error(message);
  }

  async function confirmUpdate() {
    const candidate = pendingCandidate;
    const idempotencyKey = pendingKey;
    const currentStatus = queryClient.getQueryData<EmbeddedUpdateStatus | null>(updateKeys.status);
    if (
      !candidate ||
      !idempotencyKey ||
      submitting ||
      statusError ||
      statusFetching ||
      statusRefetchError ||
      statusReconnectError ||
      isActiveStatus(currentStatus) ||
      isBlockedStatus(currentStatus)
    )
      return;
    setSubmitting(true);
    setSubmitError(undefined);
    try {
      const accepted = await admitUpdate(
        {
          target_release_id: candidate.release_id,
          manifest_digest: candidate.manifest_digest,
        },
        idempotencyKey,
      );
      markPendingReceipt(accepted.operation_id);
      setConfirmOpen(false);
      setPendingCandidate(undefined);
      setPendingKey(undefined);
      const { error } = await refetchStatus();
      setStatusReconnectError(Boolean(error));
      toast.success(t("settings.updates.accepted"));
    } catch (cause) {
      // Keep both the candidate and key so an explicit retry reuses the same
      // admission identity after a lost response or transient failure.
      await reconcileAdmissionFailure(cause);
    } finally {
      setSubmitting(false);
    }
  }

  const visibleStatus = (statusError || statusRefetchError || statusReconnectError || !recoveredStatus) && isActiveStatus(lastActiveStatus) ? lastActiveStatus : recoveredStatus;
  const activeStatus = isActiveStatus(visibleStatus);
  const blockedStatus = isBlockedStatus(visibleStatus);
  const historicalStatus = isHistoricalStatus(visibleStatus);
  const unknownStatus = Boolean(visibleStatus && !activeStatus && !blockedStatus && !historicalStatus);
  const statusReady =
    !statusLoading && !statusFetching && !statusError && !statusRefetchError && !statusReconnectError && !activeStatus && !blockedStatus && !unknownStatus;

  return (
    <section className="settings-updates">
      <div className="settings-updates-header">
        <h2 className="settings-updates-title text-title">{t("settings.updates.title")}</h2>
      </div>

      {statusLoading && !visibleStatus && <StateBlock kind="busy" title={t("settings.updates.loading")} />}

      {(statusError || statusRefetchError || statusReconnectError) && !activeStatus && (
        <StateBlock
          kind="error"
          title={t("settings.updates.statusFailed")}
          description={toUserMessage(statusError, t("settings.updates.statusFailedFallback"))}
          action={
            <Button size="sm" onClick={() => void refetchStatus()}>
              <RefreshCw size={14} />
              {t("settings.updates.reread")}
            </Button>
          }
        />
      )}

      {unknownStatus && (
        <StateBlock kind="error" title={t("settings.updates.unknownTitle")} description={t("settings.updates.unknownHint")} />
      )}

      {(statusError || statusRefetchError || statusReconnectError) && activeStatus && (
        <div
          role="status"
          aria-live="polite"
          className="settings-updates-reconnect text-caption"
        >
          {t("settings.updates.reconnecting")}
        </div>
      )}

      {blockedStatus && visibleStatus && <UpdateStatusCard status={visibleStatus} />}

      {activeStatus && visibleStatus && <UpdateStatusCard status={visibleStatus} />}

      {receipt && <UpdateReceipt status={receipt} />}

      {historicalStatus && visibleStatus && <UpdateStatusCard status={visibleStatus} historical />}

      {statusReady && checkResult && <UpdateCheckResult check={checkResult} onUpdate={requestUpdate} submitError={submitError} />}

      {statusReady && <ManualCheckPanel phase={checkPhase} error={checkError} onCheck={() => void handleCheck()} />}

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t("settings.updates.confirmTitle", { version: pendingCandidate?.version ?? "" })}
        description={pendingCandidate ? <UpdateConfirmation candidate={pendingCandidate} /> : undefined}
        confirmLabel={submitting ? t("settings.updates.submitting") : t("settings.updates.confirmAction")}
        danger
        onConfirm={() => void confirmUpdate()}
      />
    </section>
  );
}

function isActiveStep(step: string | undefined): boolean {
  return Boolean(step && ACTIVE_STEPS[step]);
}

function ManualCheckPanel({
  phase,
  error,
  onCheck,
}: {
  phase: CheckPhase;
  error?: unknown;
  onCheck: () => void;
}) {
  if (phase === "checking") {
    return <StateBlock kind="busy" title={t("settings.updates.checking")} />;
  }

  if (phase === "failed") {
    return (
      <StateBlock
        kind="error"
        title={t("settings.updates.checkFailed")}
        description={toUserMessage(error, t("settings.updates.checkFailedFallback"))}
        action={
          <Button size="sm" autoFocus onClick={onCheck}>
            <RefreshCw size={14} />
            {t("settings.updates.retryCheck")}
          </Button>
        }
      />
    );
  }

  if (phase === "idle") {
    return (
      <StateBlock
        kind="empty"
        title={t("settings.updates.idleTitle")}
        description={t("settings.updates.idleHint")}
        action={
          <Button size="sm" onClick={onCheck}>
            <RefreshCw size={14} />
            {t("settings.updates.check")}
          </Button>
        }
      />
    );
  }

  return (
    <div
      aria-label={t("settings.updates.recheckAria")}
      className="settings-updates-check-row"
    >
      <span className="settings-updates-muted text-caption">{t("settings.updates.recheckHint")}</span>
      <Button size="sm" onClick={onCheck}>
        <RefreshCw size={14} />
        {t("settings.updates.check")}
      </Button>
    </div>
  );
}

function UpdateReceipt({ status }: { status: EmbeddedUpdateStatus }) {
  const succeeded = status.step === "succeeded";
  const rolledBack = status.step === "rolled_back";
  const message = succeeded ? t("settings.updates.receiptDone") : rolledBack ? t("settings.updates.receiptRolledBack") : t("settings.updates.receiptFailed");

  return (
    <div
      role={succeeded ? "status" : "alert"}
      aria-live={succeeded ? "polite" : "assertive"}
      className="settings-updates-card"
    >
      <p className="settings-updates-copy text-body">{message}</p>
      <p className="settings-updates-copy-spaced settings-updates-muted text-caption">
        {t("settings.updates.targetVersion", { version: status.target_version ?? status.target_release_id })}
      </p>
    </div>
  );
}

function UpdateCheckResult({
  check,
  onUpdate,
  submitError,
}: {
  check: UpdateCheck;
  onUpdate: (candidate: UpdateCandidate) => void;
  submitError?: string;
}) {
  return (
    <div className="settings-updates-panel">
      <CurrentVersionCard check={check} />

      {check.status === "up_to_date" && (
        <StateBlock kind="empty" title={t("settings.updates.upToDateTitle")} description={t("settings.updates.upToDateHint")} />
      )}

      {check.candidate && (check.status === "update_available" || check.status === "base_image_required") && (
        <CandidateCard check={check} onUpdate={onUpdate} submitError={submitError} />
      )}

      {check.compatibility.kind === "incompatible" && (
        <StateBlock
          kind="error"
          title={t("settings.updates.incompatibleTitle")}
          description={t("settings.updates.incompatibleHint")}
        />
      )}
    </div>
  );
}

function ReleaseNotesList({ entries }: { entries: ReleaseNotesEntry[] | undefined }) {
  useLocale();
  if (!entries || entries.length === 0) {
    return <p className="settings-updates-muted text-caption">{t("settings.updates.notesEmpty")}</p>;
  }
  const lang = getLocale() === "zh-CN" ? "zh-CN" : "en";
  return (
    <div className="tw:grid tw:gap-2">
      {entries.map((entry) => (
        <div key={entry.version} className="tw:grid tw:gap-1">
          <div className="settings-updates-card-row">
            <Chip>{entry.version}</Chip>
            <span className="settings-updates-muted text-caption">{formatDateTime(entry.published_at)}</span>
          </div>
          <RestrictedMarkdown source={entry.notes[lang]} />
        </div>
      ))}
    </div>
  );
}

function CurrentVersionCard({ check }: { check: UpdateCheck }) {
  const [notesOpen, setNotesOpen] = useState(false);
  const notes = check.current.release_notes;
  return (
    <div className="settings-updates-card">
      <div className="settings-updates-card-row">
        <span className="settings-updates-card-title text-body">{t("settings.updates.currentTitle")}</span>
        <Chip>{check.current.version || t("settings.updates.unknown")}</Chip>
      </div>
      <DescriptionList
        items={[
          { label: t("settings.updates.checkedAt"), value: formatDateTime(check.checked_at) },
        ]}
      />
      <div>
        <Button size="sm" aria-expanded={notesOpen} onClick={() => setNotesOpen((open) => !open)}>
          {notesOpen ? t("settings.updates.notesHideCurrent") : t("settings.updates.notesShowCurrent")}
        </Button>
      </div>
      {notesOpen && <ReleaseNotesList entries={notes ? [notes] : undefined} />}
    </div>
  );
}

function CandidateCard({
  check,
  onUpdate,
  submitError,
}: {
  check: UpdateCheck;
  onUpdate: (candidate: UpdateCandidate) => void;
  submitError?: string;
}) {
  const candidate = check.candidate;
  if (!candidate) return null;

  const canUpdate = check.status === "update_available" && check.compatibility.kind === "overlay";
  const needsBaseImage = check.status === "base_image_required" || check.compatibility.kind === "base_image_required";

  return (
    <div className="settings-updates-card">
      <div className="settings-updates-card-row">
        <Package className="settings-updates-icon" />
        <span className="settings-updates-card-title text-body">{t("settings.updates.candidateTitle")}</span>
        <Chip>{candidate.version}</Chip>
        <span className="settings-updates-spacer" />
        {canUpdate && (
          <Button variant="primary" size="sm" onClick={() => onUpdate(candidate)}>
            <ArrowUpCircle size={14} />
            {t("settings.updates.update")}
          </Button>
        )}
      </div>

      <DescriptionList
        items={[
          { label: t("settings.updates.publishedAt"), value: formatDateTime(candidate.published_at) },
          ...candidateFacts(candidate),
        ]}
      />

      <div className="tw:grid tw:gap-2">
        <span className="text-body">{t("settings.updates.notesTitle")}</span>
        <ReleaseNotesList entries={candidate.release_notes} />
      </div>

      {/* An upgrade this page cannot perform: a restricted state replaces the upgrade button instead of failing only after a click. */}
      {needsBaseImage && <StateBlock kind="restricted" title={t("settings.updates.imageOutdated")} />}

      {submitError && (
        <p role="alert" className="settings-updates-error text-caption">
          {submitError}
        </p>
      )}
    </div>
  );
}

function UpdateConfirmation({ candidate }: { candidate: UpdateCandidate }) {
  return (
    <div className="settings-updates-panel">
      <p className="settings-updates-reason text-caption">
        {t("settings.updates.confirmHint")}
      </p>
      <DescriptionList
        items={[
          { label: t("settings.updates.targetVersionLabel"), value: candidate.version },
          ...candidateFacts(candidate),
        ]}
      />
    </div>
  );
}

function rollbackLabel(value: string): string {
  return value === "restore_required" ? t("settings.updates.rollbackRestore") : t("settings.updates.rollbackPlain");
}

function candidateFacts(candidate: UpdateCandidate): { label: string; value: string }[] {
  return [
    { label: t("settings.updates.migration"), value: candidate.target_migration_version ? t("settings.updates.migrationYes") : t("settings.updates.migrationNo") },
    { label: t("settings.updates.downtime"), value: candidate.required_downtime ? t("settings.updates.downtimeYes") : t("settings.updates.downtimeNo") },
    { label: t("settings.updates.backup"), value: candidate.backup_required ? t("settings.updates.backupYes") : t("settings.updates.backupNo") },
    { label: t("settings.updates.onFailure"), value: rollbackLabel(candidate.rollback_class) },
  ];
}

function UpdateStatusCard({ status, historical = false }: { status: EmbeddedUpdateStatus; historical?: boolean }) {
  const active = isActiveStatus(status);
  const blocked = isBlockedStatus(status);
  const terminal = isHistoricalStatus(status);
  const title = active ? t("settings.updates.cardCurrent") : blocked ? t("settings.updates.cardBlocked") : historical || terminal ? t("settings.updates.cardLast") : t("settings.updates.cardDefault");

  return (
    <div
      role={blocked ? undefined : active ? "status" : undefined}
      aria-live={active ? "polite" : undefined}
      className="settings-updates-card"
    >
      <div className="settings-updates-card-row">
        <span className="settings-updates-card-title text-body">{title}</span>
        <StatePill tone={stepTone(status.step)}>{stepLabel(status.step)}</StatePill>
      </div>
      <div className="settings-updates-status-line text-caption">
        {t("settings.updates.targetVersion", { version: status.target_version ?? status.target_release_id })}
        {status.updated_at && ` · ${t("settings.updates.updatedAt", { time: formatDateTime(status.updated_at) })}`}
      </div>

      {status.step === "rolled_back" && (
        <p className="settings-updates-status-warning text-caption">
          {t("settings.updates.lastRolledBack")}
        </p>
      )}
      {status.step === "rollback_blocked" && (
        <StateBlock
          kind="error"
          title={t("settings.updates.blockedTitle")}
          description={
            <>
              {status.last_error ?? t("settings.updates.blockedFallback")} {t("settings.updates.blockedHint")}
            </>
          }
        />
      )}
      {status.step === "failed" && (
        <StateBlock kind="error" title={t("settings.updates.lastFailedTitle")} description={status.last_error ?? t("settings.updates.lastFailedFallback")} />
      )}
      {active && <p className="settings-updates-reason text-caption">{t("settings.updates.leaveSafe")}</p>}
      {status.last_error && status.step !== "failed" && status.step !== "rollback_blocked" && (
        <p className="settings-updates-last-error text-caption">
          {status.last_error}
        </p>
      )}
    </div>
  );
}
