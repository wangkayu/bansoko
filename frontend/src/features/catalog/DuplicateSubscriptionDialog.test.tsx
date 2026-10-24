import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DuplicateSubscriptionDialog } from "./DuplicateSubscriptionDialog";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { t } from "../../lib/i18n";

const mocks = vi.hoisted(() => ({ duplicate: vi.fn(), error: vi.fn() }));
vi.mock("sonner", () => ({ toast: { error: mocks.error } }));
vi.mock("../../lib/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/api/client")>()),
  duplicateAnimeSubscription: mocks.duplicate,
}));

const entry: CatalogEntryView = {
  providerEntryRef: "mikan:e1",
  source: "mikan",
  title: "测试番剧",
  metadataMatch: { matched: false },
  subscriptionState: "active",
  managedAnimeRef: "ma-source",
  subscriptionVersion: 3,
};

const copyResult = { managed_anime: { id: "ma-copy" }, subscription: { state: "pending", version: 1 } };

beforeEach(() => {
  mocks.duplicate.mockReset().mockResolvedValue(copyResult);
  mocks.error.mockReset();
});
afterEach(cleanup);

describe("DuplicateSubscriptionDialog", () => {
  it("要求填写订阅备注后才能复制", () => {
    render(<DuplicateSubscriptionDialog entry={entry} onOpenChange={vi.fn()} onDuplicated={vi.fn()} />);
    const confirm = screen.getByRole("button", { name: t("grid.duplicateConfirm") });
    expect(confirm).toBeDisabled();
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "   " } });
    expect(confirm).toBeDisabled();
    expect(mocks.duplicate).not.toHaveBeenCalled();
  });

  it("按源订阅与去空格的备注复制，成功后关闭并交出新订阅", async () => {
    const onOpenChange = vi.fn();
    const onDuplicated = vi.fn();
    render(<DuplicateSubscriptionDialog entry={entry} onOpenChange={onOpenChange} onDuplicated={onDuplicated} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: " 只订阅一个字幕组 " } });
    fireEvent.click(screen.getByRole("button", { name: t("grid.duplicateConfirm") }));
    await waitFor(() => expect(onDuplicated).toHaveBeenCalledWith(copyResult));
    expect(mocks.duplicate).toHaveBeenCalledWith("ma-source", { instance_label: "只订阅一个字幕组" }, expect.any(String));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("失败时提示并保留输入；重试沿用同一个请求键", async () => {
    mocks.duplicate.mockRejectedValueOnce(new Error("boom"));
    const onDuplicated = vi.fn();
    render(<DuplicateSubscriptionDialog entry={entry} onOpenChange={vi.fn()} onDuplicated={onDuplicated} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "副本" } });
    fireEvent.click(screen.getByRole("button", { name: t("grid.duplicateConfirm") }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledTimes(1));
    expect(onDuplicated).not.toHaveBeenCalled();
    expect(screen.getByRole("textbox")).toHaveValue("副本");
    fireEvent.click(screen.getByRole("button", { name: t("grid.duplicateConfirm") }));
    await waitFor(() => expect(onDuplicated).toHaveBeenCalledTimes(1));
    const [first, second] = mocks.duplicate.mock.calls;
    expect(first[2]).toBe(second[2]);
  });
});
