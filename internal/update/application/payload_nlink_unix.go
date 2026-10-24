//go:build darwin || linux

package application

import (
	"fmt"
	"os"
	"syscall"
)

func regularLinkCount(info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink < 1 {
		return 0, fmt.Errorf("file link count is unavailable")
	}
	return uint64(stat.Nlink), nil
}
