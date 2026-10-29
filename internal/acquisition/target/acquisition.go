// Target acquisition admission: the one implementation that renders each
// included torrent entry into a canonical target, freezes the TargetSet, probes
// the filesystem, and decides adoption/download/failure before any qB work is
// admitted. Ordinary RSS evaluation and finite one-shot admission both call it,
// so the two paths cannot drift into different naming, collision, root or
// adoption rules.
package target

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/batchplan"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/filekinds"
	"bansoko/internal/library/domain"
	"bansoko/internal/parser"
	"bansoko/internal/postprocessing/naming"
	providerdomain "bansoko/internal/provider/domain"
)

// ErrTargetNotAbsolute reports a rendered target that is not an absolute path.
// Every naming template carries its own absolute deployment path, so a relative
// render is a template fault, not a missing deployment anchor.
var ErrTargetNotAbsolute = errors.New("target is not absolute")

// ParserEvidence is the frozen parser provenance of one release-title parse: the
// single explicit selection (instance parser-policy kind plus effective episode
// patterns) and the parse that selection produced. It is the one source for the
// naming plan, the target config snapshot, and the frozen downstream policy, so
// no consumer re-parses the title with a second implementation. MediaKind is
// the owner media kind the identity resolved with (movie short-circuits to the
// canonical movie:0 identity); Season and SeasonBound are the TMDB season
// frozen for naming only, never part of the identity.
type ParserEvidence struct {
	Selection parser.Selection
	Parsed    parser.EpisodeParseResult
	Identity  episodeidentity.EpisodeIdentity
	// MediaKind is the frozen owner media kind ("tv" or "movie"); empty means
	// tv. Season is the TMDB season and SeasonBound marks a preserved season 0,
	// both frozen for naming only.
	MediaKind   string
	Season      int
	SeasonBound bool
	// SourceVersion is the RSS link CAS version the offset was read from.
	SourceVersion int64
	// PolicyVersion is the instance parser-policy owner CAS version and
	// ProfileVersion is the resolved effective parser-section version. Both are
	// frozen provenance evidence, never authority.
	PolicyVersion  int64
	ProfileVersion int64
	// RulesOwnerVersion is the CAS version of the anime rules config owner the
	// profile was resolved from.
	RulesOwnerVersion int64
}

// CandidateEvidence is what a Release evaluation already resolved for its
// replacement admission: the release title (quality dimensions only), the
// coverage keys of the batch plan's planned videos after deduplication, and the
// release-level byte size. Replacement admission projects the candidate facts
// it reads from exactly this evidence, so a pre-acquisition decision is frozen
// over the same coverage the plan accepted instead of depending on a later
// candidate-facts or media-fact snapshot write. No key is unresolved coverage.
type CandidateEvidence struct {
	Title     string
	Coverage  []string
	SizeBytes *int64
	// Units maps each key to the keys of the planned file that holds it
	// (batchplan.Plan.CandidateUnits); a key absent here is its own unit.
	Units map[string][]string
}

// NamingFacts projects the frozen Release identity into the naming episode
// facts. Only a resolved identity is projected: an unresolved, unsupported, or
// invalid Release shape carries no episode to name with, and naming keeps its
// existing evidence path for those inputs instead of inventing an episode.
func (e ParserEvidence) NamingFacts() *naming.FrozenEpisodeNamingFacts {
	if e.Identity.State != episodeidentity.StateResolved || e.Identity.Episode == nil {
		return nil
	}
	raw := ""
	if e.Identity.RawEpisode != nil {
		raw = string(*e.Identity.RawEpisode)
	}
	facts := &naming.FrozenEpisodeNamingFacts{
		Raw:     raw,
		Number:  string(*e.Identity.Episode),
		Offset:  e.Identity.Offset,
		Display: string(*e.Identity.Episode),
	}
	if e.Identity.EpisodeEnd != nil {
		facts.NumberEnd = string(*e.Identity.EpisodeEnd)
	}
	return facts
}

// Provenance is the parser descriptor/config evidence frozen into the existing
// target and download JSON contracts, together with the digests of the exact
// parse this evaluation ran.
func (e ParserEvidence) Provenance() parser.Provenance {
	provenance := e.Selection.Provenance()
	provenance.InputDigest = e.Parsed.InputDigest
	provenance.ResultDigest = e.Parsed.ResultDigest
	provenance.Offset = e.Identity.Offset
	provenance.SourceVersion = e.SourceVersion
	provenance.ParseStatus = string(e.Parsed.Status)
	provenance.ParseShape = string(e.Parsed.Shape)
	provenance.PolicyVersion = e.PolicyVersion
	provenance.ProfileVersion = e.ProfileVersion
	provenance.RulesOwnerVersion = e.RulesOwnerVersion
	provenance.MediaKind = e.mediaKind()
	provenance.Season = e.Season
	provenance.SeasonBound = e.SeasonBound
	if version := e.Identity.ResolverVersion; version != "" {
		provenance.ResolverVersion = version
	}
	return provenance
}

// mediaKind normalizes the frozen metadata media kind: an empty value is the
// historical TV default, so downstream consumers never see an unspecified
// kind.
func (e ParserEvidence) mediaKind() string {
	if kind := strings.TrimSpace(e.MediaKind); kind != "" {
		return kind
	}
	return "tv"
}

