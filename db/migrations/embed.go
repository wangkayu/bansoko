// Package migrations carries the embedded copy of the authoritative SQL
// migration set so a single native binary can verify or apply the schema
// without a mounted db/migrations directory. An explicitly configured
// directory (BANSOKO_SCHEMA_DIR / --schema-dir) still takes precedence over
// this embedded fallback.
package migrations

import "embed"

// FS holds the forward-only SQL migration files exactly as tracked in this
// directory. The pattern is restricted to *.sql so helper sources in the same
// directory are never part of the schema identity.
//
//go:embed *.sql
var FS embed.FS
