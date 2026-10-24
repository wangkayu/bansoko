// Package batchplan is the per-file plan conclusion of one Release: for every
// file of the torrent manifest, whether it is placed and why not. It is the
// acceptance authority of the automatic path: a release is
// accepted when the plan has a placeable coverage key, or when the frozen
// unknown-file route keeps at least one unknown video. The release title is
// evidence only; every video's episode comes from its own filename.
//
// The package is pure. The recognition it records (first level, product file
// kinds, file rules, filename episode identity, movie main video, strict-prefix
// subtitle binding) is computed by the callers' single MF1a implementation and
// handed in; nothing here parses a name.
package batchplan

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/filekinds"
	"bansoko/internal/organizingpolicy"
)

// Version identifies the plan contract frozen into the Release media fact
// snapshot.
//
// v2 adds the per-file coverage_keys of a multi-episode file. A reader takes a
// file's keys through File.Keys, which reads a v1 file's single key the same.
const Version = "batch-plan-v1"

// Outcome is the closed per-file conclusion.
type Outcome string

const (
	OutcomePlanned             Outcome = "planned"
	OutcomeIgnoredNested       Outcome = "ignored_nested"
	OutcomeIgnoredKind         Outcome = "ignored_kind"
	OutcomeExcludedByRule      Outcome = "excluded_by_rule"
	OutcomeEpisodeUnrecognized Outcome = "episode_unrecognized"
	OutcomeCoveredExisting     Outcome = "covered_existing"
	OutcomeCoveredInFlight     Outcome = "covered_in_flight"
	OutcomeTargetCollision     Outcome = "target_collision"
	OutcomeSubtitleFollows     Outcome = "subtitle_follows"
	OutcomeSubtitleUnbound     Outcome = "subtitle_unbound"
)

// Reason codes owned by the plan. Rule exclusions carry the file_* rule
// reason and unrecognized episodes the identity failure code instead.
const (
	ReasonCoveredExisting             = "covered_existing"
	ReasonReplacementPartialCoverage  = "replacement_partial_coverage"
	ReasonCoveredInFlight             = "covered_in_flight"
	ReasonSubtitleBindingMissing      = "subtitle_binding_missing"
	ReasonSubtitleVideoExcluded       = "subtitle_video_excluded"
	ReasonUnknown                     = "unknown"
	ReasonTargetDuplicate             = "target_duplicate"
	ReasonNoPlaceableFiles            = "no_placeable_files"
	ReasonTorrentManifestMissing      = "torrent_manifest_missing"
	ReasonBatchPlanDrift              = "batch_plan_drift"
	ReasonCandidateAcquisitionFailed  = "candidate_acquisition_failed"
	ReasonEpisodeUnrecognizedFallback = "episode_missing"
)

// Unknown-file routes (the instance unclassified_route).
const (
	RouteRoute = "route"
	RouteSkip  = "skip"
)

// Download-completion actions frozen at acceptance (the download_completion
// section): what happens to the qBittorrent task once the acquisition has
// succeeded. The values equal anime/domain.DownloadCompletionAction.
const (
	CompletionSeed           = "seed"
	CompletionPause          = "pause"
	CompletionRemove         = "remove"
	CompletionRemoveWithData = "remove_with_data"
)

// ValidCompletionAction reports whether value is one of the four closed
// download-completion actions.
func ValidCompletionAction(value string) bool {
	switch value {
	case CompletionSeed, CompletionPause, CompletionRemove, CompletionRemoveWithData:
		return true
	}
	return false
}

// Seed-limit actions: what qBittorrent does to a seeding task once one of its
// share limits is reached.
const (
	SeedLimitStop           = "stop"
	SeedLimitRemove         = "remove"
	SeedLimitRemoveWithData = "remove_with_data"
)

// Bounds qBittorrent itself enforces on a per-torrent share limit.
const (
	MaxSeedingTimeMinutes = 525600
	MinSeedRatio          = 0.01
	MaxSeedRatio          = 9998
)

// SeedLimits are the optional share limits of the seed action. At least one
// limit is set; AtLimit is what qBittorrent does when one is reached. The
// product writes them to qBittorrent once and never runs a timer itself.
type SeedLimits struct {
	SeedingTimeMinutes *int64   `json:"seeding_time_minutes,omitempty"`
	Ratio              *float64 `json:"ratio,omitempty"`
	AtLimit            string   `json:"at_limit"`
}