// ParserSelectionInput is the path-neutral selection input: the instance parser
// policy kind plus the effective episode patterns and the one episode offset of
// this release (its RSS link's offset, or the one-shot input's own offset). The
// offset is applied by the episode identity resolver exactly once, never by the
// parser adapter. MediaKind ("tv" or
// "movie") is the owner media kind the identity resolves with (empty is tv);
// Season and SeasonBound are the TMDB season frozen for naming only.
type ParserSelectionInput struct {
	Kind            string
	EpisodePatterns []string
	Offset          int
	// MediaKind is the owner media kind ("tv" or "movie"); empty defaults to
	// "tv". Season is the TMDB season and SeasonBound marks a preserved season
	// 0, both frozen for naming only.
	MediaKind   string
	Season      int
	SeasonBound bool
	// SourceVersion is the RSS link CAS version the offset was read from; it is
	// frozen provenance evidence.
	SourceVersion int64
	// PolicyVersion is the instance parser-policy owner CAS version and
	// ProfileVersion is the resolved effective parser-section version; both are
	// carried into the frozen provenance as evidence.
	PolicyVersion  int64
	ProfileVersion int64
	// RulesOwnerVersion is the CAS version of the anime rules config owner the
	// profile was resolved from.
	RulesOwnerVersion int64
}

// ResolveParserEvidence runs exactly that one selection over the release title.
// A selection or parser failure is returned as an error instead of degrading to
// another parser: an unavailable/unknown kind or a violated parser contract is
// a configuration fault the caller must surface. A movie binding short-circuits
// in the resolver to the canonical movie:0 identity regardless of any digits in
// the title: the episode parse stays typed evidence only.
func ResolveParserEvidence(ctx context.Context, selection ParserSelectionInput, title string) (ParserEvidence, error) {
	chosen, err := parser.NewSelectionFromProfile(parser.ParserKind(selection.Kind), selection.EpisodePatterns, selection.Offset)
	if err != nil {
		return ParserEvidence{}, fmt.Errorf("select release parser %q: %w", selection.Kind, err)
	}
	parsed, err := parser.ParseEpisode(ctx, parser.DefaultRegistry(), chosen.Parser, parser.EpisodeParseInput{
		Kind: parser.InputReleaseTitle, Text: title,
	})
	if err != nil {
		return ParserEvidence{}, fmt.Errorf("parse release title with parser %q: %w", chosen.Kind(), err)
	}
	evidence := ParserEvidence{
		Selection: chosen, Parsed: parsed,
		MediaKind:     selection.MediaKind,
		Season:        selection.Season,
		SeasonBound:   selection.SeasonBound,
		SourceVersion: selection.SourceVersion,
		PolicyVersion: selection.PolicyVersion, ProfileVersion: selection.ProfileVersion,
		RulesOwnerVersion: selection.RulesOwnerVersion,
	}
	identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind:    evidence.mediaKind(),
		FileEvidence: parser.Evidence(parsed, parser.InputReleaseTitle),
		// The release-level identity is the title's own evidence; it never
		// becomes the identity of a file.
		Offset: chosen.Offset,
	})
	evidence.Identity = identity
	return evidence, nil
}

// ResolveFileEpisode resolves the episode of one video from its own filename
// alone, with the release's selected parser (the same frozen kind and config
// that parsed the title), its effective offset, and the frozen media kind. A
// filename without an episode yields an unresolved identity and nil
// naming facts: the file is an unknown file, and the release title never
// stands in for it.
func ResolveFileEpisode(ctx context.Context, release ParserEvidence, filename string) (episodeidentity.EpisodeIdentity, *naming.FrozenEpisodeNamingFacts, error) {
	parsed, err := parser.ParseEpisode(ctx, parser.DefaultRegistry(), release.Selection.Parser, parser.EpisodeParseInput{Kind: parser.InputFileName, Text: filename})
	if err != nil {
		return episodeidentity.EpisodeIdentity{}, nil, fmt.Errorf("parse file name with parser %q: %w", release.Selection.Kind(), err)
	}
	identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind:    release.mediaKind(),
		FileEvidence: parser.Evidence(parsed, parser.InputFileName),
		Offset:       release.Identity.Offset,
	})
	return identity, ParserEvidence{Identity: identity}.NamingFacts(), nil
}

// ResolveTargetPath canonicalizes a rendered absolute target. There is no
// deployment anchor: the template itself produced the absolute path, and a
// relative render is refused instead of being joined to a global root.
func ResolveTargetPath(rendered string) (string, error) {
	rendered = strings.TrimSpace(strings.ReplaceAll(rendered, "\\", "/"))
	if rendered == "" {
		return "", fmt.Errorf("target is empty")
	}
	path := filepath.FromSlash(rendered)
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: %s", ErrTargetNotAbsolute, rendered)
	}
	return filepath.Clean(path), nil
}

