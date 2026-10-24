// Package apiscope reads the bearer scope model out of the OpenAPI contract
// (SEC-033). Every operation declares the scope it needs in `x-scope`
// (`read`, `write`, `secrets`, or `none` for the anonymous operations), and a
// string property marked `x-secret: true` is returned as the fixed placeholder
// SecretPlaceholder to a bearer that lacks the `secrets` scope.
//
// The HTTP middleware uses this table for every /api/v1 request; the MCP tools
// replay through that same middleware, so REST and MCP enforce one model. It is
// separate from the MCP-only `x-mcp-omit` marker: an omitted property never
// appears in a tool result whatever the scope, while an `x-secret` property is
// present for everyone and holds its value only for a `secrets` bearer.
package apiscope

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"bansoko/api"
	"bansoko/internal/redact"
)

// Scope values of `x-scope`.
const (
	ScopeNone    = "none"
	ScopeRead    = "read"
	ScopeWrite   = "write"
	ScopeSecrets = "secrets"
)

const (
	scopeExtension  = "x-scope"
	secretExtension = "x-secret"
	apiPrefix       = "/api/v1"
)

// SecretPlaceholder replaces the value of every x-secret property for a bearer
// without the secrets scope. It is fixed, so a caller can send it back: a
// candidate patch keeps the stored value of the same RSS source.
const SecretPlaceholder = redact.SecretPlaceholder

// maxDepth bounds the schema walk; response bodies are finite trees.
const maxDepth = 64

// Route is one OpenAPI operation.
type Route struct {
	OperationID string
	Method      string
	// Template is the full path template, e.g. /api/v1/anime/{managed_anime_id}.
	Template string
	Scope    string
	segments []string
	// secretSchemas holds the 2xx JSON response schema per status code for
	// operations whose response can reach an x-secret property.
	secretSchemas map[string]map[string]any
}

// RevealsSecrets reports whether a 2xx response of the operation can carry an
// x-secret value.
func (r *Route) RevealsSecrets() bool { return r != nil && len(r.secretSchemas) > 0 }

// Table is the parsed scope model.
type Table struct {
	doc    map[string]any
	routes map[string][]*Route
	all    []*Route
}

var (
	loadOnce sync.Once
	loaded   *Table
	loadErr  error
)

// Load parses the embedded OpenAPI document once.
func Load() (*Table, error) {
	loadOnce.Do(func() { loaded, loadErr = Parse(api.OpenAPI) })
	return loaded, loadErr
}

// Parse builds the table. Every operation must declare a known x-scope, and
// x-scope none is reserved for anonymous operations (security: []).
func Parse(raw []byte) (*Table, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse openapi: %w", err)
	}
	table := &Table{doc: doc, routes: map[string][]*Route{}}
	paths, _ := doc["paths"].(map[string]any)
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, path := range names {
		item, _ := paths[path].(map[string]any)
		for _, method := range []string{"get", "put", "post", "delete", "patch", "head"} {
			node, ok := item[method].(map[string]any)
			if !ok {
				continue
			}
			id, _ := node["operationId"].(string)
			scope, _ := node[scopeExtension].(string)
			switch scope {
			case ScopeRead, ScopeWrite, ScopeSecrets:
				if anonymous(node) {
					return nil, fmt.Errorf("%s: anonymous operation must declare x-scope none, not %q", id, scope)
				}
			case ScopeNone:
				if !anonymous(node) {
					return nil, fmt.Errorf("%s: x-scope none is only for anonymous operations (security: [])", id)
				}
			default:
				return nil, fmt.Errorf("%s (%s %s): x-scope must be read, write, secrets or none, got %q", id, strings.ToUpper(method), path, scope)
			}
			route := &Route{
				OperationID: id,
				Method:      strings.ToUpper(method),
				Template:    apiPrefix + path,
				Scope:       scope,
				segments:    strings.Split(strings.Trim(apiPrefix+path, "/"), "/"),
			}
			for status, schema := range successSchemas(table, node) {
				if table.reachesSecret(schema) {
					if route.secretSchemas == nil {
						route.secretSchemas = map[string]map[string]any{}
					}
					route.secretSchemas[status] = schema
				}
			}
			table.routes[route.Method] = append(table.routes[route.Method], route)
			table.all = append(table.all, route)
		}
	}
	return table, nil
}

