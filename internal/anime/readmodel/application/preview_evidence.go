package application

import (
	"context"
	"path"
	"sort"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/attrrules"
	"bansoko/internal/batchplan"
	animecatalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/filekinds"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
	providerdomain "bansoko/internal/provider/domain"
)

// parserSelectionUnavailableError is the typed preview failure when the saved
// revision's selected parser/profile cannot be resolved (unknown/offline kind,
// unusable config). The preview fails closed: it never falls back to the bundled
// regex parser or to the legacy preview identity.
func parserSelectionUnavailableError(detail string) error {
	message := strings.TrimSpace(detail)
	if message == "" {
		message = "the saved revision selected parser/profile is unavailable"
	}
	return &apperrors.Business{
		Status:  422,
		Code:    "PARSER_SELECTION_UNAVAILABLE",
		Message: "the saved revision selected parser/profile is unavailable: " + message,
	}
}

// previewItemVisit is the shared per-item visitor of one preview source.
type previewItemVisit func(item acquisitiondomain.FeedItem, stableKey string) error

// eachDistinctPreviewItem walks one source's items in order, skipping the
// duplicate stable keys the refresh path would also merge.
func eachDistinctPreviewItem(items []acquisitiondomain.FeedItem, visit previewItemVisit) error {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		_, stableKey := animedomain.StableItemKey(item)
		if _, duplicate := seen[stableKey]; duplicate {
			continue
		}
		seen[stableKey] = struct{}{}
		if err := visit(item, stableKey); err != nil {
			return err
		}
	}
	return nil
}

// evaluateFeedItemsWithSelection is the saved-revision preview path. Each
// Release resolves the revision's one authoritative selection
// (`ResolveParserEvidence` over the revision's parser kind, profile, frozen
// media binding and the group's offset), and that same evidence feeds the verdict
// and the naming/target render (`FrozenEpisode` / `FrozenMediaKind`), exactly
// like the automatic pipeline. There is no second parse, no legacy regex
// fallback and no re-read of the current instance policy.
//
// Files are complete evidence: every Release, filtered or not, reports the
// files of its exact torrent manifest with their own filter verdict, so the
// caller can show what was filtered and why. A seed whose torrent could not be
// acquired falls back to the single-file view of the release name.
func (s *Service) evaluateFeedItemsWithSelection(
	ctx context.Context,
	result *previewEvaluation,
	sourceIndex int,
	sourceKind string,
	groupName string,
	group providerdomain.GroupRef,
	selection acquisitiontarget.ParserSelectionInput,
	effective animedomain.EffectiveConfig,
	hold animedomain.MetadataHold,
	animeTitle string,
	anime namingvars.AnimeEvidence,
	items []acquisitiondomain.FeedItem,
	manifests map[string]*animecatalogdomain.TorrentManifest,
	business ...*animedomain.BusinessSnapshot,
) error {
	evalInput := animedomain.SourceEvaluationInput{Index: sourceIndex, Kind: sourceKind, GroupName: groupName, Group: group, Offset: selection.Offset}
	// The attr rules and the Vars program are pinned by digest and identical for
	// every release of this call, so they are loaded once, on the first release
	// (a source with no seeds never needed the business snapshot), not per item.
	var (
		loaded  bool
		rules   attrrules.Rules
		program namingvars.Program
	)
	return eachDistinctPreviewItem(items, func(item acquisitiondomain.FeedItem, stableKey string) error {
		if len(business) == 0 || business[0] == nil {
			// Preview evaluates only the draft's own complete business; the
			// server never substitutes current Global for a missing one.
			return &apperrors.Business{Status: 422, Code: "BUSINESS_SNAPSHOT_REQUIRED", Message: "Preview requires the draft's complete business snapshot; load the Global configuration into the draft first."}
		}
		if !loaded {
			var err error
			if rules, err = s.loadPreviewAttrRules(ctx, business[0].Programs.AttrRules); err != nil {
				return err
			}
			if program, err = s.loadPreviewVarsProgram(ctx, business[0].Programs.Vars); err != nil {
				return err
			}
			loaded = true
		}
		evidence, err := acquisitiontarget.ResolveParserEvidence(ctx, selection, item.Title)
		if err != nil {
			return parserSelectionUnavailableError(err.Error())
		}
		release := animedomain.EvaluateFeedItemWithIdentity(item, evalInput, effective, hold, rules, evidence.Identity)
		if err := previewBatchPlan(ctx, &release, item, manifests[stableKey], evidence, effective, business[0].UnclassifiedRoute); err != nil {
			return parserSelectionUnavailableError(err.Error())
		}
		// Feed items are not persisted releases: no release id, the item's
		// own tag sets and hashes.
		varsEvidence, err := s.previewVarsEvidence(ctx, anime, "", item, naming.VarsEpisode(evidence.NamingFacts()))
		if err != nil {
			return err
		}
		frozen := freezeVarsWithProgram(program, varsEvidence, manifests[stableKey])
		release.FrozenVars = &frozen
		result.Releases = append(result.Releases, release)
		files, err := previewFilesForItem(ctx, item, stableKey, release, manifests[stableKey], evalInput, effective, animeTitle, rules, evidence, business[0].UnclassifiedRoute)
		if err != nil {
			return parserSelectionUnavailableError(err.Error())
		}
		result.Files = append(result.Files, files...)
		return nil
	})
}

