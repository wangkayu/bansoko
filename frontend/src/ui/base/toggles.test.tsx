import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Checkbox, RadioGroup, Segmented, Switch } from "./toggles";

// Selection controls: accessible role + name, controlled value round trip,
// disabled/loading states blocking input.

const MODES = [
  { value: "keep", label: "保留现有凭据" },
  { value: "replace", label: "替换为新值" },
  { value: "remove", label: "移除凭据", disabled: true },
] as const;
type Mode = (typeof MODES)[number]["value"];

describe("Checkbox", () => {
  it("可见文字即可访问名称，点击回传新值", async () => {
    function Harness() {
      const [checked, setChecked] = useState(false);
      return (
        <Checkbox checked={checked} onCheckedChange={setChecked}>
          显示被过滤
        </Checkbox>
      );
    }
    render(<Harness />);
    const box = screen.getByRole("checkbox", { name: "显示被过滤" });
    expect(box).toHaveAttribute("aria-checked", "false");
    await userEvent.setup().click(box);
    expect(box).toHaveAttribute("aria-checked", "true");
  });

  it("点击可见文字同样切换（等同原生 label），禁用时点文字不回调，title 说明禁用原因", async () => {
    const onCheckedChange = vi.fn();
    function Harness({ disabled }: { disabled?: boolean }) {
      const [checked, setChecked] = useState(false);
      return (
        <Checkbox
          size="sm"
          checked={checked}
          disabled={disabled}
          title={disabled ? "预览模式下不可操作" : undefined}
          onCheckedChange={(next) => {
            onCheckedChange(next);
            setChecked(next);
          }}
        >
          显示被过滤
        </Checkbox>
      );
    }
    const user = userEvent.setup();
    const { unmount } = render(<Harness />);
    await user.click(screen.getByText("显示被过滤"));
    expect(onCheckedChange).toHaveBeenLastCalledWith(true);
    expect(screen.getByRole("checkbox", { name: "显示被过滤" })).toHaveAttribute("aria-checked", "true");
    await user.click(screen.getByText("显示被过滤"));
    expect(onCheckedChange).toHaveBeenLastCalledWith(false);
    unmount();

    onCheckedChange.mockClear();
    render(<Harness disabled />);
    await user.click(screen.getByText("显示被过滤"));
    expect(onCheckedChange).not.toHaveBeenCalled();
    expect(screen.getByRole("checkbox", { name: "显示被过滤" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByTitle("预览模式下不可操作")).toBeInTheDocument();
  });

  it("带 aria-label 与可见文字时可访问名称取 aria-label", () => {
    render(
      <Checkbox checked={false} onCheckedChange={() => {}} aria-label="Always 组 计数">
        计数
      </Checkbox>,
    );
    expect(screen.getByRole("checkbox", { name: "Always 组 计数" })).toBeInTheDocument();
  });

  it("indeterminate 为 mixed；disabled 不回调", async () => {
    const onCheckedChange = vi.fn();
    render(
      <>
        <Checkbox checked={false} indeterminate onCheckedChange={() => {}} aria-label="全选" />
        <Checkbox checked={false} disabled onCheckedChange={onCheckedChange} aria-label="禁用项" />
      </>,
    );
    expect(screen.getByRole("checkbox", { name: "全选" })).toHaveAttribute("aria-checked", "mixed");
    await userEvent.setup().click(screen.getByRole("checkbox", { name: "禁用项" }));
    expect(onCheckedChange).not.toHaveBeenCalled();
  });
});

describe("Switch", () => {
  it("点击切换并回传新值", async () => {
    const onCheckedChange = vi.fn();
    render(<Switch checked={false} onCheckedChange={onCheckedChange} aria-label="全局自动对齐" />);
    await userEvent.setup().click(screen.getByRole("switch", { name: "全局自动对齐" }));
    expect(onCheckedChange).toHaveBeenCalledWith(true);
  });

  it("loading 期间禁用并标记 aria-busy", async () => {
    const onCheckedChange = vi.fn();
    render(<Switch checked loading onCheckedChange={onCheckedChange} aria-label="本番剧自动对齐" />);
    const control = screen.getByRole("switch", { name: "本番剧自动对齐" });
    expect(control).toHaveAttribute("aria-checked", "true");
    expect(control).toHaveAttribute("aria-busy", "true");
    expect(control).toHaveAttribute("aria-disabled", "true");
    await userEvent.setup().click(control);
    expect(onCheckedChange).not.toHaveBeenCalled();
  });
});

describe("RadioGroup", () => {
  it("按选项文字选择，禁用项不可选", async () => {
    const user = userEvent.setup();
    function Harness() {
      const [mode, setMode] = useState<Mode>("keep");
      return <RadioGroup aria-label="凭据" value={mode} onValueChange={setMode} options={MODES} />;
    }
    render(<Harness />);
    expect(screen.getByRole("radiogroup", { name: "凭据" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "保留现有凭据" })).toHaveAttribute("aria-checked", "true");

    await user.click(screen.getByRole("radio", { name: "替换为新值" }));
    expect(screen.getByRole("radio", { name: "替换为新值" })).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("radio", { name: "保留现有凭据" })).toHaveAttribute("aria-checked", "false");

    await user.click(screen.getByRole("radio", { name: "移除凭据" }));
    expect(screen.getByRole("radio", { name: "移除凭据" })).toHaveAttribute("aria-checked", "false");
  });
});

describe("RadioGroup card", () => {
  it("整行可点：点说明文字即选中，说明作为可访问描述；禁用时保留选中态", async () => {
    const user = userEvent.setup();
    const options = [
      { value: "a", label: "甲", description: "甲的说明" },
      { value: "b", label: "乙", description: "乙的说明" },
    ] as const;
    function Harness({ disabled }: { disabled?: boolean }) {
      const [value, setValue] = useState<"a" | "b">("a");
      return <RadioGroup aria-label="方式" variant="card" orientation="vertical" value={value} onValueChange={setValue} options={options} disabled={disabled} />;
    }
    const { unmount } = render(<Harness />);
    expect(screen.getByRole("radio", { name: "乙" })).toHaveAccessibleDescription("乙的说明");
    await user.click(screen.getByText("乙的说明"));
    expect(screen.getByRole("radio", { name: "乙" })).toHaveAttribute("aria-checked", "true");
    unmount();

    render(<Harness disabled />);
    expect(screen.getByRole("radio", { name: "甲" })).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("radio", { name: "乙" })).toHaveAttribute("aria-disabled", "true");
  });
});