// Validate reports why the limits are not a usable value, or nil.
func (l SeedLimits) Validate() error {
	if l.SeedingTimeMinutes == nil && l.Ratio == nil {
		return fmt.Errorf("seed_limits needs seeding_time_minutes or ratio")
	}
	if l.SeedingTimeMinutes != nil && (*l.SeedingTimeMinutes <= 0 || *l.SeedingTimeMinutes > MaxSeedingTimeMinutes) {
		return fmt.Errorf("seed_limits.seeding_time_minutes must be between 1 and %d", MaxSeedingTimeMinutes)
	}
	if l.Ratio != nil && (*l.Ratio < MinSeedRatio || *l.Ratio > MaxSeedRatio) {
		return fmt.Errorf("seed_limits.ratio must be between %.2f and %d", MinSeedRatio, MaxSeedRatio)
	}
	switch l.AtLimit {
	case SeedLimitStop, SeedLimitRemove, SeedLimitRemoveWithData:
	default:
		return fmt.Errorf("seed_limits.at_limit must be stop, remove, or remove_with_data")
	}
	return nil
}

// Equal compares two optional limit values by content.
func (l *SeedLimits) Equal(other *SeedLimits) bool {
	if l == nil || other == nil {
		return l == nil && other == nil
	}
	return l.AtLimit == other.AtLimit && int64PtrEqual(l.SeedingTimeMinutes, other.SeedingTimeMinutes) && float64PtrEqual(l.Ratio, other.Ratio)
}

// Clone returns an independent copy (nil stays nil).
func (l *SeedLimits) Clone() *SeedLimits {
	if l == nil {
		return nil
	}
	out := SeedLimits{AtLimit: l.AtLimit}
	if l.SeedingTimeMinutes != nil {
		value := *l.SeedingTimeMinutes
		out.SeedingTimeMinutes = &value
	}
	if l.Ratio != nil {
		value := *l.Ratio
		out.Ratio = &value
	}
	return &out
}

func int64PtrEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func float64PtrEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Levels and rule results.
const (
	LevelFirst    = "first"
	LevelNested   = "nested"
	RuleAllowed   = "allowed"
	RuleExcluded  = "excluded"
	MediaKindTV   = "tv"
	MediaKindFilm = "movie"
)

// File is one manifest file's conclusion.
type File struct {
	SourcePath string `json:"source_path"`
	Level      string `json:"level"`
	Kind       string `json:"kind"`
	// Rule is the file rule result of a first-level video ("" otherwise).
	Rule string `json:"rule,omitempty"`
	// Identity is the filename identity of a first-level video the rules keep.
	Identity *episodeidentity.EpisodeIdentity `json:"identity,omitempty"`
	// CoverageKey is the identity's coverage key; empty for an unknown video.
	// For a multi-episode file it is the first member's key.
	CoverageKey string `json:"coverage_key,omitempty"`
	// CoverageKeys are every key of a multi-episode file, ascending; absent
	// for a single episode.
	CoverageKeys []string `json:"coverage_keys,omitempty"`
	// BoundVideo is the source path a subtitle follows by strict prefix.
	BoundVideo string  `json:"bound_video,omitempty"`
	Outcome    Outcome `json:"outcome"`
	ReasonCode string  `json:"reason_code,omitempty"`
}

// Keys are every coverage key the file occupies: all members of a
// multi-episode file, the one key of a single episode, none for an unknown
// video.
func (f File) Keys() []string {
	if len(f.CoverageKeys) > 0 {
		return append([]string(nil), f.CoverageKeys...)
	}
	if f.CoverageKey != "" {
		return []string{f.CoverageKey}
	}
	return nil
}

