package mcp

import (
	"net/url"
	"regexp"
	"strings"

	"bansoko/internal/redact"
)

// Tool results never carry an RSS-derived URL, whatever field it is in. Three
// layers enforce that, applied to every tool result (success and error):
//
//  1. Schema marks. A schema property marked x-mcp-omit stays in the HTTP
//     response but is removed from every tool result whose response schema
//     reaches it, so the OpenAPI file describes the tool output exactly: the
//     response schema minus the marked properties.
//  2. Free-form subtrees. Where the schema says nothing about a value (an
//     untyped object, additionalProperties: true, a key no schema declares),
//     every key named in withheldNames is removed at any depth.
//  3. Value backstop. Any remaining absolute http(s) URL with a query string,
//     userinfo, or a credential in its path (redact.URL: a UNIT3D
//     /torrents/download/<id>.<key>, an /rss/<key> feed) is replaced with
//     withheldPlaceholder. It is a backstop, not
//     the mechanism: a legitimate field it would catch is exempted explicitly
//     with x-mcp-keep-url on its schema property.
//
// A property marked x-secret (SEC-033) is governed by the bearer scope layer
// instead of the backstop: the HTTP chain the tool replays through already
// replaced its value with the fixed "[secret]" placeholder for a bearer without
// the secrets scope, and a bearer with that scope asked for the stored value
// (which a candidate patch must be able to send back unchanged). A property
// marked both x-mcp-omit and x-secret is omitted: x-mcp-omit always wins.
const (
	omitExtension    = "x-mcp-omit"
	keepURLExtension = "x-mcp-keep-url"
	secretExtension  = "x-secret"
)

// withheldPlaceholder replaces a URL caught by the value backstop.
const withheldPlaceholder = "[withheld]"

// withheldNames lists the property names withheld inside free-form subtrees.
// It covers every x-mcp-omit property name in the OpenAPI file
// (TestWithheldNamesCoverEveryOmittedProperty) plus the raw feed field names
// that can carry a torrent or feed URL.
var withheldNames = map[string]bool{
	"cursor":                  true,
	"download_url":            true,
	"enclosure":               true,
	"enclosure_url":           true,
	"expected_payload_digest": true,
	"feed_url":                true,
	"guid":                    true,
	"identity_value":          true,
	"link":                    true,
	"locator":                 true,
	"locator_display":         true,
	"magnet":                  true,
	"newest_stable_item_key":  true,
	"passkey":                 true,
	"release_stable_item_key": true,
	"source_locator_display":  true,
	"stable_item_key":         true,
	"torrent_url":             true,
}

// maxRedactDepth bounds the walk over a response body. Response bodies are
// finite trees, so this only guards against a pathological document.
const maxRedactDepth = 64

