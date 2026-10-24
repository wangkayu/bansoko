package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

const apiPrefix = "/api/v1"

type authorizationKey struct{}

// withAuthorization carries the MCP caller's Authorization header into tool
// handlers, so each operation is replayed through the HTTP middleware with the
// caller's own credential (session or API token). The tools therefore grant
// nothing the same bearer could not do over REST.
func withAuthorization(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, authorizationKey{}, r.Header.Get("Authorization"))
}

// dispatcher replays a tool call as an in-process HTTP request against the
// full API handler chain (authentication, maintenance gate, validation,
// handlers). There is no second execution path: idempotency, CAS, redaction
// and error shapes are exactly what REST returns.
type dispatcher struct {
	api http.Handler
}

// response is one captured in-process HTTP response.
type response struct {
	status int
	header http.Header
	body   bytes.Buffer
}

func (r *response) Header() http.Header         { return r.header }
func (r *response) WriteHeader(status int)      { r.status = status }
func (r *response) Write(p []byte) (int, error) { return r.body.Write(p) }

// call executes one operation and projects the response into a tool result.
func (d *dispatcher) call(ctx context.Context, op *Operation, args map[string]any) *mcp.CallToolResult {
	resp, echo, err := d.do(ctx, op, args)
	if err != nil {
		return mcp.NewToolResultError(op.Tool + " failed: " + err.Error())
	}
	return resultFrom(op, resp, echo)
}

// do builds and runs the request. echo holds the path arguments, returned when
// the operation answers with an empty body.
func (d *dispatcher) do(ctx context.Context, op *Operation, args map[string]any) (*response, map[string]any, error) {
	if args == nil {
		args = map[string]any{}
	}
	if err := checkArguments(op, args); err != nil {
		return nil, nil, err
	}

	path := op.Path
	echo := map[string]any{}
	for _, param := range op.pathParams {
		text, err := scalar(args[param.arg])
		if err != nil {
			return nil, nil, fmt.Errorf("argument %q: %w", param.arg, err)
		}
		if text == "" {
			return nil, nil, fmt.Errorf("argument %q must not be empty", param.arg)
		}
		path = strings.ReplaceAll(path, "{"+param.wire+"}", url.PathEscape(text))
		echo[param.arg] = args[param.arg]
	}

	query := url.Values{}
	for _, param := range op.queryParams {
		value, present := args[param.arg]
		if !present || value == nil {
			continue
		}
		if list, isList := value.([]any); isList {
			for _, element := range list {
				text, err := scalar(element)
				if err != nil {
					return nil, nil, fmt.Errorf("argument %q: %w", param.arg, err)
				}
				query.Add(param.wire, text)
			}
			continue
		}
		text, err := scalar(value)
		if err != nil {
			return nil, nil, fmt.Errorf("argument %q: %w", param.arg, err)
		}
		query.Set(param.wire, text)
	}

	var body io.Reader
	if op.hasBody {
		payload := map[string]any{}
		if op.bodyAsArgument {
			if raw, ok := args["body"]; ok {
				encoded, err := json.Marshal(raw)
				if err != nil {
					return nil, nil, fmt.Errorf("argument \"body\": %w", err)
				}
				body = bytes.NewReader(encoded)
			}
		} else {
			for name := range op.bodyProps {
				if value, ok := args[name]; ok {
					payload[name] = value
				}
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				return nil, nil, fmt.Errorf("request body: %w", err)
			}
			body = bytes.NewReader(encoded)
		}
	}

	target := apiPrefix + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, op.Method, target, body)
	if err != nil {
		return nil, nil, err
	}
	if body == nil {
		// A handler always sees a non-nil body on a served request.
		req.Body = http.NoBody
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization, _ := ctx.Value(authorizationKey{}).(string); authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if op.HasIfMatch {
		text, err := scalar(args["expected_version"])
		if err != nil {
			return nil, nil, fmt.Errorf("argument \"expected_version\": %w", err)
		}
		req.Header.Set("If-Match", text)
	}
	if op.HasIdempotencyHeader {
		key := newID()
		if supplied, ok := args["idempotency_key"]; ok {
			text, err := scalar(supplied)
			if err != nil {
				return nil, nil, fmt.Errorf("argument \"idempotency_key\": %w", err)
			}
			key = text
		}
		req.Header.Set("Idempotency-Key", key)
	}

	resp := &response{status: http.StatusOK, header: http.Header{}}
	d.api.ServeHTTP(resp, req)
	return resp, echo, nil
}

