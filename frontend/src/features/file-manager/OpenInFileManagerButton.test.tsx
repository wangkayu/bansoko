import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../../lib/i18n";
import { OpenInFileManagerButton } from "./OpenInFileManagerButton";

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function CurrentSearch() {
  return <output data-testid="search">{useLocation().search}</output>;
}

function renderButton(response: Response) {
  vi.stubGlobal("fetch", vi.fn(async () => response));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/?anime=anime-1&tab=rules"]}>
        <OpenInFileManagerButton animeID="anime-1" />
        <CurrentSearch />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("OpenInFileManagerButton", () => {
  it("opens the file manager at the anime's folder", async () => {
    renderButton(json({ path: "/media/anime/Show A/Season 01" }));
    fireEvent.click(screen.getByRole("button", { name: t("fileManager.openFromAnime") }));
    await waitFor(() => expect(new URLSearchParams(screen.getByTestId("search").textContent ?? "").get("tool")).toBe("files"));
    const params = new URLSearchParams(screen.getByTestId("search").textContent ?? "");
    expect(params.get("path")).toBe("/media/anime/Show A/Season 01");
    expect(params.get("anime")).toBeNull();
  });

  it("falls back to the filesystem root when the anime has no files yet", async () => {
    renderButton(json({ code: "FILE_MANAGER_LOCATION_UNKNOWN", message: "unknown" }, 404));
    fireEvent.click(screen.getByRole("button", { name: t("fileManager.openFromAnime") }));
    await waitFor(() => expect(new URLSearchParams(screen.getByTestId("search").textContent ?? "").get("tool")).toBe("files"));
    const params = new URLSearchParams(screen.getByTestId("search").textContent ?? "");
    expect(params.get("path")).toBeNull();
  });
});
