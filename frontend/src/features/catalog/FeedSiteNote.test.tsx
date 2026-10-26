import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { renderWithQuery } from "../../testing/renderWithQuery";
import { listFeedSites } from "../../lib/api/client";
import { FeedSiteNote } from "./FeedSiteNote";

vi.mock("../../lib/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/api/client")>()),
  listFeedSites: vi.fn(async () => ({
    sites: [
      { id: "nyaa", name: "nyaa", hosts: ["nyaa.si"], identity: "feed", match: "host" },
      { id: "unit3d", name: "UNIT3D", hosts: [], identity: "expansion", match: "content" },
    ],
  })),
}));

describe("FeedSiteNote", () => {
  it("names the adapter for a listed host", async () => {
    renderWithQuery(<FeedSiteNote url="https://nyaa.si/?page=rss" />);
    expect(await screen.findByText("已适配站点：nyaa")).toHaveAttribute("data-supported", "true");
  });

  it("labels an unlisted host without blocking or naming content-detected architectures", async () => {
    renderWithQuery(<FeedSiteNote url="https://pt.example.test/rss" />);
    expect(await screen.findByText("未适配站点，不保证解析")).toHaveAttribute("data-supported", "false");
  });

  it("shows nothing for an incomplete address", () => {
    vi.mocked(listFeedSites).mockClear();
    const { container } = renderWithQuery(<FeedSiteNote url="nyaa.si" />);
    expect(container).toBeEmptyDOMElement();
    expect(listFeedSites).not.toHaveBeenCalled();
  });
});
