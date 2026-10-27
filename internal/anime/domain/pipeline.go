package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/filekinds"
	"bansoko/internal/organizingpolicy"
	"bansoko/internal/postprocessing/naming"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/templatefacts"
)

// SourceEvaluationInput carries the per-source draft facts the pure pipeline
// needs. Preview and execution share these semantics.
type SourceEvaluationInput struct {
	Index     int
	Kind      string
	GroupName string
	// Group is the provider-namespaced external identity of the RSS source
	// (Mikan's numeric group id / AniBT's group slug). The zero value means
	// the source records none: a custom RSS source, or a provider_group
	// source without provenance. It is read from the same frozen evidence as
	// GroupName, never looked up separately.
	Group providerdomain.GroupRef
	// Offset is the episode offset of this RSS link (0 when none is set).
	Offset int
}

// StableItemKey derives the dedupe identity for one feed item: a stable GUID,
// enclosure/link identity, or a versioned content hash, in that order.
func StableItemKey(item acquisitiondomain.FeedItem) (kind string, value string) {
	if guid := strings.TrimSpace(item.GUID); guid != "" {
		return "guid", guid
	}
	if enclosure := strings.TrimSpace(item.Enclosure); enclosure != "" {
		return "enclosure", enclosure
	}
	if link := strings.TrimSpace(item.Link); link != "" {
		return "link", link
	}
	digest := sha256.Sum256([]byte(item.Title + "\x00" + item.Published.UTC().String() + "\x00" + item.DownloadURL()))
	return "content_hash", "sha256:" + hex.EncodeToString(digest[:])
}

// PublicStableItemKey is the stored and shown form of StableItemKey: a GUID
// is kept, but a URL identity (a guid that is a URL, or the enclosure or link
// fallback) is replaced by its SHA-256, because a private tracker's download
// URL carries the user's key. RSS refresh and one-shot admission both store
// this form, so a release never keeps a credential in its identity key.
func PublicStableItemKey(item acquisitiondomain.FeedItem) (kind, value string) {
	kind, value = StableItemKey(item)
	if isURLIdentity(value) {
		digest := sha256.Sum256([]byte(value))
		if kind == "guid" {
			kind = "content_hash"
		}
		return kind, "sha256:" + hex.EncodeToString(digest[:])
	}
	return kind, value
}

func isURLIdentity(value string) bool {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(trimmed), "magnet:") {
		return true
	}
	parsed, err := url.Parse(trimmed)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != ""
}

// EvaluateFeedItemWithIdentity runs the versioned evaluator over one feed item
// using the frozen release episode identity that the one selected
// parser/profile already resolved. It is the automatic decision path used by
// RSS refresh, Release evaluation and one-shot admission; it never parses the
// title itself, so a second regex implementation can never decide an automatic
// identity. It performs no I/O and no persistence.
func EvaluateFeedItemWithIdentity(item acquisitiondomain.FeedItem, source SourceEvaluationInput, effective EffectiveConfig, hold MetadataHold, rules attrrules.Rules, identity episodeidentity.EpisodeIdentity) PreviewReleaseResult {
	_, stableKey := StableItemKey(item)
	result := PreviewReleaseResult{
		SourceIndex:   source.Index,
		StableItemKey: stableKey,
		RawFacts: RawReleaseFacts{
			Title: item.Title,
		},
	}
	if !item.Published.IsZero() {
		published := item.Published.UTC()
		result.RawFacts.PublishedAt = &published
	}
	if item.EnclosureType != "" {
		result.RawFacts.EnclosureType = &item.EnclosureType
	}
	if item.EnclosureLength > 0 {
		length := item.EnclosureLength
		result.RawFacts.EnclosureLength = &length
	}

	facts := acquisitiondomain.ParseReleaseFacts(item.Title, rules)
	parsed := ParsedReleaseFacts{}
	if facts.GroupName != "" {
		parsed.GroupName = &facts.GroupName
	} else if source.GroupName != "" {
		parsed.GroupName = &source.GroupName
	}
	if facts.Quality != "" {
		parsed.Quality = &facts.Quality
	}
	if facts.Language != "" {
		parsed.Languages = &[]string{facts.Language}
	}
	// Persist the special-flag verdict so the episode progress aggregation
	// can exclude NCOP/NCED/Special releases without re-parsing the title.
	special := facts.Special
	parsed.Special = &special

	// The release identity is title evidence only: a batch title
	// (range, list, season pack) is not a failure, because acceptance is the
	// batch plan's decision over the files' own identities. A resolved title
	// identity still fills the display episode and the release-level naming
	// episode; a non-resolved one leaves both empty instead of guessing.
	if episode := integerEpisode(identity); episode != nil {
		parsed.Episode = episode
	}
	result.FrozenEpisode = frozenEpisodeNamingFacts(identity)
	if identity.Kind == episodeidentity.KindMovie {
		// The canonical movie identity selects the movie naming branch for the
		// pre-download target render; it is derived from the frozen identity,
		// never from a title heuristic.
		result.FrozenMediaKind = string(TMDBMediaKindMovie)
	}
	result.ParsedFacts = &parsed

	if code, detail := FeedFilterReason(item, effective); code != "" {
		result.Verdict = ReleaseVerdictFiltered
		result.ReasonCode = code
		result.ReasonDetail = &detail
		return result
	}
	if hold.Held {
		result.Verdict = ReleaseVerdict("waiting_metadata")
		result.ReasonCode = "metadata_incomplete"
		missing := make([]string, 0, len(hold.Missing))
		for _, value := range hold.Missing {
			missing = append(missing, string(value))
		}
		detail := "missing: " + strings.Join(missing, ", ")
		result.ReasonDetail = &detail
		return result
	}
	result.Verdict = ReleaseVerdict("accepted_pending_download")
	result.ReasonCode = "accepted"
	return result
}

