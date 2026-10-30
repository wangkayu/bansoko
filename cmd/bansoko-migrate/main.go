// Command bansoko-migrate preserves the standalone development migration
// entrypoint. Production builds use bansoko-server's migrate mode.
package main

import (
	"os"

	"bansoko/internal/runtime/migratecmd"
)

func main() {
	migratecmd.Main(os.Args[1:])
}