// OrdinaryRequest is the ordinary RSS acquisition input: the frozen Release
// evaluation facts plus the replacement intent already resolved by the RSS
// adapter. It carries no policy of its own — rendering, collision and adoption
// rules come from the shared planner.
type OrdinaryRequest struct {
	ReleaseID  string
	AnimeTitle string
	GroupName  string
	// Offset is the episode offset of the RSS link the release came through;
	// SourceVersion is the link CAS version it was read from. Offset is used
	// only when Evidence is not supplied.
	Offset              int
	SourceVersion       int64
	ParserKind          string
	ParserPolicyVersion int64
	ProfileVersion      int64
	RulesOwnerVersion   int64
	// MediaKind is the frozen TMDB media kind ("tv" or "movie"); Season and
	// SeasonBound are the bound metadata season binding. They are used only
	// when Evidence is not supplied.
	MediaKind   string
	Season      int
	SeasonBound bool
	// Evidence is an already-resolved selected-parser evidence. The caller that
	// already resolved the one selection (RSS evaluation) passes it here so the
	// selected parser runs exactly once per execution.
	Evidence  *ParserEvidence
	Effective animedomain.EffectiveConfig
	Intent    Intent
	// ReplaceKeys are the coverage keys with an admitted replace decision. A
	// planned video whose every key is here is a replacement target; every
	// other target keeps Intent.
	ReplaceKeys map[string]bool
	Item        acquisitiondomain.FeedItem
	Preview     animedomain.PreviewReleaseResult
	// BatchPlan is the plan the RSS adapter already built and deduplicated
	// against the anime's placements and in-flight acquisitions. Nil plans the
	// manifest here with UnclassifiedRoute and no deduplication.
	BatchPlan         *batchplan.Plan
	UnclassifiedRoute string
	// Group is the provider-namespaced external identity of the release's RSS
	// source (Mikan's numeric group id / AniBT's group slug), from the same
	// frozen evidence as GroupName. It is zero when the source records none.
	Group providerdomain.GroupRef
	// ApprovedLoserAt reports whether the file now at path is the managed
	// loser of an admitted replace decision for this release (same path, same
	// recorded size). Such an occupant is not a collision: that one target is
	// published as an approved atomic replacement whatever the global
	// collision policy. Nil approves nothing.
	ApprovedLoserAt func(path string) bool
	// ClaimableAt reports whether the file now at path is a scan-managed
	// file of the anime that no release has claimed yet. A replace target
	// whose present occupant is such a file with the exact entry size is
	// that file, not a loser: the target turns ensure_present so the release
	// adopts (claims) it instead of downloading it again. Nil claims nothing.
	ClaimableAt func(path string) bool
}

// OneShotRequest is the finite one-shot acquisition input. It is the same
// planning request with the one-shot product boundary fixed: ensure_present
// intent (a finite seed command never replaces an incumbent). Offset is the
// one-shot input's own offset, never an existing subscription's.
//
// FileSelection is the explicit manual file selection of the one-shot (nil =
// the product default, first-level videos and subtitles). It replaces only
// the first-level gate: the selected videos and subtitles, at any depth, are
// the candidates, and the kind gate, the file rules, the unknown-file route
// and the collision policy apply to them unchanged.
type OneShotRequest struct {
	ReleaseID           string
	AnimeTitle          string
	GroupName           string
	Offset              int
	SourceVersion       int64
	ParserKind          string
	ParserPolicyVersion int64
	ProfileVersion      int64
	RulesOwnerVersion   int64
	MediaKind           string
	Season              int
	SeasonBound         bool
	Evidence            *ParserEvidence
	Effective           animedomain.EffectiveConfig
	Item                acquisitiondomain.FeedItem
	Preview             animedomain.PreviewReleaseResult
	// UnclassifiedRoute is the instance unknown-file route the plan freezes.
	// A one-shot is an explicit command: its plan is never deduplicated.
	UnclassifiedRoute string
	FileSelection     *filekinds.Selection
	// Group is the provider-namespaced external identity of the release's RSS
	// source (Mikan's numeric group id / AniBT's group slug), from the same
	// frozen evidence as GroupName. It is zero when the source records none.
	Group providerdomain.GroupRef
}

// PlanInput is the complete path-neutral planning input.
type PlanInput struct {
	ReleaseID           string
	AnimeTitle          string
	GroupName           string
	Offset              int
	SourceVersion       int64
	ParserKind          string
	ParserPolicyVersion int64
	ProfileVersion      int64
	RulesOwnerVersion   int64
	MediaKind           string
	Season              int
	SeasonBound         bool
	Evidence            *ParserEvidence
	Effective           animedomain.EffectiveConfig
	Intent              Intent
	ReplaceKeys         map[string]bool
	Item                acquisitiondomain.FeedItem
	Preview             animedomain.PreviewReleaseResult
	BatchPlan           *batchplan.Plan
	UnclassifiedRoute   string
	// FileSelection is OneShotRequest.FileSelection (nil for every other path).
	FileSelection *filekinds.Selection
	// Group is the provider-namespaced external identity of the release's RSS
	// source (Mikan's numeric group id / AniBT's group slug), read from the same
	// frozen evidence as GroupName. The zero value means the source records none.
	Group providerdomain.GroupRef
	// ApprovedLoserAt is OrdinaryRequest.ApprovedLoserAt.
	ApprovedLoserAt func(path string) bool
	// ClaimableAt is OrdinaryRequest.ClaimableAt.
	ClaimableAt func(path string) bool
}

// PlanResult is the transaction-free admission outcome. Set/Observations are
// empty on failure; ReleaseFiles always carries the per-entry target projection
// (rendered target or typed diagnostic) that the caller freezes into its own
// owner facts. BatchPlan is the final per-file plan conclusion (after target
// conflicts) that the admission freezes into the release media fact snapshot;
// it is empty when admission failed before classification.
type PlanResult struct {
	Admission    AdmissionResult
	Evidence     ParserEvidence
	Observations []Observation
	PlanReason   string
	ReleaseFiles []acquisitiondomain.ReleaseFile
	Set          Set
	BatchPlan    batchplan.Plan
}

