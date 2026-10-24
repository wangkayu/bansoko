import { Component, type ReactNode } from "react";
import { AlertTriangle } from "lucide-react";
import { Button } from "./base/controls";
import { ErrorDetails } from "./base/ErrorDetails";
import { t } from "../lib/i18n";

// ErrorBoundary — the shell-level crash surface. A single uncaught render error
// must never white-screen the SPA: show what happened and offer a retry
// (re-render children) plus a hard reload. Kept as a class component because
// error boundaries require lifecycle methods.

interface ErrorBoundaryState {
  error: Error | null;
}

export class ErrorBoundary extends Component<{ children: ReactNode }, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, info: { componentStack?: string | null }) {
    console.error("[v2] uncaught render error", error, info.componentStack);
  }

  private handleRetry = () => {
    this.setState({ error: null });
  };

  private handleReload = () => {
    window.location.reload();
  };

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;
    return (
      <div
        style={{
          display: "flex",
          minHeight: "100vh",
          alignItems: "center",
          justifyContent: "center",
          backgroundColor: "hsl(var(--background))",
          padding: "1.5rem",
        }}
      >
        <div
          style={{
            width: "100%",
            maxWidth: "28rem",
            borderRadius: "0.5rem",
            border: "1px solid hsl(var(--border))",
            backgroundColor: "hsl(var(--card))",
            padding: "1.5rem",
            textAlign: "center",
            boxShadow: "0 1px 2px 0 rgb(0 0 0 / 0.05)",
          }}
        >
          <span
            style={{
              marginLeft: "auto",
              marginRight: "auto",
              marginBottom: "0.75rem",
              display: "flex",
              height: "2.5rem",
              width: "2.5rem",
              alignItems: "center",
              justifyContent: "center",
              borderRadius: "9999px",
              backgroundColor: "hsl(var(--status-offline) / 0.1)",
            }}
          >
            <AlertTriangle style={{ height: "1.25rem", width: "1.25rem", color: "hsl(var(--status-offline))" }} />
          </span>
          <p className="text-body" style={{ fontWeight: 500, color: "hsl(var(--foreground))" }}>
            {t("ui.errorBoundary.title")}
          </p>
          <p role="alert" className="text-caption" style={{ marginTop: "0.25rem", overflowWrap: "break-word", color: "hsl(var(--muted-foreground))" }}>
            {t("ui.errorBoundary.hint")}
          </p>
          {error.message ? <ErrorDetails details={{ message: error.message, fields: [] }} className="tw:mt-2 tw:justify-items-center" /> : null}
          <div style={{ marginTop: "1rem", display: "flex", alignItems: "center", justifyContent: "center", gap: "0.5rem" }}>
            <Button variant="secondary" onClick={this.handleRetry}>
              {t("ui.errorBoundary.retry")}
            </Button>
            <Button variant="primary" onClick={this.handleReload}>
              {t("ui.errorBoundary.reload")}
            </Button>
          </div>
        </div>
      </div>
    );
  }
}
