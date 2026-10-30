import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { confirmBusinessReplace } from "./confirmBusinessReplace";
import { t } from "../../lib/i18n";

describe("confirmBusinessReplace", () => {
  afterEach(async () => {
    // The imperative root unmounts 300 ms after its promise resolves.
    await new Promise((resolve) => setTimeout(resolve, 350));
  });

  it("点击「继续」resolve(true)，并展示原文案", async () => {
    const result = confirmBusinessReplace("将替换全部业务配置。");
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent(t("config.replaceTitle"));
    expect(dialog).toHaveTextContent("将替换全部业务配置。");
    fireEvent.click(screen.getByRole("button", { name: t("config.replaceConfirm") }));
    await expect(result).resolves.toBe(true);
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
  });

  it("点击「取消」resolve(false)", async () => {
    const result = confirmBusinessReplace("x");
    await screen.findByRole("alertdialog");
    fireEvent.click(screen.getByRole("button", { name: t("act.cancel") }));
    await expect(result).resolves.toBe(false);
  });

  it("Esc 视为取消", async () => {
    const result = confirmBusinessReplace("x");
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.keyDown(dialog, { key: "Escape" });
    await expect(result).resolves.toBe(false);
  });

  it("点遮罩不关闭（与 Modal.confirm 一致），仍可取消", async () => {
    let settled = false;
    const result = confirmBusinessReplace("x").then((v) => {
      settled = true;
      return v;
    });
    await screen.findByRole("alertdialog");
    fireEvent.pointerDown(document.querySelector('[class*="bg-overlay"]') as HTMLElement);
    fireEvent.click(document.querySelector('[class*="bg-overlay"]') as HTMLElement);
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(settled).toBe(false);
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("act.cancel") }));
    await expect(result).resolves.toBe(false);
  });
});