describe("Segmented", () => {
  it("radio 语义，点击切换选中项", async () => {
    const onValueChange = vi.fn();
    render(
      <Segmented
        aria-label="名单模式"
        value="blacklist"
        onValueChange={onValueChange}
        options={[
          { value: "blacklist", label: "黑名单模式" },
          { value: "whitelist", label: "白名单模式" },
        ]}
      />,
    );
    expect(screen.getByRole("radiogroup", { name: "名单模式" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "黑名单模式" })).toHaveAttribute("aria-checked", "true");
    await userEvent.setup().click(screen.getByRole("radio", { name: "白名单模式" }));
    expect(onValueChange).toHaveBeenCalledWith("whitelist");
  });

  it("compact 尺寸只输出一套几何类，并透传 title", () => {
    render(
      <Segmented
        aria-label="选择策略"
        title="自动：按积分排名"
        size="compact"
        value="ranked"
        onValueChange={() => {}}
        options={[
          { value: "ranked", label: "自动" },
          { value: "never", label: "排除" },
        ]}
      />,
    );
    const group = screen.getByRole("radiogroup", { name: "选择策略" });
    expect(group).toHaveAttribute("title", "自动：按积分排名");
    expect(group).toHaveClass("tw:p-0.5");
    expect(group).not.toHaveClass("tw:p-1");
    const option = screen.getByRole("radio", { name: "自动" });
    expect(option).toHaveClass("tw:text-badge");
    expect(option).not.toHaveClass("tw:px-3");
  });

  it("选项 tone 只换选中态类：quiet 安静、danger 着色，默认仍是主色填充", () => {
    render(
      <Segmented
        aria-label="选择策略"
        size="compact"
        value="ranked"
        onValueChange={() => {}}
        options={[
          { value: "ranked", label: "自动", tone: "quiet" },
          { value: "never", label: "排除", tone: "danger" },
          { value: "plain", label: "普通" },
        ]}
      />,
    );
    const quiet = screen.getByRole("radio", { name: "自动" });
    expect(quiet).toHaveClass("tw:data-checked:bg-card");
    expect(quiet).not.toHaveClass("tw:data-checked:bg-primary");
    expect(screen.getByRole("radio", { name: "排除" })).toHaveClass("tw:data-checked:text-destructive");
    expect(screen.getByRole("radio", { name: "普通" })).toHaveClass("tw:data-checked:bg-primary");
  });
});
