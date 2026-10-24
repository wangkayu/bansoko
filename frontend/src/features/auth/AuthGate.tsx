import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  ApiError,
  createBootstrapOwner,
  getAuthSession,
  getBootstrapStatus,
  listAnime,
  login as loginWithPassword,
  logout as logoutSession,
  toUserMessage,
} from "../../lib/api/client";
import { ApplicationShell } from "../../shell/ApplicationShell";
import { useSessionStore } from "../../shell/sessionStore";
import { StateBlock } from "../../ui/StateBlock";
import { HelpTip } from "../../ui/base/HelpTip";
import { Input } from "../../ui/base/controls";
import { t } from "../../lib/i18n";

// Entry gate. Fresh instances go through the real bootstrap path:
// the first visit directly creates the owner account (which opens a normal
// login session); afterwards the instance is locked and requires login.
// Existing instances show password login. In development skip-auth mode the
// anonymous probe succeeds and the gate opens directly; production never
// skips auth. No default account or password is ever created.
//
// The session credential is an HttpOnly cookie, so this component can never
// read it: every page load asks the server (`GET /auth/session`) whether one
// is open before deciding between the shell and the sign-in form.

type View = "probe" | "bootstrap" | "login" | "shell";

export function AuthGate() {
  const authenticated = useSessionStore((s) => s.authenticated);
  const setSession = useSessionStore((s) => s.setAuthenticated);
  const clearSession = useSessionStore((s) => s.clearSession);
  // The first render always probes: storage cannot tell us whether the cookie
  // is live, and a revoked-but-present cookie must not open the shell.
  const [view, setView] = useState<View>("probe");
  const [skipAuth, setSkipAuth] = useState(false);
  const [error, setError] = useState<string>();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const navigate = useNavigate();

  const probe = useCallback(async () => {
    setError(undefined);
    // An open session answers first: the cookie rides along, and this is the
    // only way a page reload finds out it is still signed in.
    try {
      const session = await getAuthSession();
      setSession(session.owner.username);
      setView("shell");
      return;
    } catch (cause) {
      if (!(cause instanceof ApiError) || cause.status !== 401) {
        setError(toUserMessage(cause, t("auth.unreachableFallback")));
        setView("probe");
        return;
      }
    }
    try {
      // No session. In development skip-auth mode an anonymous read succeeds
      // and the gate opens directly; production never skips auth.
      await listAnime();
      setSkipAuth(true);
      setView("shell");
      return;
    } catch (cause) {
      if (!(cause instanceof ApiError) || cause.status !== 401) {
        setError(toUserMessage(cause, t("auth.unreachableFallback")));
        setView("probe");
        return;
      }
    }
    try {
      const status = await getBootstrapStatus();
      setView(status.state === "bootstrap_required" ? "bootstrap" : "login");
    } catch (cause) {
      setError(toUserMessage(cause, t("auth.statusReadFailed")));
      setView("probe");
    }
  }, [setSession]);

  useEffect(() => {
    if (view === "probe") {
      void probe();
    }
  }, [probe, view]);

  useEffect(() => {
    if (authenticated) {
      setView("shell");
    } else if (!skipAuth) {
      setView((current) => (current === "shell" ? "login" : current));
    }
  }, [authenticated, skipAuth]);

  async function submitOwner(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      const result = await createBootstrapOwner({ username: username.trim(), password });
      setSession(result.owner.username);
      navigate("/");
    } catch (cause) {
      // Another visitor already created the owner: the instance is locked now,
      // so move to the login view instead of leaving the user stuck.
      if (cause instanceof ApiError && cause.status === 409) {
        setView("login");
        setError(t("auth.ownerExists"));
      } else {
        setError(toUserMessage(cause, t("auth.ownerCreateFailed")));
      }
    } finally {
      setBusy(false);
    }
  }

  async function submitLogin(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      const result = await loginWithPassword({
        username: username.trim(),
        password,
        session_transport: "cookie",
      });
      setSession(result.owner.username);
      navigate("/");
    } catch (cause) {
      if (cause instanceof ApiError && cause.code === "LOGIN_RATE_LIMITED") {
        setError(
          cause.retryAfterSeconds
            ? t("auth.loginRateLimited", { minutes: Math.max(1, Math.ceil(cause.retryAfterSeconds / 60)) })
            : t("auth.loginRateLimitedNoWait"),
        );
      } else {
        setError(toUserMessage(cause, t("auth.loginFailed")));
      }
    } finally {
      setBusy(false);
    }
  }

  function logout() {
    if (authenticated && !skipAuth) {
      void logoutSession().catch(() => undefined);
    }
    clearSession();
    setSkipAuth(false);
    setView("probe");
    navigate("/");
  }

  if (view === "shell") {
    return <ApplicationShell onLogout={logout} />;
  }

  if (view === "probe" && !error) {
    return (
      <GateShell title={t("auth.connecting")}>
        <StateBlock kind="busy" title={t("auth.readingStatus")} />
      </GateShell>
    );
  }

  if (view === "probe" && error) {
    return (
      <GateShell title={t("auth.unreachable")} error={error}>
        <button
          type="button"
          onClick={() => void probe()}
          className="auth-retry"
        >
          {t("auth.retry")}
        </button>
      </GateShell>
    );
  }

  if (view === "bootstrap") {
    return (
      <GateShell title={t("auth.bootstrapTitle")} subtitle={t("auth.bootstrapSubtitle")} error={error}>
        <form onSubmit={submitOwner} className="auth-form">
          <Field
            label={t("auth.username")}
            hint={t("auth.usernameHint")}
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            required
            maxLength={128}
          />
          <Field
            label={t("auth.passwordNew")}
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            minLength={12}
            maxLength={1024}
          />
          <SubmitButton busy={busy} busyLabel={t("auth.creating")} label={t("auth.createOwner")} />
        </form>
      </GateShell>
    );
  }

  return (
    <GateShell title={t("auth.loginTitle")} subtitle={t("auth.loginSubtitle")} error={error}>
      <form onSubmit={submitLogin} className="auth-form">
        <Field label={t("auth.username")} value={username} onChange={(e) => setUsername(e.target.value)} required />
        <Field label={t("auth.password")} type="password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        <SubmitButton busy={busy} busyLabel={t("auth.loggingIn")} label={t("auth.login")} />
        <p className="auth-recovery auth-recovery-row">
          {t("auth.forgot")}
          <HelpTip label={t("auth.forgotHelpLabel")}>
            <p className="tw:m-0">
              {t("auth.recoveryPrefix")} <code>docker compose exec app /app/bootstrap reset-password</code> {t("auth.recoverySuffix")}
            </p>
          </HelpTip>
        </p>
      </form>
    </GateShell>
  );
}

