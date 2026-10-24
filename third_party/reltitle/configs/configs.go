// Package configs embeds the default YAML configuration that ships with
// reltitle, so the binary works without an external configs/ directory.
// Pass FS to config.LoadAllFS, or simply use parser.NewDefault.
package configs

import "embed"

// FS holds the 13 default configuration files from this directory.
//
//go:embed *.yaml
var FS embed.FS
