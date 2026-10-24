// Package api embeds the OpenAPI source so runtime surfaces that are derived
// from the contract (the MCP tool catalog) read the same bytes the generators
// consume. Nothing here is parsed at build time.
package api

import _ "embed"

// OpenAPI is the api/openapi.yaml document.
//
//go:embed openapi.yaml
var OpenAPI []byte
