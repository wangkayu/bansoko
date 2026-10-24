package transport

import (
	"bansoko/internal/release/torrentstructure"
	"bansoko/internal/transport/httpapi/generated"
)

// View maps the structure of one retained payload onto the HTTP contract.
func View(value torrentstructure.View) generated.ReleaseTorrentStructureView {
	files := make([]generated.ReleaseTorrentStructureFile, 0, len(value.Files))
	for _, file := range value.Files {
		files = append(files, generated.ReleaseTorrentStructureFile{Path: file.Path, SizeBytes: file.SizeBytes})
	}
	mapped := generated.ReleaseTorrentStructureView{
		Name:           value.Name,
		TorrentVersion: generated.ReleaseTorrentStructureViewTorrentVersion(value.TorrentVersion),
		TotalSize:      value.TotalSize,
		FileCount:      value.FileCount,
		Files:          files,
		Truncated:      value.Truncated,
	}
	if value.InfoHashV1 != "" {
		hash := value.InfoHashV1
		mapped.InfoHashV1 = &hash
	}
	if value.InfoHashV2 != "" {
		hash := value.InfoHashV2
		mapped.InfoHashV2 = &hash
	}
	return mapped
}
