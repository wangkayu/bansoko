import { useState } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { t } from "../lib/i18n";
import { AccountMenu } from "./AccountMenu";

const setTheme = vi.fn();
vi.mock("next-themes", () => ({ useTheme: () => ({ theme: "dark", setTheme }) }));

function Harness({ onLogout }: { onLogout: () => void }) {
  const [open, setOpen] = useState(false);
  return <AccountMenu username="kayu" onLogout={onLogout} open={open} onOpenChange={setOpen} />;
}

describe("AccountMenu", () => {
  it("展示用户名与当前主题，外观子菜单切换主题", async () => {
    const user = userEvent.setup();
    render(<Harness onLogout={() => {}} />);
    await user.click(screen.getByRole("button", { name: t("nav.account") }));
    expect(await screen.findByRole("menuitem", { name: "kayu" })).toHaveAttribute("aria-disabled", "true");
    await user.click(screen.getByRole("menuitem", { name: t("account.appearance") }));
    expect(await screen.findByRole("menuitemradio", { name: t("account.dark") })).toHaveAttribute("aria-checked", "true");
    fireEvent.click(screen.getByRole("menuitemradio", { name: t("account.system") }));
    expect(setTheme).toHaveBeenCalledWith("system");
  });

  it("退出登录回调，Esc 关闭菜单并把焦点还给触发按钮", async () => {
    const onLogout = vi.fn();
    const user = userEvent.setup();
    render(<Harness onLogout={onLogout} />);
    const trigger = screen.getByRole("button", { name: t("nav.account") });
    await user.click(trigger);
    await screen.findByRole("menu");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
    await waitFor(() => expect(trigger).toHaveFocus());
    await user.click(trigger);
    await user.click(await screen.findByRole("menuitem", { name: t("account.logout") }));
    expect(onLogout).toHaveBeenCalledTimes(1);
  });

  it("账户菜单里没有隐藏的番剧（它在设置里）", async () => {
    const user = userEvent.setup();
    render(<Harness onLogout={() => {}} />);
    await user.click(screen.getByRole("button", { name: t("nav.account") }));
    await screen.findByRole("menu");
    expect(screen.queryByRole("menuitem", { name: t("settings.nav.hidden") })).not.toBeInTheDocument();
  });
});
