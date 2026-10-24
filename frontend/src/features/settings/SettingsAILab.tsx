import { useState } from "react";
import { Alert, Button, Checkbox, Dialog, HelpTip, Input, StateBlock } from "../../ui/base";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, Check, Copy, Download, Eye, EyeOff, FileText, KeyRound, RefreshCw, Trash2 } from "lucide-react";
import { toast } from "../../ui/toast";
import {
  createApiToken,
  listApiTokens,
  readAgentGuide,
  readApiTokenRaw,
  revokeApiToken,
  rotateApiToken,
  toUserMessage,
  type ApiToken,
  type ApiTokenScope,
} from "../../lib/api/client";
import { formatDateTime } from "../../lib/labels";
import { apiTokenKeys } from "../../lib/queryKeys";
import { t } from "../../lib/i18n";
import { buildAgentSetup, downloadTextFile } from "./agentSetup";

// API tokens: manages long-lived API tokens (PATs) for external programs (MCP/CLI). The top of the
//   page gives the MCP address under "connect an AI assistant", plus three entries: copy an English
//   setup note for the assistant, download SKILL.md, and view the full English note read-only
//   (displayed verbatim, preformatted; the note body is the same file the server embeds and moves
//   with the version). After creation the raw token is stored encrypted by the backend, shown long
//   term in its list row and copyable at any time (read on demand); revoking hard-deletes (the row
//   disappears and the old key dies at once) and, being dangerous, requires confirmation. This
//   section has no page-level draft; every action takes effect immediately.
//   Permissions (scopes) are chosen at creation and never changeable: view is always included,
//   write checked by default, read sensitive values unchecked by default (a token for an AI
//   assistant should not carry it); each list row shows its token's permissions.

const SCOPE_ORDER: ApiTokenScope[] = ["read", "write", "secrets"];

function scopeSummary(scopes: ApiTokenScope[]): string {
  const labels = SCOPE_ORDER.filter((scope) => scopes.includes(scope)).map((scope) => t(`settings.token.scope.${scope}`));
  return t("settings.token.scope.list", { scopes: labels.join(t("settings.token.scope.separator")) });
}

function mcpEndpoint(): string {
  const origin = typeof window !== "undefined" ? window.location.origin : "";
  return `${origin}/api/v1/mcp`;
}

// The inline raw token: fetched once on entry and cached; the whole key is masked by default (the
//   eye reveals it in full) while copy always copies the full text; when unavailable (the deployment
//   secret unconfigured) a degraded hint shows and auth is unaffected.
function TokenRawValue({ tokenId, copied, onCopy }: { tokenId: string; copied: boolean; onCopy: (raw: string) => void }) {
  const raw = useQuery({
    queryKey: apiTokenKeys.raw(tokenId),
    queryFn: () => readApiTokenRaw(tokenId),
    retry: false,
  });
  const [revealed, setRevealed] = useState(false);
  if (raw.isLoading) {
    return (
      <StateBlock kind="busy" title={t("settings.token.reading")} />
    );
  }
  if (raw.isError || !raw.data) {
    return <Alert tone="warning" title={t("settings.token.rawUnavailable")} />;
  }
  const value = raw.data;
  return (
    <div className="settings-ai-raw">
      <code className="settings-ai-raw-value text-caption">
        {revealed ? value : "•".repeat(value.length)}
      </code>
      <Button variant="ghost" size="sm" aria-label={revealed ? t("settings.token.hideKey") : t("settings.token.showKey")} title={revealed ? t("settings.token.hideKey") : t("settings.token.showKey")} onClick={() => setRevealed((current) => !current)}>
        {revealed ? <EyeOff size={14} /> : <Eye size={14} />}
      </Button>
      <Button size="sm" onClick={() => onCopy(value)}>
        {copied ? <Check size={14} /> : <Copy size={14} />}
        {copied ? t("settings.token.copied") : t("settings.token.copy")}
      </Button>
    </div>
  );
}

