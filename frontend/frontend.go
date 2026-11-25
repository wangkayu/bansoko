// Package frontend exposes the built web UI. Release binaries are compiled
// with -tags embed_frontend, which embeds the vite build output (dist) into
// the executable so a single native binary serves the UI without any external
// static directory. Development builds compiled without the tag receive a nil
// FS and fall back to the on-disk frontend/dist directory.
package frontend

import "io/fs"

// FS returns the embedded web UI assets, or nil when the binary was built
// without the embed_frontend tag.
func FS() fs.FS { return distFS }

// Embedded reports whether this binary carries the embedded web UI. The
// version command surfaces it as build provenance.
func Embedded() bool { return embedded }
