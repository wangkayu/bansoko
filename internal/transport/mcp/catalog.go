package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"bansoko/api"
)

// The MCP tool catalog is derived from api/openapi.yaml. An operation joins
// the catalog by carrying an `x-mcp` extension:
//
//	x-mcp:
//	  tool: get_candidate          # MCP tool name (unique)
//	  group: subscription          # subscription | one_shot | files | records | configuration
//	  write: false                 # true = changes product state (maintenance gate applies)
//	  description: ...             # agent-facing text; falls back to the operation text
//	  idempotent: false            # optional; default true
//	  idempotency: required        # optional; default auto-generated Idempotency-Key
//	  rename: {q: query}           # optional; OpenAPI parameter name -> tool argument
//	  reject_enum: {input_source: [owner_draft]}  # optional; values the tool refuses
//
// Multi-step tools that cannot be a single operation are registered as
// composites under the top-level `x-mcp-composites` list, naming the
// operationIds they compose. The OpenAPI file is the only allowlist.

// Tool groups, in guide order.
const (
	GroupSubscription  = "subscription"
	GroupOneShot       = "one_shot"
	GroupFiles         = "files"
	GroupRecords       = "records"
	GroupConfiguration = "configuration"
)

var knownGroups = map[string]bool{
	GroupSubscription: true, GroupOneShot: true, GroupFiles: true,
	GroupRecords: true, GroupConfiguration: true,
}

// Operation is one allowlisted OpenAPI operation exposed as an MCP tool.
type Operation struct {
	ID          string
	Method      string
	Path        string
	Tool        string
	Group       string
	Description string
	Write       bool
	// Destructive is true for DELETE operations.
	Destructive bool
	Idempotent  bool
	// IdempotencyKeyRequired makes the idempotency_key argument mandatory
	// instead of auto-generated.
	IdempotencyKeyRequired bool
	// HasIdempotencyHeader and HasIfMatch record which headers the operation
	// declares; If-Match is exposed as the expected_version argument.
	HasIdempotencyHeader bool
	HasIfMatch           bool

	pathParams  []parameter
	queryParams []parameter
	bodyProps   map[string]bool
	hasBody     bool
	// bodyAsArgument is set when the body is not a plain object schema; the
	// whole body is then the `body` argument.
	bodyAsArgument bool
	rejectEnum     map[string][]string
	inputSchema    map[string]any
	// responseSchemas holds the 2xx JSON response schema per status; the
	// redactor walks a success body with it (redact.go).
	responseSchemas map[string]map[string]any
	// withholds is set when a 2xx schema reaches an x-mcp-omit property.
	withholds bool
	redactor  redactor
}

type parameter struct {
	wire     string
	arg      string
	required bool
	isArray  bool
}

// Composite is a hand-written multi-step tool registered against the
// operations it composes.
type Composite struct {
	Tool        string
	Group       string
	Description string
	Operations  []string
}

// Catalog is the parsed allowlist.
type Catalog struct {
	Operations []*Operation
	Composites []Composite
	byID       map[string]*Operation
}

// Operation returns the allowlisted operation with the given operationId.
func (c *Catalog) Operation(id string) *Operation { return c.byID[id] }

// ToolNames lists every tool name (operations and composites), sorted.
func (c *Catalog) ToolNames() []string {
	names := make([]string, 0, len(c.Operations)+len(c.Composites))
	for _, op := range c.Operations {
		names = append(names, op.Tool)
	}
	for _, comp := range c.Composites {
		names = append(names, comp.Tool)
	}
	sort.Strings(names)
	return names
}

var (
	catalogOnce sync.Once
	catalogVal  *Catalog
	catalogErr  error
)

// LoadCatalog parses the embedded OpenAPI document once.
func LoadCatalog() (*Catalog, error) {
	catalogOnce.Do(func() { catalogVal, catalogErr = ParseCatalog(api.OpenAPI) })
	return catalogVal, catalogErr
}

