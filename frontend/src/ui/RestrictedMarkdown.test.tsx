import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { RestrictedMarkdown } from "./RestrictedMarkdown";

describe("RestrictedMarkdown", () => {
  it("renders headings, paragraphs and both list kinds", () => {
    const { container } = render(
      <RestrictedMarkdown source={"# One\n## Two\n### Three\n\nHello\nworld\n\n- a\n* b\n\n1. x\n2. y"} />,
    );
    expect(container.querySelector("h3")?.textContent).toBe("One");
    expect(container.querySelector("h4")?.textContent).toBe("Two");
    expect(container.querySelector("h5")?.textContent).toBe("Three");
    expect(container.querySelector("p")?.textContent).toBe("Hello world");
    expect([...container.querySelectorAll("ul li")].map((li) => li.textContent)).toEqual(["a", "b"]);
    expect([...container.querySelectorAll("ol li")].map((li) => li.textContent)).toEqual(["x", "y"]);
  });

  it("shows HTML, links, images and deeper headings as plain text", () => {
    const source = '<script>alert(1)</script>\n[link](http://x.test)\n![img](http://x.test/a.png)\n#### deep';
    const { container } = render(<RestrictedMarkdown source={source} />);
    expect(container.querySelector("script, a, img")).toBeNull();
    expect(container.textContent).toContain("<script>alert(1)</script>");
    expect(container.textContent).toContain("[link](http://x.test)");
    expect(container.textContent).toContain("#### deep");
  });

  it("renders nothing for empty input", () => {
    const { container } = render(<RestrictedMarkdown source={"  \n\n"} />);
    expect(container.firstChild).toBeNull();
  });
});
