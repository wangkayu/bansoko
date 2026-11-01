import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { toast } from "../../ui/toast";
import {
  checkTitleParser,
  getTitleParserStatus,
  toUserMessage,
  updateTitleParser,
  type TitleParserStatus,
} from "../../lib/api/client";
import { formatDayDateTime } from "../../lib/labels";
import { getLocale, t } from "../../lib/i18n";
import { parserKeys } from "../../lib/queryKeys";
import { ConfirmDialog, StateBlock } from "../../ui";
import { Button } from "../../ui/base";
import { Chip, DescriptionList, StatePill } from "../../ui";
import { RestrictedMarkdown } from "../../ui/RestrictedMarkdown";

// 解析组件面板 — the reltitle component read model plus the two manual
// actions. There is no background polling: the channel is read only when the
// owner (or an agent, over the API) asks, per the rt update-channel ruling.
// Presentation follows the reviewed six-state design (rt-update-1101): the
// running parser answers "what and why", the channel card answers "what is
// available", and a not-adopted binary is a neutral fact, not an alarm.

type ChannelStatus = NonNullable<TitleParserStatus["channel"]>;

function shortVersion(value: string): string {
  const bare = value.startsWith("reltitle@") ? value.slice("reltitle@".length) : value;
  return bare.startsWith("v") ? bare.slice(1) : bare;
}

function sourceLabel(status: TitleParserStatus): string {
  return t(`settings.titleParser.source.${status.source}`);
}

// The channel publishes bilingual notes; each locale falls back to the other
// when its own is empty (test builds often carry none at all).
function channelNotesText(channel: ChannelStatus): string {
  const notes = channel.notes;
  return getLocale() === "zh-CN" ? notes.zh_cn || notes.en || "" : notes.en || notes.zh_cn || "";
}