// Plan is the frozen per-file plan conclusion of one Release.
type Plan struct {
	Version          string `json:"version"`
	FileKindsVersion string `json:"file_kinds_version"`
	// UnclassifiedRoute is the unknown-file route frozen at acceptance; retry
	// and replan read this value, never the live instance switch.
	UnclassifiedRoute string `json:"unclassified_route"`
	// DownloadCompletion is the download-completion action frozen at
	// acceptance (seed, pause, remove, remove_with_data). A plan without it
	// reads as seed: no action on the qBittorrent task.
	DownloadCompletion string `json:"download_completion,omitempty"`
	// DownloadCompletionSeedLimits are the seed action's optional share limits
	// frozen with it. A plan without them seeds without a limit.
	DownloadCompletionSeedLimits *SeedLimits `json:"download_completion_seed_limits,omitempty"`
	MediaKind                    string      `json:"media_kind"`
	Files                        []File      `json:"files"`
	// CoverageKeys are the deduplicated, sorted coverage keys of the planned
	// videos: the placeable coverage of this Release.
	CoverageKeys []string `json:"coverage_keys"`
}

// InputFile is one manifest file.
type InputFile struct {
	SourcePath string
	SizeBytes  *int64
}

// CompletionAction is the frozen download-completion action; an absent or
// unknown value reads as seed (take no action), the safest reading.
func (p Plan) CompletionAction() string {
	if ValidCompletionAction(p.DownloadCompletion) {
		return p.DownloadCompletion
	}
	return CompletionSeed
}

// CompletionSeedLimits are the frozen share limits of the seed action. They
// read as nil (seed without limit) for any other action and for a value that
// is not valid.
func (p Plan) CompletionSeedLimits() *SeedLimits {
	if p.CompletionAction() != CompletionSeed || p.DownloadCompletionSeedLimits == nil || p.DownloadCompletionSeedLimits.Validate() != nil {
		return nil
	}
	return p.DownloadCompletionSeedLimits.Clone()
}

// Input is the classification input. RuleExclusion returns "" when the file
// rules keep a first-level video, else the typed rule reason. Identity
// resolves a TV video's episode from its own filename; it is not called for a
// movie (the main video is the canonical movie identity MovieIdentity).
type Input struct {
	FileKindsVersion  string
	UnclassifiedRoute string
	// DownloadCompletion is the resolved download-completion action to freeze.
	DownloadCompletion string
	// DownloadCompletionSeedLimits are the seed action's limits to freeze.
	DownloadCompletionSeedLimits *SeedLimits
	Movie                        bool
	MovieIdentity                episodeidentity.EpisodeIdentity
	Files                        []InputFile
	RuleExclusion                func(sourcePath string) string
	Identity                     func(sourcePath string) (episodeidentity.EpisodeIdentity, error)
}

