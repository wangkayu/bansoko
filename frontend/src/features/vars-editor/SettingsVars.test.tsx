import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { NamingVarsDryRunResult, NamingVarsProgram, NamingVarsState, NamingVarsVersion } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { SettingsVars } from "./SettingsVars";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

const editorSpy = vi.hoisted(() => ({ insert: vi.fn() }));

// CodeMirror needs a real layout engine; the editor widget has its own test.
vi.mock("./CodeEditor", async () => {
  const React = await import("react");
  type Props = { value: string; onChange?: (value: string) => void; markers?: unknown; readOnly?: boolean; label: string };
  const Mock = React.forwardRef<{ insert: (text: string) => void }, Props>(function Mock({ value, onChange, markers, readOnly, label }, ref) {
    React.useImperativeHandle(ref, () => ({ insert: editorSpy.insert }));
    return <textarea aria-label={label} value={value} readOnly={readOnly} data-markers={JSON.stringify(markers)} onChange={(event) => onChange?.(event.target.value)} />;
  });
  return { default: Mock };
});

const ACTIVE_SOURCE = 'vars.anime = {ep: extract(input.release_title, /(\\d+)/) ?? ""};\n';

function version(revision: number, active: boolean, extra: Partial<NamingVarsVersion> = {}): NamingVarsVersion {
  return {
    revision,
    digest: `sha256:${revision}`,
    runtime: "naming-vars-v1",
    origin: "editor",
    note: "",
    source_bytes: 60,
    created_at: "2026-10-12T08:00:00Z",
    activated_at: active ? "2026-10-12T08:05:00Z" : null,
    active,
    ...extra,
  };
}

function stateOf(overrides: Partial<NamingVarsState> = {}): NamingVarsState {
  return {
    active: { version: version(2, true, { note: "second" }), source: ACTIVE_SOURCE },
    versions: [version(2, true, { note: "second" }), version(1, false, { origin: "file" })],
    input_facts: [
      { path: "anime.title", type: "string", nullable: true, label_key: "facts.anime.title.label", description_key: "facts.anime.title.description", sample_value: "Example" },
    ],
    samples: [
      { ref: "tv_full", release_title: "[Sample] Example - 01", file_count: 1 },
      { ref: "title_only", release_title: "[Sample] Example - 02", file_count: 1 },
    ],
    limits: { max_source_bytes: 65536, max_output_bytes: 8192, max_collection: 256, timeout_ms: 500, max_note_length: 200, max_targets: 8 },
    ...overrides,
  };
}

const cleanRun: NamingVarsDryRunResult = {
  digest: "sha256:draft",
  diagnostics: [],
  targets: [
    { kind: "sample", ref: "tv_full", title: "[Sample] Example - 01", file_count: 1, status: "ok", output: { anime: { ep: "01" } }, diagnostics: [] },
    { kind: "sample", ref: "title_only", title: "[Sample] Example - 02", file_count: 1, status: "ok", output: { anime: { ep: "" } }, diagnostics: [] },
  ],
};

type Route = (url: string, init: RequestInit | undefined) => { status?: number; body: unknown } | undefined;

