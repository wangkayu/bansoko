package target

import (
	"context"
	"fmt"
	"path"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/batchplan"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/filekinds"
)

// BatchPlanInput is everything one admission classifies a torrent manifest
// with: the frozen selected-parser evidence (filename identities, offset,
// metadata binding), the manifest, the instance file rules, and the instance
// unknown-file route that the plan freezes.
type BatchPlanInput struct {
	Evidence          ParserEvidence
	Files             []acquisitiondomain.ReleaseFile
	FileFilter        animedomain.FileFilterRules
	UnclassifiedRoute string
	// DownloadCompletion is the resolved download-completion action the plan
	// freezes (seed, pause, remove, remove_with_data).
	DownloadCompletion string
	// DownloadCompletionSeedLimits are the seed action's optional share
	// limits frozen with it; they are refused with any other action.
	DownloadCompletionSeedLimits *batchplan.SeedLimits
}

// BuildBatchPlan classifies every manifest file with the single MF1a
// recognition: first level and product kind (filekinds), the file rules
// (ManifestFileExclusion), each TV video's own filename identity
// (ResolveFileEpisode), the movie main video (organizingpolicy.SelectMovie) and
// strict-prefix subtitle binding. The release title never stands in for a
// file. Deduplication and target collisions are applied by the caller.
func BuildBatchPlan(ctx context.Context, in BatchPlanInput) (batchplan.Plan, error) {
	route := strings.TrimSpace(in.UnclassifiedRoute)
	if route != batchplan.RouteRoute && route != batchplan.RouteSkip {
		return batchplan.Plan{}, fmt.Errorf("unknown-file route %q is not route or skip", in.UnclassifiedRoute)
	}
	// An unset action is the code default (seed: no action on the task); any
	// other value must be one of the four closed actions.
	completion := strings.TrimSpace(in.DownloadCompletion)
	if completion == "" {
		completion = batchplan.CompletionSeed
	}
	if !batchplan.ValidCompletionAction(completion) {
		return batchplan.Plan{}, fmt.Errorf("download completion %q is not seed, pause, remove or remove_with_data", in.DownloadCompletion)
	}
	if err := validSeedLimits(completion, in.DownloadCompletionSeedLimits); err != nil {
		return batchplan.Plan{}, err
	}
	filenames := make(map[string]string, len(in.Files))
	files := make([]batchplan.InputFile, 0, len(in.Files))
	for _, file := range in.Files {
		sourcePath := batchplan.NormalizePath(file.SourcePath)
		if sourcePath == "" {
			sourcePath = batchplan.NormalizePath(file.Filename)
		}
		filename := strings.TrimSpace(file.Filename)
		if filename == "" {
			filename = path.Base(sourcePath)
		}
		filenames[sourcePath] = filename
		files = append(files, batchplan.InputFile{SourcePath: sourcePath, SizeBytes: file.SizeBytes})
	}
	evidence := in.Evidence
	return batchplan.Classify(batchplan.Input{
		FileKindsVersion:             filekinds.Version,
		UnclassifiedRoute:            route,
		DownloadCompletion:           completion,
		DownloadCompletionSeedLimits: in.DownloadCompletionSeedLimits,
		Movie:                        evidence.mediaKind() == "movie",
		MovieIdentity:                evidence.Identity,
		Files:                        files,
		RuleExclusion: func(sourcePath string) string {
			return animedomain.ManifestFileExclusion(filekinds.Version, in.FileFilter, sourcePath)
		},
		Identity: func(sourcePath string) (episodeidentity.EpisodeIdentity, error) {
			identity, _, err := ResolveFileEpisode(ctx, evidence, filenames[sourcePath])
			return identity, err
		},
	})
}

// validSeedLimits accepts no limits, or valid limits on the seed action.
func validSeedLimits(action string, limits *batchplan.SeedLimits) error {
	if limits == nil {
		return nil
	}
	if action != batchplan.CompletionSeed {
		return fmt.Errorf("download completion %q cannot carry seed limits", action)
	}
	if err := limits.Validate(); err != nil {
		return fmt.Errorf("download completion seed limits: %w", err)
	}
	return nil
}
