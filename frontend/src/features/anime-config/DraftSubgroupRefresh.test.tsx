import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { discoverProviderEntryGroups, type ProviderGroupDiscoveryResult } from "../../lib/api/client";
import { DraftSubgroupRefresh } from "./DraftSubgroupRefresh";
import { t } from "../../lib/i18n";

vi.mock("sonner", () => ({ toast: { info: vi.fn(), success: vi.fn(), error: vi.fn() } }));
vi.mock("../../lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/api/client")>();
  return { ...actual, discoverProviderEntryGroups: vi.fn() };
});
const mockedDiscover = vi.mocked(discoverProviderEntryGroups);

const RESULT: ProviderGroupDiscoveryResult = {
  provider: "mikan",
  provider_entry_ref: "3001",
  observed_at: "2026-10-03T00:00:00Z",
  source_version: "v1",
  schema_version: "groups-v1",
  snapshot_version: 5,
  groups: [{ provider: "mikan", group_ref: "42", display_name: t("field.subgroup"), feed_url: "https://example.test/rss", observed_at: "2026-10-03T00:00:00Z" }],
};

describe("DraftSubgroupRefresh", () => {
  beforeEach(() => {
    mockedDiscover.mockReset();
    vi.mocked(toast.error).mockReset();
  });

  // Discovery is a cross-tab fact: hand over only the group list and the RSS preview never
  // gets the observation identity, so discovered groups could never reach the preview.
  it("把本次发现的观察身份和组列表一起交给上层", async () => {
    mockedDiscover.mockResolvedValue(RESULT);
    const onDiscovered = vi.fn();
    render(<DraftSubgroupRefresh provider="mikan" providerEntryRef="3001" onDiscovered={onDiscovered} />);
    fireEvent.click(screen.getByRole("button", { name: t("config.discovery.refresh") }));
    await waitFor(() => expect(onDiscovered).toHaveBeenCalledTimes(1));
    expect(onDiscovered).toHaveBeenCalledWith(RESULT.groups, {
      provider: "mikan",
      provider_entry_ref: "3001",
      snapshot_version: 5,
    });
  });

  it("发现失败时不交出任何观察", async () => {
    mockedDiscover.mockRejectedValue(new Error("boom"));
    const onDiscovered = vi.fn();
    render(<DraftSubgroupRefresh provider="mikan" providerEntryRef="3001" onDiscovered={onDiscovered} />);
    fireEvent.click(screen.getByRole("button", { name: t("config.discovery.refresh") }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(t("config.discovery.failed"), expect.anything()));
    expect(onDiscovered).not.toHaveBeenCalled();
  });
});
