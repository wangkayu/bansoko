import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { TagInput } from "./base/controls";

function Controlled({
  initial = [] as string[],
  onChange,
  validateItem,
  placeholder,
}: {
  initial?: string[];
  onChange?: (next: string[]) => void;
  validateItem?: (tag: string) => string | null;
  placeholder?: string;
}) {
  const [value, setValue] = useState(initial);
  return (
    <TagInput
      value={value}
      placeholder={placeholder}
      validateItem={validateItem}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
    />
  );
}

describe("TagInput", () => {
  it("Enter 提交输入为徽章并清空输入框", () => {
    const onChange = vi.fn();
    render(<Controlled onChange={onChange} />);
    const input = screen.getByRole("textbox");
    fireEvent.change(input, { target: { value: "剧场版" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onChange).toHaveBeenLastCalledWith(["剧场版"]);
    expect(screen.getByText("剧场版")).toBeInTheDocument();
    expect(input).toHaveValue("");
  });

  it("失焦提交当前输入", () => {
    const onChange = vi.fn();
    render(<Controlled onChange={onChange} />);
    const input = screen.getByRole("textbox");
    fireEvent.change(input, { target: { value: "1080p" } });
    fireEvent.blur(input);
    expect(onChange).toHaveBeenLastCalledWith(["1080p"]);
  });

  it("自动去重，不重复添加已有徽章", () => {
    const onChange = vi.fn();
    render(<Controlled initial={["简中"]} onChange={onChange} />);
    const input = screen.getByRole("textbox");
    fireEvent.change(input, { target: { value: "简中" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onChange).not.toHaveBeenCalled();
    expect(input).toHaveValue("");
  });

  it("× 删除单个徽章", () => {
    const onChange = vi.fn();
    render(<Controlled initial={["A", "B"]} onChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: "删除 A" }));
    expect(onChange).toHaveBeenLastCalledWith(["B"]);
  });

  it("输入为空时 Backspace 删除最后一枚", () => {
    const onChange = vi.fn();
    render(<Controlled initial={["A", "B"]} onChange={onChange} />);
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Backspace" });
    expect(onChange).toHaveBeenLastCalledWith(["A"]);
  });

  it("validateItem 返回错误时拦截提交并内联提示", () => {
    const onChange = vi.fn();
    render(<Controlled onChange={onChange} validateItem={(tag) => (tag.length > 3 ? "太长" : null)} />);
    const input = screen.getByRole("textbox");
    fireEvent.change(input, { target: { value: "太长的词" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent("太长");
  });

  it("validateItem 校验通过后错误提示消失", () => {
    const onChange = vi.fn();
    render(<Controlled onChange={onChange} validateItem={(tag) => (tag.length > 3 ? "太长" : null)} />);
    const input = screen.getByRole("textbox");
    fireEvent.change(input, { target: { value: "太长的词" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByRole("alert")).toBeInTheDocument();
    fireEvent.change(input, { target: { value: "短" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onChange).toHaveBeenLastCalledWith(["短"]);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("只读态渲染灰徽章，无输入框，可显示全局提示", () => {
    render(<TagInput value={["剧场版", "OVA"]} readOnly globalHint="全局：剧场版, OVA" />);
    expect(screen.getByText("剧场版")).toBeInTheDocument();
    expect(screen.getByText("OVA")).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByText("全局：剧场版, OVA")).toBeInTheDocument();
  });

  it("只读空列表显示占位", () => {
    render(<TagInput value={[]} readOnly />);
    expect(screen.getByText("（空）")).toBeInTheDocument();
  });
});
