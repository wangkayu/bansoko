import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, type GlobalSectionView } from "./api/client";
import { useSettingsSection } from "./settings";
import { t } from "./i18n";

// Standalone unit tests of the shared settings form loop in useSettingsSection: load → draft
// initialization → dirty → expected_version CAS save → 409 conflict → refresh re-pull backfill.
// The fetch boundary is intercepted with vi.mock("./api/client"), verifying the PUT's
// expected_version and invalidation behavior; component-level regression lives in
// SettingsProviderDetail.test.tsx.

const listGlobalSections = vi.hoisted(() => vi.fn());
const putGlobalSection = vi.hoisted(() => vi.fn());

vi.mock("./api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api/client")>();
  return { ...actual, listGlobalSections, putGlobalSection };
});

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type TestDraft = Record<string, unknown> & { enabled: boolean; note: string };

const section: GlobalSectionView = {
  section_name: "subgroups",
  schema_version: "subgroups-v1",
  value: { enabled: true, note: "server" },
  version: 7,
  saved: true,
};

function draftFromSection(view?: GlobalSectionView): TestDraft {
  const value = view?.value ?? {};
  return {
    enabled: value.enabled === true,
    note: typeof value.note === "string" ? value.note : "",
  };
}

function wrapper({ children }: { children: ReactNode }) {
  // The default gcTime: the refetch tests depend on the cache staying alive (the test helper's
  // gcTime: 0 would have a manual refetch's result reclaimed immediately).
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function renderSection(onDirtyChange?: (dirty: boolean) => void) {
  return renderHook(
    () =>
      useSettingsSection<TestDraft>({
        sectionName: "subgroups",
        initialDraft: () => draftFromSection(),
        draftFromSection,
        successMessage: "已保存",
        failureFallback: "保存失败",
        onDirtyChange,
      }),
    { wrapper },
  );
}

describe("useSettingsSection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listGlobalSections.mockResolvedValue([section]);
    putGlobalSection.mockImplementation(async () => ({ ...section, version: section.version + 1 }));
  });

  it("加载后以 section 初始化草稿，非 dirty", async () => {
    const { result } = renderSection();
    expect(result.current.draft).toEqual({ enabled: false, note: "" });

    await waitFor(() => expect(result.current.section).toBeDefined());
    await waitFor(() => expect(result.current.draft).toEqual({ enabled: true, note: "server" }));
    expect(result.current.dirty).toBe(false);
  });

  it("updateDraft 置 dirty 并回调 onDirtyChange；卸载回调 false", async () => {
    const onDirtyChange = vi.fn();
    const { result, unmount } = renderSection(onDirtyChange);
    await waitFor(() => expect(result.current.draft.note).toBe("server"));

    act(() => result.current.updateDraft((current) => ({ ...current, note: "local" })));
    expect(result.current.dirty).toBe(true);
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    unmount();
    expect(onDirtyChange).toHaveBeenLastCalledWith(false);
  });

  it("dirty 时 section refetch 不覆盖本地草稿", async () => {
    let served = section;
    listGlobalSections.mockImplementation(async () => [served]);
    const { result } = renderSection();
    await waitFor(() => expect(result.current.draft.note).toBe("server"));

    act(() => result.current.updateDraft((current) => ({ ...current, note: "local" })));
    served = { ...section, value: { enabled: false, note: "changed" }, version: 8 };
    await act(async () => {
      await result.current.sectionsQuery.refetch();
    });

    await waitFor(() => expect(result.current.section?.version).toBe(8));
    expect(result.current.draft.note).toBe("local");
    expect(result.current.dirty).toBe(true);
  });

  it("save 成功：PUT 带 schema_version 与 expected_version，清 dirty、toast、invalidate", async () => {
    const { result } = renderSection();
    await waitFor(() => expect(result.current.section).toBeDefined());

    act(() => result.current.updateDraft((current) => ({ ...current, note: "local" })));
    await act(async () => {
      await result.current.save();
    });

    expect(putGlobalSection).toHaveBeenCalledWith(
      "subgroups",
      { schema_version: "subgroups-v1", value: { enabled: true, note: "local" } },
      7,
    );
    expect(result.current.dirty).toBe(false);
    expect(result.current.error).toBeUndefined();
    expect(result.current.conflict).toBeUndefined();
    const { toast } = await import("sonner");
    expect(toast.success).toHaveBeenCalledWith("已保存");
    // invalidate triggers the re-pull.
    await waitFor(() => expect(listGlobalSections.mock.calls.length).toBeGreaterThanOrEqual(2));
  });

  it("save 409：置 conflict 版本号与用户文案，保留本地草稿与 dirty", async () => {
    putGlobalSection.mockRejectedValue(
      new ApiError({ status: 409, code: "OWNER_VERSION_CONFLICT", message: "version conflict", currentVersion: 9, requestId: "r-409" }),
    );
    const { result } = renderSection();
    await waitFor(() => expect(result.current.section).toBeDefined());

    act(() => result.current.updateDraft((current) => ({ ...current, note: "local" })));
    await act(async () => {
      await result.current.save();
    });

    expect(result.current.conflict).toBe(9);
    expect(result.current.error).toBe(t("error.OWNER_VERSION_CONFLICT"));
    expect(result.current.dirty).toBe(true);
    expect(result.current.saving).toBe(false);
  });

  it("refreshAfterConflict：重拉 sections，以最新值重建草稿并清 dirty/conflict/error", async () => {
    putGlobalSection.mockRejectedValue(
      new ApiError({ status: 409, message: "版本冲突", currentVersion: 9 }),
    );
    const { result } = renderSection();
    await waitFor(() => expect(result.current.section).toBeDefined());

    let served = section;
    listGlobalSections.mockImplementation(async () => [served]);
    act(() => result.current.updateDraft((current) => ({ ...current, note: "local" })));
    await act(async () => {
      await result.current.save();
    });
    expect(result.current.conflict).toBe(9);

    served = { ...section, value: { enabled: false, note: "fresh" }, version: 9 };
    await act(async () => {
      await result.current.refreshAfterConflict();
    });

    expect(result.current.conflict).toBeUndefined();
    expect(result.current.error).toBeUndefined();
    expect(result.current.dirty).toBe(false);
    expect(result.current.draft).toEqual({ enabled: false, note: "fresh" });
    expect(result.current.section?.version).toBe(9);
  });

  it("save 非 409 失败：仅置 error，不置 conflict", async () => {
    putGlobalSection.mockRejectedValue(new ApiError({ status: 500, code: "INTERNAL", message: "The request failed." }));
    const { result } = renderSection();
    await waitFor(() => expect(result.current.section).toBeDefined());

    act(() => result.current.updateDraft((current) => ({ ...current, note: "local" })));
    await act(async () => {
      await result.current.save();
    });

    expect(result.current.conflict).toBeUndefined();
    expect(result.current.error).toBe(t("error.INTERNAL"));
    expect(result.current.dirty).toBe(true);
  });

  it("section 未载入时 save 是 no-op", async () => {
    listGlobalSections.mockReturnValue(new Promise<GlobalSectionView[]>(() => {}));
    const { result } = renderSection();

    await act(async () => {
      await result.current.save();
    });
    expect(putGlobalSection).not.toHaveBeenCalled();
  });
});
