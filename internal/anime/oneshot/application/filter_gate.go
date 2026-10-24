package application

import (
	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/filekinds"
)

// This gate is unconditional: unconfigured target acquisition cannot bypass it.
func validateOneShotFilterGate(item acquisitiondomain.FeedItem, effective animedomain.EffectiveConfig) error {
	release, err := animedomain.CompileRegexFilter(effective.Rules.ReleaseFilter)
	if err != nil {
		return &apperrors.Business{Status: 422, Code: "FILTER_INVALID", Message: err.Error()}
	}
	if d := release.MatchReleaseTitle(item.Title); !d.Allowed {
		return &apperrors.Business{Status: 422, Code: "RELEASE_FILTER_REJECTED", Message: d.Reason}
	}
	file, err := animedomain.CompileRegexFilter(effective.Rules.FileFilter)
	if err != nil {
		return &apperrors.Business{Status: 422, Code: "FILTER_INVALID", Message: err.Error()}
	}
	paths := make([]string, len(item.ReleaseFiles))
	for i, f := range item.ReleaseFiles {
		paths[i] = f.SourcePath
	}
	result, err := file.SelectOrganizationFiles(filekinds.Version, paths)
	if err != nil {
		return &apperrors.Business{Status: 422, Code: "FILE_FILTER_INVALID", Message: err.Error()}
	}
	if !result.DownloadAllowed {
		return &apperrors.Business{Status: 422, Code: "FILE_SELECTION_EMPTY", Message: "The file filter allows no torrent files."}
	}
	return nil
}