export function SettingsTitleParser() {
  const queryClient = useQueryClient();
  const [checking, setChecking] = useState(false);
  const [installOpen, setInstallOpen] = useState(false);
  const [installing, setInstalling] = useState(false);
  // The → to receipt of the last install performed on this page; the read
  // model itself only knows the resulting version.
  const [receipt, setReceipt] = useState<{ from: string; to: string }>();
  const {
    data: status,
    error: statusError,
    isLoading: statusLoading,
    refetch: refetchStatus,
  } = useQuery({
    queryKey: parserKeys.status,
    queryFn: getTitleParserStatus,
    retry: false,
    // Manual surface: checks and installs refetch explicitly; nothing runs
    // on focus or reconnect.
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });

  const channel = status?.channel;
  const canInstall = Boolean(channel && channel.newer && !channel.error && !installing);

  async function runCheck(): Promise<void> {
    setChecking(true);
    try {
      const next = await checkTitleParser();
      queryClient.setQueryData(parserKeys.status, next);
    } catch (cause) {
      // A 502 still caches the failure server-side; reread so the panel shows
      // the last successful channel answer with its error reason.
      toast.error(toUserMessage(cause, t("settings.titleParser.checkFailedFallback")));
      await refetchStatus();
    } finally {
      setChecking(false);
    }
  }

  async function runInstall(): Promise<void> {
    setInstallOpen(false);
    setInstalling(true);
    try {
      const previous = status;
      const next = await updateTitleParser();
      queryClient.setQueryData(parserKeys.status, next);
      if (next.version !== previous?.version) {
        setReceipt({ from: shortVersion(previous?.version ?? ""), to: shortVersion(next.version) });
        toast.success(t("settings.titleParser.installedToast", { version: shortVersion(next.version) }));
      } else {
        toast.info(t("settings.titleParser.installNoNewer"));
      }
    } catch (cause) {
      toast.error(toUserMessage(cause, t("settings.titleParser.installFailed")));
      await refetchStatus();
    } finally {
      setInstalling(false);
    }
  }

  if (statusLoading) {
    return (
      <section className="settings-title-parser">
        <h2 className="settings-title-parser-title text-title">{t("settings.titleParser.title")}</h2>
        <StateBlock kind="busy" title={t("settings.titleParser.loading")} />
      </section>
    );
  }

  if (!status) {
    return (
      <section className="settings-title-parser">
        <h2 className="settings-title-parser-title text-title">{t("settings.titleParser.title")}</h2>
        <StateBlock
          kind="error"
          title={t("settings.titleParser.statusFailed")}
          description={toUserMessage(statusError, t("settings.titleParser.statusFailedFallback"))}
          action={
            <Button size="sm" onClick={() => void refetchStatus()}>
              <RefreshCw size={14} />
              {t("settings.titleParser.reread")}
            </Button>
          }
        />
      </section>
    );
  }

  // A binary that was refused at startup or dropped mid-flight: the built-in
  // parser serves, and the reason is a neutral diagnostic, never an alarm.
  const notAdoptedReason = status.external_failure || status.note;

  return (
    <section className="settings-title-parser">
      <div className="settings-title-parser-header">
        <h2 className="settings-title-parser-title text-title">{t("settings.titleParser.title")}</h2>
      </div>
      <p className="settings-title-parser-intro text-body">{t("settings.titleParser.intro")}</p>

      <div className="settings-title-parser-card">
        <div className="settings-title-parser-card-row">
          <span className="settings-title-parser-card-title text-body">{t("settings.titleParser.componentLabel")}</span>
          {notAdoptedReason && <StatePill tone="muted">{t("settings.titleParser.usingBuiltin", { version: shortVersion(status.version) })}</StatePill>}
          <span className="settings-title-parser-spacer" />
          <Button variant="secondary" size="sm" loading={checking} onClick={() => void runCheck()}>
            <RefreshCw size={14} />
            {checking ? t("settings.titleParser.checking") : t("settings.titleParser.check")}
          </Button>
        </div>

        {notAdoptedReason ? (
          <>
            <p className="settings-title-parser-copy text-body">{t("settings.titleParser.notAdopted", { path: status.binary_path })}</p>
            <p className="settings-title-parser-hint text-caption">{notAdoptedReason}</p>
          </>
        ) : (
          <>
            <DescriptionList
              items={[
                {
                  label: t("settings.titleParser.currentVersionLabel"),
                  value: (
                    <span title={status.version}>
                      {shortVersion(status.version)}
                    </span>
                  ),
                },
                {
                  label: t("settings.titleParser.sourceLabel"),
                  value: <Chip tone={status.source === "builtin" ? "neutral" : "accent"}>{sourceLabel(status)}</Chip>,
                },
              ]}
            />
            {status.source === "manual" && (
              <p className="settings-title-parser-hint text-caption">{t("settings.titleParser.manualHint")}</p>
            )}
            {status.config_digest && (
              <p className="settings-title-parser-footnote text-caption" title={status.config_digest}>
                {t("settings.titleParser.digestFootnote", { digest: status.config_digest })}
              </p>
            )}
          </>
        )}
      </div>

      <div className="settings-title-parser-card">
        {!channel && (
          <StateBlock
            kind="empty"
            title={t("settings.titleParser.channelEmptyTitle")}
            description={t("settings.titleParser.channelEmptyHint")}
          />
        )}

        {channel && (
          <>
            <div className="settings-title-parser-card-row">
              {channel.error ? (
                <StatePill tone="warning">{t("settings.titleParser.channelErrorTitle")}</StatePill>
              ) : channel.newer ? (
                <StatePill tone="info">{t("settings.titleParser.canUpdateTo", { version: shortVersion(channel.version) })}</StatePill>
              ) : receipt ? (
                <StatePill tone="online">{t("settings.titleParser.updatedReceipt", { from: receipt.from, to: receipt.to })}</StatePill>
              ) : (
                <StatePill tone="muted">{t("settings.titleParser.upToDate")}</StatePill>
              )}
              <span className="settings-title-parser-muted text-caption">
                {t("settings.titleParser.checkedAt")} {formatDayDateTime(channel.checked_at)}
              </span>
              <span className="settings-title-parser-spacer" />
            </div>

            {channel.error && <p className="settings-title-parser-hint text-caption">{channel.error}</p>}

            {!channel.error && (
              <div className="settings-title-parser-notes">
                {channelNotesText(channel) ? (
                  <RestrictedMarkdown source={channelNotesText(channel)} />
                ) : (
                  <p className="settings-title-parser-muted text-caption">{t("settings.titleParser.notesEmpty")}</p>
                )}
              </div>
            )}

            {!channel.error && (
              <div className="settings-title-parser-card-row">
                <span className="settings-title-parser-muted text-caption">
                  <span>{t("settings.titleParser.channelFrom")}</span>
                  {channel.release_url && (
                    <>
                      {" · "}
                      <a href={channel.release_url} target="_blank" rel="noreferrer">
                        {t("settings.titleParser.releaseUrlLabel")}
                      </a>
                    </>
                  )}
                </span>
                <span className="settings-title-parser-spacer" />
                {canInstall && (
                  <Button variant="primary" size="sm" loading={installing} onClick={() => setInstallOpen(true)}>
                    {installing
                      ? t("settings.titleParser.installing")
                      : t("settings.titleParser.installVersion", { version: shortVersion(channel.version) })}
                  </Button>
                )}
              </div>
            )}
          </>
        )}
      </div>

      <ConfirmDialog
        open={installOpen}
        onOpenChange={setInstallOpen}
        title={t("settings.titleParser.installConfirmTitle")}
        description={
          <>
            <p className="settings-title-parser-copy text-body">
              {t("settings.titleParser.installConfirmHint", { version: shortVersion(channel?.version ?? "") })}
            </p>
            <p className="settings-title-parser-muted text-caption">{t("settings.titleParser.installConfirmNote")}</p>
          </>
        }
        danger
        confirmLabel={t("settings.titleParser.installConfirmAction")}
        onConfirm={() => void runInstall()}
      />
    </section>
  );
}