// checkArguments rejects unknown or missing arguments and values the tool
// withholds, before anything reaches the API.
func checkArguments(op *Operation, args map[string]any) error {
	props, _ := op.inputSchema["properties"].(map[string]any)
	var unknown []string
	for name := range args {
		if _, ok := props[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unknown argument(s) %s; accepted: %s", strings.Join(unknown, ", "), strings.Join(op.ArgumentNames(), ", "))
	}
	required, _ := op.inputSchema["required"].([]string)
	for _, name := range required {
		if value, ok := args[name]; !ok || value == nil {
			return fmt.Errorf("required argument %q not found", name)
		}
	}
	for field, withheld := range op.rejectEnum {
		if value, ok := args[field].(string); ok && containsString(withheld, value) {
			return fmt.Errorf("%s=%s is not available through MCP", field, value)
		}
	}
	return nil
}

// scalar renders a path, query or header argument.
func scalar(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", fmt.Errorf("must be a finite number")
		}
		if typed == math.Trunc(typed) && math.Abs(typed) < 1<<53 {
			return strconv.FormatInt(int64(typed), 10), nil
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case json.Number:
		return typed.String(), nil
	case nil:
		return "", fmt.Errorf("is required")
	default:
		return "", fmt.Errorf("must be a string, number or boolean")
	}
}

// resultFrom projects a captured response into the tool result: the decoded
// JSON body on success, the typed API error (status, code, message) on
// failure, and a small confirmation for empty bodies.
func resultFrom(op *Operation, resp *response, echo map[string]any) *mcp.CallToolResult {
	raw := bytes.TrimSpace(resp.body.Bytes())
	contentType := resp.header.Get("Content-Type")
	if resp.status >= 400 {
		failure := map[string]any{}
		if decoded, ok := decodeJSON(raw); ok {
			if object, isObject := decoded.(map[string]any); isObject {
				failure = object
			}
		}
		// An error body is free-form for the withholding layers.
		failure, _ = scrubFree(failure, 0).(map[string]any)
		failure["status"] = resp.status
		if _, has := failure["code"]; !has {
			failure["code"] = "HTTP_" + strconv.Itoa(resp.status)
		}
		if _, has := failure["message"]; !has && len(raw) > 0 && len(raw) < 512 {
			failure["message"] = withholdURLs(string(raw))
		}
		encoded, _ := json.Marshal(failure)
		return &mcp.CallToolResult{
			IsError:           true,
			StructuredContent: failure,
			Content:           []mcp.Content{mcp.TextContent{Type: "text", Text: string(encoded)}},
		}
	}
	if len(raw) == 0 {
		confirmation := map[string]any{"ok": true}
		if op.Destructive {
			confirmation = map[string]any{"deleted": true}
		}
		for key, value := range echo {
			confirmation[key] = scrubFree(value, 0)
		}
		return mcp.NewToolResultStructuredOnly(confirmation)
	}
	if strings.HasPrefix(contentType, "application/json") || contentType == "" {
		if decoded, ok := decodeJSON(raw); ok {
			return mcp.NewToolResultStructuredOnly(op.redact(resp.status, decoded))
		}
	}
	if op.Withholds() {
		// A body that cannot be projected is never passed through verbatim.
		return mcp.NewToolResultError(op.Tool + " failed: the response could not be decoded for redaction")
	}
	return mcp.NewToolResultText(withholdURLs(string(raw)))
}

func decodeJSON(raw []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	return value, true
}

// newID generates a fresh idempotency key for side-effecting tools.
func newID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "mcp-fallback-key"
	}
	return "mcp-" + hex.EncodeToString(raw)
}
