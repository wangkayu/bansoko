package eventactions

import (
	"context"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func canExecuteFile(dir, _ int, name string) bool {
	return unix.Faccessat(dir, name, unix.X_OK, unix.AT_EACCESS|unix.AT_SYMLINK_NOFOLLOW) == nil
}

// Production execution requires Linux descriptor-pinned exec semantics.
func canRun() bool { return false }

func execute(context.Context, *os.File, []byte, time.Duration, *resultCapture) (string, *int, *time.Time) {
	return "unavailable", nil, nil
}
