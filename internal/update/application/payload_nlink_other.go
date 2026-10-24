//go:build !darwin && !linux

package application

import (
	"fmt"
	"os"
)

func regularLinkCount(os.FileInfo) (uint64, error) {
	return 0, fmt.Errorf("file link count is unavailable on this platform")
}