function stubApi(state: NamingVarsState, route: Route = () => undefined) {
  const calls: { url: string; method: string; headers: Headers; body: unknown }[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input).replace("/api/v1", "");
    const method = init?.method ?? "GET";
    calls.push({ url, method, headers: new Headers(init?.headers), body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const custom = route(`${method} ${url}`, init);
    let reply: { status?: number; body: unknown };
    if (custom) reply = custom;
    else if (method === "GET" && url === "/naming-vars") reply = { body: state };
    else if (method === "GET" && url === "/naming-vars/releases") reply = { body: [{ id: "r1", title: "[G] Real - 03", anime_title: "Real" }] };
    else if (method === "POST" && url === "/naming-vars/dry-runs") reply = { body: cleanRun };
    else reply = { status: 404, body: { code: "NOT_FOUND", message: "missing" } };
    return new Response(JSON.stringify(reply.body), { status: reply.status ?? 200, headers: { "Content-Type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SettingsVars />
    </QueryClientProvider>,
  );
}

async function openEditor() {
  renderPage();
  fireEvent.click(await screen.findByRole("button", { name: t("varsEditor.edit") }));
  return screen.findByLabelText(t("varsEditor.editor.codeLabel"));
}

describe("SettingsVars", () => {
  beforeEach(() => editorSpy.insert.mockClear());
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("shows the active version and the history, newest first, with rollback only on past versions", async () => {
    stubApi(stateOf());
    renderPage();
    expect(await screen.findByText(`${t("varsEditor.current")} · ${t("varsEditor.version", { n: 2 })}`)).toBeInTheDocument();
    const rows = within(screen.getByRole("list", { name: t("varsEditor.history") })).getAllByRole("listitem");
    expect(rows.map((row) => row.getAttribute("data-revision"))).toEqual(["2", "1"]);
    expect(within(rows[0]).getByText(t("varsEditor.activeBadge"))).toBeInTheDocument();
    expect(within(rows[0]).queryByRole("button", { name: t("varsEditor.rollback") })).not.toBeInTheDocument();
    expect(within(rows[1]).getByRole("button", { name: t("varsEditor.rollback") })).toBeInTheDocument();
  });

  it("rolls back with the active revision as the compare-and-swap token", async () => {
    const calls = stubApi(stateOf(), (route) => {
      if (route === "PUT /naming-vars/active") return { body: { version: version(1, true, { origin: "file" }), source: "vars.a = {b: '1'};\n" } };
      return undefined;
    });
    renderPage();
    const rows = within(await screen.findByRole("list", { name: t("varsEditor.history") })).getAllByRole("listitem");
    fireEvent.click(within(rows[1]).getByRole("button", { name: t("varsEditor.rollback") }));
    fireEvent.click(await screen.findByRole("button", { name: t("varsEditor.rollbackConfirm") }));
    await waitFor(() => expect(calls.some((call) => call.method === "PUT")).toBe(true));
    const put = calls.find((call) => call.method === "PUT");
    expect(put?.url).toBe("/naming-vars/active");
    expect(put?.headers.get("If-Match")).toBe("2");
    expect(put?.body).toEqual({ revision: 1 });
  });

  it("dry-runs the draft as it is typed, with the chosen releases and no write", async () => {
    const calls = stubApi(stateOf());
    const editor = await openEditor();
    await waitFor(() => expect(calls.filter((call) => call.url === "/naming-vars/dry-runs")).toHaveLength(1), { timeout: 3000 });
    fireEvent.change(editor, { target: { value: "vars.anime = {ep: '1'};\n" } });
    await waitFor(() => expect(calls.filter((call) => call.url === "/naming-vars/dry-runs")).toHaveLength(2), { timeout: 3000 });
    const last = calls.filter((call) => call.url === "/naming-vars/dry-runs").at(-1);
    expect(last?.body).toEqual({
      source: "vars.anime = {ep: '1'};\n",
      targets: [{ kind: "sample", sample: "tv_full" }, { kind: "sample", sample: "title_only" }],
    });
    expect(await screen.findAllByText("vars.anime.ep", {}, { timeout: 3000 })).toHaveLength(2);
    expect(calls.some((call) => call.method === "POST" && call.url === "/naming-vars/versions")).toBe(false);
    // The empty value is shown as empty, not invented.
    expect(await screen.findAllByText(t("varsEditor.run.empty"))).toHaveLength(1);
  });

  it("lists static problems with their line and marks them in the editor", async () => {
    const broken: NamingVarsDryRunResult = {
      diagnostics: [{ code: "syntax_error", message: "Unexpected token }", start: 20, end: 21 }],
      targets: [],
    };
    stubApi(stateOf(), (route) => (route === "POST /naming-vars/dry-runs" ? { body: broken } : undefined));
    const editor = await openEditor();
    const source = "vars.a = {b: 'x'};\nvars.c = {d: };\n";
    fireEvent.change(editor, { target: { value: source } });
    const problems = await screen.findByText(new RegExp("Unexpected token"), {}, { timeout: 3000 });
    expect(problems.textContent).toContain(t("varsEditor.editor.line", { n: 2 }));
    await waitFor(() => {
      const markers = JSON.parse(screen.getByLabelText(t("varsEditor.editor.codeLabel")).getAttribute("data-markers") ?? "[]") as { from: number; to: number; severity: string }[];
      expect(markers).toEqual([expect.objectContaining({ from: 20, to: 21, severity: "error" })]);
    });
  });

  it("inserts an input reference at the cursor when a fact is clicked", async () => {
    stubApi(stateOf());
    await openEditor();
    fireEvent.click(await screen.findByRole("tab", { name: t("varsEditor.editor.tabs.facts") }));
    fireEvent.click(await screen.findByRole("button", { name: /anime\.title/ }));
    expect(editorSpy.insert).toHaveBeenCalledWith("input.anime.title");
  });

  it("saves with the active revision, then closes and refreshes", async () => {
    const calls = stubApi(stateOf(), (route) =>
      route === "POST /naming-vars/versions" ? { status: 201, body: { version: version(3, true), source: "vars.anime = {ep: '3'};\n" } } : undefined,
    );
    const editor = await openEditor();
    fireEvent.change(editor, { target: { value: "vars.anime = {ep: '3'};\r\n" } });
    fireEvent.change(screen.getByLabelText(t("varsEditor.editor.note")), { target: { value: "  third  " } });
    fireEvent.click(screen.getByRole("button", { name: t("varsEditor.editor.save") }));
    await waitFor(() => expect(calls.some((call) => call.url === "/naming-vars/versions")).toBe(true));
    const post = calls.find((call) => call.url === "/naming-vars/versions");
    expect(post?.headers.get("If-Match")).toBe("2");
    expect(post?.body).toEqual({ source: "vars.anime = {ep: '3'};\n", note: "  third  " });
    await waitFor(() => expect(screen.queryByLabelText(t("varsEditor.editor.codeLabel"))).not.toBeInTheDocument());
  });

  it("does not offer to save an unchanged or empty program", async () => {
    stubApi(stateOf());
    const editor = await openEditor();
    expect(screen.getByRole("button", { name: t("varsEditor.editor.save") })).toBeDisabled();
    fireEvent.change(editor, { target: { value: "   " } });
    expect(screen.getByRole("button", { name: t("varsEditor.editor.save") })).toBeDisabled();
    fireEvent.change(editor, { target: { value: "vars.a = {b: '1'};" } });
    expect(screen.getByRole("button", { name: t("varsEditor.editor.save") })).toBeEnabled();
  });

  it("shows the server's located problems when a save is refused, and keeps the draft", async () => {
    stubApi(stateOf(), (route) =>
      route === "POST /naming-vars/versions"
        ? { status: 422, body: { code: "NAMING_VARS_INVALID", message: "invalid", details: [{ field: "source", code: "output_missing", message: "naming vars output value is missing; choose an explicit fallback", start: 17, end: 20 }] } }
        : undefined,
    );
    const editor = await openEditor();
    const draft = "vars.a = {b: 1};\nvars.shown = {title: input.anime.title};\n";
    fireEvent.change(editor, { target: { value: draft } });
    fireEvent.click(screen.getByRole("button", { name: t("varsEditor.editor.save") }));
    expect(await screen.findByText(new RegExp("explicit fallback"))).toBeInTheDocument();
    expect(screen.getByLabelText(t("varsEditor.editor.codeLabel"))).toHaveValue(draft);
  });

  it("explains a compare-and-swap conflict and lets the owner keep the draft or load the latest", async () => {
    let attempts = 0;
    const calls = stubApi(stateOf(), (route) => {
      if (route === "POST /naming-vars/versions") {
        attempts += 1;
        if (attempts === 1) return { status: 409, body: { code: "OWNER_VERSION_CONFLICT", message: "changed", current_version: 5 } };
        return { status: 201, body: { version: version(6, true), source: "x" } };
      }
      return undefined;
    });
    const editor = await openEditor();
    fireEvent.change(editor, { target: { value: "vars.mine = {a: '1'};" } });
    fireEvent.click(screen.getByRole("button", { name: t("varsEditor.editor.save") }));
    expect(await screen.findByText(t("varsEditor.conflictTitle"))).toBeInTheDocument();
    expect(screen.getByText(t("varsEditor.conflictBody", { current: 5, base: 2 }))).toBeInTheDocument();
    // The draft is kept; keeping it rebases the next save on the current revision.
    expect(screen.getByLabelText(t("varsEditor.editor.codeLabel"))).toHaveValue("vars.mine = {a: '1'};");
    fireEvent.click(screen.getByRole("button", { name: t("varsEditor.conflictKeep") }));
    fireEvent.click(screen.getByRole("button", { name: t("varsEditor.editor.save") }));
    await waitFor(() => expect(attempts).toBe(2));
    const retries = calls.filter((call) => call.url === "/naming-vars/versions");
    expect(retries.map((call) => call.headers.get("If-Match"))).toEqual(["2", "5"]);
  });

  it("opens a past version read-only and can load it as a draft", async () => {
    stubApi(stateOf(), (route) =>
      route === "GET /naming-vars/versions/1" ? { body: { version: version(1, false, { origin: "file" }), source: "vars.old = {a: '1'};\n" } } : undefined,
    );
    renderPage();
    const rows = within(await screen.findByRole("list", { name: t("varsEditor.history") })).getAllByRole("listitem");
    fireEvent.click(within(rows[1]).getByRole("button", { name: t("varsEditor.view") }));
    const editor = await screen.findByLabelText(t("varsEditor.editor.codeLabel"));
    expect(editor).toHaveValue("vars.old = {a: '1'};\n");
    expect(editor).toHaveAttribute("readonly");
    expect(screen.queryByRole("button", { name: t("varsEditor.editor.save") })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("varsEditor.editor.useAsDraft") }));
    await waitFor(() => expect(screen.getByLabelText(t("varsEditor.editor.codeLabel"))).not.toHaveAttribute("readonly"));
    expect(screen.getByLabelText(t("varsEditor.editor.codeLabel"))).toHaveValue("vars.old = {a: '1'};\n");
    // A version loaded as a draft differs from the active one, so it can be saved.
    expect(screen.getByRole("button", { name: t("varsEditor.editor.save") })).toBeEnabled();
  });

  it("exports the active program as a downloadable document", async () => {
    const document = { format: "bansoko-naming-vars", format_version: 1, runtime: "naming-vars-v1", digest: "sha256:2", revision: 2, note: "", exported_at: "2026-10-12T08:00:00Z", source: ACTIVE_SOURCE };
    stubApi(stateOf(), (route) => (route === "GET /naming-vars/export" ? { body: document } : undefined));
    const createObjectURL = vi.fn(() => "blob:test");
    const revokeObjectURL = vi.fn();
    Object.assign(URL, { createObjectURL, revokeObjectURL });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: t("varsEditor.export") }));
    await waitFor(() => expect(click).toHaveBeenCalled());
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    click.mockRestore();
  });

  it("imports a chosen file only after confirmation, against the active revision", async () => {
    const document = { format: "bansoko-naming-vars", format_version: 1, runtime: "naming-vars-v1", digest: "sha256:x", revision: 7, note: "from elsewhere", exported_at: "2026-10-12T08:00:00Z", source: "vars.a = {b: '1'};\n" };
    const calls = stubApi(stateOf(), (route) =>
      route === "POST /naming-vars/imports" ? { status: 201, body: { version: version(3, true, { origin: "import" }), source: document.source } } : undefined,
    );
    const picked: HTMLInputElement[] = [];
    const realCreate = window.document.createElement.bind(window.document);
    vi.spyOn(window.document, "createElement").mockImplementation((tag: string, options?: ElementCreationOptions) => {
      const element = realCreate(tag, options);
      if (tag === "input") {
        picked.push(element as HTMLInputElement);
        vi.spyOn(element, "click").mockImplementation(() => undefined);
      }
      return element;
    });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: t("varsEditor.import") }));
    const input = picked[0];
    expect(input.type).toBe("file");
    const file = new File([JSON.stringify(document)], "naming-vars.json", { type: "application/json" });
    Object.defineProperty(input, "files", { value: [file] });
    input.onchange?.(new Event("change"));
    expect(await screen.findByText(t("varsEditor.importTitle"))).toBeInTheDocument();
    expect(calls.some((call) => call.url === "/naming-vars/imports")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: t("varsEditor.importConfirm") }));
    await waitFor(() => expect(calls.some((call) => call.url === "/naming-vars/imports")).toBe(true));
    const post = calls.find((call) => call.url === "/naming-vars/imports");
    expect(post?.headers.get("If-Match")).toBe("2");
    expect(post?.body).toEqual(document);
    vi.restoreAllMocks();
  });

  it("refuses a file that is not an export without calling the server", async () => {
    const calls = stubApi(stateOf());
    const picked: HTMLInputElement[] = [];
    const realCreate = window.document.createElement.bind(window.document);
    vi.spyOn(window.document, "createElement").mockImplementation((tag: string, options?: ElementCreationOptions) => {
      const element = realCreate(tag, options);
      if (tag === "input") {
        picked.push(element as HTMLInputElement);
        vi.spyOn(element, "click").mockImplementation(() => undefined);
      }
      return element;
    });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: t("varsEditor.import") }));
    Object.defineProperty(picked[0], "files", { value: [new File(["{\"format\":\"other\"}"], "x.json")] });
    picked[0].onchange?.(new Event("change"));
    expect(await screen.findByText(t("varsEditor.importNotFile"))).toBeInTheDocument();
    expect(calls.some((call) => call.url === "/naming-vars/imports")).toBe(false);
    vi.restoreAllMocks();
  });
});