// PreflightOrdinary plans and admits an ordinary ensure_present/replace
// acquisition.
func PreflightOrdinary(ctx context.Context, request OrdinaryRequest, probe Probe) PlanResult {
	return preflight(ctx, PlanInput{
		ReleaseID:           request.ReleaseID,
		AnimeTitle:          request.AnimeTitle,
		GroupName:           request.GroupName,
		Group:               request.Group,
		Offset:              request.Offset,
		SourceVersion:       request.SourceVersion,
		ParserKind:          request.ParserKind,
		ParserPolicyVersion: request.ParserPolicyVersion,
		ProfileVersion:      request.ProfileVersion,
		RulesOwnerVersion:   request.RulesOwnerVersion,
		MediaKind:           request.MediaKind,
		Season:              request.Season,
		SeasonBound:         request.SeasonBound,
		Evidence:            request.Evidence,
		Effective:           request.Effective,
		Intent:              request.Intent,
		ReplaceKeys:         request.ReplaceKeys,
		Item:                request.Item,
		Preview:             request.Preview,
		BatchPlan:           request.BatchPlan,
		UnclassifiedRoute:   request.UnclassifiedRoute,
		ApprovedLoserAt:     request.ApprovedLoserAt,
		ClaimableAt:         request.ClaimableAt,
	}, probe)
}

// PreflightOneShot plans and admits a finite one-shot acquisition under the
// same rules, with the one-shot product boundary applied.
func PreflightOneShot(ctx context.Context, request OneShotRequest, probe Probe) PlanResult {
	return preflight(ctx, PlanInput{
		ReleaseID:           request.ReleaseID,
		AnimeTitle:          request.AnimeTitle,
		GroupName:           request.GroupName,
		Group:               request.Group,
		Offset:              request.Offset,
		SourceVersion:       request.SourceVersion,
		ParserKind:          request.ParserKind,
		ParserPolicyVersion: request.ParserPolicyVersion,
		ProfileVersion:      request.ProfileVersion,
		RulesOwnerVersion:   request.RulesOwnerVersion,
		MediaKind:           request.MediaKind,
		Season:              request.Season,
		SeasonBound:         request.SeasonBound,
		Evidence:            request.Evidence,
		Effective:           request.Effective,
		Intent:              IntentEnsurePresent,
		Item:                request.Item,
		Preview:             request.Preview,
		UnclassifiedRoute:   request.UnclassifiedRoute,
		FileSelection:       request.FileSelection,
	}, probe)
}

