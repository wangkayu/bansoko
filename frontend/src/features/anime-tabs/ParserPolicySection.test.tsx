import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { ParserPolicySection } from "./ParserPolicySection";
import { t } from "../../lib/i18n";
import { SavePartsProvider, useSaveParts } from "../../lib/saveParts";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() } }));

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

// The live catalog: both regex and reltitle have the release_title capability; file-only entries stay out of the choices.
const parserCatalog = [
  { id: "regex", implementation_version: "regex-v1", output_contract_version: "episode-evidence-v1", display_name: "Regex rules", capabilities: ["release_title", "file_name", "decimal"], config_version: "regex-config-v1" },
  { id: "reltitle", implementation_version: "reltitle@x", output_contract_version: "episode-evidence-v1", display_name: "reltitle episode parser", capabilities: ["release_title", "file_name", "decimal"], config_version: "reltitle-config-v1" },
  { id: "file-only", implementation_version: "f-v1", output_contract_version: "episode-evidence-v1", display_name: "File only", capabilities: ["file_name"], config_version: "f-config-v1" },
];

const putBodies: unknown[] = [];
const getReads: string[] = [];

function stubFetch(opts: { stored?: string; conflict?: boolean } = {}) {
  const stored = opts.stored ?? "regex";
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes("/parser-catalog")) return json(parserCatalog);
    if (url.includes("/file-maintenance/parser-policy/anime/")) {
      if (init?.method === "PUT") {
        if (opts.conflict) return json({ code: "OWNER_VERSION_CONFLICT", current_version: 4, message: "conflict" }, 409);
        const body = JSON.parse(String(init.body)) as { parser_kind: string };
        putBodies.push(body);
        return json({ managed_anime_id: "anime-1", parser_kind: body.parser_kind, source_default_version: 1, version: 2 });
      }
      getReads.push(url);
      return json({ managed_anime_id: "anime-1", parser_kind: stored, source_default_version: 1, version: 1 });
    }
    return json({});
  }));
}

beforeEach(() => {
  putBodies.length = 0;
  getReads.length = 0;
  stubFetch();
});

afterEach(() => vi.unstubAllGlobals());

// A mock dialog footer: the registry + a single save button (disabled with no changes) + the unsaved count.
const harnessOrder = ["parser"] as const;
function SaveHarness({ children }: { children: ReactNode }) {
  const parts = useSaveParts(harnessOrder);
  const count = parts.count();
  return (
    <SavePartsProvider value={parts.registrar}>
      {children}
      <output data-testid="unsaved-count">{count}</output>
      {parts.failed && <span data-testid="failed-part">{parts.failed}</span>}
      <button type="button" disabled={count === 0} onClick={() => void parts.save()}>
        {t("act.save")}
      </button>
    </SavePartsProvider>
  );
}

function renderSection(props: { readOnly?: boolean; onKindChange?: (kind: string | undefined) => void } = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SaveHarness>
        <ParserPolicySection animeID="anime-1" readOnly={props.readOnly} onKindChange={props.onKindChange} />
      </SaveHarness>
    </QueryClientProvider>,
  );
}

const unsavedCount = () => screen.getByTestId("unsaved-count").textContent;

const regexName = () => t("policy.parser.kindRegex");
const reltitleName = () => t("policy.parser.kindReltitle");