// ParseCatalog builds the catalog from an OpenAPI document.
func ParseCatalog(raw []byte) (*Catalog, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse openapi: %w", err)
	}
	catalog := &Catalog{byID: map[string]*Operation{}}
	paths, _ := doc["paths"].(map[string]any)
	pathNames := make([]string, 0, len(paths))
	for path := range paths {
		pathNames = append(pathNames, path)
	}
	sort.Strings(pathNames)
	seenTools := map[string]string{}
	for _, path := range pathNames {
		item, _ := paths[path].(map[string]any)
		for _, method := range []string{"get", "post", "put", "patch", "delete"} {
			node, ok := item[method].(map[string]any)
			if !ok {
				continue
			}
			ext, ok := node["x-mcp"].(map[string]any)
			if !ok {
				continue
			}
			op, err := buildOperation(doc, path, method, node, ext)
			if err != nil {
				return nil, err
			}
			if prior, dup := seenTools[op.Tool]; dup {
				return nil, fmt.Errorf("mcp tool %q is declared by both %s and %s", op.Tool, prior, op.ID)
			}
			seenTools[op.Tool] = op.ID
			catalog.Operations = append(catalog.Operations, op)
			catalog.byID[op.ID] = op
		}
	}
	for _, entry := range asList(doc["x-mcp-composites"]) {
		fields, _ := entry.(map[string]any)
		comp := Composite{
			Tool:        asString(fields["tool"]),
			Group:       asString(fields["group"]),
			Description: strings.TrimSpace(asString(fields["description"])),
		}
		for _, id := range asList(fields["operations"]) {
			comp.Operations = append(comp.Operations, asString(id))
		}
		if comp.Tool == "" || !knownGroups[comp.Group] || comp.Description == "" || len(comp.Operations) == 0 {
			return nil, fmt.Errorf("x-mcp-composites entry %q needs tool, a known group, description and operations", comp.Tool)
		}
		if prior, dup := seenTools[comp.Tool]; dup {
			return nil, fmt.Errorf("mcp tool %q is declared by both %s and a composite", comp.Tool, prior)
		}
		seenTools[comp.Tool] = "composite"
		for _, id := range comp.Operations {
			if catalog.byID[id] == nil && !operationExists(paths, id) {
				return nil, fmt.Errorf("composite %q names unknown operationId %q", comp.Tool, id)
			}
		}
		catalog.Composites = append(catalog.Composites, comp)
	}
	return catalog, nil
}

func operationExists(paths map[string]any, id string) bool {
	for _, item := range paths {
		methods, _ := item.(map[string]any)
		for _, node := range methods {
			if op, ok := node.(map[string]any); ok && asString(op["operationId"]) == id {
				return true
			}
		}
	}
	return false
}

