package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/batchplan"
	animecatalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/filekinds"
)

// RSSListFileSelectionInput is one seed's preview-only manual file selection
// of a one-shot read: the seed identity and the manifest paths chosen.
type RSSListFileSelectionInput struct {
	SnapshotID    string
	StableSeedKey string
	SourcePaths   []string
}

// Manifest file recognitions (RSSListManifestFile.Recognition).
const (
	RSSListRecognitionEpisode      = "episode"
	RSSListRecognitionMovie        = "movie"
	RSSListRecognitionUnrecognized = "unrecognized"
)

// RSSListManifestFile is one file of a seed's exact torrent manifest for the
// one-shot file picker: every file at any depth, with the server's decision
// of whether it may be chosen, whether the default list holds it, a video's
// own filename recognition and the video a subtitle follows.
type RSSListManifestFile struct {
	SourcePath           string
	SizeBytes            int64
	MediaKind            string
	Selectable           bool
	ReasonCode           string
	DefaultSelected      bool
	Recognition          string
	Episodes             []int
	BoundVideoSourcePath string
}

// rssListFileSelections indexes a read's manual selections by seed identity.
type rssListFileSelections map[string]*filekinds.Selection

func rssListSelectionKey(snapshotID, stableSeedKey string) string {
	return snapshotID + "\x00" + stableSeedKey
}

func newRSSListFileSelections(inputs []RSSListFileSelectionInput) rssListFileSelections {
	if len(inputs) == 0 {
		return nil
	}
	out := rssListFileSelections{}
	for _, input := range inputs {
		paths := input.SourcePaths
		if paths == nil {
			paths = []string{}
		}
		out[rssListSelectionKey(input.SnapshotID, input.StableSeedKey)] = filekinds.NewSelection(paths)
	}
	return out
}

// forSnapshot is the selections of one snapshot keyed by stable seed key.
func (s rssListFileSelections) forSnapshot(snapshotID string) map[string]*filekinds.Selection {
	if len(s) == 0 {
		return nil
	}
	out := map[string]*filekinds.Selection{}
	prefix := snapshotID + "\x00"
	for key, selection := range s {
		if stable, ok := strings.CutPrefix(key, prefix); ok {
			out[stable] = selection
		}
	}
	return out
}

