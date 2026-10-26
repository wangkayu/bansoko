import { useState } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  previewAnimeSubgroupsSelection,
  type AnimeDraft,
  type AnimeWorkspace,
  type SubgroupScoreEntry,
  type SubgroupsSelectionPreview,
} from "../../lib/api/client";
import { SubgroupsTab } from "./SubgroupsTab";
import type { DraftTabHost } from "./host";
import { workspaceCapability } from "../workspace/capability";
import { t } from "../../lib/i18n";

// The subgroups tab's "subscribed subgroups n / max" and the subscription badges follow unsaved
// edits: after edits debounce, the server's selection preview computes them (current_input with
// the draft before subscribing, owner_draft with the unsaved subgroups after). While in flight or
// failed, the previous numbers stay and the frontend never computes them itself.

vi.mock("../../lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/api/client")>();
  return { ...actual, previewAnimeSubgroupsSelection: vi.fn() };
});
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() } }));
const mockedPreview = vi.mocked(previewAnimeSubgroupsSelection);

function entry(group_ref: string, score: number): SubgroupScoreEntry {
  return {
    provider: "anibt",
    group_ref,
    display_name: group_ref,
    selection_mode: "auto",
    manual_selected: null,
    effective_selected: false,
    score,
  };
}

function preview(mode: SubgroupsSelectionPreview["mode"], selected: string[], all: string[]): SubgroupsSelectionPreview {
  return {
    mode,
    groups: all.map((group_ref) => ({
      provider: "anibt" as const,
      group_ref,
      available: true,
      effective_selected: selected.includes(group_ref),
      reason_code: selected.includes(group_ref) ? "ranked_within_max_rss" : "policy_never",
    })),
  };
}

function summary(): HTMLElement {
  const element = document.querySelector<HTMLElement>(".sources-summary");
  if (!element) throw new Error("summary missing");
  return element;
}

function setPolicy(group: string, policy: string) {
  const radios = screen.getByRole("radiogroup", { name: t("forms.sources.policyAria", { name: group }) });
  fireEvent.click(within(radios).getByRole("radio", { name: policy }));
}

function draftFixture(): AnimeDraft {
  return {
    origin: "anibt",
    origin_entry_ref: "entry-1",
    title: "测试番",
    title_override: null,
    instance_label: null,
    metadata: { bgm: null, tmdb: null, total_episodes: null },
    subgroups: { max_rss: 2, entries: [entry("a", 80), entry("b", 70)] },
    rules: { sections: {} },
    rss_sources: [],
  } as unknown as AnimeDraft;
}

function DraftHarness({ previewable = true }: { previewable?: boolean }) {
  const [draft, setDraft] = useState(draftFixture);
  const host: DraftTabHost = {
    mode: "draft",
    draft,
    onDraftChange: setDraft,
    previewable,
    feedSnapshots: [],
    onFeedSnapshotsChange: () => {},
    observedGroups: [],
    onDiscovered: () => {},
  };
  return <SubgroupsTab host={host} />;
}

function ownerWorkspace(): AnimeWorkspace {
  const selection = (group_ref: string, selected: boolean) => ({
    available: true,
    availability_generation: 1,
    id: `sel-${group_ref}`,
    subscription_id: "subscription-1",
    provider: "anibt" as const,
    group_ref,
    display_name: group_ref,
    selection_mode: "auto" as const,
    manual_selected: null,
    effective_selected: selected,
    reason_code: selected ? "ranked_within_max_rss" : "policy_never",
    effective_score: 50,
    version: 1,
  });
  return {
    managed_anime: { id: "anime-1", title: "测试番", title_override: null, version: 1 },
    subscription: { id: "subscription-1", managed_anime_id: "anime-1", origin: "anibt", origin_entry_ref: "e", instance_label: null, state: "active", lifecycle_state: "active", lifecycle_epoch: 1, version: 1 },
    subgroups: {
      managed_anime_id: "anime-1",
      subgroups: { max_rss: 2, entries: [entry("a", 80), entry("b", 70)] },
      effective_selection: [selection("a", true), selection("b", true)],
      rss_sources: [],
      version: 1,
    },
    rss_sources: [],
  } as unknown as AnimeWorkspace;
}

function renderOwner(workspace = ownerWorkspace()) {
  return render(
    <SubgroupsTab host={{ mode: "owner", animeID: "anime-1", workspace, capability: workspaceCapability("active"), onChanged: () => {} }} />,
  );
}

