package application

import domain "bansoko/internal/anime/domain"

type previewEvaluation struct {
	Files    []domain.PreviewFileResult
	Releases []domain.PreviewReleaseResult
}

func newPreviewEvaluation() previewEvaluation {
	return previewEvaluation{Files: []domain.PreviewFileResult{}, Releases: []domain.PreviewReleaseResult{}}
}
