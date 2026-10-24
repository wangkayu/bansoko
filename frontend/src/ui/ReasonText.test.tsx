import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { t } from "../lib/i18n";
import { FilteredWithReason, ReasonText } from "./ReasonText";

describe("ReasonText", () => {
  it("shows the catalog text for a known code", () => {
    render(<ReasonText code="TARGET_COLLISION_BLOCKED" />);
    expect(screen.getByText(t("reason.target_collision_blocked"))).toBeInTheDocument();
  });

  it("shows 未知原因 with the raw code as tooltip for an unregistered code", () => {
    render(<ReasonText code="brand_new_code" />);
    expect(screen.getByText(t("common.unknownReason"))).toHaveAttribute("title", "brand_new_code");
  });

  it("renders 已过滤（原因） from the catalog", () => {
    render(<p data-testid="x"><FilteredWithReason reason={<ReasonText code="file_not_in_matched" />} /></p>);
    expect(screen.getByTestId("x")).toHaveTextContent(t("verdict.filteredWithReason", { reason: t("reason.file_not_in_matched") }));
  });
});
