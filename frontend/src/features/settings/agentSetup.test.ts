// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { TOKEN_PLACEHOLDER, buildAgentSetup, downloadTextFile } from "./agentSetup";

describe("buildAgentSetup", () => {
  it("names the MCP URL, the placeholder token, the add command and the guide pointer", () => {
    const text = buildAgentSetup("http://nas:8080/api/v1/mcp");
    expect(text).toContain("URL: http://nas:8080/api/v1/mcp");
    expect(text).toContain(`Authorization: Bearer ${TOKEN_PLACEHOLDER}`);
    expect(text).toContain(`claude mcp add bansoko --transport http http://nas:8080/api/v1/mcp --header "Authorization: Bearer ${TOKEN_PLACEHOLDER}"`);
    expect(text).toContain("bansoko://agent-guide");
    expect(text).toContain("GET http://nas:8080/api/v1/agent-guide");
  });

  it("uses a freshly created token instead of the placeholder", () => {
    const text = buildAgentSetup("/api/v1/mcp", "  raw-token-1  ");
    expect(text).toContain("Authorization: Bearer raw-token-1");
    expect(text).not.toContain(TOKEN_PLACEHOLDER);
  });

  it("is plain English text for the assistant", () => {
    // eslint-disable-next-line no-control-regex
    expect(buildAgentSetup("/api/v1/mcp")).toMatch(/^[\x09\x0a\x20-\x7e]+$/);
  });
});

describe("downloadTextFile", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    Reflect.deleteProperty(URL, "createObjectURL");
    Reflect.deleteProperty(URL, "revokeObjectURL");
  });

  it("saves the text as a named file and releases the object URL", async () => {
    const createObjectURL = vi.fn<(blob: Blob) => string>(() => "blob:skill");
    const revokeObjectURL = vi.fn();
    Object.assign(URL, { createObjectURL, revokeObjectURL });
    let clicked: { href: string; download: string } | undefined;
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicked = { href: this.href, download: this.download };
    });

    downloadTextFile("SKILL.md", "---\nname: bansoko-agent\n---\n");

    expect(clicked).toEqual({ href: "blob:skill", download: "SKILL.md" });
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:skill");
    const blob = createObjectURL.mock.calls[0][0];
    expect(await blob.text()).toBe("---\nname: bansoko-agent\n---\n");
    expect(blob.type).toContain("text/markdown");
    expect(document.querySelector("a[download]")).toBeNull();
  });
});
