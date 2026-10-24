import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ExternalValue } from "./ExternalValue";

// An external link's href may come from provider data: only http(s) renders as a link, other schemes show text only.
describe("ExternalValue", () => {
  it("renders http(s) values as external links", () => {
    render(<ExternalValue href="https://mikanani.me/Home/Bangumi/1">source</ExternalValue>);
    expect(screen.getByRole("link", { name: /source/ })).toHaveAttribute("href", "https://mikanani.me/Home/Bangumi/1");
  });

  it("renders other schemes as plain text", () => {
    for (const href of ["javascript:alert(1)", "data:text/html,x", "not a url"]) {
      const { unmount } = render(<ExternalValue href={href}>value</ExternalValue>);
      expect(screen.queryByRole("link")).not.toBeInTheDocument();
      expect(screen.getByText("value")).toBeInTheDocument();
      unmount();
    }
  });
});
