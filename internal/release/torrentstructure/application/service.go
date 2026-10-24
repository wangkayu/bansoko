// Package torrentstructure serves the structure of one release's retained
// torrent payload: the same bencode artifact the downloader was handed, parsed
// again on read. It owns no storage and never fetches anything; the payload is
// either still retained or the answer is "not retained".
package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"bansoko/internal/acquisition/torrent"
	"bansoko/internal/torrenttree"
)

// MaxFiles bounds the file list one response carries. A payload with more
// files reports its real file_count with Truncated set.
const MaxFiles = 2000

// ErrPayloadUnavailable reports a release whose torrent payload was never
// retained or is no longer in its retention window. It is a state of the
// release, not a server fault.
var ErrPayloadUnavailable = errors.New("torrent payload is not retained")

// File is one file of the payload, its path relative to the torrent root.
type File struct {
	Path      string
	SizeBytes int64
}

// View is the transport-neutral structure of one retained payload.
type View struct {
	Name           string
	TorrentVersion string
	InfoHashV1     string
	InfoHashV2     string
	TotalSize      int64
	FileCount      int
	Files          []File
	Truncated      bool
}

// Loader reads the retained payload bytes of one release. A missing payload
// is reported as ErrPayloadUnavailable.
type Loader interface {
	LoadPayload(ctx context.Context, releaseID string) ([]byte, error)
}

// Service adapts the loader to the read contract.
type Service struct {
	loader Loader
}

// NewService wires the read adapter.
func NewService(loader Loader) *Service {
	return &Service{loader: loader}
}

// Get parses the retained payload and returns its one-level structure. The
// paths are relative to the torrent root, sorted, and cut at MaxFiles.
func (s *Service) Get(ctx context.Context, releaseID string) (View, error) {
	if s == nil || s.loader == nil {
		return View{}, ErrPayloadUnavailable
	}
	payload, err := s.loader.LoadPayload(ctx, releaseID)
	if err != nil {
		if errors.Is(err, ErrPayloadUnavailable) {
			return View{}, ErrPayloadUnavailable
		}
		return View{}, fmt.Errorf("read retained torrent payload: %w", err)
	}
	metadata, err := torrent.Parse(payload)
	if err != nil {
		return View{}, ErrPayloadUnavailable
	}
	files := make([]torrenttree.File, 0, len(metadata.Files))
	for _, file := range metadata.Files {
		files = append(files, torrenttree.File{Path: file.Path, SizeBytes: file.SizeBytes})
	}
	relative := torrenttree.Relative(files)
	sort.Slice(relative, func(i, j int) bool { return relative[i].Path < relative[j].Path })
	view := View{
		Name:           strings.TrimSpace(metadata.TorrentName),
		TorrentVersion: metadata.TorrentVersion,
		InfoHashV1:     metadata.InfoHashV1,
		InfoHashV2:     metadata.InfoHashV2,
		TotalSize:      metadata.TotalSize,
		FileCount:      len(relative),
		Files:          make([]File, 0, min(len(relative), MaxFiles)),
	}
	for _, file := range relative {
		if len(view.Files) == MaxFiles {
			view.Truncated = true
			break
		}
		view.Files = append(view.Files, File{Path: file.Path, SizeBytes: file.SizeBytes})
	}
	return view, nil
}
