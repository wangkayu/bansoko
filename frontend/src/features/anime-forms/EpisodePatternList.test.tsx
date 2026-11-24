import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import {
  DEFAULT_EPISODE_PATTERNS,
  hasBlockingEpisodePatternError,
  testEpisodePatterns,
  validateGoRegex,
} from "../../lib/goRegex";
import { t } from "../../lib/i18n";
import { EpisodePatternList } from "./EpisodePatternList";
import { RegexTesterDialog } from "./RegexTesterDialog";

function ControlledList({
  initial = [] as string[],
  onChange,
  sampleTitles,
}: {
  initial?: string[];
  onChange?: (next: string[]) => void;
  sampleTitles?: string[];
}) {
  const [patterns, setPatterns] = useState(initial);
  const [testerOpen, setTesterOpen] = useState(false);
  const update = (next: string[]) => {
    setPatterns(next);
    onChange?.(next);
  };
  return (
    <>
      <EpisodePatternList
        value={patterns}
        onChange={update}
        onOpenTester={() => setTesterOpen(true)}
      />
      <RegexTesterDialog
        open={testerOpen}
        onClose={() => setTesterOpen(false)}
        patterns={patterns}
        onApply={update}
        sampleTitles={sampleTitles}
      />
    </>
  );
}

describe("goRegex", () => {
  it("校验 Go RE2 不兼容语法与捕获组提醒，并支持 Go 命名捕获组和内联标志", () => {
    expect(validateGoRegex("(?<=\\[)\\d{2}(?=\\])")?.severity).toBe("error");
    expect(validateGoRegex("(\\d+)\\1")?.severity).toBe("error");
    expect(validateGoRegex("[unclosed")?.severity).toBe("error");
    expect(validateGoRegex("第\\s*\\d+\\s*话", { requireCapture: true })?.severity).toBe("warning");
    expect(validateGoRegex("  EP(\\d+)  ", { requireCapture: true })?.severity).toBe("warning");
    expect(validateGoRegex("(?i)\\[E(?P<episode>\\d{1,3})\\]", { requireCapture: true })).toBeNull();
  });

  it("接受可映射的 Go 语法，拒绝双方都不支持的转义，局部标志只提示预览限制", () => {
    expect(validateGoRegex("\\R")?.severity).toBe("error");
    expect(validateGoRegex("\\p{Han}(\\d+)")).toBeNull();
    expect(validateGoRegex("(?<episode>\\d+)")).toBeNull();
    expect(validateGoRegex("\\Q(?=x)\\E(\\d+)")).toBeNull();
    expect(validateGoRegex("EP(?i)(\\d+)")?.severity).toBe("warning");
    expect(validateGoRegex("(?i:EP(\\d+))")?.severity).toBe("warning");
  });

  it("按第一个非空捕获提取，不会跳过非数字捕获去取后面的组", () => {
    const hit = testEpisodePatterns("Show ABC 12", ["(ABC) (\\d+)"]);
    expect(hit).toEqual({ matched: false, usedDefaults: false });
  });

  it("镜像集数规范化、空条目的内置规则和 Go 字面量匹配", () => {
    const versioned = testEpisodePatterns("Show - 05v2", ["- (\\d+v\\d+)"]);
    expect(versioned.matched).toBe(true);
    if (versioned.matched) expect(versioned.episode).toBe("5");

    const fallback = testEpisodePatterns("Show - 07", ["EP(\\d+)", "", "Show - (\\d+)"]);
    expect(fallback.matched).toBe(true);
    if (fallback.matched) {
      expect(fallback.usedDefaults).toBe(true);
      expect(fallback.episode).toBe("7");
    }

    const quoted = testEpisodePatterns("literal (?=x) 08", ["\\Q(?=x) \\E(\\d+)"]);
    expect(quoted.matched).toBe(true);
    if (quoted.matched) expect(quoted.episode).toBe("8");
  });

  it("接受 JS u 模式不支持但 Go 合法的转义与 Unicode 属性", () => {
    for (const pattern of [
      "\\- (\\d+)",
      "\\_(\\d+)",
      "\\#(\\d+)",
      "[\\-\\_](\\d+)",
      "\\p{ASCII}(\\d+)",
      "\\p{Letter}(\\d+)",
      "\\p{Han}(\\d+)",
      "\\pL(\\d+)",
    ]) {
      expect(validateGoRegex(pattern, { requireCapture: true }), pattern).toBeNull();
    }
    const hit = testEpisodePatterns("Show - 05", ["\\- (\\d+)"]);
    expect(hit.matched).toBe(true);
    if (hit.matched) expect(hit.episode).toBe("5");
  });

  it("Go 合法但无法本地预览的语法只给警告，不阻断", () => {
    for (const pattern of ["\\A(\\d+)\\z", "[[:digit:]]+x(\\d)", "(?U).*(\\d+)", "(?i:EP(\\d+))", "\\12(\\d+)", "\\123(\\d+)", "\\0(\\d+)", "\\a(\\d+)", "\\x{31}(\\d+)"]) {
      expect(validateGoRegex(pattern, { requireCapture: true })?.severity, pattern).toBe("warning");
    }
    expect(validateGoRegex("\\R")?.severity).toBe("error");
  });

  it("\\\\Q 是转义的反斜杠加字母 Q，不进入字面量模式", () => {
    expect(validateGoRegex("\\\\Q(\\d+)", { requireCapture: true })).toBeNull();
    const hit = testEpisodePatterns("a\\Q 07", ["\\\\Q (\\d+)"]);
    expect(hit.matched).toBe(true);
    if (hit.matched) expect(hit.episode).toBe("7");
  });

  it("hasBlockingEpisodePatternError 仅在确定不兼容或超过上限时阻断", () => {
    expect(hasBlockingEpisodePatternError(["EP(\\d+)", "\\- (\\d+)", "(?U).*(\\d+)", ""])).toBe(false);
    expect(hasBlockingEpisodePatternError(["EP(\\d+)", "(?=x)(\\d+)"])).toBe(true);
    expect(hasBlockingEpisodePatternError(Array.from({ length: 17 }, (_, index) => `E${index}(\\d+)`))).toBe(true);
  });

  it("testEpisodePatterns 按顺序命中并支持内置兜底与小数集", () => {
    const hit = testEpisodePatterns("[ANi] 败犬女主太多了！ - 07.5 [1080P]", []);
    expect(hit.matched).toBe(true);
    if (hit.matched) {
      expect(hit.usedDefaults).toBe(true);
      expect(hit.episode).toBe("7.5");
      expect(hit.ruleIndex).toBe(0);
    }
  });
});

