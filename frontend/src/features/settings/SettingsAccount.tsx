import { useState } from "react";
import { KeyRound, UserRound } from "lucide-react";
import { useSessionStore } from "../../shell/sessionStore";
import { toast } from "../../ui/toast";
import { Alert, Button, Dialog, Input } from "../../ui/base";
import { toUserMessage, updateAccount } from "../../lib/api/client";
import { t } from "../../lib/i18n";

// The account section: for the single owner, username and password are both changed here. Both
//   actions need the current password, so each is an S-size small form dialog rather than a
//   page-level draft. Changing the password signs out web sessions on other devices but keeps the
//   current session and API tokens.

export function SettingsAccount() {
  const username = useSessionStore((state) => state.username);
  const [dialog, setDialog] = useState<"username" | "password">();

  return (
    <div className="settings-updates">
      <div className="settings-updates-header">
        <h2 className="settings-updates-title text-title">{t("settings.account.title")}</h2>
      </div>

      <section className="settings-updates-card">
        <h3 className="settings-updates-card-title text-body">
          <UserRound className="tw:size-4" aria-hidden="true" />
          {t("settings.account.usernameTitle")}
        </h3>
        <div className="settings-updates-card-row">
          <div className="tw:grid tw:min-w-0 tw:flex-1 tw:gap-1">
            <span className="text-body">{username ?? "—"}</span>
            <span className="settings-updates-muted text-caption">{t("settings.account.usernameHint")}</span>
          </div>
          <Button variant="ghost" size="sm" onClick={() => setDialog("username")}>
            {t("settings.account.changeUsername")}
          </Button>
        </div>
      </section>

      <section className="settings-updates-card">
        <h3 className="settings-updates-card-title text-body">
          <KeyRound className="tw:size-4" aria-hidden="true" />
          {t("settings.account.passwordTitle")}
        </h3>
        <div className="settings-updates-card-row">
          <span className="settings-updates-muted tw:flex-1 text-caption">{t("settings.account.passwordHint")}</span>
          <Button variant="ghost" size="sm" onClick={() => setDialog("password")}>
            {t("settings.account.changePassword")}
          </Button>
        </div>
      </section>

      <UsernameDialog open={dialog === "username"} onOpenChange={(open) => setDialog(open ? "username" : undefined)} />
      <PasswordDialog open={dialog === "password"} onOpenChange={(open) => setDialog(open ? "password" : undefined)} />
    </div>
  );
}

function UsernameDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const setUsername = useSessionStore((state) => state.setUsername);
  const [newUsername, setNewUsername] = useState("");
  const [currentPassword, setCurrentPassword] = useState("");
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const formID = "account-username-form";

  function close() {
    setNewUsername("");
    setCurrentPassword("");
    setError(undefined);
    onOpenChange(false);
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    const trimmed = newUsername.trim();
    if (!trimmed) {
      setError(t("settings.account.newUsernameRequired"));
      return;
    }
    setBusy(true);
    setError(undefined);
    try {
      const result = await updateAccount({ current_password: currentPassword, username: trimmed });
      setUsername(result.owner.username);
      toast.success(t("settings.account.savedUsername"));
      close();
    } catch (cause) {
      setError(toUserMessage(cause, t("common.operationFailed")));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={open}
      tier="S"
      onRequestClose={() => {
        if (!busy) close();
      }}
      title={t("settings.account.usernameDialogTitle")}
      description={t("settings.account.usernameDialogDescription")}
      footer={
        <>
          <Button variant="ghost" disabled={busy} onClick={close}>
            {t("act.cancel")}
          </Button>
          <Button variant="primary" type="submit" form={formID} disabled={busy} loading={busy}>
            {t("settings.account.save")}
          </Button>
        </>
      }
    >
      <form id={formID} onSubmit={submit} className="tw:grid tw:gap-3">
        {error && <Alert tone="error" title={error} />}
        <label className="tw:grid tw:gap-1 text-caption">
          {t("settings.account.newUsername")}
          <Input
            value={newUsername}
            onChange={(event) => setNewUsername(event.target.value)}
            autoComplete="username"
            maxLength={128}
            required
            autoFocus
          />
        </label>
        <label className="tw:grid tw:gap-1 text-caption">
          {t("settings.account.currentPassword")}
          <Input
            type="password"
            value={currentPassword}
            onChange={(event) => setCurrentPassword(event.target.value)}
            autoComplete="current-password"
            required
          />
        </label>
      </form>
    </Dialog>
  );
}

function PasswordDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const formID = "account-password-form";

  function close() {
    setCurrentPassword("");
    setNewPassword("");
    setConfirmPassword("");
    setError(undefined);
    onOpenChange(false);
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (newPassword !== confirmPassword) {
      setError(t("settings.account.passwordMismatch"));
      return;
    }
    setBusy(true);
    setError(undefined);
    try {
      const result = await updateAccount({ current_password: currentPassword, new_password: newPassword });
      toast.success(
        t(result.other_sessions_revoked ? "settings.account.savedPasswordRevoked" : "settings.account.savedPassword"),
      );
      close();
    } catch (cause) {
      setError(toUserMessage(cause, t("common.operationFailed")));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={open}
      tier="S"
      onRequestClose={() => {
        if (!busy) close();
      }}
      title={t("settings.account.passwordDialogTitle")}
      description={t("settings.account.passwordDialogDescription")}
      footer={
        <>
          <Button variant="ghost" disabled={busy} onClick={close}>
            {t("act.cancel")}
          </Button>
          <Button variant="primary" type="submit" form={formID} disabled={busy} loading={busy}>
            {t("settings.account.save")}
          </Button>
        </>
      }
    >
      <form id={formID} onSubmit={submit} className="tw:grid tw:gap-3">
        {error && <Alert tone="error" title={error} />}
        <label className="tw:grid tw:gap-1 text-caption">
          {t("settings.account.currentPassword")}
          <Input
            type="password"
            value={currentPassword}
            onChange={(event) => setCurrentPassword(event.target.value)}
            autoComplete="current-password"
            required
            autoFocus
          />
        </label>
        <label className="tw:grid tw:gap-1 text-caption">
          {t("settings.account.newPassword")}
          <Input
            type="password"
            value={newPassword}
            onChange={(event) => setNewPassword(event.target.value)}
            autoComplete="new-password"
            minLength={12}
            maxLength={1024}
            required
          />
        </label>
        <label className="tw:grid tw:gap-1 text-caption">
          {t("settings.account.confirmPassword")}
          <Input
            type="password"
            value={confirmPassword}
            onChange={(event) => setConfirmPassword(event.target.value)}
            autoComplete="new-password"
            minLength={12}
            maxLength={1024}
            required
          />
        </label>
      </form>
    </Dialog>
  );
}
