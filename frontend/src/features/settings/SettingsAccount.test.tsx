import { beforeEach, describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { SettingsAccount } from "./SettingsAccount";
import { useSessionStore } from "../../shell/sessionStore";
import { t } from "../../lib/i18n";

describe("SettingsAccount", () => {
  beforeEach(() => {
    useSessionStore.setState({ authenticated: true, username: "owner" });
  });

  it("shows the current username and requires matching new passwords", async () => {
    render(<SettingsAccount />);
    expect(screen.getByText("owner")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: t("settings.account.changePassword") }));
    fireEvent.change(screen.getByLabelText(t("settings.account.currentPassword")), {
      target: { value: "original password 12" },
    });
    fireEvent.change(screen.getByLabelText(t("settings.account.newPassword")), {
      target: { value: "replacement password 12" },
    });
    fireEvent.change(screen.getByLabelText(t("settings.account.confirmPassword")), {
      target: { value: "different password 12" },
    });
    fireEvent.click(screen.getByRole("button", { name: t("settings.account.save") }));

    expect(await screen.findByText(t("settings.account.passwordMismatch"))).toBeInTheDocument();
  });

  it("requires a non-blank new username before submitting a rename", async () => {
    render(<SettingsAccount />);
    fireEvent.click(screen.getByRole("button", { name: t("settings.account.changeUsername") }));
    fireEvent.change(screen.getByLabelText(t("settings.account.newUsername")), { target: { value: "   " } });
    fireEvent.change(screen.getByLabelText(t("settings.account.currentPassword")), {
      target: { value: "original password 12" },
    });
    fireEvent.click(screen.getByRole("button", { name: t("settings.account.save") }));

    expect(await screen.findByText(t("settings.account.newUsernameRequired"))).toBeInTheDocument();
  });
});