describe("字幕组 tab 未保存编辑的选择预览", () => {
  beforeEach(() => {
    mockedPreview.mockReset();
  });

  it("草稿：编辑后按服务端预览更新 n / max，请求带完整草稿", async () => {
    mockedPreview.mockResolvedValueOnce(preview("current_input", ["a", "b"], ["a", "b"]));
    render(<DraftHarness />);
    await waitFor(() => expect(summary()).toHaveTextContent(`${t("forms.sources.summary")} 2`));

    mockedPreview.mockResolvedValueOnce(preview("current_input", ["a"], ["a", "b"]));
    setPolicy("b", t("forms.sources.policyNever"));
    // During the debounce the previous numbers stay, with a light updating notice.
    expect(summary()).toHaveTextContent(`${t("forms.sources.summary")} 2`);
    expect(summary()).toHaveTextContent(t("forms.sources.selectionUpdating"));
    await waitFor(() => expect(summary()).toHaveTextContent(`${t("forms.sources.summary")} 1`));
    expect(summary()).not.toHaveTextContent(t("forms.sources.selectionUpdating"));

    const last = mockedPreview.mock.calls.at(-1)?.[0];
    expect(last?.input_source).toBe("current_input");
    const sent = last?.draft?.subgroups.entries.find((item) => item.group_ref === "b");
    expect(sent).toMatchObject({ selection_mode: "manual", manual_selected: false });
  });

  it("草稿：连续编辑只在停下后请求一次", async () => {
    mockedPreview.mockResolvedValue(preview("current_input", ["a"], ["a", "b"]));
    render(<DraftHarness />);
    await waitFor(() => expect(mockedPreview).toHaveBeenCalledTimes(1));
    setPolicy("b", t("forms.sources.policyNever"));
    setPolicy("b", t("forms.sources.policyAlways"));
    setPolicy("a", t("forms.sources.policyNever"));
    await waitFor(() => expect(mockedPreview).toHaveBeenCalledTimes(2));
    await act(() => new Promise((resolve) => setTimeout(resolve, 400)));
    expect(mockedPreview).toHaveBeenCalledTimes(2);
  });

  it("草稿：尚不具备读条件时不请求，显示未预览", async () => {
    render(<DraftHarness previewable={false} />);
    await act(() => new Promise((resolve) => setTimeout(resolve, 400)));
    expect(mockedPreview).not.toHaveBeenCalled();
    expect(summary()).toHaveTextContent(t("forms.sources.notPreviewed"));
  });

  it("草稿：预览失败保留上一次数字并给出简短错误", async () => {
    mockedPreview.mockResolvedValueOnce(preview("current_input", ["a", "b"], ["a", "b"]));
    render(<DraftHarness />);
    await waitFor(() => expect(summary()).toHaveTextContent(`${t("forms.sources.summary")} 2`));
    mockedPreview.mockRejectedValueOnce(new Error("boom"));
    setPolicy("b", t("forms.sources.policyNever"));
    expect(await screen.findByText(t("forms.sources.selectionFailed"))).toBeInTheDocument();
    expect(summary()).toHaveTextContent(`${t("forms.sources.summary")} 2`);
  });

  it("owner：无未保存编辑时用 workspace 保存态，不请求预览", async () => {
    renderOwner();
    expect(summary()).toHaveTextContent(`${t("forms.sources.summary")} 2`);
    await act(() => new Promise((resolve) => setTimeout(resolve, 400)));
    expect(mockedPreview).not.toHaveBeenCalled();
  });

  it("owner：未保存编辑以 owner_draft 预览，数字跟随服务端结果", async () => {
    mockedPreview.mockResolvedValueOnce(preview("owner_draft", ["a"], ["a", "b"]));
    renderOwner();
    setPolicy("b", t("forms.sources.policyNever"));
    // Until the preview returns, the saved-state numbers keep showing.
    expect(summary()).toHaveTextContent(`${t("forms.sources.summary")} 2`);
    await waitFor(() => expect(summary()).toHaveTextContent(`${t("forms.sources.summary")} 1`));
    const call = mockedPreview.mock.calls[0]?.[0];
    expect(call?.input_source).toBe("owner_draft");
    expect(call?.managed_anime_id).toBe("anime-1");
    expect(call?.subgroups?.entries.find((item) => item.group_ref === "b")).toMatchObject({ selection_mode: "manual", manual_selected: false });
    expect(call?.draft).toBeUndefined();
  });

  it("owner：服务端没有事实的组显示未预览", async () => {
    const workspace = ownerWorkspace();
    workspace.subgroups.subgroups.entries.push(entry("c", 60));
    mockedPreview.mockResolvedValueOnce(preview("owner_draft", ["a"], ["a", "b"]));
    renderOwner(workspace);
    setPolicy("b", t("forms.sources.policyNever"));
    await waitFor(() => expect(summary()).toHaveTextContent(`${t("forms.sources.summary")} 1`));
    const row = screen.getByText("c").closest("li");
    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getByText(t("forms.sources.notPreviewed"))).toBeInTheDocument();
  });
});
