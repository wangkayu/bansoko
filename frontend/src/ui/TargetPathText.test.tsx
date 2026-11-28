import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { TargetPathText } from "./TargetPathText";

describe("TargetPathText", () => {
  it("renders plain paths without extra placeholder wrappers", () => {
    const { container } = render(<TargetPathText value="/media/anime/Show S01E01.mkv" />);
    expect(container.textContent).toBe("/media/anime/Show S01E01.mkv");
    expect(container.querySelector("[data-placeholder-token]")).toBeNull();
  });

  it("highlights ⟨...⟩ placeholders with neutral tone by default while preserving text", () => {
    const { container } = render(
      <TargetPathText value="/media/anime/Show S01E01 - ⟨ep_title⟩ [⟨res⟩].mkv" />,
    );
    expect(container.textContent).toBe("/media/anime/Show S01E01 - ⟨ep_title⟩ [⟨res⟩].mkv");
    const tokens = container.querySelectorAll("[data-placeholder-token='neutral']");
    expect(tokens).toHaveLength(2);
    expect(tokens[0]?.textContent).toBe("⟨ep_title⟩");
    expect(tokens[1]?.textContent).toBe("⟨res⟩");
  });

  it("applies error tone and focusable hint trigger for unresolved fallback placeholders", () => {
    render(
      <TargetPathText
        value="/media/anime/Show S01E01 - ⟨ep_title⟩.mkv"
        placeholderTone="error"
        placeholderHint="TMDB key not configured"
      />,
    );
    const token = screen.getByText("⟨ep_title⟩");
    expect(token).toHaveAttribute("data-placeholder-token", "error");
    expect(token).toHaveAttribute("tabindex", "0");
  });
});