// urlPattern finds absolute http(s) URLs inside a string value.
var urlPattern = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>]+`)

// redactor applies the three layers to a decoded body by walking it together
// with its OpenAPI response schema.
type redactor struct {
	doc map[string]any
}

func isOmitted(property any) bool {
	node, _ := property.(map[string]any)
	return node[omitExtension] == true
}

// resolve follows a local $ref (several hops) and returns the target schema.
func (r redactor) resolve(node map[string]any) map[string]any {
	for i := 0; i < 8 && node != nil; i++ {
		ref, ok := node["$ref"].(string)
		if !ok {
			return node
		}
		node = r.lookup(ref)
	}
	return node
}

func (r redactor) lookup(ref string) map[string]any {
	parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	var target any = r.doc
	for _, part := range parts {
		object, _ := target.(map[string]any)
		target = object[part]
	}
	node, _ := target.(map[string]any)
	return node
}

// branches flattens a schema into the schemas a value may match: the schema
// itself (a $ref node first, so keywords next to the $ref count) plus every
// allOf/oneOf/anyOf member, recursively.
func (r redactor) branches(node map[string]any, depth int, out []map[string]any) []map[string]any {
	if node == nil || depth > maxRedactDepth {
		return out
	}
	if _, isRef := node["$ref"]; isRef {
		out = append(out, node)
		node = r.resolve(node)
		if node == nil {
			return out
		}
	}
	out = append(out, node)
	for _, key := range []string{"allOf", "oneOf", "anyOf"} {
		for _, member := range asList(node[key]) {
			if schema, ok := member.(map[string]any); ok {
				out = r.branches(schema, depth+1, out)
			}
		}
	}
	return out
}

// freeForm reports whether the variants say nothing about the value's shape:
// no properties, items or typed additionalProperties, and no scalar type.
func freeForm(variants []map[string]any) bool {
	for _, variant := range variants {
		if _, ok := variant["properties"]; ok {
			return false
		}
		if _, ok := variant["items"]; ok {
			return false
		}
		if additional, ok := variant["additionalProperties"].(map[string]any); ok && len(additional) > 0 {
			return false
		}
		if _, ok := variant["enum"]; ok {
			return false
		}
		switch variant["type"] {
		case "string", "integer", "number", "boolean", "array":
			return false
		}
	}
	return true
}

func keepsURL(variants []map[string]any) bool {
	for _, variant := range variants {
		if variant[keepURLExtension] == true || variant[secretExtension] == true {
			return true
		}
	}
	return false
}

// apply returns value with the three layers applied, following schema. Maps
// and slices are edited in place; a replaced string is returned.
func (r redactor) apply(value any, schema map[string]any, depth int) any {
	if depth > maxRedactDepth {
		return withheldPlaceholder
	}
	if schema == nil {
		return scrubFree(value, depth)
	}
	variants := r.branches(schema, depth, nil)
	if freeForm(variants) {
		return scrubFree(value, depth)
	}
	switch typed := value.(type) {
	case string:
		if keepsURL(variants) {
			return typed
		}
		return withholdURLs(typed)
	case map[string]any:
		for key, child := range typed {
			var childSchemas []map[string]any
			omitted := false
			declared := false
			for _, variant := range variants {
				props, _ := variant["properties"].(map[string]any)
				property, ok := props[key]
				if !ok {
					continue
				}
				declared = true
				if isOmitted(property) {
					omitted = true
					break
				}
				if node, ok := property.(map[string]any); ok {
					childSchemas = append(childSchemas, node)
				}
			}
			if omitted || (!declared && withheldNames[key]) {
				delete(typed, key)
				continue
			}
			if !declared {
				for _, variant := range variants {
					if additional, ok := variant["additionalProperties"].(map[string]any); ok && len(additional) > 0 {
						childSchemas = append(childSchemas, additional)
					}
				}
			}
			if len(childSchemas) == 0 {
				typed[key] = scrubFree(child, depth+1)
				continue
			}
			for _, childSchema := range childSchemas {
				child = r.apply(child, childSchema, depth+1)
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
			if len(itemSchemas) == 0 {
				typed[index] = scrubFree(element, depth+1)
				continue
			}
			for _, items := range itemSchemas {
				element = r.apply(element, items, depth+1)
			}
			typed[index] = element
		}
		return typed
	}
	return value
}

// scrubFree applies layers 2 and 3 to a value no schema describes.
func scrubFree(value any, depth int) any {
	if depth > maxRedactDepth {
		return withheldPlaceholder
	}
	switch typed := value.(type) {
	case string:
		return withholdURLs(typed)
	case map[string]any:
		for key, child := range typed {
			if withheldNames[key] {
				delete(typed, key)
				continue
			}
			typed[key] = scrubFree(child, depth+1)
		}
		return typed
	case []any:
		for index, element := range typed {
			typed[index] = scrubFree(element, depth+1)
		}
		return typed
	}
	return value
}

// withholdURLs replaces every absolute http(s) URL in text that carries a
// query string, userinfo, or a path credential (where passkeys and tokens
// live) with the placeholder. An unparseable URL is withheld too.
func withholdURLs(text string) string {
	if !strings.Contains(strings.ToLower(text), "http") {
		return text
	}
	return urlPattern.ReplaceAllStringFunc(text, func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(raw, "?") || redact.ContainsCredential(raw) {
			return withheldPlaceholder
		}
		return raw
	})
}

// reachesOmit reports whether any property below schema carries x-mcp-omit.
// A tool whose response reaches one never passes an undecodable body through.
func (r redactor) reachesOmit(root any) bool {
	seen := map[string]bool{}
	var walk func(node any) bool
	walk = func(node any) bool {
		switch typed := node.(type) {
		case map[string]any:
			if ref, ok := typed["$ref"].(string); ok && !seen[ref] {
				seen[ref] = true
				if walk(r.lookup(ref)) {
					return true
				}
			}
			if props, ok := typed["properties"].(map[string]any); ok {
				for _, property := range props {
					if isOmitted(property) {
						return true
					}
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

// successSchemas extracts the application/json schema of every 2xx response
// of an operation, keyed by status code.
func successSchemas(doc map[string]any, node map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	r := redactor{doc: doc}
	responses, _ := node["responses"].(map[string]any)
	for status, raw := range responses {
		if !strings.HasPrefix(status, "2") {
			continue
		}
		response := r.resolve(asMap(raw))
		content, _ := response["content"].(map[string]any)
		media, _ := content["application/json"].(map[string]any)
		if schema, ok := media["schema"].(map[string]any); ok {
			out[status] = schema
		}
	}
	return out
}