export function SettingsAILab() {
  const queryClient = useQueryClient();
  const tokens = useQuery({ queryKey: apiTokenKeys.all, queryFn: listApiTokens });
  const [name, setName] = useState("");
  const [allowWrite, setAllowWrite] = useState(true);
  const [allowSecrets, setAllowSecrets] = useState(false);
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [revokeTarget, setRevokeTarget] = useState<ApiToken | null>(null);
  const [rotateTarget, setRotateTarget] = useState<ApiToken | null>(null);
  // A token just created (or rotated) this session: carried directly when copying the note, otherwise a placeholder stands in.
  const [freshToken, setFreshToken] = useState<string | undefined>(undefined);
  const [setupCopied, setSetupCopied] = useState(false);
  const [guideOpen, setGuideOpen] = useState(false);
  const [downloadingSkill, setDownloadingSkill] = useState(false);

  const createMutation = useMutation({
    mutationFn: () => {
      const scopes: ApiTokenScope[] = ["read"];
      if (allowWrite) scopes.push("write");
      if (allowSecrets) scopes.push("secrets");
      return createApiToken(name.trim(), scopes);
    },
    onSuccess: (result) => {
      setFreshToken(result.token);
      toast.success(t("settings.token.created"));
      setName("");
      setAllowWrite(true);
      setAllowSecrets(false);
      void queryClient.invalidateQueries({ queryKey: apiTokenKeys.all });
    },
    onError: (cause) => {
      toast.error(toUserMessage(cause, t("settings.token.createFailed")));
    },
  });

  const rotateMutation = useMutation({
    mutationFn: (target: ApiToken) => rotateApiToken(target.id),
    onSuccess: (result) => {
      setFreshToken(result.token);
      toast.success(t("settings.token.rotated"));
      setRotateTarget(null);
      void queryClient.invalidateQueries({ queryKey: apiTokenKeys.all });
      void queryClient.invalidateQueries({ queryKey: apiTokenKeys.raw(result.token_id) });
    },
    onError: (cause) => {
      toast.error(toUserMessage(cause, t("settings.token.rotateFailed")));
    },
  });

  const revokeMutation = useMutation({
    mutationFn: (target: ApiToken) => revokeApiToken(target.id),
    onSuccess: () => {
      toast.success(t("settings.token.deleted"));
      setRevokeTarget(null);
      void queryClient.invalidateQueries({ queryKey: apiTokenKeys.all });
    },
    onError: (cause) => {
      toast.error(toUserMessage(cause, t("settings.token.deleteFailed")));
    },
  });

  async function copyRaw(tokenId: string, raw: string) {
    try {
      await navigator.clipboard.writeText(raw);
      setCopiedId(tokenId);
      window.setTimeout(() => setCopiedId((current) => (current === tokenId ? null : current)), 1500);
    } catch {
      toast.error(t("settings.token.copyFailed"));
    }
  }

  async function copySetup() {
    try {
      await navigator.clipboard.writeText(buildAgentSetup(mcpEndpoint(), freshToken));
      setSetupCopied(true);
      window.setTimeout(() => setSetupCopied(false), 1500);
    } catch {
      toast.error(t("settings.token.copyFailed"));
    }
  }

  async function downloadSkill() {
    setDownloadingSkill(true);
    try {
      downloadTextFile("SKILL.md", await readAgentGuide("skill"));
    } catch (cause) {
      toast.error(toUserMessage(cause, t("settings.token.connect.downloadFailed")));
    } finally {
      setDownloadingSkill(false);
    }
  }

  const confirmationOpen = revokeTarget !== null || rotateTarget !== null;

  return (
    <div className="settings-ai">
      <div aria-hidden={confirmationOpen || undefined} className="tw:grid tw:gap-3">
      <div className="settings-updates-header">
        <h2 className="settings-updates-title text-title">{t("settings.token.title")}</h2>
      </div>
      <section className="settings-ai-section">
        <h3 className="settings-ai-section-title text-body">
          <Bot className="settings-ai-icon" />
          {t("settings.token.connect.title")}
        </h3>
        <p className="settings-ai-spacing-1 text-caption settings-ai-muted">{t("settings.token.connect.hint")}</p>
        <div className="settings-ai-endpoint settings-ai-spacing-2">
          <span className="text-caption settings-ai-muted">{t("settings.token.connect.endpoint")}</span>
          <code className="settings-ai-raw-value text-caption">{mcpEndpoint()}</code>
        </div>
        <div className="settings-ai-actions settings-ai-spacing-2">
          <Button size="sm" onClick={() => void copySetup()}>
            {setupCopied ? <Check size={14} /> : <Copy size={14} />}
            {setupCopied ? t("settings.token.connect.setupCopied") : t("settings.token.connect.copySetup")}
          </Button>
          <Button size="sm" onClick={() => void downloadSkill()} loading={downloadingSkill}>
            <Download size={14} />
            {t("settings.token.connect.downloadSkill")}
          </Button>
          <Button size="sm" onClick={() => setGuideOpen(true)}>
            <FileText size={14} />
            {t("settings.token.connect.viewGuide")}
          </Button>
        </div>
      </section>

      <section className="settings-ai-section">
        <h3 className="settings-ai-section-title text-body">
          <KeyRound className="settings-ai-icon" />
          {t("settings.token.createTitle")}
          <HelpTip label={t("settings.token.helpLabel")}>
            <p className="tw:m-0">{t("settings.token.help.intro")}</p>
            <p className="tw:m-0">{t("settings.token.help.remote")}</p>
            <p className="tw:m-0">{t("settings.token.help.cli")}</p>
          </HelpTip>
        </h3>
        <p className="settings-ai-spacing-1 text-caption settings-ai-muted">{t("settings.token.purpose")}</p>
        <form
          className="settings-ai-form"
          onSubmit={(event) => {
            event.preventDefault();
            if (name.trim()) createMutation.mutate();
          }}
        >
          <label className="settings-ai-field text-caption">
            {t("settings.token.nameField")}
            <div className="settings-ai-input">
              <Input
                aria-label={t("settings.token.nameLabel")}
                placeholder={t("settings.token.namePlaceholder")}
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
            </div>
          </label>
          <fieldset className="settings-ai-scopes">
            <legend className="text-caption">{t("settings.token.scope.field")}</legend>
            <Checkbox checked disabled onCheckedChange={() => undefined}>
              {t("settings.token.scope.read")}
              <span className="text-caption settings-ai-muted"> · {t("settings.token.scope.readHint")}</span>
            </Checkbox>
            <Checkbox checked={allowWrite} onCheckedChange={setAllowWrite}>
              {t("settings.token.scope.write")}
              <span className="text-caption settings-ai-muted"> · {t("settings.token.scope.writeHint")}</span>
            </Checkbox>
            <Checkbox checked={allowSecrets} onCheckedChange={setAllowSecrets}>
              {t("settings.token.scope.secrets")}
              <span className="text-caption settings-ai-muted"> · {t("settings.token.scope.secretsHint")}</span>
            </Checkbox>
            <p className="tw:m-0 text-caption settings-ai-muted">{t("settings.token.scope.fixed")}</p>
          </fieldset>
          <Button variant="primary" type="submit" disabled={!name.trim() || createMutation.isPending} loading={createMutation.isPending}>
            {createMutation.isPending ? t("settings.token.creating") : t("settings.token.create")}
          </Button>
        </form>
        {tokens.isError && <Alert tone="error" title={t("settings.token.listFailed")} className="settings-ai-spacing-3" />}
      </section>

      <section className="settings-ai-section">
        <h3 className="settings-ai-section-title text-body">{t("settings.token.listTitle")}</h3>
        <p className="settings-ai-spacing-1 text-caption settings-ai-muted">{t("settings.token.listHint")}</p>
        {tokens.isLoading ? (
          <StateBlock kind="busy" title={t("settings.token.loading")} />
        ) : tokens.data && tokens.data.length === 0 ? (
          <StateBlock kind="empty" title={t("settings.token.empty")} description={t("settings.token.emptyHint")} />
        ) : (
          <ul className="settings-ai-token-list">
            {tokens.data?.map((token) => (
              <li key={token.id} className="settings-ai-token-row">
                <div className="settings-ai-token-head">
                  <div className="settings-ai-token-meta">
                    <p className="settings-ai-token-name text-body">{token.name}</p>
                    <p className="text-caption settings-ai-muted">
                      {t("settings.token.createdAt", { time: formatDateTime(token.created_at) })}
                      {" · "}
                      {token.last_used_at ? t("settings.token.lastUsed", { time: formatDateTime(token.last_used_at) }) : t("settings.token.neverUsed")}
                    </p>
                    <p className="text-caption settings-ai-muted">{scopeSummary(token.scopes)}</p>
                  </div>
                  <div className="settings-ai-actions">
                    <Button size="sm" onClick={() => setRotateTarget(token)}>
                      <RefreshCw size={14} />
                      {t("settings.token.rotate")}
                    </Button>
                    <Button variant="dangerOutline" size="sm" onClick={() => setRevokeTarget(token)}>
                      <Trash2 size={14} />
                      {t("settings.token.delete")}
                    </Button>
                  </div>
                </div>
                <TokenRawValue tokenId={token.id} copied={copiedId === token.id} onCopy={(raw) => void copyRaw(token.id, raw)} />
              </li>
            ))}
          </ul>
        )}
      </section>
      </div>

      {guideOpen && <AgentGuideDialog onClose={() => setGuideOpen(false)} />}

      {revokeTarget && (
        <Dialog
          open
          blocking
          tier="S"
          onRequestClose={() => setRevokeTarget(null)}
          title={t("settings.token.deleteTitle", { name: revokeTarget.name })}
          footer={
            <>
              <Button onClick={() => setRevokeTarget(null)}>{t("settings.token.cancel")}</Button>
              <Button variant="danger" onClick={() => revokeMutation.mutate(revokeTarget)} loading={revokeMutation.isPending}>
                {t("settings.token.delete")}
              </Button>
            </>
          }
        >
          <TwoLines text={t("settings.token.deleteHint")} />
        </Dialog>
      )}

      {rotateTarget && (
        <Dialog
          open
          blocking
          tier="S"
          onRequestClose={() => setRotateTarget(null)}
          title={t("settings.token.rotateTitle", { name: rotateTarget.name })}
          footer={
            <>
              <Button onClick={() => setRotateTarget(null)}>{t("settings.token.cancel")}</Button>
              <Button variant="primary" onClick={() => rotateMutation.mutate(rotateTarget)} loading={rotateMutation.isPending}>
                {t("settings.token.rotate")}
              </Button>
            </>
          }
        >
          <TwoLines text={t("settings.token.rotateHint")} />
        </Dialog>
      )}
    </div>
  );
}