// previewFilesForItem projects one release's files from its exact torrent
// manifest: the manifest is the authority for which files exist, so a release
// whose manifest is absent or empty projects no file row at all. Nothing is
// ever derived from the feed's own filename: that would publish a target for a
// file list nobody read, contradicting the release-level verdict that already
// fails closed with torrent_manifest_missing.
func previewFilesForItem(
	ctx context.Context,
	item acquisitiondomain.FeedItem,
	stableKey string,
	release animedomain.PreviewReleaseResult,
	manifest *animecatalogdomain.TorrentManifest,
	evalInput animedomain.SourceEvaluationInput,
	effective animedomain.EffectiveConfig,
	animeTitle string,
	rules attrrules.Rules,
	evidence acquisitiontarget.ParserEvidence,
	unknownRoute string,
) ([]animedomain.PreviewFileResult, error) {
	if manifest == nil || len(manifest.Files) == 0 {
		return nil, nil
	}
	manifestFiles, err := previewManifestFiles(ctx, manifest, evidence)
	if err != nil {
		return nil, err
	}
	projected := animedomain.PreviewFilesForManifest(item, release, manifestFiles, effective, animeTitle, evalInput, rules, unknownRoute)
	results := make([]animedomain.PreviewFileResult, 0, len(projected))
	for _, file := range projected {
		results = append(results, animedomain.PreviewFileResult{
			SourceIndex:    evalInput.Index,
			StableItemKey:  stableKey,
			SourcePath:     file.SourcePath,
			Included:       file.Included,
			ReasonCode:     file.ReasonCode,
			RenderedTarget: file.RenderedTarget,
			RenderError:    file.RenderError,
			Safety:         file.Safety,
			BoundVideo:     file.BoundVideo,
			EmptyFacts:     file.EmptyFacts,
			SubLang:        file.Subtitle.Lang,
			SubLangTag:   file.Subtitle.LangTag,
			SubLangSource:  file.Subtitle.LangSource,
		})
	}
	return results, nil
}

