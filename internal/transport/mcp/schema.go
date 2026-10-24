package mcp

import (
	"sort"
	"strings"
)

// converter turns OpenAPI 3.1 schema fragments into self-contained JSON Schema
// for tool input. Named object schemas are emitted once under $defs and
// referenced, so a large shared type (AnimeDraft) is not repeated per use and
// recursive types terminate. Small scalar schemas (IDs, versions, enums) are
// inlined because a reference would only add noise.
type converter struct {
	doc   map[string]any
	defs  map[string]any
	depth int
}

// maxDefDepth bounds how deep named object schemas are expanded. A full
// AnimeDraft expansion is over 20 KB per tool; past this depth a named object
// collapses to a described stub and the server's own validation reports the
// precise field errors.
const maxDefDepth = 2

func newConverter(doc map[string]any) *converter {
	return &converter{doc: doc, defs: map[string]any{}}
}

func (c *converter) definitions() map[string]any { return c.defs }

func (c *converter) lookup(ref string) (string, map[string]any) {
	const prefix = "#/components/"
	if !strings.HasPrefix(ref, prefix) {
		return "", nil
	}
	parts := strings.Split(strings.TrimPrefix(ref, prefix), "/")
	if len(parts) != 2 {
		return "", nil
	}
	components, _ := c.doc["components"].(map[string]any)
	section, _ := components[parts[0]].(map[string]any)
	node, _ := section[parts[1]].(map[string]any)
	return parts[1], node
}

// parameter resolves a parameter object or a $ref to one.
func (c *converter) parameter(raw any) map[string]any {
	node, _ := raw.(map[string]any)
	if ref, ok := node["$ref"].(string); ok {
		_, target := c.lookup(ref)
		return target
	}
	return node
}

// requestBody resolves a request body object or a $ref to one.
func (c *converter) requestBody(node map[string]any) map[string]any {
	if ref, ok := node["$ref"].(string); ok {
		_, target := c.lookup(ref)
		return target
	}
	return node
}

// deref follows a schema $ref one hop and returns the target (or the node).
func (c *converter) deref(node map[string]any) map[string]any {
	for i := 0; i < 8; i++ {
		ref, ok := node["$ref"].(string)
		if !ok {
			return node
		}
		_, target := c.lookup(ref)
		if target == nil {
			return node
		}
		node = target
	}
	return node
}

func isScalar(node map[string]any) bool {
	switch node["type"] {
	case "string", "integer", "number", "boolean":
		return node["properties"] == nil && node["allOf"] == nil && node["oneOf"] == nil && node["anyOf"] == nil
	}
	return false
}

var droppedKeys = map[string]bool{
	"nullable": true, "example": true, "examples": true, "discriminator": true,
	"externalDocs": true, "xml": true, "readOnly": true, "writeOnly": true, "deprecated": true,
}

// schema converts a schema node.
func (c *converter) schema(node any) any {
	m, ok := node.(map[string]any)
	if !ok {
		return node
	}
	if ref, ok := m["$ref"].(string); ok {
		name, target := c.lookup(ref)
		if target == nil {
			return map[string]any{}
		}
		siblings := map[string]any{}
		for k, v := range m {
			if k != "$ref" && !droppedKeys[k] && !strings.HasPrefix(k, "x-") {
				siblings[k] = v
			}
		}
		if isScalar(target) {
			inlined, _ := c.schema(target).(map[string]any)
			for k, v := range siblings {
				inlined[k] = v
			}
			return inlined
		}
		if _, seen := c.defs[name]; !seen {
			if c.depth >= maxDefDepth {
				stub := map[string]any{"type": "object", "description": name + " object; field details are in the agent guide and the API contract."}
				if target["type"] == "array" {
					stub["type"] = "array"
				}
				c.defs[name] = stub
			} else {
				c.defs[name] = map[string]any{} // placeholder terminates recursion
				c.depth++
				c.defs[name] = c.schema(target)
				c.depth--
			}
		}
		out := map[string]any{"$ref": "#/$defs/" + name}
		for k, v := range siblings {
			out[k] = v
		}
		return out
	}
	out := map[string]any{}
	// Sorted traversal keeps the $defs expansion order, and with it the
	// generated schema, byte-identical between runs.
	for _, k := range sortedKeys(m) {
		v := m[k]
		if droppedKeys[k] || strings.HasPrefix(k, "x-") {
			continue
		}
		switch k {
		case "properties":
			props := map[string]any{}
			source := asMap(v)
			for _, name := range sortedKeys(source) {
				props[name] = c.schema(source[name])
			}
			out[k] = props
		case "items", "not":
			out[k] = c.schema(v)
		case "additionalProperties":
			if sub, ok := v.(map[string]any); ok {
				out[k] = c.schema(sub)
			} else {
				out[k] = v
			}
		case "allOf", "oneOf", "anyOf":
			var list []any
			for _, e := range asList(v) {
				list = append(list, c.schema(e))
			}
			out[k] = list
		default:
			out[k] = v
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func withDescription(schema any, description string) any {
	m, ok := schema.(map[string]any)
	if !ok {
		return schema
	}
	if _, has := m["description"]; !has {
		m["description"] = description
	}
	return m
}