// The read-only note dialog: the English note goes verbatim into a preformatted block (no markdown dependency), with the frame's copy from the catalog.
function AgentGuideDialog({ onClose }: { onClose: () => void }) {
  const guide = useQuery({ queryKey: ["agent-guide"], queryFn: () => readAgentGuide("guide"), retry: false, staleTime: Infinity });
  return (
    <Dialog
      open
      tier="M"
      onRequestClose={onClose}
      title={t("settings.token.connect.guideTitle")}
      description={t("settings.token.connect.guideHint")}
      footer={<Button onClick={onClose}>{t("ui.close")}</Button>}
    >
      {guide.isLoading ? (
        <StateBlock kind="busy" title={t("settings.token.connect.guideLoading")} />
      ) : guide.isError || guide.data === undefined ? (
        <Alert
          tone="error"
          title={t("settings.token.connect.guideFailed")}
          action={
            <Button size="sm" onClick={() => void guide.refetch()}>
              {t("settings.token.connect.guideRetry")}
            </Button>
          }
        />
      ) : (
        <pre className="settings-ai-guide text-caption" tabIndex={0} lang="en">
          {guide.data}
        </pre>
      )}
    </Dialog>
  );
}

// The confirm dialog has two lines: what it does, plus what is unaffected / whether it can be undone (a newline separates them in the copy table).
function TwoLines({ text }: { text: string }) {
  return (
    <div className="settings-ai-modal-copy text-body">
      {text.split("\n").map((line) => (
        <p key={line} className="tw:m-0">
          {line}
        </p>
      ))}
    </div>
  );
}
