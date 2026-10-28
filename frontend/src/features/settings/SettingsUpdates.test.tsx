import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import { SettingsUpdates } from "./SettingsUpdates";
import { updateKeys } from "../../lib/queryKeys";
import type { EmbeddedUpdateStatus } from "../../lib/api/client";
import { getLocale, setLocale, t } from "../../lib/i18n";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

let fetchMock: ReturnType<typeof vi.fn>;

type FetchHandler = (url: string, init?: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: FetchHandler) {
  fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => handler(String(input), init));
  vi.stubGlobal("fetch", fetchMock);
}

function renderUpdates(seedStatus?: EmbeddedUpdateStatus) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  if (seedStatus) client.setQueryData(updateKeys.status, seedStatus);
  return render(
    <QueryClientProvider client={client}>
      <SettingsUpdates />
    </QueryClientProvider>,
  );
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve;
    reject = nextReject;
  });
  return { promise, resolve, reject };
}

const candidate = {
  release_id: "release-20260829-01",
  version: "0.9.0-rc.5",
  release_sequence: 42,
  published_at: "2026-08-29T01:00:00Z",
  target_migration_version: "040_online_update",
  required_downtime: true,
  backup_required: true,
  rollback_class: "restore_required",
  manifest_digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  payload_size: 1024,
};

const updateCheck = {
  status: "update_available",
  current: {
    version: "0.9.0-rc.4",
    release_id: "release-20260828-01",
    image_compatibility: "base-v3",
  },
  candidate,
  compatibility: {
    kind: "overlay",
    min_image_version: "base-v3",
    reason: "The candidate can be applied as an embedded overlay.",
  },
  checked_at: "2026-08-29T02:00:00Z",
};

const acceptedStatus = {
  operation_id: "embedded-update-1",
  channel: "embedded",
  step: "accepted",
  target_release_id: candidate.release_id,
  target_version: candidate.version,
  target_schema_version: candidate.target_migration_version,
  created_at: "2026-08-29T02:01:00Z",
  updated_at: "2026-08-29T02:01:00Z",
};
const pendingBackupStatus = { ...acceptedStatus, step: "pending_backup" };

