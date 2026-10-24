import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Button, Field, Input, PasswordInput, Select, SpinnerIcon, TabsList, TabsPanel, TabsRoot, TagInput, Textarea } from "./controls";

// Form controls: Select with disabled state and options, an empty-string option
// value, password reveal, textarea, destructive button variants.

describe("Select", () => {
  it("带 group 的选项在同名标题下分组展示，选择仍回传 value", async () => {
    const onChange = vi.fn();
    render(
      <Select
        label="应用季度"
        value="2"
        onChange={onChange}
        options={[
          { value: "1", label: "S1", group: "真实季度" },
          { value: "2", label: "S2", group: "真实季度" },
          { value: "custom", label: "自定义", group: "自定义" },
        ]}
      />,
    );
    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: "应用季度" }));
    expect(await screen.findByText("真实季度")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "真实季度" })).toContainElement(screen.getByRole("option", { name: "S1" }));
    await user.click(screen.getByRole("option", { name: "自定义" }));
    expect(onChange).toHaveBeenCalledWith("custom");
  });

  it("disabled 时无法展开", async () => {
    render(<Select label="类型" value="a" disabled options={[{ value: "a", label: "甲" }]} onChange={() => {}} />);
    await userEvent.click(screen.getByRole("combobox", { name: "类型" }));
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("禁用的选项带 aria-disabled 且点击不触发 onChange", async () => {
    const onChange = vi.fn();
    render(
      <Select
        label="类型"
        value="a"
        options={[
          { value: "a", label: "甲" },
          { value: "b", label: "乙（已占用）", disabled: true },
        ]}
        onChange={onChange}
      />,
    );
    await userEvent.click(screen.getByRole("combobox", { name: "类型" }));
    const disabled = await screen.findByRole("option", { name: "乙（已占用）" });
    expect(disabled).toHaveAttribute("aria-disabled", "true");
    await userEvent.click(disabled);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("空字符串也是合法的选项值（如「直连」）", async () => {
    function Harness() {
      const [value, setValue] = useState("r1");
      return (
        <Select
          label="路由"
          value={value}
          options={[
            { value: "", label: "直连" },
            { value: "r1", label: "家宽出口" },
          ]}
          onChange={setValue}
        />
      );
    }
    render(<Harness />);
    expect(screen.getByRole("combobox", { name: "路由" })).toHaveTextContent("家宽出口");
    await userEvent.click(screen.getByRole("combobox", { name: "路由" }));
    expect(await screen.findByRole("option", { name: "家宽出口" })).toHaveAttribute("aria-selected", "true");
    await userEvent.click(screen.getByRole("option", { name: "直连" }));
    expect(screen.getByRole("combobox", { name: "路由" })).toHaveTextContent("直连");
  });
});

describe("PasswordInput", () => {
  it("默认遮罩，点击眼睛切换明文", async () => {
    render(<PasswordInput aria-label="Token" defaultValue="secret" />);
    const input = screen.getByLabelText("Token");
    expect(input).toHaveAttribute("type", "password");
    await userEvent.click(screen.getByRole("button", { name: "显示" }));
    expect(input).toHaveAttribute("type", "text");
    await userEvent.click(screen.getByRole("button", { name: "隐藏" }));
    expect(input).toHaveAttribute("type", "password");
  });
});

describe("Textarea", () => {
  it("受控输入并透传 rows", async () => {
    const onChange = vi.fn();
    render(<Textarea aria-label="正文" rows={2} value="a" onChange={onChange} />);
    const area = screen.getByLabelText("正文");
    expect(area).toHaveAttribute("rows", "2");
    await userEvent.type(area, "b");
    expect(onChange).toHaveBeenCalled();
  });
});

describe("Button 危险变体", () => {
  it("dangerOutline / dangerGhost 都用 destructive 文字，不使用实心底", () => {
    render(
      <>
        <Button variant="dangerOutline">撤销</Button>
        <Button variant="dangerGhost">删除</Button>
      </>,
    );
    for (const name of ["撤销", "删除"]) {
      const className = screen.getByRole("button", { name }).className;
      expect(className).toContain("tw:text-destructive");
      expect(className).not.toContain("tw:bg-destructive");
    }
  });

  it("icon 尺寸是 36px 方形且不带横向内边距；secondary 的按下态走 aria-pressed", () => {
    render(
      <Button size="icon" aria-label="筛选" aria-pressed>
        x
      </Button>,
    );
    const button = screen.getByRole("button", { name: "筛选" });
    expect(button).toHaveClass("tw:size-9", "tw:aria-pressed:bg-primary/10");
    expect(button).not.toHaveClass("tw:h-9");
    expect(button.className).not.toMatch(/tw:px-/);
  });

  it("loading 时禁用并标记 aria-busy", () => {
    render(<Button loading>保存</Button>);
    const button = screen.getByRole("button", { name: "保存" });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("aria-busy", "true");
  });

  it("禁用的主按钮换成 muted 底与 muted 字，不只降透明度；loading 保持主按钮外观", () => {
    const { rerender } = render(<Button variant="primary" disabled>保存</Button>);
    const button = screen.getByRole("button", { name: "保存" });
    expect(button).toHaveClass("tw:bg-muted", "tw:text-muted-foreground");
    expect(button).not.toHaveClass("tw:bg-primary");
    expect(button.className).not.toMatch(/opacity/);

    rerender(<Button variant="primary" loading>保存</Button>);
    expect(screen.getByRole("button", { name: "保存" })).toHaveClass("tw:bg-primary", "tw:text-primary-foreground");
  });
});

describe("SpinnerIcon", () => {
  it("SpinnerIcon 对辅助技术隐藏", () => {
    render(<SpinnerIcon />);
    expect(document.querySelector("svg.lucide-loader-circle, svg.lucide-loader-2")).toHaveAttribute("aria-hidden", "true");
  });
});

// Input/Field/TagInput are these controls, so size and width options must stay
// one class per property.
describe("Input sizes", () => {
  it("默认 36px 占满容器；sm 为 32px；fullWidth=false 不输出宽度类", () => {
    render(
      <>
        <Input aria-label="默认" />
        <Input aria-label="紧凑" controlSize="sm" fullWidth={false} className="tw:w-24" />
      </>,
    );
    const normal = screen.getByLabelText("默认");
    expect(normal).toHaveClass("tw:h-9", "tw:w-full");
    expect(normal).not.toHaveClass("tw:h-8");
    const compact = screen.getByLabelText("紧凑");
    expect(compact).toHaveClass("tw:h-8", "tw:w-24");
    expect(compact).not.toHaveClass("tw:h-9");
    expect(compact).not.toHaveClass("tw:w-full");
  });

  it("leadingIcon：图标在包裹层里，宽度类给包裹层，输入框自己让出图标的内边距", () => {
    render(<Input aria-label="筛选" fullWidth={false} className="tw:w-56" leadingIcon={<svg data-testid="icon" />} />);
    const input = screen.getByLabelText("筛选");
    expect(input).toHaveClass("tw:pl-9", "tw:pr-3", "tw:w-full", "tw:h-9");
    expect(input).not.toHaveClass("tw:px-3");
    expect(input.parentElement).toHaveClass("tw:w-56");
    expect(screen.getByTestId("icon").parentElement).toHaveAttribute("aria-hidden", "true");
  });

  it("Textarea 不再同时带固定高度与 auto 高度", () => {
    render(<Textarea aria-label="说明" />);
    expect(screen.getByLabelText("说明")).not.toHaveClass("tw:h-9");
  });
});

describe("Field", () => {
  it("标签关联控件并显示提示行", () => {
    render(
      <Field label="来源偏移" hint="留空表示继承默认值">
        <Input />
      </Field>,
    );
    expect(screen.getByLabelText(/来源偏移/)).toBeInstanceOf(HTMLInputElement);
    expect(screen.getByText("留空表示继承默认值")).toBeInTheDocument();
  });
});

describe("TagInput", () => {
  function Controlled({ validateItem }: { validateItem?: (tag: string) => string | null }) {
    const [value, setValue] = useState<string[]>(["简中"]);
    return <TagInput ariaLabel="关键词" value={value} onChange={setValue} validateItem={validateItem} />;
  }

  it("Enter 提交、× 删除、空输入 Backspace 删最后一项", () => {
    render(<Controlled />);
    const input = screen.getByRole("textbox", { name: "关键词" });
    fireEvent.change(input, { target: { value: "1080p" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByText("1080p")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "删除 简中" }));
    expect(screen.queryByText("简中")).not.toBeInTheDocument();
    fireEvent.keyDown(input, { key: "Backspace" });
    expect(screen.queryByText("1080p")).not.toBeInTheDocument();
  });

  it("校验失败时内联报错且不提交", () => {
    render(<Controlled validateItem={(tag) => (tag.startsWith("(") ? "正则不合法" : null)} />);
    const input = screen.getByRole("textbox", { name: "关键词" });
    fireEvent.change(input, { target: { value: "(" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByRole("alert")).toHaveTextContent("正则不合法");
    expect(input).toHaveValue("(");
  });

  it("只读态只渲染徽章与继承提示", () => {
    render(<TagInput value={["OVA"]} readOnly globalHint="全局：OVA" />);
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByText("OVA")).toBeInTheDocument();
    expect(screen.getByText("全局：OVA")).toBeInTheDocument();
  });
});

describe("Tabs parts", () => {
  function Host() {
    const [tab, setTab] = useState("a");
    return (
      <TabsRoot value={tab} onChange={setTab}>
        <TabsList id="strip" items={[{ key: "a", label: "甲" }, { key: "b", label: "乙" }]} />
        <div>
          <TabsPanel value="a" keepMounted>
            <input aria-label="甲输入" defaultValue="" />
          </TabsPanel>
          <TabsPanel value="b" keepMounted>
            乙内容
          </TabsPanel>
        </div>
      </TabsRoot>
    );
  }

  it("切换页签时 keepMounted 面板保留未保存输入", async () => {
    render(<Host />);
    expect(document.getElementById("strip")).toHaveAttribute("role", "tablist");
    fireEvent.change(screen.getByLabelText("甲输入"), { target: { value: "草稿" } });
    await userEvent.click(screen.getByRole("tab", { name: "乙" }));
    expect(screen.getByRole("tab", { name: "乙" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel", { name: "乙" })).toHaveTextContent("乙内容");
    await userEvent.click(screen.getByRole("tab", { name: "甲" }));
    expect(screen.getByLabelText("甲输入")).toHaveValue("草稿");
  });
});