describe("ParserPolicySection", () => {
  it("二选一：正则与 reltitle 各带一行说明，已保存值选中，未改动时保存禁用", async () => {
    renderSection();
    const group = await screen.findByRole("radiogroup", { name: t("policy.parser.kindLabel") });
    expect(group).toBeInTheDocument();
    const radios = screen.getAllByRole("radio");
    expect(radios).toHaveLength(2);
    expect(screen.getByRole("radio", { name: regexName() })).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("radio", { name: reltitleName() })).toHaveAttribute("aria-checked", "false");
    expect(screen.getByRole("radio", { name: reltitleName() })).toHaveAccessibleDescription(t("policy.parser.kindReltitleDescription"));
    expect(screen.getByRole("radio", { name: regexName() })).toHaveAccessibleDescription(t("policy.parser.kindRegexDescription"));
    expect(screen.queryByText("File only")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("act.save") })).toBeDisabled();
  });

  it("点选项文字即可选择；选回原值后保存再次禁用", async () => {
    renderSection();
    await screen.findByRole("radiogroup", { name: t("policy.parser.kindLabel") });
    await userEvent.click(screen.getByText(t("policy.parser.kindReltitleDescription")));
    expect(screen.getByRole("radio", { name: reltitleName() })).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("button", { name: t("act.save") })).toBeEnabled();
    await userEvent.click(screen.getByText(regexName()));
    expect(screen.getByRole("button", { name: t("act.save") })).toBeDisabled();
  });

  it("选中 reltitle 后保存走 expected-version CAS，并把显示中的取值报给规则 tab", async () => {
    const kinds: (string | undefined)[] = [];
    renderSection({ onKindChange: (kind) => kinds.push(kind) });
    await screen.findByRole("radiogroup", { name: t("policy.parser.kindLabel") });
    await waitFor(() => expect(kinds.at(-1)).toBe("regex"));
    await userEvent.click(screen.getByRole("radio", { name: reltitleName() }));
    expect(kinds.at(-1)).toBe("reltitle");
    await userEvent.click(screen.getByRole("button", { name: t("act.save") }));
    await waitFor(() => expect(putBodies).toEqual([{ expected_version: 1, parser_kind: "reltitle" }]));
    expect(kinds.at(-1)).toBe("reltitle");
  });

  it("readOnly：选项禁用但保留选中态，不登记未保存改动", async () => {
    stubFetch({ stored: "reltitle" });
    renderSection({ readOnly: true });
    await screen.findByRole("radiogroup", { name: t("policy.parser.kindLabel") });
    const reltitle = screen.getByRole("radio", { name: reltitleName() });
    expect(reltitle).toHaveAttribute("aria-checked", "true");
    expect(reltitle).toHaveAttribute("aria-disabled", "true");
    await userEvent.click(screen.getByRole("radio", { name: regexName() }));
    expect(reltitle).toHaveAttribute("aria-checked", "true");
    expect(unsavedCount()).toBe("0");
    expect(screen.getByRole("button", { name: t("act.save") })).toBeDisabled();
  });

  it("冲突：409 显示刷新块，刷新按钮重读实例策略", async () => {
    stubFetch({ conflict: true });
    renderSection();
    await screen.findByRole("radiogroup", { name: t("policy.parser.kindLabel") });
    await userEvent.click(screen.getByRole("radio", { name: reltitleName() }));
    await userEvent.click(screen.getByRole("button", { name: t("act.save") }));

    expect(await screen.findByRole("alert")).toHaveTextContent(t("common.versionConflict"));
    expect(screen.getByTestId("failed-part")).toHaveTextContent(t("policy.parser.title"));
    expect(unsavedCount()).toBe("1");
    const before = getReads.length;
    await userEvent.click(screen.getByRole("button", { name: t("common.refreshLatest") }));
    await waitFor(() => expect(getReads.length).toBeGreaterThan(before));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
  });
});

describe("ParserPolicySection unsaved edits", () => {
  it("registers an unsaved choice with the surface registry and clears it after reselecting or saving", async () => {
    renderSection();
    await screen.findByRole("radiogroup", { name: t("policy.parser.kindLabel") });
    expect(unsavedCount()).toBe("0");
    // The card no longer has its own save button or a "save separately" note: only the footer's single save.
    expect(screen.getAllByRole("button", { name: t("act.save") })).toHaveLength(1);

    await userEvent.click(screen.getByRole("radio", { name: reltitleName() }));
    expect(unsavedCount()).toBe("1");

    // Selecting the original value back: no longer counts as unsaved.
    await userEvent.click(screen.getByRole("radio", { name: regexName() }));
    expect(unsavedCount()).toBe("0");

    // Change again then save: the count zeroes once the server value catches up with the local choice.
    await userEvent.click(screen.getByRole("radio", { name: reltitleName() }));
    expect(unsavedCount()).toBe("1");
    stubFetch({ stored: "reltitle" });
    await userEvent.click(screen.getByRole("button", { name: t("act.save") }));
    await waitFor(() => expect(unsavedCount()).toBe("0"));
    expect(screen.getByRole("radio", { name: reltitleName() })).toHaveAttribute("aria-checked", "true");
  });
});