// identity is a digest of every selection, part of the view's config
// identity: the same draft read under a different selection renders
// different files.
func (s rssListFileSelections) identity() string {
	if len(s) == 0 {
		return ""
	}
	keys := make([]string, 0, len(s))
	for key := range s {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		hash.Write([]byte(key))
		for _, value := range s[key].Paths() {
			hash.Write([]byte{0})
			hash.Write([]byte(value))
		}
		hash.Write([]byte{1})
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

// oneShotManifestFiles projects every manifest file of one seed for the file
// picker. The decisions come from the same batch plan an acceptance builds:
// selectable is the file rules (and the kind gate) without the first-level
// gate; default_selected is the default plan's candidate set; a video's
// recognition and a subtitle's bound video come from the plan over every
// selectable file. For a movie binding only a video of the read's own plan
// (its selection, else the default files) carries a recognition, since the
// main video depends on which videos are chosen; an unchosen video has none.
func oneShotManifestFiles(ctx context.Context, manifest *animecatalogdomain.TorrentManifest, evidence acquisitiontarget.ParserEvidence, effective animedomain.EffectiveConfig, route string, selection *filekinds.Selection) ([]RSSListManifestFile, error) {
	if manifest == nil || len(manifest.Files) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(route) == "" {
		route = batchplan.RouteSkip
	}
	files := make([]acquisitiondomain.ReleaseFile, 0, len(manifest.Files))
	sizes := map[string]int64{}
	paths := []string{}
	selectable := map[string]string{} // path -> "" selectable, else reason
	allSelectable := []string{}
	for _, file := range manifest.Files {
		sourcePath := filekinds.NormalizePath(file.SourcePath)
		if sourcePath == "" {
			continue
		}
		size := file.SizeBytes
		files = append(files, acquisitiondomain.ReleaseFile{SourcePath: file.SourcePath, Filename: file.Filename, SizeBytes: &size, MediaKind: file.MediaKind})
		sizes[sourcePath] = size
		paths = append(paths, sourcePath)
	}
	sort.Strings(paths)
	// Without the first-level gate (every path selected), the exclusion is the
	// kind gate and the file rules alone.
	everything := filekinds.NewSelection(paths)
	for _, sourcePath := range paths {
		reason := animedomain.ManifestFileExclusionIn(filekinds.Version, effective.Rules.FileFilter, sourcePath, everything)
		selectable[sourcePath] = reason
		if reason == "" {
			allSelectable = append(allSelectable, sourcePath)
		}
	}
	build := func(selected *filekinds.Selection) (batchplan.Plan, error) {
		return acquisitiontarget.BuildBatchPlan(ctx, acquisitiontarget.BatchPlanInput{
			Evidence: evidence, Files: files, FileFilter: effective.Rules.FileFilter, UnclassifiedRoute: route, Selection: selected,
			DownloadCompletion: string(effective.DownloadCompletion.Rules.Action), DownloadCompletionSeedLimits: effective.DownloadCompletion.Rules.SeedLimits,
		})
	}
	defaults, err := build(nil)
	if err != nil {
		return nil, err
	}
	full, err := build(filekinds.NewSelection(allSelectable))
	if err != nil {
		return nil, err
	}
	chosen := defaults
	if selection != nil {
		if chosen, err = build(selection); err != nil {
			return nil, err
		}
	}
	out := make([]RSSListManifestFile, 0, len(paths))
	for _, sourcePath := range paths {
		kind := filekinds.Classify(filekinds.Version, sourcePath)
		row := RSSListManifestFile{SourcePath: sourcePath, SizeBytes: sizes[sourcePath], MediaKind: kind}
		reason := selectable[sourcePath]
		row.Selectable = reason == ""
		row.ReasonCode = reason
		if entry, ok := defaults.File(sourcePath); ok {
			switch kind {
			case filekinds.KindVideo:
				row.DefaultSelected = entry.Outcome != batchplan.OutcomeIgnoredNested && entry.Outcome != batchplan.OutcomeIgnoredKind && entry.Outcome != batchplan.OutcomeExcludedByRule
			case filekinds.KindSubtitle:
				if entry.BoundVideo != "" {
					video, _ := defaults.File(entry.BoundVideo)
					row.DefaultSelected = video.Outcome != batchplan.OutcomeExcludedByRule
				}
			}
		}
		if row.Selectable {
			switch kind {
			case filekinds.KindVideo:
				if defaults.MediaKind == batchplan.MediaKindFilm {
					// The movie main video depends on which videos are chosen:
					// only a video of the read's own plan carries a recognition,
					// so exactly one row reads as the main video.
					if picked, ok := chosen.File(sourcePath); ok && (picked.Outcome == batchplan.OutcomePlanned || picked.Outcome == batchplan.OutcomeEpisodeUnrecognized) {
						recognizeManifestVideo(&row, picked, true)
					}
					break
				}
				entry, _ := full.File(sourcePath)
				recognizeManifestVideo(&row, entry, false)
			case filekinds.KindSubtitle:
				if entry, ok := full.File(sourcePath); ok {
					row.BoundVideoSourcePath = entry.BoundVideo
				}
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// recognizeManifestVideo reads a video's recognition off its plan entry: a
// planned video is a recognized episode (or the movie main video), an
// episode_unrecognized one is an unknown file.
func recognizeManifestVideo(row *RSSListManifestFile, entry batchplan.File, movie bool) {
	if entry.Outcome != batchplan.OutcomePlanned {
		row.Recognition = RSSListRecognitionUnrecognized
		return
	}
	if movie {
		row.Recognition = RSSListRecognitionMovie
		return
	}
	row.Recognition = RSSListRecognitionEpisode
	episodes := []int{}
	for _, key := range entry.Keys() {
		parsed, ok := episodeidentity.DecomposeCoverageKey(key)
		if !ok {
			continue
		}
		number, err := strconv.Atoi(parsed.Episode)
		if err != nil {
			continue
		}
		episodes = append(episodes, number)
	}
	if len(episodes) > 0 {
		sort.Ints(episodes)
		row.Episodes = episodes
	}
}