// NormalizePath is the canonical manifest path form the plan is keyed by.
func NormalizePath(value string) string {
	return strings.Trim(strings.ReplaceAll(strings.TrimSpace(value), `\`, "/"), "/")
}

func baseName(value string) string {
	value = NormalizePath(value)
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[index+1:]
	}
	return value
}

func stem(value string) string {
	name := baseName(value)
	if index := strings.LastIndex(name, "."); index > 0 {
		return name[:index]
	}
	return name
}

// Classify builds the plan conclusion of every manifest file before any
// deduplication: ignored (nested, other kind), excluded by the file rules,
// planned (a coverage key), or an unknown video; subtitles follow their bound
// video. Files are ordered by source path so the result never depends on the
// manifest order the client reported.
func Classify(in Input) (Plan, error) {
	plan := Plan{Version: Version, FileKindsVersion: in.FileKindsVersion, UnclassifiedRoute: in.UnclassifiedRoute, DownloadCompletion: in.DownloadCompletion, DownloadCompletionSeedLimits: in.DownloadCompletionSeedLimits.Clone(), MediaKind: MediaKindTV, Files: []File{}, CoverageKeys: []string{}}
	if in.Movie {
		plan.MediaKind = MediaKindFilm
	}
	ordered := make([]InputFile, 0, len(in.Files))
	seen := map[string]bool{}
	for _, file := range in.Files {
		file.SourcePath = NormalizePath(file.SourcePath)
		if file.SourcePath == "" || seen[file.SourcePath] {
			continue
		}
		seen[file.SourcePath] = true
		ordered = append(ordered, file)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].SourcePath < ordered[j].SourcePath })
	movieCandidates := []organizingpolicy.Video{}
	for _, file := range ordered {
		kind, ignored := filekinds.Eligibility(in.FileKindsVersion, file.SourcePath)
		entry := File{SourcePath: file.SourcePath, Kind: kind, Level: LevelFirst}
		if ignored == filekinds.ReasonIgnoredNested {
			entry.Level = LevelNested
		}
		switch {
		case ignored == filekinds.ReasonIgnoredNested:
			entry.Outcome, entry.ReasonCode = OutcomeIgnoredNested, filekinds.ReasonIgnoredNested
		case ignored == filekinds.ReasonIgnoredKind:
			entry.Outcome, entry.ReasonCode = OutcomeIgnoredKind, filekinds.ReasonIgnoredKind
		case kind == filekinds.KindVideo:
			entry.Rule = RuleAllowed
			if in.RuleExclusion != nil {
				if reason := in.RuleExclusion(file.SourcePath); reason != "" {
					entry.Rule, entry.Outcome, entry.ReasonCode = RuleExcluded, OutcomeExcludedByRule, reason
				}
			}
			if entry.Rule == RuleAllowed && in.Movie {
				var size *uint64
				if file.SizeBytes != nil && *file.SizeBytes >= 0 {
					value := uint64(*file.SizeBytes)
					size = &value
				}
				movieCandidates = append(movieCandidates, organizingpolicy.Video{Ref: organizingpolicy.FileRef(file.SourcePath), Order: uint32(len(movieCandidates)), Size: size, Eligible: true})
			}
		}
		plan.Files = append(plan.Files, entry)
	}
	movieMain := ""
	if in.Movie {
		movieMain = string(organizingpolicy.SelectMovie(movieCandidates).Main)
	}
	for index := range plan.Files {
		file := &plan.Files[index]
		if file.Kind != filekinds.KindVideo || file.Outcome != "" {
			continue
		}
		var identity episodeidentity.EpisodeIdentity
		switch {
		case in.Movie && file.SourcePath == movieMain:
			identity = in.MovieIdentity
		case in.Movie:
			file.Outcome, file.ReasonCode = OutcomeEpisodeUnrecognized, ReasonUnknown
			continue
		case in.Identity != nil:
			resolved, err := in.Identity(file.SourcePath)
			if err != nil {
				return Plan{}, err
			}
			identity = resolved
		}
		frozen := identity
		file.Identity = &frozen
		if keys := identity.Keys(); identity.State == episodeidentity.StateResolved && len(keys) > 0 && strings.TrimSpace(keys[0]) != "" {
			file.CoverageKey = keys[0]
			if len(keys) > 1 {
				file.CoverageKeys = keys
			}
			file.Outcome = OutcomePlanned
			continue
		}
		file.Outcome = OutcomeEpisodeUnrecognized
		file.ReasonCode = ReasonEpisodeUnrecognizedFallback
		if identity.FailureCode != nil && strings.TrimSpace(*identity.FailureCode) != "" {
			file.ReasonCode = strings.TrimSpace(*identity.FailureCode)
		}
	}
	// Subtitles bind by strict prefix over every first-level video in path
	// order (rule-excluded ones included, so a subtitle of an excluded video
	// is excluded with it rather than reported unbound).
	videos := []string{}
	videoStems := []string{}
	for _, file := range plan.Files {
		if file.Kind == filekinds.KindVideo && file.Level == LevelFirst {
			videos = append(videos, file.SourcePath)
			videoStems = append(videoStems, stem(file.SourcePath))
		}
	}
	for index := range plan.Files {
		file := &plan.Files[index]
		if file.Kind != filekinds.KindSubtitle || file.Outcome != "" {
			continue
		}
		if bound := filekinds.BindSubtitle(baseName(file.SourcePath), videoStems); bound >= 0 {
			file.BoundVideo = videos[bound]
		}
	}
	plan.Finalize()
	return plan, nil
}

func (p *Plan) indexOf(sourcePath string) int {
	sourcePath = NormalizePath(sourcePath)
	for index := range p.Files {
		if p.Files[index].SourcePath == sourcePath {
			return index
		}
	}
	return -1
}

// File returns the conclusion for one manifest path.
func (p *Plan) File(sourcePath string) (File, bool) {
	if p == nil {
		return File{}, false
	}
	if index := p.indexOf(sourcePath); index >= 0 {
		return p.Files[index], true
	}
	return File{}, false
}

// CandidateKeys are the sorted distinct coverage keys of the keyed videos that
// are still planned: the keys deduplication asks pools about.
func (p *Plan) CandidateKeys() []string {
	keys := map[string]bool{}
	for _, file := range p.Files {
		if file.Kind == filekinds.KindVideo && file.Outcome == OutcomePlanned {
			for _, key := range file.Keys() {
				keys[key] = true
			}
		}
	}
	return sortedKeys(keys)
}

// CandidateUnits maps every candidate key to the keys of the planned video
// that holds it: a single episode's unit is its own key, a multi-episode
// file's unit is all its keys. Replacement judges a key inside its unit (one
// file replaces only what it covers, and a multi-episode file replaces only
// when every key would).
func (p *Plan) CandidateUnits() map[string][]string {
	units := map[string][]string{}
	for _, file := range p.Files {
		if file.Kind != filekinds.KindVideo || file.Outcome != OutcomePlanned {
			continue
		}
		keys := file.Keys()
		for _, key := range keys {
			units[key] = mergeUnit(units[key], keys)
		}
	}
	return units
}

// mergeUnit joins two key sets: two planned files of one key (a duplicate
// the target plan resolves later) share one unit.
func mergeUnit(current, keys []string) []string {
	set := map[string]bool{}
	for _, key := range current {
		set[key] = true
	}
	for _, key := range keys {
		set[key] = true
	}
	return sortedKeys(set)
}

// PlannedVideos are the source paths of the planned videos, in path order.
func (p *Plan) PlannedVideos() []string {
	out := []string{}
	for _, file := range p.Files {
		if file.Kind == filekinds.KindVideo && file.Outcome == OutcomePlanned {
			out = append(out, file.SourcePath)
		}
	}
	return out
}

// Verdict is one coverage key's deduplication conclusion.
type Verdict string

const (
	// VerdictOpen: no placement or in-flight claim holds the key (empty pool,
	// all mode, or a pool that could not be compared).
	VerdictOpen Verdict = "open"
	// VerdictExisting: a present placement is better or equal (retain/keep).
	VerdictExisting Verdict = "existing"
	// VerdictReplace: the candidate would replace a present placement.
	VerdictReplace Verdict = "replace"
	// VerdictInFlight: a non-terminal acquisition already claims the key.
	VerdictInFlight Verdict = "in_flight"
	// VerdictReplacePartial: the candidate is better, but a present placement
	// of the key also holds keys outside the candidate file (an existing
	// multi-episode file), so replacing it would drop those episodes. The
	// present placement stays.
	VerdictReplacePartial Verdict = "replace_partial"
)

// ApplyDedup marks each planned video by the verdicts of all its keys. Any
// existing key covers the whole file (a multi-episode file replaces only when
// every key would); a partial replacement keeps the present file; an
// in-flight claim on any key covers it in flight. A file whose every key is
// open or replace stays planned, in a batch too: each file replaces only the
// placements of its own keys.
func (p *Plan) ApplyDedup(verdicts map[string]Verdict) {
	for index := range p.Files {
		file := &p.Files[index]
		if file.Kind != filekinds.KindVideo || file.Outcome != OutcomePlanned {
			continue
		}
		existing, partial, inFlight := false, false, false
		for _, key := range file.Keys() {
			switch verdicts[key] {
			case VerdictExisting:
				existing = true
			case VerdictReplacePartial:
				partial = true
			case VerdictInFlight:
				inFlight = true
			}
		}
		switch {
		case existing:
			file.Outcome, file.ReasonCode = OutcomeCoveredExisting, ReasonCoveredExisting
		case partial:
			file.Outcome, file.ReasonCode = OutcomeCoveredExisting, ReasonReplacementPartialCoverage
		case inFlight:
			file.Outcome, file.ReasonCode = OutcomeCoveredInFlight, ReasonCoveredInFlight
		}
	}
	p.Finalize()
}

// MarkCollision drops one planned video (and so its subtitles) with a typed
// target conflict reason.
func (p *Plan) MarkCollision(sourcePath, reason string) {
	if index := p.indexOf(sourcePath); index >= 0 {
		file := &p.Files[index]
		if file.Outcome == OutcomePlanned || file.Outcome == OutcomeEpisodeUnrecognized {
			file.Outcome, file.ReasonCode = OutcomeTargetCollision, reason
		}
	}
	p.Finalize()
}

// placed reports a video that is written to the library: planned, or an
// unknown video under the route.
func (p *Plan) placed(file File) bool {
	if file.Kind != filekinds.KindVideo {
		return false
	}
	return file.Outcome == OutcomePlanned || (file.Outcome == OutcomeEpisodeUnrecognized && p.UnclassifiedRoute == RouteRoute)
}

// Finalize recomputes the subtitle conclusions from their videos and the
// placeable coverage keys from the planned videos.
func (p *Plan) Finalize() {
	byPath := map[string]File{}
	keys := map[string]bool{}
	for _, file := range p.Files {
		if file.Kind == filekinds.KindVideo {
			byPath[file.SourcePath] = file
			if file.Outcome == OutcomePlanned {
				for _, key := range file.Keys() {
					keys[key] = true
				}
			}
		}
	}
	for index := range p.Files {
		file := &p.Files[index]
		if file.Kind != filekinds.KindSubtitle || file.Level != LevelFirst || file.Outcome == OutcomeIgnoredKind {
			continue
		}
		switch video, ok := byPath[file.BoundVideo]; {
		case file.BoundVideo == "" || !ok:
			file.Outcome, file.ReasonCode = OutcomeSubtitleUnbound, ReasonSubtitleBindingMissing
		case p.placed(video):
			file.Outcome, file.ReasonCode = OutcomeSubtitleFollows, ""
		default:
			file.Outcome, file.ReasonCode = OutcomeSubtitleUnbound, ReasonSubtitleVideoExcluded
		}
	}
	p.CoverageKeys = sortedKeys(keys)
}

// UnknownVideos counts the first-level videos the rules keep that carry no
// episode identity of their own.
func (p *Plan) UnknownVideos() int {
	count := 0
	for _, file := range p.Files {
		if file.Kind == filekinds.KindVideo && file.Outcome == OutcomeEpisodeUnrecognized {
			count++
		}
	}
	return count
}

// Accepted is the acceptance rule: at least one placeable coverage key, or
// the frozen route keeps unknown files and at least one unknown video exists.
// Otherwise the release is skipped with no_placeable_files (not a failure).
func (p *Plan) Accepted() bool {
	if p == nil {
		return false
	}
	return len(p.CoverageKeys) > 0 || (p.UnclassifiedRoute == RouteRoute && p.UnknownVideos() > 0)
}

// Required reports whether a manifest file is part of the acquisition's
// required set: a placed video, or a subtitle following one.
func (p *Plan) Required(sourcePath string) bool {
	file, ok := p.File(sourcePath)
	if !ok {
		return false
	}
	return p.placed(file) || file.Outcome == OutcomeSubtitleFollows
}

// Episodes projects the coverage keys onto the bound season and the episode
// numbers (display only). ok is false when no key is a TV episode key.
func (p *Plan) Episodes() (season int, episodes []int, ok bool) {
	if p == nil {
		return 0, nil, false
	}
	for _, key := range p.CoverageKeys {
		keySeason, episode, decomposed := episodeidentity.DecomposeCoverageKey(key)
		if !decomposed {
			continue
		}
		number, err := strconv.Atoi(episode)
		if err != nil {
			continue
		}
		season, ok = keySeason, true
		episodes = append(episodes, number)
	}
	sort.Ints(episodes)
	return season, episodes, ok
}

func sortedKeys(keys map[string]bool) []string {
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// EpisodesLabel renders episode numbers as the release label text: runs of
// consecutive numbers collapse to a range ("01-04"), the rest are listed
// ("01, 03, 07"). Numbers are zero-padded to two digits.
func EpisodesLabel(episodes []int) string {
	if len(episodes) == 0 {
		return ""
	}
	sorted := append([]int(nil), episodes...)
	sort.Ints(sorted)
	unique := sorted[:0]
	for index, value := range sorted {
		if index == 0 || value != sorted[index-1] {
			unique = append(unique, value)
		}
	}
	pad := func(value int) string {
		text := strconv.Itoa(value)
		if len(text) < 2 {
			text = "0" + text
		}
		return text
	}
	parts := []string{}
	for start := 0; start < len(unique); {
		end := start
		for end+1 < len(unique) && unique[end+1] == unique[end]+1 {
			end++
		}
		if end > start {
			parts = append(parts, pad(unique[start])+"-"+pad(unique[end]))
		} else {
			parts = append(parts, pad(unique[start]))
		}
		start = end + 1
	}
	return strings.Join(parts, ", ")
}
