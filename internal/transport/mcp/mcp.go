// Package mcp hosts the in-process Model Context Protocol server for the
// HTTP API. Its tools are derived from api/openapi.yaml: an operation becomes a
// tool by carrying an `x-mcp` extension (see catalog.go), and every call is
// replayed as an in-process HTTP request through the same middleware chain and
// handlers REST uses. There is no second execution path, so authentication,
// maintenance gating, idempotency, CAS, frozen inputs, redaction and error
// codes are exactly the REST behavior.
//
// The server is mounted by httpapi at /api/v1/mcp behind the standard bearer
// middleware (session token or API token). Multi-step tools are registered as
// composites in the same OpenAPI file.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"bansoko/internal/agentguide"
	"bansoko/internal/transport/httpapi/generated"
)

const (
	serverName    = "bansoko"
	serverVersion = "1.0.0"
	endpointPath  = "/api/v1/mcp"
)

// compositeBuilder builds one hand-written multi-step tool. Its operations are
// listed under `x-mcp-composites` in the OpenAPI file.
type compositeBuilder func(catalog *Catalog, d *dispatcher, spec Composite) (server.ServerTool, error)

// compositeBuilders maps a composite tool name to its implementation. The
// OpenAPI list and this map must agree (TestCompositesMatchOpenAPI).
var compositeBuilders = map[string]compositeBuilder{
	"poll_workspace": buildPollWorkspace,
}

// New builds the MCP server from the OpenAPI catalog. api is the complete API
// handler chain (authentication and maintenance gate included); tool calls are
// replayed against it with the MCP caller's Authorization header.
func New(api http.Handler) (*server.MCPServer, error) {
	catalog, err := LoadCatalog()
	if err != nil {
		return nil, err
	}
	d := &dispatcher{api: api}
	mcpServer := server.NewMCPServer(
		serverName,
		serverVersion,
		server.WithInstructions(agentguide.Instructions()),
		server.WithResourceCapabilities(false, false),
		server.WithPromptCapabilities(false),
	)

	for _, op := range catalog.Operations {
		mcpServer.AddTool(operationTool(op), operationHandler(d, op))
	}
	registered := map[string]bool{}
	for _, spec := range catalog.Composites {
		build, ok := compositeBuilders[spec.Tool]
		if !ok {
			return nil, fmt.Errorf("composite tool %q has no implementation", spec.Tool)
		}
		entry, err := build(catalog, d, spec)
		if err != nil {
			return nil, err
		}
		mcpServer.AddTool(entry.Tool, entry.Handler)
		registered[spec.Tool] = true
	}
	for name := range compositeBuilders {
		if !registered[name] {
			return nil, fmt.Errorf("composite implementation %q is not registered in x-mcp-composites", name)
		}
	}

	mcpServer.AddResource(
		mcp.NewResource(agentguide.ResourceURI, "Bansoko agent guide",
			mcp.WithResourceDescription("The full operating guide: concepts, task recipes, safety rules, error codes."),
			mcp.WithMIMEType("text/markdown")),
		func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return []mcp.ResourceContents{mcp.TextResourceContents{
				URI: agentguide.ResourceURI, MIMEType: "text/markdown", Text: agentguide.Guide(),
			}}, nil
		},
	)
	mcpServer.AddPrompt(
		mcp.NewPrompt(agentguide.PromptName,
			mcp.WithPromptDescription("The full Bansoko operating guide for agents.")),
		func(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{
				Description: "Bansoko agent guide",
				Messages: []mcp.PromptMessage{
					mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(agentguide.Guide())),
				},
			}, nil
		},
	)
	return mcpServer, nil
}

// Handler returns the streamable-http handler mounted at /api/v1/mcp by the
// transport layer, behind the standard bearer middleware. api is the complete
// API handler chain the tools replay against. It panics when the embedded
// OpenAPI catalog is invalid; that is a build-time defect pinned by tests.
func Handler(api http.Handler) http.Handler {
	mcpServer, err := New(api)
	if err != nil {
		panic("mcp: " + err.Error())
	}
	return server.NewStreamableHTTPServer(mcpServer,
		server.WithEndpointPath(endpointPath),
		server.WithHTTPContextFunc(withAuthorization),
	)
}

func operationTool(op *Operation) mcp.Tool {
	tool := mcp.NewToolWithRawSchema(op.Tool, op.Description, op.InputSchema())
	tool.Annotations = annotations(op)
	return tool
}