// preflight is the single implementation of the pre-download admission
// boundary. The batch plan decides what is placed: only its planned videos get
// a pre-download target, a target conflict drops only that file, and a plan
// with nothing placeable is skipped (no_placeable_files) rather than failed.
// Every failure path returns a typed reason and a plan detail instead of
// continuing to qB.
func preflight(ctx context.Context, input PlanInput, probe Probe) PlanResult {
	files := append([]acquisitiondomain.ReleaseFile(nil), input.Item.ReleaseFiles...)
	fail := func(reason, detail string) PlanResult {
		return PlanResult{
			ReleaseFiles: files,
			Admission:    AdmissionResult{Admission: AdmissionFailed, Reason: reason},
			PlanReason:   detail,
		}
	}
	// Policy gates precede probe/root/parser errors; no manual input can bypass them.
	releaseFilter, err := animedomain.CompileRegexFilter(input.Effective.Rules.ReleaseFilter)
	if err != nil {
		return fail("release_filter_invalid", err.Error())
	}
	if d := releaseFilter.MatchReleaseTitle(input.Item.Title); !d.Allowed {
		return fail("release_"+d.Reason, "release title rejected by filter")
	}
	fileFilter, err := animedomain.CompileRegexFilter(input.Effective.Rules.FileFilter)
	if err != nil {
		return fail("file_filter_invalid", err.Error())
	}
	paths := make([]string, len(files))
	for i, file := range files {
		paths[i] = file.SourcePath
	}
	// The organization selection validates the manifest paths (canonical,
	// unique, within the inventory budget); the plan decides placement.
	organization, err := fileFilter.SelectOrganizationFilesIn(filekinds.Version, paths, input.FileSelection)
	if animedomain.IsInventoryBudgetExceeded(err) {
		return fail("torrent_too_large", "the torrent lists too many files to freeze its download record")
	}
	if err != nil {
		return fail("file_filter_path_invalid", err.Error())
	}
	if len(input.Item.ReleaseFiles) == 0 {
		return fail("target_entry_facts_missing", "torrent metadata did not contain exact per-entry file sizes")
	}
	// One selected parser/profile governs the naming plan. When the caller
	// already resolved the one selection (RSS evaluation does), that evidence is
	// reused so the selected parser runs exactly once per execution. The
	// release identity is title evidence only: every video's episode comes from
	// its own filename, so a batch title (range, season pack) is not a failure.
	var evidence ParserEvidence
	if input.Evidence != nil {
		evidence = *input.Evidence
	} else {
		resolved, err := ResolveParserEvidence(ctx, ParserSelectionInput{
			Kind:              input.ParserKind,
			EpisodePatterns:   input.Effective.Rules.Parser.EpisodePatterns,
			Offset:            input.Offset,
			MediaKind:         input.MediaKind,
			Season:            input.Season,
			SeasonBound:       input.SeasonBound,
			SourceVersion:     input.SourceVersion,
			PolicyVersion:     input.ParserPolicyVersion,
			ProfileVersion:    input.ProfileVersion,
			RulesOwnerVersion: input.RulesOwnerVersion,
		}, input.Item.Title)
		if err != nil {
			return fail("parser_selection_unavailable", "the selected parser/profile is unavailable: "+strings.TrimSpace(err.Error()))
		}
		evidence = resolved
	}
	var plan batchplan.Plan
	if input.BatchPlan != nil {
		plan = cloneBatchPlan(*input.BatchPlan)
	} else {
		plan, err = BuildBatchPlan(ctx, BatchPlanInput{Evidence: evidence, Files: files, FileFilter: input.Effective.Rules.FileFilter, UnclassifiedRoute: input.UnclassifiedRoute, Selection: input.FileSelection, DownloadCompletion: string(input.Effective.DownloadCompletion.Rules.Action), DownloadCompletionSeedLimits: input.Effective.DownloadCompletion.Rules.SeedLimits})
		if err != nil {
			return fail("file_parser_unavailable", strings.TrimSpace(err.Error()))
		}
	}
	if BatchPlanTooLarge(plan) {
		return fail("torrent_too_large", "the torrent lists too many files to freeze its download record")
	}
	result := func(admission AdmissionResult, set Set, observations []Observation) PlanResult {
		return PlanResult{ReleaseFiles: files, Set: set, Observations: observations, Admission: admission, Evidence: evidence, BatchPlan: plan}
	}
	if !plan.Accepted() {
		return result(AdmissionResult{Admission: AdmissionSkipped, Reason: batchplan.ReasonNoPlaceableFiles}, Set{}, nil)
	}
	if probe == nil {
		return fail("target_probe_unavailable", "target filesystem probe is unavailable")
	}
	preview := input.Preview
	preview.FrozenEpisode = evidence.NamingFacts()
	// The release-level vars evaluation is frozen once on the carrier's policy;
	// each target names it by digest (see VarsDigest).
	varsDigest, err := VarsDigest(preview.FrozenVars)
	if err != nil {
		return fail("naming_vars_unavailable", strings.TrimSpace(err.Error()))
	}
	collision := collisionPolicyForTarget(fileEffectCollisionPolicy(input.Effective))
	operation := fileEffectOperation(input.Effective)
	animeTitle := strings.TrimSpace(input.AnimeTitle)
	if animeTitle == "" {
		animeTitle = strings.TrimSpace(input.Item.Title)
	}
	source := animedomain.SourceEvaluationInput{Index: 0, GroupName: input.GroupName, Group: input.Group, Offset: input.Offset}
	entries := make([]Entry, 0, len(files))
	fileIndex := map[string]int{}
	// coverage is each planned video's coverage keys; the duplicate-target
	// rule below compares them.
	coverage := map[string]string{}
	for index := range files {
		file := &files[index]
		file.TargetPath = nil
		file.TargetDiagnostic = nil
		// Only the plan's planned videos get a pre-download target. A
		// subtitle is an attachment of its bound video and is planned after
		// the download; an unknown video is decided by the frozen route after
		// the download; a covered or excluded video is never placed.
		filename := strings.TrimSpace(file.Filename)
		if filename == "" {
			filename = filepath.Base(strings.ReplaceAll(file.SourcePath, "\\", "/"))
		}
		sourcePath := normalizeTargetRef(file.SourcePath)
		if sourcePath == "" {
			sourcePath = normalizeTargetRef(filename)
		}
		planned, ok := plan.File(sourcePath)
		if !ok || planned.Kind != filekinds.KindVideo || planned.Outcome != batchplan.OutcomePlanned {
			continue
		}
		fileIndex[sourcePath] = index
		coverage[sourcePath] = strings.Join(planned.Keys(), ",")
		previewItem := input.Item
		previewItem.TorrentFilename = organization.Decisions[index].Input
		// The render core uses the enclosure only as a logical source display.
		// It is deliberately set to the torrent-relative source path; no network
		// request is performed here.
		previewItem.Enclosure = file.SourcePath
		previewItem.TorrentURL = ""
		rules, _, err := attrrules.Current()
		if err != nil {
			return fail("attr_rules_unavailable", strings.TrimSpace(err.Error()))
		}
		if file.SizeBytes == nil || *file.SizeBytes < 0 {
			setTargetDiagnostic(file, "target_entry_size_unknown", "目标路径生成失败，文件大小未知，未执行重命名")
			return fail("target_entry_size_unknown", "included torrent entry has no exact byte size")
		}
		manifestFile := animedomain.ManifestFileInput{SourcePath: sourcePath, Filename: filename, MediaKind: file.MediaKind, SizeBytes: *file.SizeBytes}
		if plan.MediaKind != batchplan.MediaKindFilm {
			// The plan froze this video's own filename identity; naming
			// renders from exactly that identity.
			var identity episodeidentity.EpisodeIdentity
			if planned.Identity != nil {
				identity = *planned.Identity
			}
			manifestFile.FrozenEpisode, manifestFile.PerFileEpisode = ParserEvidence{Identity: identity}.NamingFacts(), true
		}
		rendered := animedomain.PreviewExactFile(previewItem, preview, manifestFile, input.Effective, animeTitle, source, rules)
		if !rendered.Included {
			continue
		}
		// Validity is never overridable: an invalid or unsafe render, or a
		// naming/rules failure that produced no target, fails closed.
		if rendered.Safety != animedomain.PreviewFileResultSafety("valid") {
			setTargetDiagnostic(file, targetDiagnosticCode(rendered.ReasonCode, "target_render_failed"), "目标路径生成失败，未执行重命名")
			return fail("target_render_failed", "the selected file produced an invalid or unsafe target: "+reasonOrUnknown(rendered.ReasonCode))
		}
		if rendered.RenderedTarget == nil || strings.TrimSpace(*rendered.RenderedTarget) == "" {
			setTargetDiagnostic(file, targetDiagnosticCode(rendered.ReasonCode, "target_render_failed"), "目标路径生成失败，未执行重命名")
			return fail("target_render_failed", "included torrent entry did not produce a final target: "+reasonOrUnknown(rendered.ReasonCode))
		}
		renderedTarget := *rendered.RenderedTarget
		targetPath, err := ResolveTargetPath(renderedTarget)
		if err != nil {
			reasonCode := "target_path_invalid"
			reasonDetail := "rendered target path is invalid"
			message := "目标路径无效，未执行重命名"
			if errors.Is(err, ErrTargetNotAbsolute) {
				reasonCode = "target_not_absolute"
				reasonDetail = "naming template rendered a relative target; targets must be absolute"
				message = "命名目标不是绝对路径，未生成目标"
			}
			setTargetDiagnostic(file, reasonCode, message)
			return fail(reasonCode, reasonDetail)
		}
		render := map[string]any{
			"renderer":        "anime.preview_file.v2",
			"naming_renderer": naming.RendererVersion,
			"target":          targetPath,
			"source":          sourcePath,
		}
		if varsDigest != "" {
			render["vars_digest"] = varsDigest
		}
		renderSnapshot, _ := json.Marshal(render)
		configSnapshot, _ := json.Marshal(map[string]any{
			"collision_policy": collision,
			"operation":        operation,
			"parser":           evidence.Provenance(),
		})
		file.TargetPath = &targetPath
		entries = append(entries, Entry{
			TargetRef:         sourcePath,
			SourcePath:        sourcePath,
			MediaKind:         targetMediaKind(file.MediaKind),
			TargetPath:        targetPath,
			ExpectedSizeBytes: file.SizeBytes,
			Intent:            fileIntent(input, planned),
			CollisionPolicy:   collision,
			RenderSnapshot:    renderSnapshot,
			ConfigSnapshot:    configSnapshot,
		})
	}
	drop := func(sourcePath, reason string) {
		plan.MarkCollision(sourcePath, reason)
		if index, ok := fileIndex[sourcePath]; ok {
			setTargetDiagnostic(&files[index], reason, "目标路径冲突，此文件不入库")
		}
	}
	// Two planned videos rendering one target path. When they are the same
	// episode (identical coverage keys: a v2 or a second encode of one episode,
	// the duplicate the batch plan leaves to this step) batchplan.SameEpisodeTargets
	// decides in source path order: by default the first keeps it and the others
	// are dropped; under a manual one-shot selection the global collision policy
	// decides (skip keeps the first, versioned keeps all under versioned names,
	// overwrite keeps the last). Otherwise the name cannot tell different
	// episodes apart (typically a value that rendered empty), so every member is
	// dropped (target_duplicate): a sibling is never overwritten or versioned,
	// whatever the collision policy.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].SourcePath < entries[j].SourcePath })
	groups := map[string][]string{}
	for _, entry := range entries {
		groups[entry.TargetPath] = append(groups[entry.TargetPath], entry.SourcePath)
	}
	sameEpisode := func(members []string) bool {
		first := coverage[members[0]]
		if first == "" {
			return false
		}
		for _, member := range members[1:] {
			if coverage[member] != first {
				return false
			}
		}
		return true
	}
	decisions := map[string]batchplan.SameEpisodeTarget{}
	for _, members := range groups {
		if len(members) < 2 || !sameEpisode(members) {
			continue
		}
		for index, decision := range batchplan.SameEpisodeTargets(string(collision), len(members), input.FileSelection != nil) {
			decisions[members[index]] = decision
		}
	}
	unique := entries[:0]
	claimed := map[string]bool{}
	for _, entry := range entries {
		members := groups[entry.TargetPath]
		if len(members) > 1 && !sameEpisode(members) {
			drop(entry.SourcePath, batchplan.ReasonTargetDuplicate)
			continue
		}
		if decision, ok := decisions[entry.SourcePath]; ok {
			if !decision.Keep {
				drop(entry.SourcePath, decision.Reason)
				continue
			}
			if decision.Version > 1 {
				versioned := batchplan.VersionedTargetPath(entry.TargetPath, decision.Version)
				entry.TargetPath = versioned
				entry.RenderSnapshot = versionedRenderSnapshot(entry.RenderSnapshot, versioned)
				// The plan carries the number, so a later re-render of the
				// naming template (file operation planning) lands on the same
				// versioned target.
				plan.MarkTargetVersion(entry.SourcePath, decision.Version)
				if index, ok := fileIndex[entry.SourcePath]; ok {
					files[index].TargetPath = &versioned
				}
			}
		}
		// A versioned name may still meet another file's own target: the
		// later one by source path is a duplicate.
		if claimed[entry.TargetPath] {
			drop(entry.SourcePath, batchplan.ReasonTargetDuplicate)
			continue
		}
		claimed[entry.TargetPath] = true
		unique = append(unique, entry)
	}
	entries = unique
	if len(entries) == 0 {
		if !plan.Accepted() {
			// Every planned video collided with another one of this release.
			return PlanResult{ReleaseFiles: files, Admission: AdmissionResult{Admission: AdmissionFailed, Reason: batchplan.ReasonTargetDuplicate}, PlanReason: "every planned target collides with another file of the release", Evidence: evidence, BatchPlan: plan}
		}
		// Only routed unknown videos remain: nothing has a pre-download
		// target, and the frozen route places them after the download.
		return result(AdmissionResult{Admission: AdmissionDownload, Reason: "target_work_required"}, Set{ReleaseID: strings.TrimSpace(input.ReleaseID), Targets: []Spec{}}, []Observation{})
	}
	set, err := Plan(input.ReleaseID, entries)
	if err != nil {
		return fail(targetPlanReason(err), "target set is invalid: "+strings.TrimSpace(err.Error()))
	}
	observations := make([]Observation, 0, len(set.Targets))
	for _, spec := range set.Targets {
		observations = append(observations, probe.Observe(spec))
	}
	if input.ClaimableAt != nil && claimScanManagedTargets(entries, set, observations, input.ClaimableAt) {
		if set, err = Plan(input.ReleaseID, entries); err != nil {
			return fail(targetPlanReason(err), "target set is invalid: "+strings.TrimSpace(err.Error()))
		}
	}
	if input.ApprovedLoserAt != nil && approveLoserTargets(entries, set, observations, input.ApprovedLoserAt) {
		// Target ids derive from the release and the entry, so the
		// observations still name the re-planned targets.
		if set, err = Plan(input.ReleaseID, entries); err != nil {
			return fail(targetPlanReason(err), "target set is invalid: "+strings.TrimSpace(err.Error()))
		}
	}
	blocked := DecideTargets(set, observations)
	if len(blocked) > 0 {
		refs := make([]string, 0, len(blocked))
		for ref := range blocked {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		for _, ref := range refs {
			drop(ref, blocked[ref])
		}
		kept := entries[:0]
		for _, entry := range entries {
			if blocked[entry.TargetRef] == "" {
				kept = append(kept, entry)
			}
		}
		entries = kept
		if len(entries) == 0 {
			if plan.Accepted() {
				return result(AdmissionResult{Admission: AdmissionDownload, Reason: "target_work_required"}, Set{ReleaseID: strings.TrimSpace(input.ReleaseID), Targets: []Spec{}}, []Observation{})
			}
			// Every placeable file conflicts with the library: the release
			// fails with the first conflict instead of being skipped.
			return PlanResult{ReleaseFiles: files, Admission: AdmissionResult{Admission: AdmissionFailed, Reason: blocked[refs[0]]}, PlanReason: "every planned target conflicts with the library", Evidence: evidence, BatchPlan: plan}
		}
		set, err = Plan(input.ReleaseID, entries)
		if err != nil {
			return fail(targetPlanReason(err), "target set is invalid: "+strings.TrimSpace(err.Error()))
		}
		remaining := observations[:0]
		for _, observation := range observations {
			for _, spec := range set.Targets {
				if spec.ID == observation.TargetID {
					remaining = append(remaining, observation)
					break
				}
			}
		}
		observations = remaining
	}
	admission := Decide(set, observations)
	if admission.Admission == AdmissionAdopted && plan.UnclassifiedRoute == batchplan.RouteRoute && plan.UnknownVideos() > 0 {
		// Routed unknown videos are placed after a download; the exact
		// planned targets alone never make the release adopted.
		admission = AdmissionResult{Admission: AdmissionDownload, Reason: "target_work_required", Matched: admission.Matched}
	}
	return result(admission, set, observations)
}

// cloneBatchPlan copies a caller's plan so target conflicts never mutate it.
func cloneBatchPlan(plan batchplan.Plan) batchplan.Plan {
	plan.Files = append([]batchplan.File(nil), plan.Files...)
	plan.CoverageKeys = append([]string(nil), plan.CoverageKeys...)
	return plan
}

// normalizeTargetRef canonicalizes one torrent-entry reference: trimmed with
// forward slashes, exactly the form a frozen target's SourcePath/TargetRef
// carries. The explicit file selection and the planner compare the same form.
func normalizeTargetRef(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
}

func reasonOrUnknown(reason *string) string {
	if reason == nil || strings.TrimSpace(*reason) == "" {
		return "unknown"
	}
	return strings.TrimSpace(*reason)
}

// versionedRenderSnapshot records a versioned target in the frozen render
// snapshot, so the snapshot names the target the entry plans.
func versionedRenderSnapshot(raw json.RawMessage, target string) json.RawMessage {
	var snapshot map[string]any
	if json.Unmarshal(raw, &snapshot) != nil || snapshot == nil {
		return raw
	}
	snapshot["target"] = target
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return raw
	}
	return encoded
}

