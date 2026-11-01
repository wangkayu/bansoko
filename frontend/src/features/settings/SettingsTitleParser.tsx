import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpCircle, RefreshCw } from "lucide-react";
import { toast } from "../../ui/toast";
import {
  checkTitleParser,
  getTitleParserStatus,
  toUserMessage,
  updateTitleParser,
  type TitleParserStatus,
} from "../../lib/api/client";
import { formatDateTime } from "../../lib/labels";
import { getLocale, t } from "../../lib/i18n";
import { parserKeys } from "../../lib/queryKeys";
import { Chip, ConfirmDialog, DescriptionList, StateBlock, StatePill } from "../../ui";
import { Alert, Button } from "../../ui/base";
import { RestrictedMarkdown } from "../../ui/RestrictedMarkdown";

// 解析组件面板 — the reltitle component read model plus the two manual
// actions. There is no background polling: the channel is read only when the
// owner (or an agent, over the API) asks, per the rt update-channel ruling.

type ChannelStatus = NonNullable<TitleParserStatus["channel"]>;

function sourceLabel(status: TitleParserStatus): string {
  return t(`settings.titleParser.source.${status.source}`);
}

function laneLabel(status: TitleParserStatus): string {
  return t(`settings.titleParser.lane.${status.lane}`);
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
        toast.success(t("settings.titleParser.installedToast", { version: next.version }));
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

  return (
    <section className="settings-title-parser">
      <div className="settings-title-parser-header">
        <h2 className="settings-title-parser-title text-title">{t("settings.titleParser.title")}</h2>
      </div>
      <p className="settings-title-parser-intro text-body">{t("settings.titleParser.intro")}</p>

      {status.external_failure && (
        <Alert
          tone="warning"
          title={t("settings.titleParser.externalFailureTitle")}
          description={status.external_failure}
        />
      )}

      <div className="settings-title-parser-card">
        <div className="settings-title-parser-card-row">
          <span className="settings-title-parser-card-title text-body">{t("settings.titleParser.currentTitle")}</span>
          <Chip>{sourceLabel(status)}</Chip>
        </div>
        <DescriptionList
          items={[
            { label: t("settings.titleParser.versionLabel"), value: status.version },
            ...(status.line ? [{ label: t("settings.titleParser.lineLabel"), value: status.line }] : []),
            ...(status.config_digest
              ? [{ label: t("settings.titleParser.digestLabel"), value: status.config_digest }]
              : []),
            { label: t("settings.titleParser.laneLabel"), value: laneLabel(status) },
            ...(status.binary_path
              ? [{ label: t("settings.titleParser.pathLabel"), value: status.binary_path }]
              : []),
            ...(status.note ? [{ label: t("settings.titleParser.noteLabel"), value: status.note }] : []),
            ...(status.message
              ? [{ label: t("settings.titleParser.messageLabel"), value: status.message }]
              : []),
          ]}
        />
      </div>

      <div className="settings-title-parser-card">
        <div className="settings-title-parser-card-row">
          <span className="settings-title-parser-card-title text-body">{t("settings.titleParser.channelTitle")}</span>
          {channel &&
            (channel.error ? (
              <StatePill tone="warning">{t("settings.titleParser.channelErrorTitle")}</StatePill>
            ) : channel.newer ? (
              <StatePill tone="online">{t("settings.titleParser.hasUpdate")}</StatePill>
            ) : (
              <StatePill tone="muted">{t("settings.titleParser.upToDate")}</StatePill>
            ))}
          <span className="settings-title-parser-spacer" />
          <Button size="sm" loading={checking} onClick={() => void runCheck()}>
            <RefreshCw size={14} />
            {checking ? t("settings.titleParser.checking") : t("settings.titleParser.check")}
          </Button>
          {canInstall && (
            <Button variant="primary" size="sm" loading={installing} onClick={() => setInstallOpen(true)}>
              <ArrowUpCircle size={14} />
              {installing ? t("settings.titleParser.installing") : t("settings.titleParser.install")}
            </Button>
          )}
        </div>

        {!channel && (
          <StateBlock
            kind="empty"
            title={t("settings.titleParser.channelEmptyTitle")}
            description={t("settings.titleParser.channelEmptyHint")}
          />
        )}

        {channel && (
          <>
            <DescriptionList
              items={[
                { label: t("settings.titleParser.checkedAt"), value: formatDateTime(channel.checked_at) },
                { label: t("settings.titleParser.channelVersionLabel"), value: channel.version },
                ...(channel.release_url
                  ? [
                      {
                        label: t("settings.titleParser.releaseUrlLabel"),
                        value: (
                          <a href={channel.release_url} target="_blank" rel="noreferrer">
                            {channel.release_url}
                          </a>
                        ),
                      },
                    ]
                  : []),
              ]}
            />
            {channel.error && (
              <Alert tone="warning" title={t("settings.titleParser.channelErrorTitle")} description={channel.error} />
            )}
            <div className="settings-title-parser-notes">
              <span className="settings-title-parser-card-title text-body">
                {t("settings.titleParser.notesTitle")}
              </span>
              {channelNotesText(channel) ? (
                <RestrictedMarkdown source={channelNotesText(channel)} />
              ) : (
                <p className="settings-title-parser-muted text-caption">{t("settings.titleParser.notesEmpty")}</p>
              )}
            </div>
          </>
        )}
      </div>

      <ConfirmDialog
        open={installOpen}
        onOpenChange={setInstallOpen}
        title={t("settings.titleParser.installConfirmTitle")}
        description={t("settings.titleParser.installConfirmHint", {
          version: channel?.version ?? "",
        })}
        confirmLabel={t("settings.titleParser.installConfirmAction")}
        onConfirm={() => void runInstall()}
      />
    </section>
  );
}
