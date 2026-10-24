import { t } from "../lib/i18n";
import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { SecretField, type SecretMode } from "./SecretField";

function Controlled({
  editing = true,
  initialMode = "keep" as SecretMode,
  inputLabel,
  subject,
  removeHint,
  onModeChange,
  onChange,
}: {
  editing?: boolean;
  initialMode?: SecretMode;
  inputLabel?: string;
  subject?: string;
  removeHint?: string;
  onModeChange?: (mode: SecretMode) => void;
  onChange?: (next: string) => void;
}) {
  const [mode, setMode] = useState<SecretMode>(initialMode);
  const [value, setValue] = useState("");
  return (
    <SecretField
      editing={editing}
      mode={mode}
      subject={subject}
      removeHint={removeHint}
      inputLabel={inputLabel}
      value={value}
      onModeChange={(next) => {
        setMode(next);
        onModeChange?.(next);
      }}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
    />
  );
}

describe("SecretField", () => {
  it("新建模式只有输入、无 radio", () => {
    render(<Controlled editing={false} inputLabel="API Key" />);
    expect(screen.queryByRole("radio")).not.toBeInTheDocument();
    expect(screen.getByLabelText("API Key")).toBeInTheDocument();
  });

  it("新建输入必填值经 onChange 上报", () => {
    const onChange = vi.fn();
    render(<Controlled editing={false} inputLabel="API Key" onChange={onChange} />);
    fireEvent.change(screen.getByLabelText("API Key"), { target: { value: "sk-123" } });
    expect(onChange).toHaveBeenLastCalledWith("sk-123");
  });

  it("编辑模式显示三态 radio，keep 态显示保留说明且无输入", () => {
    render(<Controlled editing initialMode="keep" subject="bot_token" inputLabel="Bot Token" />);
    expect(screen.getByLabelText("保留现有凭据")).toBeChecked();
    expect(screen.getByText(`bot_token${t("secret.keepHint")}`)).toBeInTheDocument();
    expect(screen.queryByLabelText("Bot Token")).not.toBeInTheDocument();
  });

  it("切到替换为新值显示写入说明与条件输入", () => {
    const onModeChange = vi.fn();
    render(<Controlled editing initialMode="keep" subject="bot_token" inputLabel="Bot Token" onModeChange={onModeChange} />);
    fireEvent.click(screen.getByLabelText("替换为新值"));
    expect(onModeChange).toHaveBeenLastCalledWith("replace");
    expect(screen.getByText(`bot_token${t("secret.replaceHint")}`)).toBeInTheDocument();
    expect(screen.getByLabelText("Bot Token")).toBeInTheDocument();
  });

  it("切到移除凭据显示可配的后果说明", () => {
    const onModeChange = vi.fn();
    render(
      <Controlled
        editing
        initialMode="keep"
        subject="bot_token"
        removeHint="：保存后目标将没有凭据，测试投递会失败，直至重新配置。"
        onModeChange={onModeChange}
      />,
    );
    fireEvent.click(screen.getByLabelText("移除凭据"));
    expect(onModeChange).toHaveBeenLastCalledWith("remove");
    expect(screen.getByText(/保存后目标将没有凭据，测试投递会失败，直至重新配置。/)).toBeInTheDocument();
  });

  it("不传 inputLabel 时任何模式都不渲染内置输入（多字段共用三态场景）", () => {
    render(<Controlled editing initialMode="replace" subject="webhook_url / signing_secret" />);
    expect(screen.queryByLabelText("保留现有凭据")).toBeInTheDocument();
    expect(document.querySelector("input[type=password]")).toBeNull();
  });
});
