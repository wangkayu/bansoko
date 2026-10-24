import { describe, expect, it } from "vitest";
import {
  BASE_INPUTS,
  byteOffsetToIndex,
  catalogFacts,
  diagnosticRange,
  dryRunTargets,
  inputToken,
  lineOf,
  markersFor,
  normalizeSource,
  saveBlocker,
} from "./varsEditor";

describe("byteOffsetToIndex", () => {
  it("is the identity for ASCII", () => {
    expect(byteOffsetToIndex("vars.a = 1;", 5)).toBe(5);
  });

  it("converts UTF-8 byte offsets to UTF-16 indexes across CJK and astral characters", () => {
    const text = "// 标题 😀\nvars.a = 1;";
    const offset = new TextEncoder().encode("// 标题 😀\n").length;
    expect(byteOffsetToIndex(text, offset)).toBe("// 标题 😀\n".length);
  });

  it("clamps out-of-range and middle-of-character offsets", () => {
    expect(byteOffsetToIndex("ab", 99)).toBe(2);
    expect(byteOffsetToIndex("标", 1)).toBe(0);
    expect(byteOffsetToIndex("ab", -3)).toBe(0);
  });
});

describe("diagnosticRange", () => {
  it("maps start/end bytes onto the text", () => {
    const text = "// 说明\nvars.a = {b: };";
    const start = new TextEncoder().encode("// 说明\nvars.a = {b: ").length;
    expect(diagnosticRange(text, { start, end: start + 1 })).toEqual({ from: text.indexOf("}"), to: text.indexOf("}") + 1 });
  });

  it("widens an empty span to one character and ignores unlocated findings", () => {
    expect(diagnosticRange("abc", { start: 1, end: 1 })).toEqual({ from: 1, to: 2 });
    expect(diagnosticRange("abc", { start: 3, end: 3 })).toEqual({ from: 2, to: 3 });
    expect(diagnosticRange("abc", {})).toBeUndefined();
    expect(diagnosticRange("", { start: 0, end: 0 })).toBeUndefined();
  });

  it("builds markers only for located findings", () => {
    const markers = markersFor("abc\ndef", [{ message: "a", start: 4, end: 7 }, { message: "b" }], "error");
    expect(markers).toEqual([{ from: 4, to: 7, message: "a", severity: "error" }]);
  });
});

describe("source helpers", () => {
  it("normalizes line endings", () => {
    expect(normalizeSource("a\r\nb\rc")).toBe("a\nb\nc");
  });

  it("counts lines from 1", () => {
    expect(lineOf("a\nb\nc", 0)).toBe(1);
    expect(lineOf("a\nb\nc", 4)).toBe(3);
  });

  it("writes input references", () => {
    expect(inputToken("anime.title")).toBe("input.anime.title");
  });

  it("puts the base inputs ahead of the registered facts", () => {
    const facts = catalogFacts([{ path: "anime.title", type: "string", nullable: true, label_key: "facts.anime.title.label", description_key: "facts.anime.title.description", sample_value: "Example" }]);
    expect(facts.slice(0, BASE_INPUTS.length).map((fact) => fact.path)).toEqual(["release_title", "files"]);
    expect(facts[2]).toMatchObject({ path: "anime.title", nullable: true, sample_value: "Example" });
  });

  it("blocks empty, oversized and unchanged saves", () => {
    expect(saveBlocker("  \n", "x", 100)).toBe("empty");
    expect(saveBlocker("x".repeat(101), "x", 100)).toBe("tooLarge");
    expect(saveBlocker("标".repeat(40), "x", 100)).toBe("tooLarge");
    expect(saveBlocker("x\r\n", "x\n", 100)).toBe("unchanged");
    expect(saveBlocker("y", "x", 100)).toBeUndefined();
  });
});

describe("dryRunTargets", () => {
  it("lists samples, then the release, then the typed-in title", () => {
    expect(dryRunTargets({ samples: ["tv_full"], releaseId: "r1", customTitle: "  T  " })).toEqual([
      { kind: "sample", sample: "tv_full" },
      { kind: "release", release_id: "r1" },
      { kind: "custom", title: "T" },
    ]);
    expect(dryRunTargets({ samples: [], releaseId: "", customTitle: " " })).toEqual([]);
  });
});