// frozenEpisodeNamingFacts projects a resolved identity into the naming
// episode facts; any other identity carries no episode to name with.
func frozenEpisodeNamingFacts(identity episodeidentity.EpisodeIdentity) *naming.FrozenEpisodeNamingFacts {
	if identity.State != episodeidentity.StateResolved || identity.Episode == nil {
		return nil
	}
	raw := string(*identity.Episode)
	if identity.RawEpisode != nil {
		raw = string(*identity.RawEpisode)
	}
	facts := &naming.FrozenEpisodeNamingFacts{Raw: raw, Number: string(*identity.Episode), Offset: identity.Offset, Display: string(*identity.Episode)}
	// ep.source reports which evidence owner supplied the episode: the frozen
	// identity's selected source (file_parser, release_parser, anibt or
	// movie). A file-manager match overwrites it with "manual" at its own
	// construction site; nothing here invents a class.
	if identity.SelectedSource != nil {
		facts.Source = string(*identity.SelectedSource)
	}
	if identity.EpisodeEnd != nil {
		facts.NumberEnd = string(*identity.EpisodeEnd)
	}
	return facts
}

// ReasonFilteredByRules is the reason of a release the keyword release
// filter rejected; its detail names the failing rule.
const ReasonFilteredByRules = "filtered_by_rules"

// FeedFilterReason is the feed-layer filter of one release: the AniBT tag row
// (AniBT releases only), then the title-parse tag row (every release), then
// the keyword release filter. All must pass and the first failing one is the
// returned reason; an empty code means the release passes this layer.
//
// It reads only facts the feed item itself carries (its title, its AniBT tag
// set and its title tags), never a torrent fact, so its answer is final before
// any .torrent is fetched: a refresh may skip the fetch of a release it
// rejects, and an unexpanded RSS list row may show the rejection. A
// filtered verdict from EvaluateFeedItemWithIdentity is exactly this decision.
func FeedFilterReason(item acquisitiondomain.FeedItem, effective EffectiveConfig) (code, detail string) {
	if code, detail := AniBTTagFilterReason(item, effective.TagFilters.Rules.Anibt); code != "" {
		return code, detail
	}
	if code, detail := TitleTagFilterReason(item, effective.TagFilters.Rules.Title); code != "" {
		return code, detail
	}
	if reason := filterReason(item.Title, effective.Rules.ReleaseFilter); reason != "" {
		return ReasonFilteredByRules, reason
	}
	return "", ""
}