function GateShell({
  title,
  subtitle,
  error,
  children,
}: {
  title: string;
  subtitle?: string;
  error?: string;
  children: React.ReactNode;
}) {
  return (
    <main className="auth-gate">
      <div className="auth-glow">
        <div className="auth-glow-orb" />
      </div>

      <section className="auth-card">
        <header className="auth-header">
          <div className="auth-brand">
            <span className="auth-brand-name">{t("nav.productName")}</span>
          </div>
          <h1 className="auth-title">{title}</h1>
          {subtitle && <p className="auth-subtitle">{subtitle}</p>}
        </header>

        {error && <StateBlock kind="error" title={error} className="auth-error" />}
        {children}
      </section>
    </main>
  );
}

function SubmitButton({ busy, busyLabel, label }: { busy: boolean; busyLabel: string; label: string }) {
  return (
    <button
      type="submit"
      className="auth-submit"
      disabled={busy}
    >
      {busy ? busyLabel : label}
    </button>
  );
}

function Field({ label, hint, ...props }: React.InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: string }) {
  const control = (
    <label className={hint ? "auth-field-label" : "auth-field"}>
      <span>{label}</span>
      <Input {...props} />
    </label>
  );
  if (!hint) return control;
  // The hint sits outside the label so it never enters the input's accessible name.
  return (
    <div className="auth-field">
      {control}
      <span className="auth-recovery">{hint}</span>
    </div>
  );
}
