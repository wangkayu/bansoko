// Command bansoko-bootstrap preserves the standalone development and recovery
// entrypoint. Production builds use bansoko-server's bootstrap mode.
package main

import (
	"os"

	bootstrapcmd "bansoko/internal/runtime/bootstrap"
)

func main() {
	os.Exit(bootstrapcmd.Main(os.Args[1:]))
}