// annotations reflect the tool's real side-effect profile. The mcp-go default
// is destructiveHint=true, which is wrong for ordinary pipeline tools.
func annotations(op *Operation) mcp.ToolAnnotation {
	readOnly := !op.Write
	destructive := op.Write && op.Destructive
	idempotent := op.Idempotent || readOnly
	return mcp.ToolAnnotation{
		ReadOnlyHint:    mcp.ToBoolPtr(readOnly),
		DestructiveHint: mcp.ToBoolPtr(destructive),
		IdempotentHint:  mcp.ToBoolPtr(idempotent),
		OpenWorldHint:   mcp.ToBoolPtr(false),
	}
}

func operationHandler(d *dispatcher, op *Operation) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return d.call(ctx, op, request.GetArguments()), nil
	}
}

// buildPollWorkspace is the only composite: it repeats getAnimeWorkspace until
// the owner chain settles or the timeout elapses.
func buildPollWorkspace(catalog *Catalog, d *dispatcher, spec Composite) (server.ServerTool, error) {
	op := catalog.Operation("getAnimeWorkspace")
	if op == nil {
		return server.ServerTool{}, fmt.Errorf("composite %q needs the allowlisted operation getAnimeWorkspace", spec.Tool)
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"managed_anime_id": map[string]any{"type": "string", "description": "Managed anime ID"},
			"timeout_seconds": map[string]any{
				"type": "number", "description": "Max wait in seconds, default 30, clamped to [1, 120]",
			},
		},
		"required":             []string{"managed_anime_id"},
		"additionalProperties": false,
	}
	raw, _ := json.Marshal(schema)
	tool := mcp.NewToolWithRawSchema(spec.Tool, spec.Description, raw)
	tool.Annotations = mcp.ToolAnnotation{
		ReadOnlyHint: mcp.ToBoolPtr(true), DestructiveHint: mcp.ToBoolPtr(false),
		IdempotentHint: mcp.ToBoolPtr(true), OpenWorldHint: mcp.ToBoolPtr(false),
	}
	handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		managedAnimeID, _ := args["managed_anime_id"].(string)
		timeout := 30 * time.Second
		if seconds, ok := args["timeout_seconds"].(float64); ok && seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
			if timeout < time.Second {
				timeout = time.Second
			}
			if timeout > 120*time.Second {
				timeout = 120 * time.Second
			}
		}
		deadline := time.Now().Add(timeout)
		for {
			resp, echo, err := d.do(ctx, op, map[string]any{"managed_anime_id": managedAnimeID})
			if err != nil {
				return mcp.NewToolResultError(spec.Tool + " failed: " + err.Error()), nil
			}
			if resp.status >= 400 {
				return resultFrom(op, resp, echo), nil
			}
			var workspace generated.AnimeWorkspace
			if err := json.Unmarshal(resp.body.Bytes(), &workspace); err != nil {
				return mcp.NewToolResultError(spec.Tool + " failed: workspace response could not be decoded"), nil
			}
			snapshot, _ := decodeJSON(resp.body.Bytes())
			snapshot = op.redact(resp.status, snapshot)
			if workspaceStable(workspace) {
				return mcp.NewToolResultStructuredOnly(map[string]any{"stable": true, "workspace": snapshot}), nil
			}
			if time.Now().After(deadline) {
				return mcp.NewToolResultStructuredOnly(map[string]any{"stable": false, "timed_out": true, "workspace": snapshot}), nil
			}
			select {
			case <-ctx.Done():
				return mcp.NewToolResultError(spec.Tool + " canceled: " + ctx.Err().Error()), nil
			case <-time.After(pollInterval):
			}
		}
	}
	return server.ServerTool{Tool: tool, Handler: handler}, nil
}

// pollInterval is the delay between workspace reads; tests shorten it.
var pollInterval = 2 * time.Second

// workspaceStable reports whether the owner chain reached a terminal state:
// every download task completed/failed, every artifact verified/failed, and
// every file operation completed/failed. Pending/running/downloading/submitting
// and waiting_retry states mean the pipeline is still advancing (waiting_retry
// can be retried), so polling should continue.
func workspaceStable(ws generated.AnimeWorkspace) bool {
	for _, task := range ws.DownloadTasks {
		switch task.State {
		case generated.DownloadTaskStateCompleted,
			generated.DownloadTaskStateFailed:
		default:
			return false
		}
	}
	for _, artifact := range ws.Artifacts {
		switch artifact.State {
		case generated.ArtifactStateVerified,
			generated.ArtifactStateFailed:
		default:
			return false
		}
	}
	for _, op := range ws.FileOperations {
		switch op.State {
		case generated.FileOperationStateCompleted,
			generated.FileOperationStateFailed:
		default:
			return false
		}
	}
	return true
}
