import { describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { useState } from "react";
import { SavePartsProvider, useSavePart, useSaveParts, type SaveOutcome, type SaveRun } from "./saveParts";
import { changeCount, subgroupsChangeCount } from "./draft";

function Part({ id, count, save }: { id: string; count: number; save: () => Promise<SaveOutcome> }) {
  useSavePart(id, { count, label: `part-${id}`, save });
  return null;
}

function Harness({
  parts,
  onRun,
}: {
  parts: { id: string; count: number; save: () => Promise<SaveOutcome> }[];
  onRun: (run: SaveRun) => void;
}) {
  const registry = useSaveParts(["a", "b", "c"]);
  const [shown, setShown] = useState(true);
  return (
    <SavePartsProvider value={registry.registrar}>
      {shown && parts.map((part) => <Part key={part.id} {...part} />)}
      <span data-testid="count">{registry.count()}</span>
      <span data-testid="failed">{registry.failed ?? ""}</span>
      <button onClick={() => void registry.save().then(onRun)}>save</button>
      <button onClick={() => setShown(false)}>unmount</button>
    </SavePartsProvider>
  );
}

describe("useSaveParts", () => {
  it("submits dirty parts in order, stops at the first failure and names it", async () => {
    const calls: string[] = [];
    const ok = (id: string, message?: string) => async () => {
      calls.push(id);
      return { ok: true as const, message };
    };
    const fail = (id: string) => async () => {
      calls.push(id);
      return { ok: false as const };
    };
    const onRun = vi.fn();
    render(
      <Harness
        onRun={onRun}
        parts={[
          { id: "c", count: 1, save: ok("c") },
          { id: "b", count: 2, save: fail("b") },
          { id: "a", count: 1, save: ok("a", "saved a") },
        ]}
      />,
    );
    expect(screen.getByTestId("count")).toHaveTextContent("4");
    await act(async () => screen.getByText("save").click());
    expect(calls).toEqual(["a", "b"]);
    expect(onRun).toHaveBeenCalledWith({ ok: false, messages: ["saved a"] });
    expect(screen.getByTestId("failed")).toHaveTextContent("part-b");
  });

  it("skips clean parts and unregisters on unmount", async () => {
    const save = vi.fn(async () => ({ ok: true as const }));
    const onRun = vi.fn();
    render(<Harness onRun={onRun} parts={[{ id: "a", count: 0, save }, { id: "b", count: 1, save }]} />);
    await act(async () => screen.getByText("save").click());
    expect(save).toHaveBeenCalledTimes(1);
    act(() => screen.getByText("unmount").click());
    expect(screen.getByTestId("count")).toHaveTextContent("0");
  });
});

describe("change counts", () => {
  it("counts object fields and keyed entries, zero only when equal", () => {
    expect(changeCount({ a: 1, b: 2 }, { a: 1, b: 2 })).toBe(0);
    expect(changeCount({ a: 1, b: 2 }, { a: 3, b: 4 })).toBe(2);
    const entry = (ref: string, score: number) => ({
      provider: "mikan" as const,
      group_ref: ref,
      display_name: ref,
      selection_mode: "auto" as const,
      effective_selected: true,
      score,
    });
    expect(
      subgroupsChangeCount(
        { max_rss: 2, entries: [entry("1", 50), entry("2", 50)] },
        { max_rss: 3, entries: [entry("1", 60), entry("2", 50), entry("3", 50)] },
      ),
    ).toBe(3);
  });
});