// filterReason uses the exact release title, independently of parsed facts.
func filterReason(title string, rules ReleaseFilterRules) string {
	compiled, err := CompileRegexFilter(rules)
	if err != nil {
		return "release_filter_invalid"
	}
	decision := compiled.MatchReleaseTitle(title)
	if !decision.Allowed {
		return "release_" + decision.Reason
	}
	return ""
}

// previewFileOutcome is the naming/file-effect decision for one filename. It
// is the shared core of the single-file and per-manifest-file renderers so both
// produce byte-identical targets from identical inputs.
type previewFileOutcome struct {
	Included       bool
	ReasonCode     *string
	RenderedTarget *string
	RenderError    *string
	Safety         PreviewFileResultSafety
	// SubtitleSuffix is the subtitle suffix the naming rules rendered for this
	// file, set only for a subtitle that reached the naming engine and only
	// when the rules render one. A bound subtitle's own target is composed
	// from it (companionSidecarTarget), so the preview shows the filename the
	// placement stage freezes.
	SubtitleSuffix string
	// EmptyFacts are the naming facts that had no value and rendered empty in
	// this file's target (sorted); SuffixEmptyFacts those of the subtitle
	// suffix alone, which a companion target adds to its video's.
	EmptyFacts       []string
	SuffixEmptyFacts []string
	// Subtitle is a subtitle's resolved language (sub.*), read-only evidence
	// for the reader; zero for a video.
	Subtitle templatefacts.SubFacts
}

// PreviewExactFile consumes real inventory facts rather than video/zero
// placeholders. It is the only entry point for one manifest file: a release
// with no manifest has no file to render at all, so there is deliberately no
// single-file view derived from the feed's own filename.
func PreviewExactFile(item acquisitiondomain.FeedItem, release PreviewReleaseResult, file ManifestFileInput, effective EffectiveConfig, animeTitle string, source SourceEvaluationInput, rules attrrules.Rules) ManifestFileResult {
	outcome := renderManifestFile(item, release, effective, animeTitle, source, rules, path.Base(file.SourcePath), file, "", false)
	return ManifestFileResult{FileRef: file.FileRef, Ordinal: file.Ordinal, SourcePath: file.SourcePath, Filename: file.Filename, SizeBytes: file.SizeBytes, MediaKind: file.MediaKind, Included: outcome.Included, ReasonCode: outcome.ReasonCode, RenderedTarget: outcome.RenderedTarget, RenderError: outcome.RenderError, Safety: outcome.Safety, BoundVideo: file.BoundVideo, EmptyFacts: outcome.EmptyFacts, Subtitle: outcome.Subtitle}
}

