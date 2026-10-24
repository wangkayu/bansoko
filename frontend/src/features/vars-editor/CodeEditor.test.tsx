import { act, render, screen } from "@testing-library/react";
import { createRef } from "react";
import { describe, expect, it, vi } from "vitest";
import CodeEditor, { type CodeEditorHandle } from "./CodeEditor";

// jsdom has no layout, so CodeMirror's measuring is stubbed; what is checked is
// that the real editor mounts, highlights JavaScript, reports edits, inserts at
// the cursor and shows the diagnostics it is given.
function stubLayout() {
  const rect = { x: 0, y: 0, top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0, toJSON: () => ({}) };
  Range.prototype.getBoundingClientRect = () => rect as DOMRect;
  Range.prototype.getClientRects = () => ({ length: 0, item: () => null, [Symbol.iterator]: function* () {} }) as unknown as DOMRectList;
  document.elementFromPoint = () => null;
}

describe("CodeEditor", () => {
  it("highlights JavaScript, reports edits and inserts at the cursor", async () => {
    stubLayout();
    const onChange = vi.fn();
    const ref = createRef<CodeEditorHandle>();
    render(<CodeEditor ref={ref} value={"vars.a = {b: 'x'}; // note"} onChange={onChange} label="Program" />);
    const content = screen.getByRole("textbox", { name: "Program" });
    expect(content.textContent).toContain("vars.a");
    // Highlight classes come from the HighlightStyle, so a token is wrapped in a styled span.
    expect(content.querySelectorAll("span[class]").length).toBeGreaterThan(0);

    act(() => ref.current?.insert("input.anime.title"));
    expect(onChange).toHaveBeenLastCalledWith(expect.stringContaining("input.anime.title"));
  });

  it("shows given diagnostics and respects read-only", () => {
    stubLayout();
    const ref = createRef<CodeEditorHandle>();
    const onChange = vi.fn();
    const { container } = render(
      <CodeEditor ref={ref} value={"vars.a = {b: };"} readOnly onChange={onChange} label="Program" markers={[{ from: 13, to: 14, message: "Unexpected token", severity: "error" }]} />,
    );
    expect(container.querySelector(".cm-lintRange-error")).not.toBeNull();
    act(() => ref.current?.insert("x"));
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.getByRole("textbox", { name: "Program" })).toHaveAttribute("contenteditable", "false");
  });
});