// previewManifestFiles adapts the persisted projection into the evaluation
// package's manifest input without leaking the catalog domain into it. A TV
// video's episode is resolved from its own filename with the release's
// selected parser, exactly like the pre-download plan; a movie keeps its
// canonical identity. Subtitles of a TV and a movie bind by the same strict
// filename prefix.
func previewManifestFiles(ctx context.Context, manifest *animecatalogdomain.TorrentManifest, evidence acquisitiontarget.ParserEvidence) ([]animedomain.ManifestFileInput, error) {
	files := make([]animedomain.ManifestFileInput, 0, len(manifest.Files))
	type boundCandidate struct {
		path     string
		basename string
		episode  *naming.FrozenEpisodeNamingFacts
	}
	var candidates []boundCandidate
	for _, file := range manifest.Files {
		input := animedomain.ManifestFileInput{
			FileRef: file.FileRef, Ordinal: file.Ordinal, SourcePath: file.SourcePath,
			Filename: file.Filename, SizeBytes: file.SizeBytes, MediaKind: file.MediaKind, Language: file.Language,
		}
		if evidence.MediaKind != "movie" && file.MediaKind == "video" {
			name := file.Filename
			if name == "" {
				name = path.Base(file.SourcePath)
			}
			_, episode, err := acquisitiontarget.ResolveFileEpisode(ctx, evidence, name)
			if err != nil {
				return nil, err
			}
			input.FrozenEpisode, input.PerFileEpisode = episode, true
		}
		if kind, ignored := filekinds.Eligibility(filekinds.Version, file.SourcePath); ignored == "" && kind == filekinds.KindVideo {
			name := file.Filename
			if name == "" {
				name = path.Base(file.SourcePath)
			}
			candidates = append(candidates, boundCandidate{path: file.SourcePath, basename: strings.TrimSuffix(name, path.Ext(name)), episode: input.FrozenEpisode})
		}
		files = append(files, input)
	}
	// A subtitle follows its video by strict filename prefix (manifest path
	// order breaks ties) and renders that video's own filename episode.
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })
	basenames := make([]string, len(candidates))
	for index, candidate := range candidates {
		basenames[index] = candidate.basename
	}
	for index := range files {
		if files[index].MediaKind != "subtitle" {
			continue
		}
		name := files[index].Filename
		if name == "" {
			name = path.Base(files[index].SourcePath)
		}
		// A TV subtitle renders its bound video's own episode; a movie
		// subtitle keeps the release's movie identity like its video.
		files[index].PerFileEpisode = evidence.MediaKind != "movie"
		if bound := filekinds.BindSubtitle(name, basenames); bound >= 0 {
			files[index].BoundVideo = candidates[bound].path
			files[index].FrozenEpisode = candidates[bound].episode
		}
	}
	return files, nil
}

// previewBatchPlan applies the admission's acceptance rule to an accepted
// preview release: the release title is evidence only, so a release with no
// placeable file is shown filtered (no_placeable_files) and a batch shows its
// planned episodes. A seed whose torrent fetch failed shows the automatic
// pipeline's parse failure with the fetch's own code; a seed whose torrent was
// never fetched keeps the title verdict, since the preview cannot plan it. The
// preview never reads the library, so it applies no deduplication.
func previewBatchPlan(ctx context.Context, release *animedomain.PreviewReleaseResult, item acquisitiondomain.FeedItem, manifest *animecatalogdomain.TorrentManifest, evidence acquisitiontarget.ParserEvidence, effective animedomain.EffectiveConfig, route string) error {
	if release.Verdict != animedomain.ReleaseVerdict("accepted_pending_download") {
		return nil
	}
	if code := strings.TrimSpace(item.TorrentErrorCode); code != "" {
		release.Verdict = animedomain.ReleaseVerdict("parse_failed")
		release.ReasonCode = code
		release.ReasonDetail = nil
		return nil
	}
	if manifest == nil || len(manifest.Files) == 0 {
		return nil
	}
	if strings.TrimSpace(route) == "" {
		route = batchplan.RouteSkip
	}
	files := make([]acquisitiondomain.ReleaseFile, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		size := file.SizeBytes
		files = append(files, acquisitiondomain.ReleaseFile{SourcePath: file.SourcePath, Filename: file.Filename, SizeBytes: &size, MediaKind: file.MediaKind})
	}
	plan, err := acquisitiontarget.BuildBatchPlan(ctx, acquisitiontarget.BatchPlanInput{Evidence: evidence, Files: files, FileFilter: effective.Rules.FileFilter, UnclassifiedRoute: route, DownloadCompletion: string(effective.DownloadCompletion.Rules.Action), DownloadCompletionSeedLimits: effective.DownloadCompletion.Rules.SeedLimits})
	if err != nil {
		return err
	}
	if !plan.Accepted() {
		release.Verdict = animedomain.ReleaseVerdictFiltered
		release.ReasonCode = batchplan.ReasonNoPlaceableFiles
		release.ReasonDetail = nil
		return nil
	}
	animedomain.ApplyPlanCoverage(release, &plan)
	return nil
}
