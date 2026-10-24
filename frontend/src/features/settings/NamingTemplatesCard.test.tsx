import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { NamingTemplateLibraryInput, NamingTemplateLibraryView } from "../../lib/api/client";
import { NamingTemplatesCard } from "./NamingTemplatesCard";
import { t } from "../../lib/i18n";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

const rules = {
  language: "js-naming-1" as const,
  unknown: { directory_template: "u-dir", video_filename_template: "u-file" },
  tv: { directory_template: "tv-dir", video_filename_template: "tv-file" },
  movie: { directory_template: "movie-dir", video_filename_template: "movie-file" },
  subtitle_suffix_template: "sfx",
};

const library: NamingTemplateLibraryView = {
  default_key: "default",
  version: 4,
  templates: [{ key: "default", name: "Default", rules }],
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function stubFetch() {
  const fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    if (url.includes("/config/naming-templates")) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as NamingTemplateLibraryInput;
        return jsonResponse({ ...body, version: 5 } satisfies NamingTemplateLibraryView);
      }
      return jsonResponse(library);
    }
    if (url.includes("/template-catalogs/naming")) {
      return jsonResponse({ language: "js-naming-1", fields: [], functions: [], shorthands: [], samples: [{ ref: "sample-tv", label: "剧集样例" }], vars_keys: [], vars_loaded_at: null });
    }
    if (url.includes("/template-previews/naming")) {
      // The preview is a debounced request that can fire before the assertion on a slow machine, so a valid preview shape is needed.
      return jsonResponse({ valid: true, rendered: "", errors: [], warnings: [], resolved_values: [] });
    }
    return jsonResponse([]);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ul>
        <NamingTemplatesCard />
      </ul>
    </QueryClientProvider>,
  );
}

async function openDialog() {
  fireEvent.click(await screen.findByRole("button", { name: t("settings.rules.edit") }));
  return screen.findByRole("dialog");
}

describe("NamingTemplatesCard", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("新建复制当前模板、设为默认后旧默认可删除，保存整份模板库并带 If-Match", async () => {
    const fetchMock = stubFetch();
    renderCard();
    const dialog = await openDialog();

    // The only default template cannot be deleted and needs no further set-as-default.
    expect(within(dialog).getByRole("button", { name: t("settings.rules.templates.delete") })).toBeDisabled();
    expect(within(dialog).getByRole("button", { name: t("settings.rules.templates.setDefault") })).toBeDisabled();
    expect(within(dialog).getByText(t("settings.rules.templates.deleteDefaultHint"))).toBeInTheDocument();
    // The sample picker appears only in global settings.
    expect(await within(dialog).findByRole("combobox", { name: t("naming.sample") })).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: t("settings.rules.templates.add") }));
    const name = within(dialog).getByLabelText(t("settings.rules.templates.name"));
    expect(name).toHaveValue(t("settings.rules.templates.copyName", { name: "Default" }));
    fireEvent.change(name, { target: { value: "简洁" } });
    fireEvent.change(within(dialog).getByLabelText(t("naming.filename")), { target: { value: "clean-file" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("settings.rules.templates.setDefault") }));

    await userEvent.click(within(dialog).getByRole("combobox", { name: t("settings.rules.templates.select") }));
    await userEvent.click(await screen.findByRole("option", { name: "Default" }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("settings.rules.templates.delete") }));

    fireEvent.click(within(dialog).getByRole("button", { name: t("settings.rules.save") }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(true));
    const [, init] = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT")!;
    expect((init?.headers as Headers).get("If-Match")).toBe("4");
    const body = JSON.parse(String(init?.body)) as NamingTemplateLibraryInput;
    expect(body.templates).toHaveLength(1);
    expect(body.default_key).toBe(body.templates[0]!.key);
    expect(body.templates[0]!.name).toBe("简洁");
    expect(body.templates[0]!.rules.tv.video_filename_template).toBe("clean-file");
    expect(body.templates[0]!.rules.subtitle_suffix_template).toBe("sfx");
  });
});
