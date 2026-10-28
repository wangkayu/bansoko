package application

import (
	domain "bansoko/internal/anime/domain"
	"bansoko/internal/filekinds"
)

type previewEvaluation struct {
	Files    []domain.PreviewFileResult
	Releases []domain.PreviewReleaseResult
	// OneShot (input) asks for every item's manifest files (the one-shot file
	// picker), and Selections (input) are the manual file selections of this
	// snapshot's seeds keyed by stable seed key.
	OneShot    bool
	Selections map[string]*filekinds.Selection
	// ManifestFiles (output) are the picker rows per stable seed key.
	ManifestFiles map[string][]RSSListManifestFile
}

func newPreviewEvaluation() previewEvaluation {
	return previewEvaluation{Files: []domain.PreviewFileResult{}, Releases: []domain.PreviewReleaseResult{}}
}