func anonymous(node map[string]any) bool {
	security, present := node["security"]
	if !present {
		return false
	}
	list, _ := security.([]any)
	return len(list) == 0
}

// Routes lists every operation, in path order.
func (t *Table) Routes() []*Route { return append([]*Route(nil), t.all...) }

// Match finds the operation serving method and an escaped request path
// (r.URL.EscapedPath()). HEAD matches the GET operation. Among templates that
// match, a literal segment wins over a parameter at the first position where
// they differ, which is the net/http ServeMux precedence for these paths.
func (t *Table) Match(method, escapedPath string) (*Route, bool) {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	// ServeMux matches each segment unescaped (/anim%65 serves /anime), so the
	// scope lookup must too, or an encoded literal would skip the check.
	segments := strings.Split(strings.Trim(escapedPath, "/"), "/")
	for index, segment := range segments {
		if unescaped, err := url.PathUnescape(segment); err == nil {
			segments[index] = unescaped
		}
	}
	var best *Route
	for _, route := range t.routes[method] {
		if !route.matches(segments) {
			continue
		}
		if best == nil || moreSpecific(route, best) {
			best = route
		}
	}
	return best, best != nil
}

func (r *Route) matches(segments []string) bool {
	if len(segments) != len(r.segments) {
		return false
	}
	for index, template := range r.segments {
		if isParam(template) {
			if segments[index] == "" {
				return false
			}
			continue
		}
		if template != segments[index] {
			return false
		}
	}
	return true
}

func moreSpecific(a, b *Route) bool {
	for index := range a.segments {
		aParam, bParam := isParam(a.segments[index]), isParam(b.segments[index])
		if aParam != bParam {
			return !aParam
		}
	}
	return false
}

func isParam(segment string) bool {
	return strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")
}

// MaskSecrets replaces every x-secret value in a 2xx JSON response body of the
// route with SecretPlaceholder. changed is false when nothing was replaced (the
// body is then returned as is). A body that does not decode is an error: the
// caller must not pass it through.
func (t *Table) MaskSecrets(route *Route, status int, body []byte) (masked []byte, changed bool, err error) {
	if route == nil {
		return body, false, nil
	}
	schema, ok := route.secretSchemas[strconv.Itoa(status)]
	if !ok || len(bytes.TrimSpace(body)) == 0 {
		return body, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false, fmt.Errorf("decode %s response for secret masking: %w", route.OperationID, err)
	}
	replaced := 0
	value = t.mask(value, schema, 0, &replaced)
	if replaced == 0 {
		return body, false, nil
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, false, fmt.Errorf("encode masked %s response: %w", route.OperationID, err)
	}
	return buffer.Bytes(), true, nil
}

func (t *Table) mask(value any, schema map[string]any, depth int, replaced *int) any {
	if schema == nil || depth > maxDepth {
		return value
	}
	variants := t.branches(schema, depth, nil)
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			var childSchemas []map[string]any
			secret := false
			for _, variant := range variants {
				props, _ := variant["properties"].(map[string]any)
				property, ok := props[key].(map[string]any)
				if !ok {
					continue
				}
				if t.isSecret(property, depth) {
					secret = true
				}
				childSchemas = append(childSchemas, property)
			}
			if len(childSchemas) == 0 {
				for _, variant := range variants {
					if additional, ok := variant["additionalProperties"].(map[string]any); ok && len(additional) > 0 {
						childSchemas = append(childSchemas, additional)
					}
				}
			}
			if secret {
				if text, isString := child.(string); isString && text != "" {
					typed[key] = SecretPlaceholder
					*replaced++
				}
				continue
			}
			for _, childSchema := range childSchemas {
				child = t.mask(child, childSchema, depth+1, replaced)
			}
			typed[key] = child
		}
		return typed
	case []any:
		var itemSchemas []map[string]any
		for _, variant := range variants {
			if items, ok := variant["items"].(map[string]any); ok {
				itemSchemas = append(itemSchemas, items)
			}
		}
		for index, element := range typed {
			for _, items := range itemSchemas {
				element = t.mask(element, items, depth+1, replaced)
			}
			typed[index] = element
		}
		return typed
	}
	return value
}

