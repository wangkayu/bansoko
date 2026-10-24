import { useState } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { en } from "../lib/i18n/en";
import { zhCN } from "../lib/i18n/zh-CN";
import { getLocale, LOCALE_STORAGE_KEY, setLocale } from "../lib/i18n";
import { formatDateTime, formatTimeAgo } from "../lib/labels";
import { LocaleScope } from "../ui/LocaleScope";
import { AccountMenu } from "./AccountMenu";

vi.mock("next-themes", () => ({ useTheme: () => ({ theme: "light", setTheme: () => {} }) }));

function Harness() {
  const [open, setOpen] = useState(false);
  return <AccountMenu username="kayu" onLogout={() => {}} open={open} onOpenChange={setOpen} />;
}

// Stable references like route elements never re-render with a parent;
// LocaleScope remounts the subtree per language instead.
const stableChild = <Harness />;

describe("language switch", () => {
  it("switches the UI to English without a reload and remembers the choice", async () => {
    const user = userEvent.setup();
    render(<LocaleScope>{stableChild}</LocaleScope>);

    await user.click(screen.getByRole("button", { name: zhCN.nav.account }));
    await user.click(await screen.findByRole("menuitem", { name: zhCN.account.language }));
    fireEvent.click(await screen.findByRole("menuitemradio", { name: "English" }));

    await waitFor(() => expect(screen.getByRole("button", { name: en.nav.account })).toBeInTheDocument());
    expect(getLocale()).toBe("en");
    expect(document.documentElement.lang).toBe("en");
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("en");

    await user.click(screen.getByRole("button", { name: en.nav.account }));
    expect(await screen.findByRole("menuitem", { name: en.account.language })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: en.account.logout })).toBeInTheDocument();
  });

  it("formats dates and relative times in the active language", () => {
    const now = Date.parse("2026-10-11T12:00:00Z");
    expect(formatTimeAgo("2026-10-11T11:15:00Z", now)).toBe(zhCN.common.timeAgo.minutes.replace("{n}", "45"));
    act(() => setLocale("en"));
    expect(formatTimeAgo("2026-10-11T11:15:00Z", now)).toBe(en.common.timeAgo.minutes.replace("{n}", "45"));
    expect(formatDateTime("2026-10-11T12:00:00Z")).toBe(new Date("2026-10-11T12:00:00Z").toLocaleString("en", { hour12: false }));
  });
});
