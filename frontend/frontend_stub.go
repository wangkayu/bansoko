//go:build !embed_frontend

package frontend

import "io/fs"

const embedded = false

// distFS is nil: development builds serve the web UI from the on-disk
// frontend/dist directory resolved by the composition root.
var distFS fs.FS