func fileEffectCollisionPolicy(effective animedomain.EffectiveConfig) string {
	return string(effective.Rules.FileEffect.CollisionPolicy)
}

func fileEffectOperation(effective animedomain.EffectiveConfig) string {
	return string(effective.Rules.FileEffect.Operation)
}

func setTargetDiagnostic(file *acquisitiondomain.ReleaseFile, code, message string) {
	if file == nil {
		return
	}
	sourcePath := strings.TrimSpace(strings.ReplaceAll(file.SourcePath, "\\", "/"))
	file.TargetPath = nil
	file.TargetDiagnostic = &acquisitiondomain.TargetDiagnostic{
		Code:       code,
		Message:    message,
		SourcePath: sourcePath,
	}
}

func targetDiagnosticCode(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return strings.TrimSpace(*value)
}

func targetMediaKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "subtitle":
		return "subtitle"
	case "other":
		return "other"
	default:
		return "video"
	}
}

// claimScanManagedTargets turns each replace target whose present occupant
// has the exact entry size and is a scan-managed file no release has claimed
// into an ensure_present target: the release claims that file (adoption)
// instead of replacing it with a download of the same file.
func claimScanManagedTargets(entries []Entry, set Set, observations []Observation, claimable func(string) bool) bool {
	matched := map[string]bool{}
	for _, observation := range observations {
		if observation.State == ObservationPresentMatched {
			matched[observation.TargetID] = true
		}
	}
	refs := map[string]bool{}
	for _, spec := range set.Targets {
		if spec.Intent == IntentReplace && matched[spec.ID] && claimable(spec.TargetPath) {
			refs[spec.TargetRef] = true
		}
	}
	for index := range entries {
		if refs[entries[index].TargetRef] {
			entries[index].Intent = IntentEnsurePresent
		}
	}
	return len(refs) > 0
}

