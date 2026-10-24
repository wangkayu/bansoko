import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ApiError } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { AuthGate } from "./AuthGate";
import { useSessionStore } from "../../shell/sessionStore";
import { createTestQueryClient } from "../../testing/renderWithQuery";

// AuthGate: the entry gateway's four states (probe -> bootstrap / login -> shell). Covers:
//   skip-auth pass-through, bootstrap account creation, login, and failure degradation
//   (connection failure / 409).

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

let fetchMock: ReturnType<typeof vi.fn<FetchStub>>;

const BOOTSTRAP_REQUIRED = JSON.stringify({ state: "bootstrap_required" });
const INSTANCE_READY = JSON.stringify({ state: "ready" });

function stubFetch(options: {
  mode?: "skip-auth" | "bootstrap" | "login";
  bootstrapState?: "bootstrap_required" | "ready";
  bootstrapConflict?: boolean;
  loginFail?: boolean;
  loginRateLimited?: boolean;
  /** A live cookie session: the probe then opens the shell directly. */
  sessionOpen?: { username: string };
} = {}) {
  const mode = options.mode ?? "skip-auth";
  fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/v1/auth/session")) {
      // The credential is an HttpOnly cookie, so this probe is the only way
      // the page learns it is signed in.
      if (options.sessionOpen) {
        return new Response(
          JSON.stringify({ owner: { id: "o1", username: options.sessionOpen.username, role: "owner" }, expires_at: "2030-01-01T00:00:00Z" }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      return new Response(JSON.stringify({ code: "SESSION_REQUIRED", message: "required" }), {
        status: 401,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.endsWith("/api/v1/anime")) {
      // The anonymous probe: skip-auth answers 200, otherwise 401.
      if (mode === "skip-auth") {
        return new Response(JSON.stringify([]), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify({ code: "UNAUTHORIZED", message: "unauthorized" }), {
        status: 401,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.endsWith("/api/v1/bootstrap/status")) {
      return new Response(
        options.bootstrapState === "ready" ? INSTANCE_READY : BOOTSTRAP_REQUIRED,
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }
    if (url.endsWith("/api/v1/bootstrap/owner") && init?.method === "POST") {
      if (options.bootstrapConflict) {
        return new Response(JSON.stringify({ code: "OWNER_EXISTS", message: "already created" }), {
          status: 409,
          headers: { "Content-Type": "application/json" },
        });
      }
      // A cookie-transport response carries no token: the credential arrives
      // as Set-Cookie, which the browser applies on its own.
      return new Response(
        JSON.stringify({ expires_at: "2030-01-01T00:00:00Z", owner: { username: "admin" } }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }
    if (url.endsWith("/api/v1/auth/login") && init?.method === "POST") {
      if (options.loginRateLimited) {
        return new Response(JSON.stringify({ code: "LOGIN_RATE_LIMITED", message: "too many attempts" }), {
          status: 429,
          headers: { "Content-Type": "application/json", "Retry-After": "900" },
        });
      }
      if (options.loginFail) {
        return new Response(JSON.stringify({ code: "AUTH_FAILED", message: "bad credentials" }), {
          status: 401,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(
        JSON.stringify({ expires_at: "2030-01-01T00:00:00Z", owner: { username: "user" } }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }
    if (url.endsWith("/api/v1/events/stream")) {
      // The realtime invalidation stream: never resolves in tests.
      return new Promise<Response>(() => {});
    }
    return new Response(JSON.stringify([]), { status: 200, headers: { "Content-Type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetchMock);
}

function renderGate(client?: QueryClient) {
  return render(
    <MemoryRouter initialEntries={["/"]}>
      <QueryClientProvider client={client ?? createTestQueryClient()}>
        <AuthGate />
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("AuthGate", () => {
  beforeEach(() => {
    // Each test is independent of the localStorage session state.
    window.localStorage.clear();
    useSessionStore.setState({ authenticated: false, username: null });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("skip-auth 模式下匿名探测成功直接进入 shell", async () => {
    stubFetch({ mode: "skip-auth" });
    const { container } = renderGate();

    // In the shell state this renders the ApplicationShell -> TopNav brand link.
    expect(await screen.findByRole("link", { name: "Bansoko" })).toBeInTheDocument();
    expect(container.innerHTML).toContain("Bansoko");
    const animeCalls = fetchMock.mock.calls.filter(([input]) => String(input).endsWith("/api/v1/anime"));
    expect(animeCalls.length).toBeGreaterThan(0);
  });

  it("已有 cookie 会话时直接进入 shell，不显示登录表单", async () => {
    stubFetch({ mode: "login", bootstrapState: "ready", sessionOpen: { username: "owner" } });
    renderGate();

    expect(await screen.findByRole("link", { name: "Bansoko" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: t("auth.loginTitle") })).not.toBeInTheDocument();
  });

  it("bootstrap 需要时展示创建所有者表单并成功建号进入 shell", async () => {
    stubFetch({ mode: "bootstrap" });
    renderGate();

    expect(await screen.findByText(t("auth.bootstrapTitle"))).toBeInTheDocument();
    const [usernameInput, passwordInput] = screen.getAllByRole("textbox");
    await waitFor(() => expect(usernameInput).toBeInTheDocument());

    // The password field is a password input (not inside the textbox role).
    const password = screen.getByLabelText(t("auth.passwordNew"));
    await waitFor(() => expect(password).toBeInTheDocument());
  });

  it("首次设置只说设置管理员账号和密码，用户名长度提示在输入框外", async () => {
    stubFetch({ mode: "bootstrap" });
    renderGate();

    expect(await screen.findByText(t("auth.bootstrapSubtitle"))).toBeInTheDocument();
    expect(screen.getByText(t("auth.usernameHint"))).toBeInTheDocument();
    expect(screen.getByLabelText(t("auth.username"))).toHaveAttribute("maxlength", "128");
    expect(screen.queryByText(/V2|锁闭|默认账号/)).not.toBeInTheDocument();
  });

  it("登录页不出现 V2，恢复入口说明放进“?”提示", async () => {
    stubFetch({ mode: "login", bootstrapState: "ready" });
    renderGate();

    expect(await screen.findByRole("heading", { name: t("auth.loginTitle") })).toBeInTheDocument();
    expect(screen.queryByText(/V2/)).not.toBeInTheDocument();
    expect(screen.queryByText(/reset-password/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("auth.forgotHelpLabel") }));
    expect(await screen.findByText(/reset-password/)).toBeInTheDocument();
  });

  it("登录被限流时显示等待分钟数", async () => {
    stubFetch({ mode: "login", bootstrapState: "ready", loginRateLimited: true });
    renderGate();

    expect(await screen.findByRole("heading", { name: t("auth.loginTitle") })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(t("auth.username")), { target: { value: "owner" } });
    fireEvent.change(screen.getByLabelText(t("auth.password")), { target: { value: "wrong password 12" } });
    fireEvent.click(screen.getByRole("button", { name: t("auth.login") }));

    expect(await screen.findByText(t("auth.loginRateLimited", { minutes: 15 }))).toBeInTheDocument();
  });
});
