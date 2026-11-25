//go:build embed_frontend

package frontend

import (
	"embed"
	"io/fs"
)

const embedded = true

// dist holds the complete vite build output. The all: prefix keeps files with
// leading dots/underscores (for example well-known assets) part of the
// embedded tree so the served output is byte-identical to the build output.
//
//go:embed all:dist
var dist embed.FS

var distFS = func() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("frontend: embedded dist tree unavailable: " + err.Error())
	}
	return sub
}()