// approveLoserTargets turns each replace target whose present occupant is the
// managed loser of an admitted replace decision into an approved atomic
// replacement (overwrite for that one target). Any other occupant keeps the
// global collision policy.
func approveLoserTargets(entries []Entry, set Set, observations []Observation, approved func(string) bool) bool {
	present := map[string]bool{}
	for _, observation := range observations {
		if observation.State == ObservationPresentMatched || observation.State == ObservationPresentMismatched {
			present[observation.TargetID] = true
		}
	}
	refs := map[string]bool{}
	for _, spec := range set.Targets {
		if spec.Intent != IntentReplace || spec.CollisionPolicy == domain.CollisionOverwrite || !present[spec.ID] {
			continue
		}
		if approved(spec.TargetPath) {
			refs[spec.TargetRef] = true
		}
	}
	for index := range entries {
		if !refs[entries[index].TargetRef] {
			continue
		}
		entries[index].CollisionPolicy = domain.CollisionOverwrite
		var config map[string]any
		if json.Unmarshal(entries[index].ConfigSnapshot, &config) == nil && config != nil {
			config["collision_policy"] = domain.CollisionOverwrite
			if raw, err := json.Marshal(config); err == nil {
				entries[index].ConfigSnapshot = raw
			}
		}
	}
	return len(refs) > 0
}

