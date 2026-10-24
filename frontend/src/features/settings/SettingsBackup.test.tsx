import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import { SettingsBackup } from "./SettingsBackup";
import { readBackupFileHeader, waitForRestart } from "./backupFile";
import { t } from "../../lib/i18n";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type FetchHandler = (url: string, init?: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: FetchHandler) {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => handler(String(input), init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function renderBackup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SettingsBackup />
    </QueryClientProvider>,
  );
}

const info = { schema_version: "038", export_available: true, restore_available: true, restore_unavailable_reason: null };

function backupFile(schemaVersion: string, name = "bansoko-20261020T010203Z-schema038.bansoko-backup"): File {
  const header = JSON.stringify({
    app: "bansoko",
    format: 1,
    schema_version: schemaVersion,
    created_at: "2026-10-20T01:02:03Z",
    payload_bytes: 4,
    payload_sha256: "0".repeat(64),
  });
  return new File([`BANSOKO-BACKUP/1\n${header}\n`, new Uint8Array([0x50, 0x47, 0x44, 0x4d])], name);
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe("readBackupFileHeader", () => {
  it("reads the schema number and creation time", async () => {
    expect(await readBackupFileHeader(backupFile("038_drop_backup_runs.sql"))).toEqual({
      schemaNumber: "038",
      createdAt: "2026-10-20T01:02:03Z",
    });
  });

  it("rejects a file without the signature", async () => {
    expect(await readBackupFileHeader(new File(["PGDMP not ours"], "x.dump"))).toBeNull();
  });
});

describe("waitForRestart", () => {
  it("reports restored once the old sessions are rejected", async () => {
    let calls = 0;
    stubFetch(() => {
      calls += 1;
      if (calls === 1) return Promise.reject(new TypeError("connection refused"));
      return jsonResponse({ code: "SESSION_INVALID", message: "expired" }, 401);
    });
    await expect(waitForRestart(1, 1000)).resolves.toBe("restored");
  });

  it("reports unchanged when the service answers again with the old session", async () => {
    let calls = 0;
    stubFetch(() => {
      calls += 1;
      if (calls === 1) return jsonResponse(info);
      if (calls === 2) return Promise.reject(new TypeError("connection refused"));
      return jsonResponse(info);
    });
    await expect(waitForRestart(1, 1000)).resolves.toBe("unchanged");
  });
});

describe("SettingsBackup", () => {
  it("shows the data version, both actions and the key note", async () => {
    stubFetch(() => jsonResponse(info));
    renderBackup();
    expect(await screen.findByText("038")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.backup.exportAction") })).toBeEnabled();
    expect(screen.getByRole("button", { name: t("settings.backup.restoreAction") })).toBeEnabled();
    expect(screen.getByText(t("settings.backup.keyNote"))).toBeInTheDocument();
  });

  it("explains why restore is off and disables it", async () => {
    stubFetch(() => jsonResponse({ ...info, restore_available: false, restore_unavailable_reason: "privileges_insufficient" }));
    renderBackup();
    expect(await screen.findByText(t("settings.backup.restoreUnavailable.privileges_insufficient"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.backup.restoreAction") })).toBeDisabled();
    expect(screen.queryByText("privileges_insufficient")).not.toBeInTheDocument();
  });

  it("prepares the export, then lets the browser download it by ticket", async () => {
    const fileName = "bansoko-20261020T010203Z-schema038.bansoko-backup";
    const ticket = "t".repeat(43);
    const clicked: { href: string; download: string }[] = [];
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push({ href: this.getAttribute("href") ?? "", download: this.download });
    });
    const fetchMock = stubFetch((url, init) => {
      if (url.endsWith("/backup/download-ticket") && init?.method === "POST") {
        return jsonResponse({ ticket, file_name: fileName, size_bytes: 4, expires_at: "2026-10-20T01:07:03Z" }, 201);
      }
      return jsonResponse(info);
    });
    renderBackup();
    await userEvent.click(await screen.findByRole("button", { name: t("settings.backup.exportAction") }));
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.backup.exported", { file: fileName })));
    expect(vi.mocked(toast.error).mock.calls).toEqual([]);
    expect(clicked).toEqual([{ href: `/api/v1/backup/download?ticket=${ticket}`, download: fileName }]);
    // The file itself never passes through fetch (and so never through page memory).
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/backup/download?"))).toBe(false);
    click.mockRestore();
  });

  it("refuses a backup of another data version before uploading", async () => {
    const fetchMock = stubFetch(() => jsonResponse(info));
    renderBackup();
    await screen.findByText("038");
    await userEvent.upload(screen.getByTestId("backup-file-input"), backupFile("030_cleanup_group_scope_generations.sql"));
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(t("settings.backup.versionMismatch", { backup: "030", current: "038" })),
    );
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/backup/restore"))).toBe(false);
    expect(screen.queryByText(t("settings.backup.confirmTitle"))).not.toBeInTheDocument();
  });

  it("uploads the file only after the replace confirmation", async () => {
    let uploaded: RequestInit | undefined;
    stubFetch((url, init) => {
      if (url.endsWith("/backup/restore")) {
        uploaded = init;
        return jsonResponse({ restarting: true, backup_created_at: "2026-10-20T01:02:03Z" }, 202);
      }
      return jsonResponse(info);
    });
    renderBackup();
    await screen.findByText("038");
    const file = backupFile("038_drop_backup_runs.sql");
    await userEvent.upload(screen.getByTestId("backup-file-input"), file);
    expect(await screen.findByText(t("settings.backup.confirmTitle"))).toBeInTheDocument();
    expect(uploaded).toBeUndefined();
    await userEvent.click(screen.getByRole("button", { name: t("settings.backup.confirmAction") }));
    expect(await screen.findByText(t("settings.backup.restarting"))).toBeInTheDocument();
    expect(uploaded?.method).toBe("POST");
    expect(uploaded?.body).toBe(file);
    expect(new Headers(uploaded?.headers).get("Content-Type")).toBe("application/octet-stream");
  });

  it("reports a refused restore with the catalogued message and stays usable", async () => {
    stubFetch((url) => {
      if (url.endsWith("/backup/restore")) {
        return jsonResponse({ code: "BACKUP_FILE_INVALID", message: "The file is not a valid backup of this app." }, 422);
      }
      return jsonResponse(info);
    });
    renderBackup();
    await screen.findByText("038");
    await userEvent.upload(screen.getByTestId("backup-file-input"), backupFile("038_drop_backup_runs.sql"));
    await userEvent.click(await screen.findByRole("button", { name: t("settings.backup.confirmAction") }));
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(vi.mocked(toast.error).mock.calls[0][0]).toBe(t("error.BACKUP_FILE_INVALID"));
    expect(await screen.findByRole("button", { name: t("settings.backup.restoreAction") })).toBeEnabled();
  });
});