// isSecret reports whether a property schema (or any schema it resolves to)
// carries x-secret.
func (t *Table) isSecret(property map[string]any, depth int) bool {
	for _, variant := range t.branches(property, depth, nil) {
		if variant[secretExtension] == true {
			return true
		}
	}
	return false
}

func (t *Table) lookup(ref string) map[string]any {
	parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	var target any = t.doc
	for _, part := range parts {
		object, _ := target.(map[string]any)
		target = object[part]
	}
	node, _ := target.(map[string]any)
	return node
}

func (t *Table) resolve(node map[string]any) map[string]any {
	for i := 0; i < 8 && node != nil; i++ {
		ref, ok := node["$ref"].(string)
		if !ok {
			return node
		}
		node = t.lookup(ref)
	}
	return node
}

// branches flattens a schema into the schemas a value may match: the schema
// itself (a $ref node first, so keywords next to the $ref count) plus every
// allOf/oneOf/anyOf member, recursively.
func (t *Table) branches(node map[string]any, depth int, out []map[string]any) []map[string]any {
	if node == nil || depth > maxDepth {
		return out
	}
	if _, isRef := node["$ref"]; isRef {
		out = append(out, node)
		node = t.resolve(node)
		if node == nil {
			return out
		}
	}
	out = append(out, node)
	for _, key := range []string{"allOf", "oneOf", "anyOf"} {
		members, _ := node[key].([]any)
		for _, member := range members {
			if schema, ok := member.(map[string]any); ok {
				out = t.branches(schema, depth+1, out)
			}
		}
	}
	return out
}

// reachesSecret reports whether any property below root carries x-secret.
func (t *Table) reachesSecret(root any) bool {
	seen := map[string]bool{}
	var walk func(node any) bool
	walk = func(node any) bool {
		switch typed := node.(type) {
		case map[string]any:
			if typed[secretExtension] == true {
				return true
			}
			if ref, ok := typed["$ref"].(string); ok && !seen[ref] {
				seen[ref] = true
				if walk(t.lookup(ref)) {
					return true
				}
			}
			for key, child := range typed {
				if key == "$ref" {
					continue
				}
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range typed {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(root)
}

// successSchemas extracts the application/json schema of every 2xx response.
func successSchemas(t *Table, node map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	responses, _ := node["responses"].(map[string]any)
	for status, raw := range responses {
		if !strings.HasPrefix(status, "2") {
			continue
		}
		rawMap, _ := raw.(map[string]any)
		response := t.resolve(rawMap)
		content, _ := response["content"].(map[string]any)
		media, _ := content["application/json"].(map[string]any)
		if schema, ok := media["schema"].(map[string]any); ok {
			out[status] = schema
		}
	}
	return out
}

// SecretProperties lists every property marked x-secret as
// "Schema.property" (components) or "operationId:path" (inline), sorted. The
// contract gate pins this list.
func (t *Table) SecretProperties() []string {
	var out []string
	components, _ := t.doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	for name, raw := range schemas {
		schema, _ := raw.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		for property, node := range props {
			if fields, ok := node.(map[string]any); ok && fields[secretExtension] == true {
				out = append(out, name+"."+property)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Document exposes the parsed OpenAPI document for contract tests.
func (t *Table) Document() map[string]any { return t.doc }
