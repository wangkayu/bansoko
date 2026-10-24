import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, toUserMessage } from "./api/client";
import { errorDetailsFor, fieldLabel, formatErrorDetails } from "./errors";
import { t } from "./i18n";
import { ErrorDetailsFor } from "../ui/base/ErrorDetails";

afterEach(() => vi.unstubAllGlobals());

describe("toUserMessage", () => {
  it("uses the catalog text for a known code and keeps server text in details", () => {
    const error = new ApiError({ status: 401, code: "CREDENTIALS_INVALID", message: "Invalid credentials", requestId: "r-1" });
    const message = toUserMessage(error, "x");
    expect(message).toBe(t("error.CREDENTIALS_INVALID"));
    const details = errorDetailsFor(message);
    expect(details).toMatchObject({ code: "CREDENTIALS_INVALID", message: "Invalid credentials", requestId: "r-1" });
  });

  it("falls back to the generic text for an unknown code", () => {
    const error = new ApiError({ status: 500, code: "SOMETHING_NEW", message: "boom" });
    expect(toUserMessage(error, "x")).toBe(t("common.operationFailed"));
  });

  it("maps field paths to Chinese names and hides unmapped paths", () => {
    expect(fieldLabel("rss_sources[0].locator")).toBe(t("field.locator"));
    expect(fieldLabel("mystery[2].zzz")).toBeUndefined();
    const error = new ApiError({
      status: 422,
      code: "VALIDATION_FAILED",
      message: "invalid",
      details: [
        { field: "rss_sources[0].locator", code: "X", message: "bad url" },
        { field: "mystery.zzz", code: "X", message: "bad" },
      ],
    });
    const message = toUserMessage(error, "x");
    expect(message).toBe(`${t("error.VALIDATION_FAILED")}（${t("field.locator")}）`);
    expect(message).not.toContain("rss_sources");
    expect(formatErrorDetails(errorDetailsFor(message)!)).toContain("rss_sources[0].locator - bad url");
  });

  it("uses the call-site fallback for non-API errors", () => {
    expect(toUserMessage(new Error("boom"), "保存失败")).toBe("保存失败");
    expect(toUserMessage(undefined, "保存失败")).toBe("保存失败");
  });
});

describe("ErrorDetails", () => {
  function setup() {
    const message = toUserMessage(new ApiError({ status: 500, code: "INTERNAL", message: "The request failed.", requestId: "req-7" }), "x");
    render(<ErrorDetailsFor message={message} />);
  }

  it("is collapsed by default and expands to show details", () => {
    setup();
    expect(screen.queryByText(/req-7/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("errorDetails.toggle") }));
    expect(screen.getByText(/req-7/)).toBeInTheDocument();
  });

  it("copies through the clipboard", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    setup();
    fireEvent.click(screen.getByRole("button", { name: t("errorDetails.toggle") }));
    fireEvent.click(screen.getByRole("button", { name: t("errorDetails.copy") }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(expect.stringContaining("req-7")));
    expect(await screen.findByText(t("errorDetails.copied"))).toBeInTheDocument();
  });

  it("selects the text when the clipboard rejects", async () => {
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText: vi.fn().mockRejectedValue(new Error("denied")) } });
    setup();
    fireEvent.click(screen.getByRole("button", { name: t("errorDetails.toggle") }));
    fireEvent.click(screen.getByRole("button", { name: t("errorDetails.copy") }));
    expect(await screen.findByText(t("errorDetails.copyFailed"))).toBeInTheDocument();
    expect(window.getSelection()?.toString()).toContain("req-7");
  });
});