describe("SettingsUpdates", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    window.sessionStorage.clear();
  });

  it("loads an empty status without checking until the user asks", async () => {
    stubFetch((url) => {
      if (url.endsWith("/update/status")) return jsonResponse({ code: "UPDATE_NOT_FOUND" }, 404);
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    expect(await screen.findByRole("button", { name: t("settings.updates.check") })).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/update/check"))).toBe(false);
  });

  it.each([
    ["pending_migration", t("settings.updates.step.pending_migration")],
    ["migration_succeeded", t("settings.updates.step.migration_succeeded")],
    ["promoted", t("settings.updates.step.promoted")],
    ["readiness_failed", t("settings.updates.step.readiness_failed")],
    ["rollback_started", t("settings.updates.step.rollback_started")],
    ["database_restored", t("settings.updates.step.database_restored")],
    ["fence_closure_pending", t("settings.updates.step.fence_closure_pending")],
  ] as const)("shows the %s step as current update progress", async (step, label) => {
    stubFetch((url) => {
      if (url.endsWith("/update/status")) return jsonResponse({ ...pendingBackupStatus, step });
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    expect(await screen.findByText(label)).toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.cardCurrent"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.leaveSafe"))).toBeInTheDocument();
  });

  it("keeps pending_backup active while using the existing unknown-step label", async () => {
    stubFetch((url) => {
      if (url.endsWith("/update/status")) return jsonResponse(pendingBackupStatus);
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    expect(await screen.findByText(t("settings.updates.step.unknown"))).toBeInTheDocument();
    expect(screen.queryByText(/1\.5/)).not.toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.cardCurrent"))).toBeInTheDocument();
  });

  it.each(["fetched", "staged", "maintenance_entered", "backup_verified"] as const)(
    "fails closed for obsolete %s status rather than showing active progress",
    async (step) => {
      stubFetch((url) => {
        if (url.endsWith("/update/status")) return jsonResponse({ ...pendingBackupStatus, step });
        return jsonResponse({ code: "NOT_FOUND" }, 404);
      });
      renderUpdates();

      expect(await screen.findByText(t("settings.updates.unknownTitle"))).toBeInTheDocument();
      expect(screen.queryByText(t("settings.updates.cardCurrent"))).not.toBeInTheDocument();
      expect(screen.queryByText(t("settings.updates.leaveSafe"))).not.toBeInTheDocument();
      expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith("/update/status"))).toHaveLength(1);
    },
  );

  it.each(["succeeded", "failed", "rolled_back"] as const)("keeps manual check visible for historical %s", async (step) => {
    stubFetch((url) => {
      if (url.endsWith("/update/status")) {
        return jsonResponse({ ...pendingBackupStatus, step, updated_at: "2026-08-29T02:05:00Z" });
      }
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    expect(await screen.findByText(t("settings.updates.cardLast"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.updates.check") })).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/update/check"))).toBe(false);
  });

  it("blocks checking while rollback is blocked", async () => {
    stubFetch((url) => {
      if (url.endsWith("/update/status")) {
        return jsonResponse({ ...pendingBackupStatus, step: "rollback_blocked", last_error: "需要部署侧恢复" });
      }
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    expect(await screen.findByText(t("settings.updates.step.rollback_blocked"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("settings.updates.check") })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/update/check"))).toBe(false);
  });

  it("shows a focused check retry after an error and invokes check again", async () => {
    const user = userEvent.setup();
    let checks = 0;
    stubFetch((url) => {
      if (url.endsWith("/update/status")) return jsonResponse({}, 404);
      if (url.endsWith("/update/check")) {
        checks += 1;
        return jsonResponse({ code: "CHECK_UNAVAILABLE", message: "更新源暂不可用" }, 503);
      }
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    await user.click(await screen.findByRole("button", { name: t("settings.updates.check") }));
    expect(await screen.findByRole("alert")).toHaveTextContent(t("settings.updates.checkFailed"));
    const retry = screen.getByRole("button", { name: t("settings.updates.retryCheck") });
    expect(retry).toHaveFocus();
    expect(checks).toBe(1);

    await user.click(retry);
    await waitFor(() => expect(checks).toBe(2));
    expect(screen.getByRole("button", { name: t("settings.updates.retryCheck") })).toBeInTheDocument();
  });

  it("retains active progress during disconnect and refetches on online/focus", async () => {
    const active = { ...pendingBackupStatus, step: "pending_migration", updated_at: "2026-08-29T02:03:00Z" };
    const statusResponse = deferred<Response>();
    let statusReads = 0;
    stubFetch((url) => {
      if (url.endsWith("/update/status")) {
        statusReads += 1;
        if (statusReads === 1) return jsonResponse(active);
        if (statusReads === 2) return statusResponse.promise;
        return jsonResponse(active);
      }
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    expect(await screen.findByText(t("settings.updates.step.pending_migration"))).toBeInTheDocument();
    const statusCalls = () => fetchMock.mock.calls.filter(([url]) => String(url).endsWith("/update/status"));
    expect(statusCalls()).toHaveLength(1);

    fireEvent(window, new Event("online"));
    await waitFor(() => expect(statusCalls()).toHaveLength(2));
    statusResponse.reject(new Error("disconnected"));
    await waitFor(() => expect(screen.getByText(t("settings.updates.reconnecting"))).toBeInTheDocument());
    expect(screen.getByText(t("settings.updates.step.pending_migration"))).toBeInTheDocument();

    fireEvent(window, new Event("focus"));
    await waitFor(() => expect(statusCalls()).toHaveLength(3));
    expect(screen.getByText(t("settings.updates.step.pending_migration"))).toBeInTheDocument();
  });

  it("shows one receipt on active-to-succeeded and not for initial history", async () => {
    const succeeded = { ...pendingBackupStatus, step: "succeeded", updated_at: "2026-08-29T02:05:00Z" };
    let statusReads = 0;
    stubFetch((url) => {
      if (url.endsWith("/update/status")) {
        statusReads += 1;
        return jsonResponse(statusReads === 1 ? { ...pendingBackupStatus, step: "pending_migration" } : succeeded);
      }
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    const firstRender = renderUpdates();

    expect(await screen.findByText(t("settings.updates.step.pending_migration"))).toBeInTheDocument();
    expect(window.sessionStorage.getItem("pending_update_operation_id")).toBe(pendingBackupStatus.operation_id);
    expect(window.sessionStorage.getItem("operation_id")).toBeNull();

    fireEvent(window, new Event("focus"));
    expect(await screen.findByText(t("settings.updates.receiptDone"))).toBeInTheDocument();
    await waitFor(() => expect(window.sessionStorage.getItem("pending_update_operation_id")).toBeNull());
    expect(screen.getAllByText(t("settings.updates.receiptDone"))).toHaveLength(1);
    firstRender.unmount();

    window.sessionStorage.clear();
    renderUpdates();
    expect(await screen.findByText(t("settings.updates.cardLast"))).toBeInTheDocument();
    expect(screen.queryByText(t("settings.updates.receiptDone"))).not.toBeInTheDocument();
  });
  it("drops the pre-update check result once the operation ends", async () => {
    const user = userEvent.setup();
    const succeeded = { ...pendingBackupStatus, step: "succeeded", updated_at: "2026-08-29T02:05:00Z" };
    let statusReads = 0;
    stubFetch((url) => {
      if (url.endsWith("/update/status")) {
        statusReads += 1;
        if (statusReads === 1) return jsonResponse({ code: "UPDATE_NOT_FOUND" }, 404);
        if (statusReads === 2) return jsonResponse(acceptedStatus);
        return jsonResponse(succeeded);
      }
      if (url.endsWith("/update/check")) return jsonResponse(updateCheck);
      if (url.endsWith("/update/admit")) return jsonResponse(acceptedStatus, 201);
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    await user.click(await screen.findByRole("button", { name: t("settings.updates.check") }));
    await user.click(await screen.findByRole("button", { name: t("settings.updates.update") }));
    await user.click(await screen.findByRole("button", { name: t("settings.updates.confirmAction") }));
    expect(await screen.findByText(t("settings.updates.step.accepted"))).toBeInTheDocument();

    fireEvent(window, new Event("focus"));
    expect(await screen.findByText(t("settings.updates.receiptDone"))).toBeInTheDocument();
    expect(screen.queryByText(t("settings.updates.candidateTitle"))).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("settings.updates.update") })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.updates.check") })).toBeInTheDocument();
  });

  it("performs one read-only check after the user asks", async () => {
    const user = userEvent.setup();
    const checkResponse = deferred<Response>();
    stubFetch((url) => {
      if (url.endsWith("/update/status")) {
        return jsonResponse({ code: "UPDATE_NOT_FOUND", message: "not found" }, 404);
      }
      if (url.endsWith("/update/check")) return checkResponse.promise;
      return jsonResponse({ code: "NOT_FOUND", message: "not found" }, 404);
    });
    renderUpdates();

    const checkButton = await screen.findByRole("button", { name: t("settings.updates.check") });
    await user.click(checkButton);

    expect(
      fetchMock.mock.calls.filter(([url, init]) => String(url).endsWith("/update/check") && init?.method === "GET"),
    ).toHaveLength(1);
    expect(screen.queryByRole("button", { name: t("settings.updates.check") })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url, init]) => init?.method === "POST" || String(url).includes("/deployment/"))).toBe(false);

    checkResponse.resolve(jsonResponse(updateCheck));
    expect(await screen.findByText(t("settings.updates.candidateTitle"))).toBeInTheDocument();
    expect(screen.getByText(candidate.version)).toBeInTheDocument();
    expect(screen.queryByText("检查并更新")).not.toBeInTheDocument();
    expect(screen.queryByText("重新检查")).not.toBeInTheDocument();
    expect(screen.queryByText("自动升级")).not.toBeInTheDocument();
    expect(screen.queryByText("选择版本")).not.toBeInTheDocument();
  });

  it("shows only the checked candidate and its update facts", async () => {
    const user = userEvent.setup();
    stubFetch((url) => {
      if (url.endsWith("/update/status")) return jsonResponse({}, 404);
      if (url.endsWith("/update/check")) return jsonResponse(updateCheck);
      return jsonResponse({ code: "NOT_FOUND", message: "not found" }, 404);
    });
    renderUpdates();

    await user.click(await screen.findByRole("button", { name: t("settings.updates.check") }));
    expect(await screen.findByText(t("settings.updates.currentTitle"))).toBeInTheDocument();
    expect(screen.queryByText("发布编号")).not.toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.publishedAt"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.migration"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.downtimeYes"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.backupYes"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.rollbackRestore"))).toBeInTheDocument();
    expect(screen.queryByText(candidate.release_id)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.updates.update") })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "升级到此版本" })).not.toBeInTheDocument();
  });
  describe("release notes", () => {
    const notes = (version: string, zh: string, en: string) => ({
      version,
      published_at: "2026-08-29T01:00:00Z",
      notes: { "zh-CN": zh, en },
    });

    async function checkWith(body: unknown) {
      const user = userEvent.setup();
      stubFetch((url) => {
        if (url.endsWith("/update/status")) return jsonResponse({}, 404);
        if (url.endsWith("/update/check")) return jsonResponse(body);
        return jsonResponse({ code: "NOT_FOUND", message: "not found" }, 404);
      });
      renderUpdates();
      await user.click(await screen.findByRole("button", { name: t("settings.updates.check") }));
      await screen.findByText(t("settings.updates.currentTitle"));
      return user;
    }

    it("lists candidate notes newest first in the UI language", async () => {
      const original = getLocale();
      try {
        setLocale("zh-CN", { persist: false });
        await checkWith({
          ...updateCheck,
          candidate: {
            ...candidate,
            release_notes: [notes("0.9.0", "## 新增\n- 中文甲", "## Added\n- English A"), notes("0.8.0", "中文乙", "English B")],
          },
        });
        expect(screen.getByText("中文甲")).toBeInTheDocument();
        expect(screen.getByText("中文乙")).toBeInTheDocument();
        expect(screen.queryByText("English A")).not.toBeInTheDocument();
        const first = screen.getByText("中文甲");
        const second = screen.getByText("中文乙");
        expect(first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        act(() => setLocale("en", { persist: false }));
        expect(await screen.findByText("English A")).toBeInTheDocument();
        expect(screen.queryByText("中文甲")).not.toBeInTheDocument();
      } finally {
        setLocale(original, { persist: false });
      }
    });

    it("shows the empty state when the candidate has no notes", async () => {
      await checkWith(updateCheck);
      expect(screen.getByText(t("settings.updates.notesEmpty"))).toBeInTheDocument();
    });

    it("opens the current version notes, or the empty state when absent", async () => {
      const user = await checkWith({
        ...updateCheck,
        current: { ...updateCheck.current, release_notes: notes("0.9.0-rc.4", "当前说明正文", "Current body") },
      });
      expect(screen.queryByText("当前说明正文")).not.toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: t("settings.updates.notesShowCurrent") }));
      expect(screen.getByText(getLocale() === "zh-CN" ? "当前说明正文" : "Current body")).toBeInTheDocument();
    });

    it("shows the empty state for a current version without notes", async () => {
      const user = await checkWith(updateCheck);
      expect(screen.getAllByText(t("settings.updates.notesEmpty"))).toHaveLength(1);
      await user.click(screen.getByRole("button", { name: t("settings.updates.notesShowCurrent") }));
      expect(screen.getAllByText(t("settings.updates.notesEmpty"))).toHaveLength(2);
    });
  });

  it("refetches after the accepted response and displays only authoritative progress", async () => {
    const user = userEvent.setup();
    let statusReads = 0;
    stubFetch((url) => {
      if (url.endsWith("/update/status")) {
        statusReads += 1;
        if (statusReads === 1) return jsonResponse({ code: "UPDATE_NOT_FOUND" }, 404);
        if (statusReads === 2) return jsonResponse(acceptedStatus);
        return jsonResponse({ ...pendingBackupStatus, step: "pending_backup" });
      }
      if (url.endsWith("/update/check")) return jsonResponse(updateCheck);
      if (url.endsWith("/update/admit")) return jsonResponse(acceptedStatus, 201);
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    await user.click(await screen.findByRole("button", { name: t("settings.updates.check") }));
    await user.click(await screen.findByRole("button", { name: t("settings.updates.update") }));
    await user.click(await screen.findByRole("button", { name: t("settings.updates.confirmAction") }));

    expect(await screen.findByText(t("settings.updates.step.accepted"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.cardCurrent"))).toBeInTheDocument();
    fireEvent(window, new Event("focus"));
    await waitFor(() => expect(statusReads).toBeGreaterThanOrEqual(3));
    expect(await screen.findByText(t("settings.updates.step.unknown"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.cardCurrent"))).toBeInTheDocument();
  });

  it("polls accepted status through the maintenance disconnect without user focus", async () => {
    let reads = 0;
    stubFetch((url) => {
      if (url.endsWith("/update/status")) {
        reads += 1;
        if (reads === 1) return jsonResponse(acceptedStatus);
        if (reads === 2) return jsonResponse({}, 404);
        return jsonResponse({ ...acceptedStatus, step: "succeeded" });
      }
      return jsonResponse({}, 404);
    });
    vi.useFakeTimers();
    try {
      const { act } = await import("@testing-library/react");
      await act(async () => { renderUpdates(); await vi.advanceTimersByTimeAsync(1); });
      await act(async () => { await vi.advanceTimersByTimeAsync(10); });
      expect(screen.getByText(t("settings.updates.step.accepted"))).toBeInTheDocument();
      await act(async () => { await vi.advanceTimersByTimeAsync(1600); });
      await act(async () => { await vi.advanceTimersByTimeAsync(1600); });
      expect(screen.getByText(t("settings.updates.receiptDone"))).toBeInTheDocument();
      expect(reads).toBeGreaterThanOrEqual(3);
    } finally { vi.useRealTimers(); }
  });

  it("shows authoritative accepted status without claiming downloaded or verified progress", async () => {
    const user = userEvent.setup();
    let statusReads = 0;
    stubFetch((url) => {
      if (url.endsWith("/update/status")) return statusReads++ === 0
        ? jsonResponse({ code: "UPDATE_NOT_FOUND" }, 404)
        : jsonResponse(acceptedStatus);
      if (url.endsWith("/update/check")) return jsonResponse(updateCheck);
      if (url.endsWith("/update/admit")) return jsonResponse(acceptedStatus, 201);
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    await user.click(await screen.findByRole("button", { name: t("settings.updates.check") }));
    await user.click(await screen.findByRole("button", { name: t("settings.updates.update") }));
    await user.click(await screen.findByRole("button", { name: t("settings.updates.confirmAction") }));

    expect(await screen.findByText(t("settings.updates.step.accepted"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.cardCurrent"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.leaveSafe"))).toBeInTheDocument();
    expect(screen.queryByText("新版本已下载并验签")).not.toBeInTheDocument();
  });
  it("does not POST before confirmation, then admits the candidate with its digest and stable key", async () => {
    const user = userEvent.setup();
    let admitInit: RequestInit | undefined;
    stubFetch((url, init) => {
      if (url.endsWith("/update/status")) return jsonResponse({}, 404);
      if (url.endsWith("/update/check")) return jsonResponse(updateCheck);
      if (url.endsWith("/update/admit") && init?.method === "POST") {
        admitInit = init;
        return jsonResponse(acceptedStatus, 201);
      }
      return jsonResponse({ code: "NOT_FOUND", message: "not found" }, 404);
    });
    renderUpdates();

    await user.click(await screen.findByRole("button", { name: t("settings.updates.check") }));
    await user.click(await screen.findByRole("button", { name: t("settings.updates.update") }));
    expect(await screen.findByText(t("settings.updates.confirmTitle", { version: candidate.version }))).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);

    await user.click(screen.getByRole("button", { name: t("settings.updates.confirmAction") }));
    await waitFor(() => expect(admitInit).toBeDefined());

    const admitBody = JSON.parse(String(admitInit?.body));
    expect(admitBody).toEqual({
      target_release_id: candidate.release_id,
      manifest_digest: candidate.manifest_digest,
    });
    const key = new Headers(admitInit?.headers).get("Idempotency-Key");
    expect(key).toMatch(/.+/);
    expect(await screen.findByRole("button", { name: t("settings.updates.check") })).toBeInTheDocument();
  });

  it("adopts the active operation after UPDATE_LOCKED reconciliation", async () => {
    const user = userEvent.setup();
    let statusReads = 0;
    let admitCalls = 0;
    stubFetch((url, init) => {
      if (url.endsWith("/update/status")) {
        statusReads += 1;
        return statusReads === 1 ? jsonResponse({}, 404) : jsonResponse(pendingBackupStatus);
      }
      if (url.endsWith("/update/check")) return jsonResponse(updateCheck);
      if (url.endsWith("/update/admit") && init?.method === "POST") {
        admitCalls += 1;
        return jsonResponse({ code: "UPDATE_LOCKED", message: "已有更新任务" }, 409);
      }
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    await user.click(await screen.findByRole("button", { name: t("settings.updates.check") }));
    await user.click(await screen.findByRole("button", { name: t("settings.updates.update") }));
    await user.click(await screen.findByRole("button", { name: t("settings.updates.confirmAction") }));

    expect(await screen.findByText(t("settings.updates.cardCurrent"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.step.unknown"))).toBeInTheDocument();
    expect(screen.queryByText(t("settings.updates.submitFailed"))).not.toBeInTheDocument();
    expect(screen.queryByText(t("settings.updates.confirmTitle", { version: candidate.version }))).not.toBeInTheDocument();
    expect(vi.mocked(toast.error)).not.toHaveBeenCalled();
    expect(admitCalls).toBe(1);
  });

  it("reuses the same idempotency key after admission reconciliation finds no active operation", async () => {
    const user = userEvent.setup();
    let statusReads = 0;
    const admitKeys: string[] = [];
    let admitCalls = 0;
    stubFetch((url, init) => {
      if (url.endsWith("/update/status")) {
        statusReads += 1;
        return jsonResponse({}, 404);
      }
      if (url.endsWith("/update/check")) return jsonResponse(updateCheck);
      if (url.endsWith("/update/admit") && init?.method === "POST") {
        admitCalls += 1;
        admitKeys.push(new Headers(init.headers).get("Idempotency-Key") ?? "");
        return admitCalls === 1 ? jsonResponse({ code: "SERVICE_BUSY", message: "timeout" }, 503) : jsonResponse(acceptedStatus, 201);
      }
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    await user.click(await screen.findByRole("button", { name: t("settings.updates.check") }));
    await user.click(await screen.findByRole("button", { name: t("settings.updates.update") }));
    await user.click(await screen.findByRole("button", { name: t("settings.updates.confirmAction") }));
    expect(await screen.findByText(t("error.SERVICE_BUSY"))).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: t("settings.updates.update") }));
    await user.click(await screen.findByRole("button", { name: t("settings.updates.confirmAction") }));
    await waitFor(() => expect(admitCalls).toBe(2));
    expect(admitKeys[0]).toBeTruthy();
    expect(admitKeys[1]).toBe(admitKeys[0]);
    expect(await screen.findByRole("button", { name: t("settings.updates.check") })).toBeInTheDocument();
  });

  it("keeps the update surface fail-closed when status cannot be read", async () => {
    let statusReads = 0;
    stubFetch((url) => {
      if (url.endsWith("/update/status")) {
        statusReads += 1;
        return statusReads === 1
          ? jsonResponse({ code: "STATUS_UNAVAILABLE", message: "状态暂不可用" }, 503)
          : jsonResponse({ code: "UPDATE_NOT_FOUND" }, 404);
      }
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    expect(await screen.findByText(t("settings.updates.statusFailed"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("settings.updates.check") })).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: t("settings.updates.reread") }));
    expect(await screen.findByRole("button", { name: t("settings.updates.check") })).toBeInTheDocument();
    expect(statusReads).toBe(2);
  });

  it("keeps a cached historical status fail-closed until status recovers", async () => {
    const statusResponse = deferred<Response>();
    let statusReads = 0;
    let activeSnapshot: EmbeddedUpdateStatus | null = null;
    stubFetch((url) => {
      if (url.endsWith("/update/status")) {
        statusReads += 1;
        if (statusReads === 1) {
          activeSnapshot = { ...pendingBackupStatus, step: "succeeded" };
          return jsonResponse(activeSnapshot);
        }
        return statusResponse.promise;
      }
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    expect(await screen.findByText(t("settings.updates.cardLast"))).toBeInTheDocument();
    fireEvent(window, new Event("focus"));
    await waitFor(() => expect(statusReads).toBe(2));
    expect(screen.queryByRole("button", { name: t("settings.updates.check") })).not.toBeInTheDocument();

    statusResponse.reject(new Error("status unavailable"));
    expect(await screen.findByText(t("settings.updates.statusFailed"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("settings.updates.check") })).not.toBeInTheDocument();
  });

  it("fails closed for an unknown journal step", async () => {
    stubFetch((url) => {
      if (url.endsWith("/update/status")) return jsonResponse({ ...pendingBackupStatus, step: "future_step" });
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    expect(await screen.findByText(t("settings.updates.unknownTitle"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("settings.updates.check") })).not.toBeInTheDocument();
  });

  it("does not expose check while status refetch is pending", async () => {
    const refetchResponse = deferred<Response>();
    let statusReads = 0;
    stubFetch((url) => {
      if (url.endsWith("/update/status")) {
        statusReads += 1;
        return statusReads === 1
          ? jsonResponse({ ...pendingBackupStatus, step: "succeeded" })
          : refetchResponse.promise;
      }
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    expect(await screen.findByRole("button", { name: t("settings.updates.check") })).toBeInTheDocument();
    fireEvent(window, new Event("focus"));
    await waitFor(() => expect(statusReads).toBe(2));
    expect(screen.queryByRole("button", { name: t("settings.updates.check") })).not.toBeInTheDocument();
    refetchResponse.resolve(jsonResponse({ ...pendingBackupStatus, step: "succeeded" }));
    expect(await screen.findByRole("button", { name: t("settings.updates.check") })).toBeInTheDocument();
  });

  it("recovers the no-record state from status 404", async () => {
    stubFetch((url) => {
      if (url.endsWith("/update/status")) return jsonResponse({ code: "UPDATE_NOT_FOUND" }, 404);
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    expect(await screen.findByText(t("settings.updates.idleTitle"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.updates.check") })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("recovers and polls an active update status without using a supervisor endpoint", async () => {
    stubFetch((url) => {
      if (url.endsWith("/update/status")) {
        return jsonResponse({
          ...pendingBackupStatus,
          step: "pending_migration",
          updated_at: "2026-08-29T02:03:00Z",
        });
      }
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    expect(await screen.findByText(t("settings.updates.cardCurrent"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.step.pending_migration"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.updates.leaveSafe"))).toBeInTheDocument();
    expect(fetchMock.mock.calls.every(([url]) => !String(url).includes("/deployment/"))).toBe(true);
  });

  it("recovers a terminal status and shows the final conclusion", async () => {
    stubFetch((url) => {
      if (url.endsWith("/update/status")) {
        return jsonResponse({
          ...pendingBackupStatus,
          step: "succeeded",
          updated_at: "2026-08-29T02:05:00Z",
        });
      }
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    // Success is expressed only by the update-complete status marker, with no extra verdict line repeating it.
    expect(await screen.findByText(t("settings.updates.step.succeeded"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.updates.check") })).toBeInTheDocument();
    expect(screen.queryByText(t("settings.updates.leaveSafe"))).not.toBeInTheDocument();
  });

  it("shows bootstrap-specific base image guidance and blocks update admission", async () => {
    const user = userEvent.setup();
    const bootstrapReason = "This candidate requires embedded-bootstrap-v2; this instance runs embedded-bootstrap-v1. Switch to a base image that carries the required bootstrap.";
    const bootstrapCheck = {
      ...updateCheck,
      status: "base_image_required",
      compatibility: {
        kind: "base_image_required",
        min_image_version: "base-v1",
        reason: bootstrapReason,
      },
    };
    stubFetch((url) => {
      if (url.endsWith("/update/status")) return jsonResponse({}, 404);
      if (url.endsWith("/update/check")) return jsonResponse(bootstrapCheck);
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    await user.click(await screen.findByRole("button", { name: t("settings.updates.check") }));
    expect(await screen.findByText(t("settings.updates.imageOutdated"))).toBeInTheDocument();
    expect(screen.queryByText(bootstrapReason)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("settings.updates.update") })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
  });

  it("shows immutable image guidance and no update button for base_image_required", async () => {
    const user = userEvent.setup();
    const baseImageCheck = {
      ...updateCheck,
      status: "base_image_required",
      compatibility: {
        kind: "base_image_required",
        min_image_version: "base-v4",
        reason: "A newer base image is required.",
      },
    };
    stubFetch((url) => {
      if (url.endsWith("/update/status")) return jsonResponse({}, 404);
      if (url.endsWith("/update/check")) return jsonResponse(baseImageCheck);
      return jsonResponse({ code: "NOT_FOUND" }, 404);
    });
    renderUpdates();

    await user.click(await screen.findByRole("button", { name: t("settings.updates.check") }));
    expect(await screen.findByText(t("settings.updates.imageOutdated"))).toBeInTheDocument();
    expect(screen.queryByText("A newer base image is required.")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("settings.updates.update") })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
  });
});