// UncoveredApprovedTarget proves at acceptance that every replacement the
// approval pass lifted above the global policy can be carried out: each
// overwrite target of a set whose global policy is not overwrite was observed
// absent, or its path holds an approval the acceptance freezes into the
// delivery (covered). It returns the first target path without either (empty
// when every one is covered); the caller refuses the acceptance instead of
// downloading a file the file plan would then refuse.
func UncoveredApprovedTarget(set Set, observations []Observation, effective animedomain.EffectiveConfig, covered map[string]bool) string {
	if collisionPolicyForTarget(fileEffectCollisionPolicy(effective)) == domain.CollisionOverwrite {
		return ""
	}
	missing := map[string]bool{}
	for _, observation := range observations {
		if observation.State == ObservationMissing {
			missing[observation.TargetID] = true
		}
	}
	paths := []string{}
	for _, spec := range set.Targets {
		if spec.CollisionPolicy == domain.CollisionOverwrite && !missing[spec.ID] && !covered[spec.TargetPath] {
			paths = append(paths, spec.TargetPath)
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func collisionPolicyForTarget(policy string) domain.CollisionPolicy {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "version", "versioned":
		return domain.CollisionVersion
	case "overwrite":
		return domain.CollisionOverwrite
	case "fail", "block":
		return domain.CollisionBlock
	default:
		return domain.CollisionBlock
	}
}

func targetPlanReason(err error) string {
	if err == nil {
		return "target_plan_failed"
	}
	message := strings.ToLower(err.Error())
	for _, value := range []string{"duplicate", "path", "size", "intent", "collision"} {
		if strings.Contains(message, value) {
			return "target_" + value + "_invalid"
		}
	}
	return "target_plan_failed"
}

// fileIntent is one planned video's intent: replace when every key it covers
// has an admitted replace decision (a multi-episode file replaces only as a
// whole), else the request intent.
func fileIntent(input PlanInput, file batchplan.File) Intent {
	keys := file.Keys()
	if len(keys) == 0 || len(input.ReplaceKeys) == 0 {
		return input.Intent
	}
	for _, key := range keys {
		if !input.ReplaceKeys[key] {
			return input.Intent
		}
	}
	return IntentReplace
}
