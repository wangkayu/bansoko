import { render, screen } from "@testing-library/react";
import { CheckCircle2 } from "lucide-react";
import { describe, expect, it } from "vitest";
import { Chip } from "./Chip";
import { StatePill } from "./StatePill";

// Status = round dot + label, no box. Calm tones are muted inline text;
// warning/offline add a soft tint behind ink text. Category labels are Chip: a
// filled capsule without dot or outline.

const OLD_LOOK = /state-pill|tw:border(?:\s|$)|tw:border-(?:online|warning|offline|border|muted-foreground)|size-\[7px\]|rounded-\[2px\]/;

function dotOf(el: HTMLElement) {
  return el.querySelector(":scope > span[aria-hidden='true']");
}

describe("StatePill", () => {
  it.each(["online", "info", "muted"] as const)("tone=%s 是无框的行内说明：次要色文字、无底色", (tone) => {
    render(<StatePill tone={tone}>测试状态</StatePill>);
    const pill = screen.getByText("测试状态");
    expect(pill).toHaveClass("tw:text-muted-foreground", "tw:font-normal");
    expect(pill.className).not.toMatch(/tw:bg-|tw:px-/);
    expect(pill.className).not.toMatch(OLD_LOOK);
  });

  it.each([
    ["warning", "tw:bg-warning/12", "tw:bg-warning"],
    ["offline", "tw:bg-offline/12", "tw:bg-offline"],
  ] as const)("tone=%s 用同色系浅底 + 中等字重正文色突出", (tone, ground, dot) => {
    render(<StatePill tone={tone}>需要注意</StatePill>);
    const pill = screen.getByText("需要注意");
    expect(pill).toHaveClass(ground, "tw:text-foreground", "tw:font-medium", "tw:rounded-control");
    expect(pill.className).not.toMatch(OLD_LOOK);
    expect(dotOf(pill)).toHaveClass(dot, "tw:rounded-full", "tw:size-1.5");
  });

  it("圆点对辅助技术隐藏，文字始终在场；muted 是空心圆，其余实心", () => {
    render(
      <>
        <StatePill tone="online">在线</StatePill>
        <StatePill>默认</StatePill>
      </>,
    );
    const online = dotOf(screen.getByText("在线"));
    expect(online).toHaveAttribute("aria-hidden", "true");
    expect(online).toHaveClass("tw:bg-online", "tw:rounded-full");
    const muted = dotOf(screen.getByText("默认"));
    expect(muted).toHaveClass("tw:border-muted-foreground", "tw:rounded-full");
    expect(muted?.className).not.toMatch(/tw:bg-/);
  });

  it("busy 的圆点只在允许动效时闪动", () => {
    render(<StatePill busy>读取中</StatePill>);
    const dot = dotOf(screen.getByText("读取中"));
    expect(dot).toHaveClass("tw:motion-safe:animate-pulse", "tw:bg-muted-foreground");
    expect(dot?.className).not.toMatch(/(?:^|\s)tw:animate-/);
  });

  it("有 icon 时 icon 代替圆点，带语气色且对辅助技术隐藏", () => {
    render(<StatePill tone="online" icon={CheckCircle2}>带图标</StatePill>);
    const pill = screen.getByText("带图标");
    const icon = pill.querySelector("svg");
    expect(icon).toHaveClass("tw:text-online");
    expect(icon).toHaveAttribute("aria-hidden", "true");
    expect(dotOf(pill)).toBeNull();
  });

  it("透传 title 与外层 className", () => {
    render(<StatePill tone="warning" title="说明" className="tw:shrink-0">合并</StatePill>);
    const pill = screen.getByTitle("说明");
    expect(pill).toHaveTextContent("合并");
    expect(pill).toHaveClass("tw:shrink-0", "tw:bg-warning/12");
  });
});

describe("Chip", () => {
  it("默认 neutral：muted 填充胶囊、正文色，无圆点无描边", () => {
    render(<Chip title="类型">qBittorrent</Chip>);
    const chip = screen.getByTitle("类型");
    expect(chip).toHaveTextContent("qBittorrent");
    expect(chip).toHaveClass("tw:bg-muted", "tw:text-foreground", "tw:rounded-control");
    expect(chip.querySelector("span[aria-hidden='true']")).toBeNull();
    expect(chip.className).not.toMatch(OLD_LOOK);
  });

  it("accent 用于当前默认/生效项", () => {
    render(<Chip tone="accent">默认模板</Chip>);
    expect(screen.getByText("默认模板")).toHaveClass("tw:bg-accent", "tw:text-accent-foreground");
  });
});
