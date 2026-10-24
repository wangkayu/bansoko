package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	accessapplication "bansoko/internal/accesscontrol/application"
	"bansoko/internal/agentguide"
	"bansoko/internal/runtime/maintenance"
)

func newAgentSurfaceServer(t *testing.T, skipAuth bool, gate *maintenance.Gate) *httptest.Server {
	t.Helper()
	deps := Dependencies{
		DB:          &sql.DB{},
		Access:      accessapplication.NewService(nil),
		Environment: "development",
		SkipAuth:    skipAuth,
	}
	if gate != nil {
		deps.MaintenanceGate = gate
	}
	srv, err := NewServer(deps)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

func newMCPClient(t *testing.T, base string, headers map[string]string) *client.Client {
	t.Helper()
	options := []transport.StreamableHTTPCOption{}
	if headers != nil {
		options = append(options, transport.WithHTTPHeaders(headers))
	}
	cli, err := client.NewStreamableHttpClient(base+"/api/v1/mcp", options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	ctx := context.Background()
	if err := cli.Start(ctx); err != nil {
		t.Fatal(err)
	}
	request := mcp.InitializeRequest{}
	request.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	request.Params.ClientInfo = mcp.Implementation{Name: "agent-surface-test", Version: "1.0.0"}
	if _, err := cli.Initialize(ctx, request); err != nil {
		t.Fatal(err)
	}
	return cli
}

func callMCP(t *testing.T, cli *client.Client, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := cli.CallTool(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: tool, Arguments: args}})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return result
}

func toolText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// A pure read tool runs through the real middleware chain and handler.
func TestMCPToolReplaysThroughTheRealAPIHandler(t *testing.T) {
	server := newAgentSurfaceServer(t, true, nil)
	cli := newMCPClient(t, server.URL, nil)
	result := callMCP(t, cli, "parse_title", map[string]any{"title": "[Group] Show - 01 [1080p]"})
	// This rig has no parser registry loaded, so the handler may answer with
	// its typed parse failure; either way the answer is the REST handler's own.
	text := toolText(result)
	if !strings.Contains(text, "parser_version") && !strings.Contains(text, "TITLE_PARSE_FAILED") {
		t.Fatalf("parse_title did not reach the REST handler: %+v", result)
	}
}

// The maintenance gate applies per operation: writes are refused with the
// typed code while read-only tools (including read-only POSTs) keep working.
func TestMCPMaintenanceGateAppliesPerOperation(t *testing.T) {
	gate := maintenance.NewGate()
	server := newAgentSurfaceServer(t, true, gate)
	cli := newMCPClient(t, server.URL, nil)
	gate.EnterMaintenance()

	rejected := callMCP(t, cli, "refresh_rss", map[string]any{"rss_source_id": "src-1"})
	if !rejected.IsError || !strings.Contains(toolText(rejected), "WRITES_REJECTED") {
		t.Fatalf("write during maintenance = %+v", rejected)
	}
	allowed := callMCP(t, cli, "parse_title", map[string]any{"title": "[Group] Show - 01 [1080p]"})
	if strings.Contains(toolText(allowed), "WRITES_REJECTED") {
		t.Fatalf("read-only POST during maintenance was gated: %s", toolText(allowed))
	}
}

// Without a bearer token the MCP endpoint is closed, and a tool never gets
// further than the caller's own credential.
func TestMCPRequiresTheCallersBearerToken(t *testing.T) {
	server := newAgentSurfaceServer(t, false, nil)
	response, err := http.Post(server.URL+"/api/v1/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous MCP status = %d, want 401", response.StatusCode)
	}
}

func TestAgentGuideEndpoint(t *testing.T) {
	server := newAgentSurfaceServer(t, true, nil)
	get := func(query string) (int, string, string) {
		response, err := http.Get(server.URL + "/api/v1/agent-guide" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var body strings.Builder
		buffer := make([]byte, 4096)
		for {
			n, readErr := response.Body.Read(buffer)
			body.Write(buffer[:n])
			if readErr != nil {
				break
			}
		}
		return response.StatusCode, response.Header.Get("Content-Type"), body.String()
	}

	status, contentType, body := get("")
	if status != 200 || !strings.HasPrefix(contentType, "text/markdown") || body != agentguide.Guide() {
		t.Fatalf("guide = %d %q (%d bytes)", status, contentType, len(body))
	}
	status, _, body = get("?format=guide")
	if status != 200 || body != agentguide.Guide() {
		t.Fatalf("format=guide = %d", status)
	}
	status, contentType, body = get("?format=skill")
	if status != 200 || !strings.HasPrefix(contentType, "text/markdown") || body != agentguide.Skill(agentguide.SkillName) {
		t.Fatalf("format=skill = %d %q", status, contentType)
	}
	if !strings.HasPrefix(body, "---\nname: bansoko-agent\ndescription: ") {
		t.Fatalf("skill front matter = %q", body[:80])
	}
	status, _, body = get("?format=bogus")
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "AGENT_GUIDE_FORMAT_INVALID") {
		t.Fatalf("format=bogus = %d %s", status, body)
	}
}

func TestAgentGuideRequiresAuthentication(t *testing.T) {
	server := newAgentSurfaceServer(t, false, nil)
	response, err := http.Get(server.URL + "/api/v1/agent-guide")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous guide status = %d, want 401", response.StatusCode)
	}
}