describe("EpisodePatternList & RegexTesterDialog", () => {
  it("支持原地编辑、上下调序、删除以及空列表一键填入内置规则", async () => {
    const onChange = vi.fn();
    render(<ControlledList initial={["EP(\\d+)", "\\[(\\d+)\\]"]} onChange={onChange} />);

    // Inline edit row 1
    const row1 = screen.getByLabelText(t("forms.rules.patternEditor.rowAria", { n: 1 }));
    fireEvent.change(row1, { target: { value: "E(\\d+)" } });
    expect(onChange).toHaveBeenLastCalledWith(["E(\\d+)", "\\[(\\d+)\\]"]);

    // Move row 2 up
    await userEvent.click(screen.getByLabelText(t("forms.rules.patternEditor.moveUp", { n: 2 })));
    expect(onChange).toHaveBeenLastCalledWith(["\\[(\\d+)\\]", "E(\\d+)"]);

    // Delete both, then fill built-ins
    await userEvent.click(screen.getByLabelText(t("ui.tagRemove", { tag: "\\[(\\d+)\\]" })));
    await userEvent.click(screen.getByLabelText(t("ui.tagRemove", { tag: "E(\\d+)" })));
    expect(onChange).toHaveBeenLastCalledWith([]);

    await userEvent.click(screen.getByRole("button", { name: t("forms.rules.patternEditor.fillBuiltins") }));
    expect(onChange).toHaveBeenLastCalledWith([...DEFAULT_EPISODE_PATTERNS]);
  });

  it("限制最多 16 条规则", async () => {
    const onChange = vi.fn();
    render(<ControlledList initial={Array.from({ length: 16 }, (_, index) => `E${index}(\\d+)`)} onChange={onChange} />);
    const input = screen.getByLabelText(t("settings.rules.episodePatterns"));
    fireEvent.change(input, { target: { value: "EP(\\d+)" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByRole("alert")).toHaveTextContent(t("forms.rules.patternEditor.maxLimit", { max: 16 }));
    expect(onChange).not.toHaveBeenCalled();
  });

  it("多行粘贴先去重再判断上限", () => {
    const onChange = vi.fn();
    const initial = Array.from({ length: 15 }, (_, index) => `E${index}(\\d+)`);
    render(<ControlledList initial={initial} onChange={onChange} />);
    const input = screen.getByLabelText(t("settings.rules.episodePatterns"));
    fireEvent.paste(input, { clipboardData: { getData: () => "E0(\\d+)\nEP(\\d+)" } });
    expect(onChange).toHaveBeenLastCalledWith([...initial, "EP(\\d+)"]);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("空行提示将回退到内置规则", () => {
    render(<ControlledList initial={["EP(\\d+)", ""]} />);
    expect(screen.getByText(t("forms.rules.patternEditor.emptyRowFallback"))).toBeInTheDocument();
  });

  it("试测弹窗中存在不兼容正则时禁用应用并提示", async () => {
    const onChange = vi.fn();
    render(<ControlledList initial={["EP(\\d+)"]} onChange={onChange} sampleTitles={["Show EP03"]} />);
    await userEvent.click(screen.getByRole("button", { name: t("forms.rules.patternTester.openButton") }));
    const dialog = await screen.findByRole("dialog", { name: t("forms.rules.patternTester.title") });
    const row = within(dialog).getByLabelText(t("forms.rules.patternEditor.rowAria", { n: 1 }));
    fireEvent.change(row, { target: { value: "(?=x)(\\d+)" } });
    expect(within(dialog).getByText(t("forms.rules.patternTester.blockedApply"))).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: t("forms.rules.patternTester.apply") })).toBeDisabled();
    expect(onChange).not.toHaveBeenCalled();
  });

  it("独立试测弹窗支持批量匹配、在弹窗内增补规则并应用回外层草稿", async () => {
    const onChange = vi.fn();
    render(
      <ControlledList
        initial={["第(\\d+)话"]}
        onChange={onChange}
        sampleTitles={[
          "[Sub] Show 第12话 [1080p]",
          "[Sub] Show S03E08 [1080p]",
        ]}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: t("forms.rules.patternTester.openButton") }));
    const dialog = await screen.findByRole("dialog", { name: t("forms.rules.patternTester.title") });
    expect(within(dialog).getByText(t("forms.rules.patternTester.summaryMatched", { matched: 1, total: 2 }))).toBeInTheDocument();

    // Add SxxExx rule inside dialog
    const addInput = within(dialog).getByLabelText(t("settings.rules.episodePatterns"));
    fireEvent.change(addInput, { target: { value: "S\\d+E0*(\\d+)" } });
    fireEvent.keyDown(addInput, { key: "Enter" });

    expect(within(dialog).getByText(t("forms.rules.patternTester.summaryMatched", { matched: 2, total: 2 }))).toBeInTheDocument();
    // Outer form is not mutated until Apply is clicked.
    expect(onChange).not.toHaveBeenCalled();

    await userEvent.click(within(dialog).getByRole("button", { name: t("forms.rules.patternTester.apply") }));
    expect(onChange).toHaveBeenLastCalledWith(["第(\\d+)话", "S\\d+E0*(\\d+)"]);
  });
});
