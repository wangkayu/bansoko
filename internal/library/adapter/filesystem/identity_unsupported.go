//go:build !darwin && !linux

package filesystem

import (
	"os"

	"bansoko/internal/fileidentity"
	"bansoko/internal/library/domain"
)

func platformFileIdentity(info os.FileInfo) domain.FileIdentity {
	return fileidentity.FromFileInfo(info)
}
