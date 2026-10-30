import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { SettingsAccount } from "./SettingsAccount";
import { useSessionStore } from "../../shell/sessionStore";
import { t } from "../../lib/i18n";

const mocks = vi.hoisted(() => ({ update: vi.fn() }));
vi.mock("../../lib/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/api/client")>()),
  updateAccount: mocks.update,
}));

function openDialog() {
  fireEvent.click(screen.getByRole("button", { name: t("settings.account.changeCredentials") }));
}

function fillCurrentPassword() {
  fireEvent.change(screen.getByLabelText(t("settings.account.currentPassword")), {
    target: { value: "original password 12" },
  });
}

describe("SettingsAccount", () => {
  beforeEach(() => {
    useSessionStore.setState({ authenticated: true, username: "owner" });
    mocks.update.mockReset().mockResolvedValue({
      owner: { username: "new-owner" },
      other_sessions_revoked: true,
    });
  });

  it("shows the current username and requires matching new passwords", async () => {
    render(<SettingsAccount />);
    expect(screen.getByText("owner")).toBeInTheDocument();

    openDialog();
    fillCurrentPassword();
    fireEvent.change(screen.getByLabelText(t("settings.account.newPassword")), {
      target: { value: "replacement password 12" },
    });
    fireEvent.change(screen.getByLabelText(t("settings.account.confirmPassword")), {
      target: { value: "different password 12" },
    });
    fireEvent.click(screen.getByRole("button", { name: t("settings.account.save") }));

    expect(await screen.findByText(t("settings.account.passwordMismatch"))).toBeInTheDocument();
    expect(mocks.update).not.toHaveBeenCalled();
  });

  it("rejects a save with no replacement username or password", async () => {
    render(<SettingsAccount />);
    openDialog();
    fireEvent.change(screen.getByLabelText(t("settings.account.newUsername")), { target: { value: "   " } });
    fillCurrentPassword();
    fireEvent.click(screen.getByRole("button", { name: t("settings.account.save") }));

    expect(await screen.findByText(t("settings.account.changeRequired"))).toBeInTheDocument();
    expect(mocks.update).not.toHaveBeenCalled();
  });

  it("updates the username and password in one request", async () => {
    render(<SettingsAccount />);
    openDialog();
    fireEvent.change(screen.getByLabelText(t("settings.account.newUsername")), { target: { value: "  new-owner  " } });
    fillCurrentPassword();
    fireEvent.change(screen.getByLabelText(t("settings.account.newPassword")), {
      target: { value: "replacement password 12" },
    });
    fireEvent.change(screen.getByLabelText(t("settings.account.confirmPassword")), {
      target: { value: "replacement password 12" },
    });
    fireEvent.click(screen.getByRole("button", { name: t("settings.account.save") }));

    await waitFor(() => expect(mocks.update).toHaveBeenCalledWith({
      current_password: "original password 12",
      username: "new-owner",
      new_password: "replacement password 12",
    }));
    await waitFor(() => expect(useSessionStore.getState().username).toBe("new-owner"));
  });

  it("omits a blank username when only the password changes", async () => {
    render(<SettingsAccount />);
    openDialog();
    fillCurrentPassword();
    fireEvent.change(screen.getByLabelText(t("settings.account.newPassword")), {
      target: { value: "replacement password 12" },
    });
    fireEvent.change(screen.getByLabelText(t("settings.account.confirmPassword")), {
      target: { value: "replacement password 12" },
    });
    fireEvent.click(screen.getByRole("button", { name: t("settings.account.save") }));

    await waitFor(() => expect(mocks.update).toHaveBeenCalledWith({
      current_password: "original password 12",
      new_password: "replacement password 12",
    }));
  });
});