// companionSidecarTarget is the one landing rule for a subtitle that follows a
// video: the subtitle lands in the video's directory, under the video's
// basename, keeping its own extension. The video's own extension is replaced,
// never doubled. The name itself is composed by the same rule the placement
// stage freezes (naming.CompanionSidecarFilename, which applies the rendered
// suffix), so preview and frozen target agree for the same inputs.
func companionSidecarTarget(videoTarget *string, suffix, subtitleFilename string) (string, bool) {
	if videoTarget == nil || strings.TrimSpace(*videoTarget) == "" {
		return "", false
	}
	video := strings.ReplaceAll(*videoTarget, `\`, "/")
	// The extension is lowercased exactly like the artifact render
	// (fileoperation's buildNamingContext), so casing cannot drift.
	extension := strings.TrimPrefix(strings.ToLower(path.Ext(strings.ReplaceAll(subtitleFilename, `\`, "/"))), ".")
	if extension == "" {
		return "", false
	}
	directory, basename := path.Split(strings.TrimSuffix(video, path.Ext(video)))
	return directory + naming.CompanionSidecarFilename(basename, suffix, extension), true
}

// ManifestFileInput is one file of the exact torrent manifest an evaluation
// renders. It mirrors the persisted projection without making this package
// depend on the catalog domain.
type ManifestFileInput struct {
	FileRef    string
	Ordinal    int
	SourcePath string
	Filename   string
	SizeBytes  int64
	MediaKind  string
	Language   string
	// FrozenEpisode is the episode resolved from this file's own filename
	// alone. With PerFileEpisode set it is the only episode authority for the
	// render (nil means the filename carries no episode, so the file is an
	// unknown file); the release title never stands in for it.
	FrozenEpisode  *naming.FrozenEpisodeNamingFacts
	PerFileEpisode bool
	// BoundVideo is the SourcePath of the video a subtitle follows by strict
	// filename prefix ("" when it matches no video). A subtitle renders its
	// bound video's episode, never the release title's.
	BoundVideo string
	// Unknown marks a movie video that is not the main video: an unknown
	// file without an episode identity (set by PreviewFilesForManifest).
	Unknown bool
}

// Unknown-file routes of PreviewFilesForManifest: the instance (or draft)
// unclassified_route. An empty route is the pre-download plan, which never
// gives an unknown file a target.
const (
	UnknownRouteRoute = "route"
	UnknownRouteSkip  = "skip"
)

// ManifestFileResult is one rendered file decision of a manifest evaluation.
// A file stays in the result even when it is excluded or unrenderable, so the
// read model can show what was filtered and why instead of silently dropping it.
type ManifestFileResult struct {
	FileRef        string
	Ordinal        int
	SourcePath     string
	Filename       string
	SizeBytes      int64
	MediaKind      string
	Language       string
	Included       bool
	ReasonCode     *string
	RenderedTarget *string
	RenderError    *string
	Safety         PreviewFileResultSafety
	// BoundVideo is the SourcePath of the video a bound subtitle follows by the
	// product's strict-prefix binding ("" for a video or an unbound subtitle).
	// It travels with the result so a reader lists the subtitle under that
	// video instead of applying the binding rule again.
	BoundVideo string
	// EmptyFacts are the naming facts that had no value and rendered empty in
	// RenderedTarget (sorted catalog paths).
	EmptyFacts []string
	// Subtitle is a subtitle's resolved language (sub.lang, sub.lang_tag,
	// sub.lang_source); zero for a video or when it has none.
	Subtitle templatefacts.SubFacts
}

// PreviewFilesForManifest renders every file of one release's exact torrent
// manifest through the same naming engine, file filter and path-safety rules
// the single-file preview uses. The manifest is the authority for which files
// exist: a multi-file torrent yields one result per projected file instead of a
// single name derived from the torrent's folder name.
//
// An unknown video (a TV video whose filename carries no episode, or a movie
// video that is not the main video) follows unknownRoute like the library
// plan: route renders the unknown template, skip excludes it as
// unknown_skipped. Its prefix-bound subtitles follow it.
func PreviewFilesForManifest(
	item acquisitiondomain.FeedItem,
	release PreviewReleaseResult,
	files []ManifestFileInput,
	effective EffectiveConfig,
	animeTitle string,
	source SourceEvaluationInput,
	rules attrrules.Rules,
	unknownRoute string,
) []ManifestFileResult {
	if len(files) == 0 {
		return []ManifestFileResult{}
	}
	ordered := make([]ManifestFileInput, len(files))
	copy(ordered, files)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Ordinal < ordered[j].Ordinal })
	movie := release.FrozenMediaKind == string(TMDBMediaKindMovie)
	if movie {
		markMovieUnknownVideos(ordered, effective.Rules.FileFilter)
	}
	results := make([]ManifestFileResult, len(ordered))
	bySource := map[string]int{}
	unknownBySource := map[string]bool{}
	build := func(index int, file ManifestFileInput, outcome previewFileOutcome) {
		filename := strings.TrimSpace(file.Filename)
		if filename == "" {
			filename = path.Base(file.SourcePath)
		}
		results[index] = ManifestFileResult{
			FileRef: file.FileRef, Ordinal: file.Ordinal, SourcePath: file.SourcePath,
			Filename: filename, SizeBytes: file.SizeBytes, MediaKind: file.MediaKind, Language: file.Language,
			Included: outcome.Included, ReasonCode: outcome.ReasonCode,
			RenderedTarget: outcome.RenderedTarget, RenderError: outcome.RenderError, Safety: outcome.Safety,
			BoundVideo: file.BoundVideo, EmptyFacts: outcome.EmptyFacts, Subtitle: outcome.Subtitle,
		}
	}
	// Videos first: a subtitle follows the decision made for its bound video.
	for pass := 0; pass < 2; pass++ {
		for index, file := range ordered {
			if (file.MediaKind == "subtitle") != (pass == 1) {
				continue
			}
			filename := strings.TrimSpace(file.Filename)
			if filename == "" {
				filename = path.Base(file.SourcePath)
			}
			// Eligibility and file rules decide before anything is rendered: an
			// ignored or rule-excluded file is shown read-only with its typed
			// reason and never reaches the naming engine. A subtitle the hard
			// gates dropped is not a candidate at all, so its verdict is kept
			// and the companion rule below must not resurrect it.
			exclusion := ManifestFileExclusion(filekinds.Version, effective.Rules.FileFilter, file.SourcePath)
			var outcome previewFileOutcome
			if exclusion != "" {
				outcome = previewFileOutcome{ReasonCode: &exclusion, Safety: PreviewFileResultSafety("valid")}
			} else if pass == 1 && (file.PerFileEpisode || movie) && file.BoundVideo == "" {
				reason := "subtitle_binding_missing"
				outcome = previewFileOutcome{ReasonCode: &reason, Safety: PreviewFileResultSafety("valid")}
			} else if bound, ok := bySource[file.BoundVideo]; pass == 1 && ok && !results[bound].Included {
				reason := "subtitle_video_excluded"
				outcome = previewFileOutcome{ReasonCode: &reason, Safety: PreviewFileResultSafety("valid")}
			} else if pass == 1 && unknownBySource[file.BoundVideo] {
				// A subtitle of a routed unknown video is an unknown file too.
				outcome = renderPreviewFileForName(item, release, effective, animeTitle, source, rules, filename, file.MediaKind, file.SizeBytes, nil, true, true)
			} else {
				outcome = renderManifestFile(item, release, effective, animeTitle, source, rules, filename, file, unknownRoute, true)
				unknownBySource[file.SourcePath] = pass == 0 && unknownRoute == UnknownRouteRoute && unknownVideo(file)
			}
			// A subtitle follows its bound video: it lands beside that video's
			// own target under the video's basename, with the suffix the rules
			// rendered for the subtitle and its own extension (MF design 4a).
			// The subtitle naming template is never rendered, so the expected
			// target here is the one the placement stage freezes.
			//
			// Only a subtitle that is still a candidate follows: one the hard
			// gates dropped (ignored_nested, ignored_kind) keeps that verdict,
			// which is exactly the rule the plan's Finalize applies when it
			// skips a subtitle that is not first-level or was ignored by kind.
			//
			// A subtitle whose own render failed has no suffix to compose from:
			// it keeps that failure instead of publishing a target it cannot
			// compute (the suffix render is part of the same render).
			if pass == 1 && file.MediaKind == "subtitle" && exclusion == "" && outcome.RenderError == nil {
				if bound, ok := bySource[file.BoundVideo]; ok && results[bound].Included {
					if companion, ok := companionSidecarTarget(results[bound].RenderedTarget, outcome.SubtitleSuffix, filename); ok {
						outcome = previewFileOutcome{Included: true, RenderedTarget: &companion, Safety: PreviewFileResultSafety("valid"), EmptyFacts: naming.MergeFacts(results[bound].EmptyFacts, outcome.SuffixEmptyFacts), Subtitle: outcome.Subtitle}
					}
				}
			}
			build(index, file, outcome)
			bySource[file.SourcePath] = index
		}
		markDuplicateTargets(results, ordered, pass == 1)
	}
	return results
}

// markDuplicateTargets applies the target plan's duplicate rule to one pass of
// a manifest preview, so the preview shows what admission and placement will
// do. Videos of one episode (same frozen episode) that render one target keep
// the first by source path, and so do subtitles bound to one video that
// render one target (settled by order, like the placement stage); any other
// group of files rendering one target — different episodes, unknown videos,
// or subtitles of different videos — is dropped as a whole
// (target_duplicate). Running after the video pass lets a dropped video's
// subtitles follow it as subtitle_video_excluded.
func markDuplicateTargets(results []ManifestFileResult, files []ManifestFileInput, subtitles bool) {
	groups := map[string][]int{}
	for index, result := range results {
		if (result.MediaKind == "subtitle") != subtitles || !result.Included || result.RenderedTarget == nil {
			continue
		}
		groups[*result.RenderedTarget] = append(groups[*result.RenderedTarget], index)
	}
	reason := "target_duplicate"
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		sort.SliceStable(members, func(i, j int) bool { return results[members[i]].SourcePath < results[members[j]].SourcePath })
		drop := members
		if (!subtitles && sameFrozenEpisode(files, members)) || (subtitles && sameBoundVideo(results, members)) {
			drop = members[1:]
		}
		for _, index := range drop {
			results[index].Included = false
			results[index].ReasonCode = &reason
		}
	}
}

// sameBoundVideo reports whether every subtitle is bound to one video.
func sameBoundVideo(results []ManifestFileResult, members []int) bool {
	video := results[members[0]].BoundVideo
	if video == "" {
		return false
	}
	for _, index := range members[1:] {
		if results[index].BoundVideo != video {
			return false
		}
	}
	return true
}

// sameFrozenEpisode reports whether every file is a per-file episode with the
// same frozen episode (a v2 or a second encode of one episode).
func sameFrozenEpisode(files []ManifestFileInput, members []int) bool {
	first := files[members[0]]
	if !first.PerFileEpisode || first.FrozenEpisode == nil {
		return false
	}
	for _, index := range members[1:] {
		other := files[index]
		if !other.PerFileEpisode || other.FrozenEpisode == nil || other.FrozenEpisode.Number != first.FrozenEpisode.Number || other.FrozenEpisode.NumberEnd != first.FrozenEpisode.NumberEnd {
			return false
		}
	}
	return true
}

// markMovieUnknownVideos applies the movie rule of the library plan: among the
// first-level videos the file rules keep, the largest known size (ties and
// unknown sizes by manifest order) is the main video; every other one is an
// unknown file.
func markMovieUnknownVideos(files []ManifestFileInput, filter FileFilterRules) {
	candidates := []organizingpolicy.Video{}
	for index, file := range files {
		if file.MediaKind != "video" || ManifestFileExclusion(filekinds.Version, filter, file.SourcePath) != "" {
			continue
		}
		size := uint64(max(file.SizeBytes, 0))
		candidates = append(candidates, organizingpolicy.Video{Ref: organizingpolicy.FileRef(file.SourcePath), Order: uint32(index), Size: &size, Eligible: true})
	}
	main := organizingpolicy.SelectMovie(candidates).Main
	for index := range files {
		if files[index].MediaKind == "video" && organizingpolicy.FileRef(files[index].SourcePath) != main {
			files[index].Unknown = true
		}
	}
}

// unknownVideo reports a video without an episode identity of its own: a
// movie's non-main video, or a TV video whose filename carries no episode.
func unknownVideo(file ManifestFileInput) bool {
	if file.MediaKind != "video" {
		return false
	}
	return file.Unknown || file.PerFileEpisode && file.FrozenEpisode == nil
}

// renderManifestFile renders one manifest file. An unknown video never borrows
// an episode from the release title: under the route it renders the unknown
// template, under skip it is excluded as unknown_skipped, and the
// pre-download plan (empty route) gives it no target with the reason unknown.
//
// preview marks a render shown to the user before the download (the RSS,
// draft and one-shot previews): a fact read from the downloaded file
// (media.resolution, media.video_codec, media.audio_codec, media.container)
// renders as its placeholder ⟨path⟩ and never counts as empty. The
// acquisition target set renders without placeholders (PreviewExactFile).
func renderManifestFile(item acquisitiondomain.FeedItem, release PreviewReleaseResult, effective EffectiveConfig, animeTitle string, source SourceEvaluationInput, rules attrrules.Rules, filename string, file ManifestFileInput, unknownRoute string, preview bool) previewFileOutcome {
	if unknownVideo(file) {
		switch unknownRoute {
		case UnknownRouteRoute:
			return renderPreviewFileForName(item, release, effective, animeTitle, source, rules, filename, file.MediaKind, file.SizeBytes, nil, true, preview)
		case UnknownRouteSkip:
			reason := "unknown_skipped"
			return previewFileOutcome{ReasonCode: &reason, Safety: PreviewFileResultSafety("valid")}
		default:
			reason := "unknown"
			return previewFileOutcome{ReasonCode: &reason, Safety: PreviewFileResultSafety("valid")}
		}
	}
	episode := release.FrozenEpisode
	if file.PerFileEpisode {
		episode = file.FrozenEpisode
	}
	return renderPreviewFileForName(item, release, effective, animeTitle, source, rules, filename, file.MediaKind, file.SizeBytes, episode, false, preview)
}

// renderPreviewFileForName is the one file-effect decision shared by the
// single-file preview and the per-manifest-file renderer. Parsed/Derived
// variables come from the resolver registry with the same link offset as the
// release verdict. When the caller supplies a frozen Release episode identity
// the naming consumes it instead of re-parsing the title, so the pre-download
// target equals the eventual artifact render.
func renderPreviewFileForName(item acquisitiondomain.FeedItem, release PreviewReleaseResult, effective EffectiveConfig, animeTitle string, source SourceEvaluationInput, rules attrrules.Rules, filename, mediaKind string, sizeBytes int64, episode *naming.FrozenEpisodeNamingFacts, unknown, preview bool) previewFileOutcome {
	outcome := previewFileOutcome{Included: true, Safety: PreviewFileResultSafety("valid")}
	extension := strings.TrimPrefix(strings.ToLower(path.Ext(filename)), ".")

	ctx := naming.NamingContext{
		Anime:   naming.AnimeNamingFacts{Title: animeTitle},
		Release: naming.ReleaseNamingFacts{OriginalTitle: item.Title, FrozenEpisode: episode},
		File: naming.FileNamingFacts{
			OriginalName: filename,
			Basename:     strings.TrimSuffix(filename, path.Ext(filename)),
			Extension:    extension,
			SizeBytes:    sizeBytes,
			Kind:         mediaKind,
		},
		Source: naming.SourceNamingFacts{Kind: source.Kind, GroupName: source.GroupName, Group: source.Group},
	}
	// The value namespaces come only from the item's frozen tag sets (a Release
	// read back from storage, or an item frozen when it was built); a set the
	// item does not carry stays missing instead of being re-parsed here.
	ctx = naming.WithTemplateFacts(ctx, templatefacts.FromTagSets(item.TitleTags, item.AniBTTags))
	// The frozen movie identity (canonical movie:0) selects the movie naming
	// branch exactly like the artifact render does; an empty value keeps the
	// TV default. It is derived from the one frozen identity, never parsed
	// out of the title.
	ctx.Anime.TMDB.MediaKind = release.FrozenMediaKind
	var varsErr error
	if release.FrozenVars != nil {
		if release.FrozenVars.Status != "ok" {
			varsErr = fmt.Errorf("frozen Vars evaluation failed: %s", release.FrozenVars.Diagnostic)
		} else {
			ctx, varsErr = naming.WithVars(ctx, release.FrozenVars.Output)
		}
		// The binding and torrent facts come from the same frozen release
		// evidence the Vars input was built from.
		ctx = naming.WithFrozenReleaseFacts(ctx, release.FrozenVars.Input.Facts)
	} else {
		varsErr = fmt.Errorf("exact frozen Vars evaluation is required")
	}
	if varsErr != nil {
		outcome.Safety = PreviewFileResultSafety("invalid")
		reason := "naming_vars_unavailable"
		outcome.ReasonCode = &reason
		outcome.RenderError = boundedRenderError(varsErr)
		return outcome
	}
	// The pre-download target planner has no inspected artifact facts yet.
	// Mirror the file-operation planner's release-quality fallback so the
	// frozen TargetSet path remains equal to the eventual artifact render when
	// the torrent filename itself omits a resolution marker.
	if parsed := acquisitiondomain.ParseReleaseFacts(item.Title, rules); parsed.Resolution != "" {
		ctx.File.Media.Resolution = parsed.Resolution
	}
	// The frozen typed identity is the only episode authority; a render
	// without one has a null parsed.episode.
	resolved := naming.DefaultRegistry().ResolveAll(naming.ResolverInput{Facts: ctx})
	if mediaKind == "subtitle" {
		outcome.Subtitle = naming.SubtitleLanguageFacts(resolved.Context)
	}
	engine := naming.NewEngine()
	if preview {
		engine = engine.Preview()
	}
	compiled, err := engine.CompileRulesWithVars(NamingRulesToDomain(effective.Rules.Naming), ctx.Vars)
	if err != nil {
		outcome.Safety = PreviewFileResultSafety("invalid")
		reason := "naming_template_invalid"
		outcome.ReasonCode = &reason
		outcome.RenderError = boundedRenderError(err)
		return outcome
	}
	render := compiled.RenderVideoDetailed
	if unknown {
		render = compiled.RenderUnknownDetailed
	}
	video, err := render(engine, resolved.Context)
	directory, renderedFilename := video.Directory, video.Filename
	outcome.EmptyFacts = video.EmptyFacts
	if err == nil && mediaKind == "subtitle" {
		// A subtitle's own target is not this render: it follows its bound
		// video (companionSidecarTarget), whose name carries the suffix the
		// rules render for the subtitle. Only a subtitle renders a suffix, so
		// a video's target never picks one up.
		var suffix naming.Rendered
		suffix, err = compiled.RenderSubtitleSuffixDetailed(engine, resolved.Context)
		outcome.SubtitleSuffix, outcome.SuffixEmptyFacts = suffix.Value, suffix.EmptyFacts
	}
	if err != nil {
		// A refusal names the facts that rendered empty (naming.Error.Missing);
		// any other failure has none to list.
		outcome.EmptyFacts = nil
		var missingErr *naming.Error
		if errors.As(err, &missingErr) && naming.IsFactError(missingErr.Code) {
			outcome.EmptyFacts = append([]string(nil), missingErr.Missing...)
		}
		safety := PreviewFileResultSafety("invalid")
		var namingErr *naming.Error
		if errors.As(err, &namingErr) && namingErr.Code == naming.ErrCodeNamingTargetInvalid {
			safety = PreviewFileResultSafety("unsafe")
		}
		outcome.Safety = safety
		if outcome.ReasonCode == nil {
			reason := "naming_render_invalid"
			if errors.As(err, &namingErr) {
				reason = namingErr.Code
				if naming.IsFactError(namingErr.Code) {
					// Names the missing fact for the preview and the target
					// diagnostic (naming_fact_missing_<fact>).
					reason = namingErr.ReasonCode()
				}
			}
			outcome.ReasonCode = &reason
		}
		outcome.RenderError = boundedRenderError(err)
		return outcome
	}
	if renderedFilename != "" {
		target := renderedFilename
		if directory != "" {
			target = directory + "/" + renderedFilename
		}
		outcome.RenderedTarget = &target
	}
	return outcome
}

// ManifestFileExclusion is the single product decision for one manifest file
// before any naming: it returns "" when the file is eligible and selected, or
// the typed read-only reason. A file below the first level is ignored_nested,
// a file that is neither video nor subtitle under the frozen kinds version is
// ignored_kind, and a first-level video the user's file rules reject carries
// the file_* rule reason. File rules match the filename of a video only;
// subtitles are not subject to them.
func ManifestFileExclusion(fileKindsVersion string, rules FileFilterRules, sourcePath string) string {
	kind, ignored := filekinds.Eligibility(fileKindsVersion, sourcePath)
	if ignored != "" {
		return ignored
	}
	if kind != filekinds.KindVideo {
		return ""
	}
	compiled, err := CompileRegexFilter(rules)
	if err != nil {
		return "file_filter_invalid"
	}
	if decision := compiled.MatchFilename(sourcePath); !decision.Allowed {
		return "file_" + decision.Reason
	}
	return ""
}

// boundedRenderError truncates a render failure message to the contract bound
// (512 chars, see PreviewFileResult.render_error in the OpenAPI schema).
func boundedRenderError(err error) *string {
	if err == nil {
		return nil
	}
	message := err.Error()
	const maxRunes = 512
	runes := []rune(message)
	if len(runes) > maxRunes {
		message = string(runes[:maxRunes-1]) + "…"
	}
	return &message
}
