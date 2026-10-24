// The text handed to an AI assistant. It is agent-facing, so it is English and
// does not go through the UI copy catalogs (see backend/agent-surfaces.md).

/** Placeholder shown when no freshly created token is at hand. */
export const TOKEN_PLACEHOLDER = "<API-TOKEN>";

/**
 * A short setup snippet: where the MCP server is, how to authenticate, and one
 * line pointing at the full guide. The full guide is fetched by the assistant
 * itself, so it always matches the running version.
 */
export function buildAgentSetup(endpoint: string, token?: string): string {
  const bearer = token && token.trim() ? token.trim() : TOKEN_PLACEHOLDER;
  const guideURL = endpoint.replace(/\/mcp$/, "/agent-guide");
  return [
    "Bansoko MCP server",
    `URL: ${endpoint}`,
    `Authorization: Bearer ${bearer}`,
    "",
    "Claude Code:",
    `claude mcp add bansoko --transport http ${endpoint} --header "Authorization: Bearer ${bearer}"`,
    "",
    `Before using any tool, read the full guide: MCP resource bansoko://agent-guide, or GET ${guideURL} with the same bearer token.`,
  ].join("\n");
}

/** Saves text as a file through a temporary object URL. */
export function downloadTextFile(filename: string, text: string, mimeType = "text/markdown"): void {
  const url = URL.createObjectURL(new Blob([text], { type: `${mimeType};charset=utf-8` }));
  try {
    const link = document.createElement("a");
    link.href = url;
    link.download = filename;
    document.body.appendChild(link);
    link.click();
    link.remove();
  } finally {
    URL.revokeObjectURL(url);
  }
}
