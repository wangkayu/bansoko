package domain

import (
	"errors"
	"io/fs"
	"strings"
)

// PartialDownload freezes a strict subset of the verified torrent manifest.
// Paths include the torrent root; qB file indexes are resolved at execution.
type PartialDownload struct {
	Paths     []string `json:"paths"`
	FileCount int      `json:"file_count"`
}

func (p PartialDownload) Validate() error {
	if len(p.Paths) == 0 || len(p.Paths) >= p.FileCount {
		return errors.New("partial download requires an explicit strict subset of the torrent")
	}
	seen := make(map[string]struct{}, len(p.Paths))
	for _, path := range p.Paths {
		if !fs.ValidPath(path) || path == "." || strings.Contains(path, "\\") {
			return errors.New("invalid partial download path")
		}
		if _, exists := seen[path]; exists {
			return errors.New("duplicate partial download path")
		}
		seen[path] = struct{}{}
	}
	return nil
}