func buildOperation(doc map[string]any, path, method string, node, ext map[string]any) (*Operation, error) {
	id := asString(node["operationId"])
	op := &Operation{
		ID:          id,
		Method:      strings.ToUpper(method),
		Path:        path,
		Tool:        asString(ext["tool"]),
		Group:       asString(ext["group"]),
		Write:       ext["write"] == true,
		Destructive: method == "delete",
		Idempotent:  ext["idempotent"] != false,
		rejectEnum:  map[string][]string{},
	}
	if op.Tool == "" || id == "" {
		return nil, fmt.Errorf("%s %s: x-mcp needs a tool name and the operation an operationId", op.Method, path)
	}
	if !knownGroups[op.Group] {
		return nil, fmt.Errorf("%s: x-mcp group %q is not one of the known groups", id, op.Group)
	}
	if _, ok := ext["write"].(bool); !ok {
		return nil, fmt.Errorf("%s: x-mcp write must be an explicit boolean", id)
	}
	if method == "get" && op.Write {
		return nil, fmt.Errorf("%s: a GET operation cannot be a write tool", id)
	}
	op.IdempotencyKeyRequired = ext["idempotency"] == "required"
	rename := map[string]string{}
	if m, ok := ext["rename"].(map[string]any); ok {
		for wire, arg := range m {
			rename[wire] = asString(arg)
		}
	}
	for field, values := range asStringMap(ext["reject_enum"]) {
		op.rejectEnum[field] = values
	}
	op.Description = strings.TrimSpace(asString(ext["description"]))
	if op.Description == "" {
		op.Description = strings.TrimSpace(asString(node["description"]))
	}
	if op.Description == "" {
		op.Description = strings.TrimSpace(asString(node["summary"]))
	}
	if op.Description == "" {
		return nil, fmt.Errorf("%s: no description for tool %s", id, op.Tool)
	}

	conv := newConverter(doc)
	properties := map[string]any{}
	var required []string
	taken := func(arg string) error {
		if _, exists := properties[arg]; exists {
			return fmt.Errorf("%s: argument %q is produced twice", id, arg)
		}
		return nil
	}

	var params []any
	if p, ok := item(doc, "paths", path)["parameters"]; ok {
		params = append(params, asList(p)...)
	}
	params = append(params, asList(node["parameters"])...)
	for _, raw := range params {
		param := conv.parameter(raw)
		if param == nil {
			return nil, fmt.Errorf("%s: unresolvable parameter %v", id, raw)
		}
		name := asString(param["name"])
		where := asString(param["in"])
		isRequired := param["required"] == true
		switch where {
		case "header":
			switch strings.ToLower(name) {
			case "if-match":
				op.HasIfMatch = true
				if err := taken("expected_version"); err != nil {
					return nil, err
				}
				properties["expected_version"] = map[string]any{
					"type": "integer", "minimum": 0,
					"description": "Expected current owner version (If-Match) for optimistic concurrency; read it from the owner first. Use 0 only to create.",
				}
				required = append(required, "expected_version")
			case "idempotency-key":
				op.HasIdempotencyHeader = true
				if err := taken("idempotency_key"); err != nil {
					return nil, err
				}
				properties["idempotency_key"] = map[string]any{
					"type": "string", "minLength": 8, "maxLength": 128,
					"description": "Caller-supplied idempotency key; replaying the same key returns the first result. Generated automatically when omitted.",
				}
				if op.IdempotencyKeyRequired {
					required = append(required, "idempotency_key")
				}
			default:
				return nil, fmt.Errorf("%s: unsupported header parameter %q", id, name)
			}
		case "path", "query":
			arg := name
			if renamed, ok := rename[name]; ok {
				arg = renamed
			}
			if err := taken(arg); err != nil {
				return nil, err
			}
			schema := conv.schema(param["schema"])
			if desc := asString(param["description"]); desc != "" {
				schema = withDescription(schema, desc)
			}
			properties[arg] = schema
			p := parameter{wire: name, arg: arg, required: isRequired}
			if s, ok := param["schema"].(map[string]any); ok {
				p.isArray = s["type"] == "array"
			}
			if where == "path" {
				op.pathParams = append(op.pathParams, p)
				required = append(required, arg)
			} else {
				op.queryParams = append(op.queryParams, p)
				if isRequired {
					required = append(required, arg)
				}
			}
		default:
			return nil, fmt.Errorf("%s: unsupported parameter location %q", id, where)
		}
	}

	if body, ok := node["requestBody"].(map[string]any); ok {
		op.hasBody = true
		body = conv.requestBody(body)
		content, _ := body["content"].(map[string]any)
		media, _ := content["application/json"].(map[string]any)
		if media == nil {
			return nil, fmt.Errorf("%s: only application/json request bodies are supported", id)
		}
		bodySchema, _ := media["schema"].(map[string]any)
		target := conv.deref(bodySchema)
		props, _ := target["properties"].(map[string]any)
		if target["type"] == "object" && props != nil && target["allOf"] == nil && target["oneOf"] == nil && target["anyOf"] == nil {
			op.bodyProps = map[string]bool{}
			names := make([]string, 0, len(props))
			for name := range props {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if err := taken(name); err != nil {
					return nil, err
				}
				properties[name] = conv.schema(props[name])
				op.bodyProps[name] = true
			}
			for _, name := range asStringList(target["required"]) {
				required = append(required, name)
			}
		} else {
			op.bodyAsArgument = true
			if err := taken("body"); err != nil {
				return nil, err
			}
			properties["body"] = conv.schema(bodySchema)
			if body["required"] == true {
				required = append(required, "body")
			}
		}
	}

	for field, values := range op.rejectEnum {
		prop, ok := properties[field].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: reject_enum names unknown argument %q", id, field)
		}
		if enum, ok := prop["enum"].([]any); ok {
			var kept []any
			for _, v := range enum {
				if !containsString(values, asString(v)) {
					kept = append(kept, v)
				}
			}
			prop["enum"] = kept
		}
	}

	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	sort.Strings(required)
	if len(required) > 0 {
		schema["required"] = required
	}
	if defs := conv.definitions(); len(defs) > 0 {
		schema["$defs"] = defs
	}
	op.inputSchema = schema

	op.responseSchemas = successSchemas(doc, node)
	op.redactor = redactor{doc: doc}
	for _, response := range op.responseSchemas {
		if op.redactor.reachesOmit(response) {
			op.withholds = true
			break
		}
	}
	return op, nil
}

// Withholds reports whether some 2xx response of the operation declares an
// x-mcp-omit property, so its tool result is the body minus those properties.
func (o *Operation) Withholds() bool { return o.withholds }

// redact applies the withholding layers (redact.go) to a decoded success body,
// following the response schema declared for that status, and returns it.
func (o *Operation) redact(status int, body any) any {
	schema, ok := o.responseSchemas[strconv.Itoa(status)]
	if ok {
		return o.redactor.apply(body, schema, 0)
	}
	if len(o.responseSchemas) == 0 {
		return scrubFree(body, 0)
	}
	// An undeclared success status still must not leak: apply every declared
	// success schema.
	for _, candidate := range o.responseSchemas {
		body = o.redactor.apply(body, candidate, 0)
	}
	return body
}

// InputSchema returns the generated JSON Schema for the tool arguments.
func (o *Operation) InputSchema() json.RawMessage {
	raw, _ := json.Marshal(o.inputSchema)
	return raw
}

// ArgumentNames lists the tool argument names, sorted.
func (o *Operation) ArgumentNames() []string {
	props, _ := o.inputSchema["properties"].(map[string]any)
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func item(doc map[string]any, section, key string) map[string]any {
	parent, _ := doc[section].(map[string]any)
	node, _ := parent[key].(map[string]any)
	return node
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func asStringList(v any) []string {
	var out []string
	for _, e := range asList(v) {
		out = append(out, asString(e))
	}
	return out
}

func asStringMap(v any) map[string][]string {
	out := map[string][]string{}
	m, _ := v.(map[string]any)
	for k, e := range m {
		out[k] = asStringList(e)
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}
