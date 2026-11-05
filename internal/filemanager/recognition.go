package filemanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"bansoko/internal/download/domain"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/filekinds"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/parser"
	"bansoko/internal/parser/reltitletags"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/templatefacts"
)

// Recognition: rename and move a manually selected set of files by rendering
// the naming rules one-shot step two renders with over each file's own facts.
// It uses the naming core under a different input adapter: the file list comes
// from the user's selection instead of a seed manifest, and the episode comes
// from parsing the file's own name instead of the release structure. The shared
// boundary stops at the plan — the rendered target of every file.
//
// The operation writes no identity: it admits ordinary move/rename user file
// operations (unmanaged, no placement, no companion relation, no record). A
// video and the subtitles bound to it by the one subtitle rule form one group;
// the video renders from the rules' video variant and each subtitle from the
// same rules' subtitle suffix. A file with no recognized episode follows the
// instance unclassified-route policy: skip (the default) leaves it and its
// subtitles where they are, route organizes it with the rules' unknown variant.
//
// The rendered directory is absolute, exactly as one-shot renders it: the
// folder the files were selected in only says where they come from. A target
// must lie inside one of the file manager's authorized roots; a target in
// another directory is a move, and the submit creates the missing levels
// first. Conflicts are detected again at submit and settled by this operation's
// own choice: block (admit nothing), skip (leave the conflicting files) or
// version (take the first free " (N)" sibling).

// Recognition error codes (each stable; none carries a private path).
const (
	CodeRecognitionRequiresDetached = "FILE_MANAGER_RECOGNITION_REQUIRES_DETACHED"
	CodeRecognitionMixedDirectory   = "FILE_MANAGER_RECOGNITION_MIXED_DIRECTORY"
	CodeRecognitionPlanChanged      = "FILE_MANAGER_RECOGNITION_PLAN_CHANGED"
	CodeRecognitionNotRenderable    = "FILE_MANAGER_RECOGNITION_NOT_RENDERABLE"
	CodeRecognitionNothingToDo      = "FILE_MANAGER_RECOGNITION_NOTHING_TO_DO"
	CodeRecognitionInvalid          = "FILE_MANAGER_RECOGNITION_INVALID"
	CodeRecognitionConflictInvalid  = "FILE_MANAGER_RECOGNITION_CONFLICT_INVALID"
	CodeRecognitionUnavailable      = "FILE_MANAGER_RECOGNITION_UNAVAILABLE"
)

// maxRecognitionFiles bounds one recognition request.
const maxRecognitionFiles = 256

// RecognitionDisposition is what recognition does with one selected file.
type RecognitionDisposition string

const (
	// RecognitionRename renames the file inside its directory.
	RecognitionRename RecognitionDisposition = "rename"
	// RecognitionMove moves the file to a rendered directory of its own.
	RecognitionMove RecognitionDisposition = "move"
	// RecognitionUnchanged renders the file's own name; nothing is moved.
	RecognitionUnchanged RecognitionDisposition = "unchanged"
	// RecognitionSkip leaves the file where it is (unclassified-route skip).
	RecognitionSkip RecognitionDisposition = "skip"
	// RecognitionError: the file cannot be rendered; the operation cannot be
	// submitted while a row carries this disposition.
	RecognitionError RecognitionDisposition = "error"
)

// Recognition blocker codes. A blocker is a typed diagnostic of one row, not
// a system fault.
const (
	RecognitionBlockerFactsMissing    = "facts_missing"
	RecognitionBlockerTemplateInvalid = "template_invalid"
	RecognitionBlockerTargetInvalid   = "target_invalid"
	RecognitionBlockerOutsideRoots    = "outside_roots"
	RecognitionBlockerRuntimeFacts    = "runtime_facts_unavailable"
	RecognitionBlockerVideoMissing    = "video_missing"
	RecognitionBlockerVideoFailed     = "video_failed"
)

// RecognitionConflictChoice settles occupied or duplicated targets. It is this
// operation's own choice; it never changes the instance collision policy.
// Overwrite replaces existing plain files only: a managed, in-progress or
// duplicated target is never overwritten.
type RecognitionConflictChoice string

const (
	RecognitionConflictBlock     RecognitionConflictChoice = "block"
	RecognitionConflictSkip      RecognitionConflictChoice = "skip"
	RecognitionConflictVersion   RecognitionConflictChoice = "version"
	RecognitionConflictOverwrite RecognitionConflictChoice = "overwrite"
)

// OccupantDuplicate marks a target another row of the same request renders.
const OccupantDuplicate MatchOccupant = "duplicate"

// RecognitionRenderer renders one file's target and a subtitle's suffix with
// the operation's naming rules (filemaintenance.RenderRecognition).
type RecognitionRenderer interface {
	RenderRecognition(ctx context.Context, input filemaintenance.RecognitionRenderInput) (filemaintenance.RecognitionRendered, error)
	RenderRecognitionSuffix(ctx context.Context, input filemaintenance.RecognitionRenderInput) (string, error)
}

// RecognitionPolicy reads the instance unclassified-route policy. Unwired, the
// default (skip) applies.
type RecognitionPolicy interface {
	UnclassifiedRoute(ctx context.Context) (string, error)
}

// RecognitionMediaProber reads one selected video's current media facts.
type RecognitionMediaProber interface {
	Probe(ctx context.Context, path string) (domain.MediaFacts, error)
}

// SetRecognitionMediaProber wires the explicit runtime-facts probe. Recognition
// without it keeps working; only a request that asks for runtime facts reports
// the missing probe on each video row.
func (s *Service) SetRecognitionMediaProber(prober RecognitionMediaProber) {
	if s != nil {
		s.recognitionMedia = prober
	}
}

// SetRecognition wires the recognition ports. Unwired, recognition is
// refused. The anime reader is the same one a match uses: a match is an
// optional metadata source for the render, never a written relation.
func (s *Service) SetRecognition(renderer RecognitionRenderer, animes MatchAnimeReader, policy RecognitionPolicy) {
	if s != nil {
		s.recognition, s.recognitionPolicy = renderer, policy
		if animes != nil {
			s.matchAnimes = animes
		}
	}
}

func (s *Service) requireRecognition() error {
	if s.recognition == nil {
		return conflict(CodeRecognitionUnavailable, "file manager recognition is not available")
	}
	return nil
}

// RecognitionParser selects the parser that reads each file's own name.
type RecognitionParser struct {
	ID     string
	Config json.RawMessage
}

// RecognitionMatch is the optional metadata source of the render: the anime
// whose facts the template may read, the season this operation names files
// with, an episode the user chose (one video only), an offset applied to every
// recognized episode and, optionally, the temporary draft of the anime facts
// this operation names with.
type RecognitionMatch struct {
	ManagedAnimeID string
	Season         *int
	Episode        *int
	Offset         int
	// Anime is the settings column's temporary draft. It is the operation's
	// own copy of the facts the render reads, initialized from the matched
	// anime and never written back; nil reads the anime's stored rows.
	Anime *RecognitionAnimeDraft
}

// RecognitionAnimeDraft is the anime facts one recognition render names with.
// A nil binding means this render has no such binding; every value is the
// draft's, not a re-read of the anime.
type RecognitionAnimeDraft struct {
	Title     string
	MediaKind string
	Year      *int
	BGM       *RecognitionAnimeBinding
	TMDB      *RecognitionTMDBBinding
}

// RecognitionAnimeBinding is the draft's Bangumi binding.
type RecognitionAnimeBinding struct {
	WorkID string
}

// RecognitionTMDBBinding is the draft's TMDB binding.
type RecognitionTMDBBinding struct {
	WorkID    string
	MediaKind string
	Season    int
}

// RecognitionRequest is one preview or submit request: the selected absolute
// file paths (all in one directory — the folder they come from, not their
// target), the parser, the operation's naming rules and the optional match.
// The rules render absolute targets exactly as one-shot renders them; the
// selected folder only says where the files come from.
type RecognitionRequest struct {
	Paths  []string
	Parser RecognitionParser
	Naming naming.NamingRules
	Match  *RecognitionMatch
	// LoadRuntimeFacts explicitly reads current video media facts and resolves
	// referenced provider episode titles. Preview and submit share this switch.
	LoadRuntimeFacts bool
}

// RecognitionEpisode is the episode identity recognition gave one file.
type RecognitionEpisode struct {
	Raw       string
	Number    string
	NumberEnd string
	Offset    int
	// Source is the episode evidence source: file_parser, or manual for an
	// episode the user chose.
	Source string
}

// RecognitionBlocker is one reason a row cannot be rendered.
type RecognitionBlocker struct {
	Code  string
	Facts []string
}

// RecognitionConflict is an occupied or duplicated target. VersionPath is the
// advisory path the version choice would take.
type RecognitionConflict struct {
	OccupiedBy  MatchOccupant
	VersionPath string
}

// RecognitionRow is one selected file and what recognition does with it.
type RecognitionRow struct {
	Path       string
	Name       string
	Kind       string
	Group      string
	Revision   string
	Episode    *RecognitionEpisode
	TargetPath string
	TargetName string
	// EmptyFacts are referenced facts that rendered empty after runtime loading.
	// They are informational, not blockers, and stay hidden in the default mode.
	EmptyFacts  []string
	Disposition RecognitionDisposition
	Blockers    []RecognitionBlocker
	Conflict    *RecognitionConflict

	// subtitleSuffix and subtitleExtension compose the target when the video's
	// final name changes (the version choice).
	subtitleSuffix    string
	subtitleExtension string
}

// RecognitionPreview is the side-effect-free outcome of one recognition
// request. PlanDigest identifies the rendered plan the submit must confirm.
type RecognitionPreview struct {
	// Directory is the folder the selected files were read from; every row's
	// TargetPath is rendered absolute by the naming rules.
	Directory    string
	Rows         []RecognitionRow
	UnknownRoute string
	// TitleHint is the title the parser read from the first recognized file's
	// own name, offered as the match's title candidate — the same read the
	// seed search exposes per seed as TitleHint; empty when no name parses a
	// title.
	TitleHint  string
	PlanDigest string
	Conflicts  []string
}

// SubmitRecognitionRequest confirms one previewed recognition. Conflict is how
// the submit settles occupied or duplicated targets.
type SubmitRecognitionRequest struct {
	IdempotencyKey string
	Actor          string
	RecognitionRequest
	ExpectedPlanDigest string
	Conflict           RecognitionConflictChoice
}

// RecognitionOutcome is what a submit admitted.
type RecognitionOutcome struct {
	Operations []UserOperation
	// Renamed and Moved count admitted files by how they travel.
	Renamed int
	Moved   int
}

// recognitionFile is one selected file with the evidence parsed from its name.
type recognitionFile struct {
	inspection inspection
	kind       string
	rt         templatefacts.Values
	identity   episodeidentity.EpisodeIdentity
	media      naming.MediaNamingFacts
	// runtimeFactsErr blocks only this video and its bound subtitles. The raw
	// tool error stays server-side; the row exposes a stable blocker code.
	runtimeFactsErr error
}

// recognitionGroup is one video with its bound subtitles, one video-less
// subtitle (an error row) or one standalone other file.
type recognitionGroup struct {
	stem      string
	video     int
	subtitles []int
	others    []int
}

// recognitionOperation is one file recognition will move.
type recognitionOperation struct {
	row             int
	group           int
	videoRow        int
	fileIndex       int
	role            string
	source          string
	sourceSpellings []string
	target          string
	// suffix and extension compose a subtitle target; a versioned video's
	// subtitles are recomposed from its new stem.
	suffix    string
	extension string
}

// recognitionPlan is the rendered plan of one request.
type recognitionPlan struct {
	request      RecognitionRequest
	directory    string
	files        []recognitionFile
	groups       []recognitionGroup
	rows         []RecognitionRow
	operations   []recognitionOperation
	unknownRoute string
	digest       string
}

func normalizeRecognitionParser(value RecognitionParser) (parser.ParserSelection, error) {
	id := strings.TrimSpace(value.ID)
	if id == "" {
		id = parser.ParserReltitle
	}
	selection := parser.ParserSelection{ID: id, Config: append(json.RawMessage(nil), value.Config...)}
	if _, err := parser.DefaultRegistry().ValidateSelection(selection); err != nil {
		return parser.ParserSelection{}, invalid(CodeRecognitionInvalid, "parser selection is not valid")
	}
	return selection, nil
}

func validateRecognitionMatch(match *RecognitionMatch) error {
	if match == nil {
		return nil
	}
	// Three forms: a managed anime (optionally overlaid with the draft), the
	// draft alone naming with facts the user typed or matched from a metadata
	// provider — the same facts one-shot's task draft carries — or a bare
	// episode adjustment over a pure parse of the file names.
	if strings.TrimSpace(match.ManagedAnimeID) == "" && match.Anime == nil && match.Episode == nil && match.Offset == 0 {
		return invalid(CodeRecognitionInvalid, "a match needs an anime, a draft or an episode adjustment")
	}
	if match.Season != nil && (*match.Season < 0 || *match.Season > maxMatchSeason) {
		return invalid(CodeRecognitionInvalid, "season is out of range")
	}
	if match.Episode != nil && (*match.Episode < 1 || *match.Episode > maxMatchEpisode) {
		return invalid(CodeRecognitionInvalid, "episode is out of range")
	}
	if match.Offset < -maxMatchEpisode || match.Offset > maxMatchEpisode {
		return invalid(CodeRecognitionInvalid, "offset is out of range")
	}
	if draft := match.Anime; draft != nil {
		if err := validateRecognitionAnimeDraft(draft); err != nil {
			return err
		}
	}
	return nil
}

// validateRecognitionAnimeDraft bounds the facts one render may read: they are
// the same values an anime stores, so a render never names with something the
// library itself could not hold.
func validateRecognitionAnimeDraft(draft *RecognitionAnimeDraft) error {
	title := strings.TrimSpace(draft.Title)
	if title == "" || len([]byte(title)) > 512 {
		return invalid(CodeRecognitionInvalid, "the draft title is not usable")
	}
	if draft.MediaKind != "tv" && draft.MediaKind != "movie" {
		return invalid(CodeRecognitionInvalid, "the draft media kind is not tv or movie")
	}
	if draft.Year != nil && (*draft.Year < 1900 || *draft.Year > 2100) {
		return invalid(CodeRecognitionInvalid, "the draft year is out of range")
	}
	if draft.BGM != nil && strings.TrimSpace(draft.BGM.WorkID) == "" {
		return invalid(CodeRecognitionInvalid, "the draft Bangumi binding has no work")
	}
	if draft.TMDB != nil {
		if strings.TrimSpace(draft.TMDB.WorkID) == "" {
			return invalid(CodeRecognitionInvalid, "the draft TMDB binding has no work")
		}
		if draft.TMDB.MediaKind != "tv" && draft.TMDB.MediaKind != "movie" {
			return invalid(CodeRecognitionInvalid, "the draft TMDB kind is not tv or movie")
		}
		if draft.TMDB.Season < 0 {
			return invalid(CodeRecognitionInvalid, "the draft TMDB season is out of range")
		}
	}
	return nil
}

// recognitionRoute reads the instance unclassified-route policy; unwired is
// the documented default.
func (s *Service) recognitionRoute(ctx context.Context) (string, error) {
	if s.recognitionPolicy == nil {
		return string(filemaintenance.DefaultFileEffectPolicy()), nil
	}
	route, err := s.recognitionPolicy.UnclassifiedRoute(ctx)
	if err != nil {
		return "", err
	}
	if route == "" {
		return string(filemaintenance.DefaultFileEffectPolicy()), nil
	}
	if err := filemaintenance.ValidateFileEffectPolicy(filemaintenance.UnclassifiedRoutePolicyValue(route)); err != nil {
		return "", fmt.Errorf("unclassified route %q is not route or skip", route)
	}
	return route, nil
}

// recognitionEntries re-derives every selected file and refuses a selection
// recognition does not apply to: a directory, a managed or in-progress file,
// an executor namespace, two directories, duplicate paths.
func (s *Service) recognitionEntries(ctx context.Context, paths []string) ([]recognitionFile, string, error) {
	if len(paths) == 0 || len(paths) > maxRecognitionFiles {
		return nil, "", invalid(CodeRecognitionInvalid, "recognition takes 1..256 files")
	}
	seen := map[string]bool{}
	var directory string
	files := make([]recognitionFile, 0, len(paths))
	var locked []string
	for _, value := range paths {
		if strings.TrimSpace(value) == "" {
			return nil, "", invalid(CodeRecognitionInvalid, "recognition takes absolute file paths")
		}
		inspected, err := s.inspect(ctx, value)
		if err != nil {
			return nil, "", err
		}
		if seen[inspected.absolute] {
			return nil, "", invalid(CodeRecognitionInvalid, "a file is selected twice")
		}
		seen[inspected.absolute] = true
		entry := inspected.detail.Entry
		if entry.Kind != KindFile || internalNamespace(inspected.absolute) {
			return nil, "", invalid(CodeRecognitionInvalid, "recognition applies to plain files only")
		}
		if entry.Locked {
			locked = append(locked, inspected.absolute)
			continue
		}
		if directory == "" {
			directory = filepath.Dir(inspected.absolute)
		} else if filepath.Dir(inspected.absolute) != directory {
			return nil, "", conflict(CodeRecognitionMixedDirectory, "recognition takes files of one directory")
		}
		files = append(files, recognitionFile{
			inspection: inspected,
			kind:       filekinds.Classify(filekinds.Version, entry.Name),
		})
	}
	if len(locked) > 0 {
		refusal := conflict(CodeRecognitionRequiresDetached, "recognition applies to unmanaged files only")
		refusal.Paths = locked
		return nil, "", refusal
	}
	return files, directory, nil
}

// recognitionEvidence parses each file's own name: the episode identity of
// every non-subtitle file, and the rt.* values of every file. A subtitle's
// episode comes from its group's video; its own name supplies its rt values
// (its sub_lang tag) and the subtitle facts the resolver derives from it.
func recognitionEvidence(ctx context.Context, files []recognitionFile, selection parser.ParserSelection, offset int) error {
	resolver := episodeidentity.NewResolver()
	for index := range files {
		name := files[index].inspection.detail.Entry.Name
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		tags := reltitletags.Extract(stem)
		files[index].rt = templatefacts.ReltitleValues(&tags)
		if files[index].kind == filekinds.KindSubtitle {
			continue
		}
		parsed, err := parser.ParseEpisode(ctx, parser.DefaultRegistry(), selection, parser.EpisodeParseInput{Kind: parser.InputFileName, Text: name})
		if err != nil {
			return err
		}
		files[index].identity = resolver.Resolve(episodeidentity.EpisodeIdentityInput{
			MediaKind: "tv", FileRef: name,
			FileEvidence: parser.Evidence(parsed, parser.InputFileName), Offset: offset,
		})
	}
	return nil
}

// loadRecognitionRuntimeFacts probes every selected video separately. A failed
// probe is a row blocker, not an empty media fact; a missing prober follows the
// same fail-closed path. Subtitles inherit their video's facts while rendering.
func (s *Service) loadRecognitionRuntimeFacts(ctx context.Context, files []recognitionFile) error {
	for index := range files {
		if files[index].kind != filekinds.KindVideo {
			continue
		}
		if s.recognitionMedia == nil {
			files[index].runtimeFactsErr = errors.New("recognition media prober is not wired")
			continue
		}
		facts, err := s.recognitionMedia.Probe(ctx, files[index].inspection.absolute)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			files[index].runtimeFactsErr = err
			continue
		}
		files[index].media = naming.MediaNamingFacts{
			Resolution: facts.Resolution, VideoCodec: facts.VideoCodec,
			AudioCodec: facts.AudioCodec, Container: facts.Container, HDR: facts.HDR,
		}
	}
	return nil
}

// recognitionGroups binds each subtitle to the video whose name it starts
// with (the one subtitle rule), keeps every video as its own group and every
// other file as a standalone group. Two videos with one stem cannot both own
// the same subtitles: the later one is a standalone group.
func recognitionGroups(files []recognitionFile) []recognitionGroup {
	groups := make([]recognitionGroup, 0, len(files))
	stemGroup := map[string]int{}
	var videoStems []string
	var videoGroups []int
	for index, file := range files {
		if file.kind != filekinds.KindVideo {
			continue
		}
		name := file.inspection.detail.Entry.Name
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		if _, taken := stemGroup[stem]; taken {
			groups = append(groups, recognitionGroup{stem: stem, video: -1, others: []int{index}})
			continue
		}
		stemGroup[stem] = len(groups)
		videoStems = append(videoStems, stem)
		videoGroups = append(videoGroups, len(groups))
		groups = append(groups, recognitionGroup{stem: stem, video: index})
	}
	for index, file := range files {
		name := file.inspection.detail.Entry.Name
		switch file.kind {
		case filekinds.KindSubtitle:
			stem := strings.TrimSuffix(name, filepath.Ext(name))
			bound := filekinds.BindSubtitle(name, videoStems)
			if bound < 0 {
				groups = append(groups, recognitionGroup{stem: stem, video: -1, subtitles: []int{index}})
				continue
			}
			group := videoGroups[bound]
			groups[group].subtitles = append(groups[group].subtitles, index)
		case filekinds.KindVideo:
			// Videos own their group; a duplicate-stem video was added above.
		default:
			stem := strings.TrimSuffix(name, filepath.Ext(name))
			groups = append(groups, recognitionGroup{stem: stem, video: -1, others: []int{index}})
		}
	}
	return groups
}

// recognitionEpisode is the episode of one video: an episode the user chose
// (with the operation offset applied) is final; otherwise the parser's
// resolved identity, which already carries its own offset. nil means the video
// is unclassified.
func recognitionEpisode(identity episodeidentity.EpisodeIdentity, match *RecognitionMatch) *RecognitionEpisode {
	if match != nil && match.Episode != nil {
		number := *match.Episode + match.Offset
		if number < 0 {
			return nil
		}
		return &RecognitionEpisode{
			Raw: strconv.Itoa(*match.Episode), Number: strconv.Itoa(number),
			Offset: match.Offset, Source: "manual",
		}
	}
	if identity.State != episodeidentity.StateResolved || identity.Episode == nil || identity.RawEpisode == nil {
		return nil
	}
	episode := &RecognitionEpisode{
		Raw:    string(*identity.RawEpisode),
		Number: string(*identity.Episode),
		Offset: identity.Offset,
		Source: string(episodeidentity.EpisodeSourceFileParser),
	}
	if identity.EpisodeEnd != nil {
		episode.NumberEnd = string(*identity.EpisodeEnd)
	}
	return episode
}

func recognitionMatchID(match *RecognitionMatch) string {
	if match == nil {
		return ""
	}
	return match.ManagedAnimeID
}

func recognitionMatchSeason(match *RecognitionMatch) *int {
	if match == nil {
		return nil
	}
	return match.Season
}

// recognitionAnimeDraft maps the operation's temporary draft onto the render
// input. nil keeps the matched anime's stored rows.
func recognitionAnimeDraft(match *RecognitionMatch) *filemaintenance.RecognitionAnimeDraft {
	if match == nil || match.Anime == nil {
		return nil
	}
	draft := &filemaintenance.RecognitionAnimeDraft{
		Title:     match.Anime.Title,
		MediaKind: match.Anime.MediaKind,
		Year:      match.Anime.Year,
	}
	if match.Anime.BGM != nil {
		draft.BGMWorkID = match.Anime.BGM.WorkID
		draft.BGM = true
	}
	if match.Anime.TMDB != nil {
		draft.TMDBWorkID = match.Anime.TMDB.WorkID
		draft.TMDBMediaKind = match.Anime.TMDB.MediaKind
		draft.TMDBSeason = match.Anime.TMDB.Season
		draft.TMDB = true
	}
	return draft
}

func recognitionRenderInput(plan *recognitionPlan, file recognitionFile, episode *RecognitionEpisode) filemaintenance.RecognitionRenderInput {
	match := plan.request.Match
	input := filemaintenance.RecognitionRenderInput{
		Rules:            plan.request.Naming,
		UnknownRoute:     plan.unknownRoute == string(filemaintenance.UnclassifiedRoutePolicyValueRoute),
		FileName:         file.inspection.detail.Entry.Name,
		FileKind:         file.kind,
		ManagedAnimeID:   recognitionMatchID(match),
		Season:           recognitionMatchSeason(match),
		AnimeDraft:       recognitionAnimeDraft(match),
		Rt:               file.rt,
		Media:            file.media,
		LoadRuntimeFacts: plan.request.LoadRuntimeFacts,
	}
	if file.inspection.info != nil {
		input.SizeBytes = file.inspection.info.Size()
	}
	if episode != nil {
		input.Episode = episode.Number
		input.EpisodeEnd = episode.NumberEnd
		input.EpisodeRaw = episode.Raw
		input.EpisodeDisplay = episode.Number
		input.EpisodeOffset = episode.Offset
		input.EpisodeSource = episode.Source
	}
	return input
}

// renderRecognition renders one file's target and maps a render refusal to its
// row blocker.
func (s *Service) renderRecognition(ctx context.Context, input filemaintenance.RecognitionRenderInput) (filemaintenance.RecognitionRendered, *RecognitionBlocker, error) {
	rendered, err := s.recognition.RenderRecognition(ctx, input)
	if err == nil {
		return rendered, nil, nil
	}
	blocker, err := recognitionRenderBlocker(err)
	return filemaintenance.RecognitionRendered{}, blocker, err
}

// renderRecognitionSuffix renders a subtitle's suffix and maps a render refusal
// to its row blocker.
func (s *Service) renderRecognitionSuffix(ctx context.Context, input filemaintenance.RecognitionRenderInput) (string, *RecognitionBlocker, error) {
	value, err := s.recognition.RenderRecognitionSuffix(ctx, input)
	if err == nil {
		return value, nil, nil
	}
	blocker, err := recognitionRenderBlocker(err)
	return "", blocker, err
}

// recognitionRenderBlocker maps a typed render refusal to its row blocker; any
// other error stays an error.
func recognitionRenderBlocker(err error) (*RecognitionBlocker, error) {
	var renderErr *filemaintenance.RecognitionRenderError
	if !errors.As(err, &renderErr) {
		return nil, err
	}
	code := RecognitionBlockerTemplateInvalid
	switch renderErr.Code {
	case filemaintenance.RecognitionRenderFactsMissing:
		code = RecognitionBlockerFactsMissing
	case filemaintenance.RecognitionRenderTargetInvalid:
		code = RecognitionBlockerTargetInvalid
	case filemaintenance.RecognitionRenderRuntimeFacts:
		code = RecognitionBlockerRuntimeFacts
	}
	return &RecognitionBlocker{Code: code, Facts: renderErr.Facts}, nil
}

// recognitionPlan renders one request. It reads the filesystem and owner facts
// but writes nothing.
func (s *Service) recognitionPlan(ctx context.Context, request RecognitionRequest) (recognitionPlan, error) {
	if err := validateRecognitionMatch(request.Match); err != nil {
		return recognitionPlan{}, err
	}
	selection, err := normalizeRecognitionParser(request.Parser)
	if err != nil {
		return recognitionPlan{}, err
	}
	// A managed-anime match re-reads the anime; a draft-only match names with
	// the draft alone and touches nothing stored.
	if request.Match != nil && strings.TrimSpace(request.Match.ManagedAnimeID) != "" {
		if s.matchAnimes == nil {
			return recognitionPlan{}, conflict(CodeRecognitionUnavailable, "file manager match is not available")
		}
		anime, err := s.matchAnimes.MatchAnime(ctx, request.Match.ManagedAnimeID)
		if err != nil {
			return recognitionPlan{}, err
		}
		if anime == nil {
			return recognitionPlan{}, notFound(CodeMatchAnimeUnavailable, "anime does not exist")
		}
	}
	files, directory, err := s.recognitionEntries(ctx, request.Paths)
	if err != nil {
		return recognitionPlan{}, err
	}
	offset := 0
	if request.Match != nil {
		offset = request.Match.Offset
	}
	if err := recognitionEvidence(ctx, files, selection, offset); err != nil {
		return recognitionPlan{}, err
	}
	if request.LoadRuntimeFacts {
		if err := s.loadRecognitionRuntimeFacts(ctx, files); err != nil {
			return recognitionPlan{}, err
		}
	}
	videos := 0
	for _, file := range files {
		if file.kind == filekinds.KindVideo {
			videos++
		}
	}
	if request.Match != nil && request.Match.Episode != nil && videos != 1 {
		return recognitionPlan{}, invalid(CodeRecognitionInvalid, "a chosen episode applies to exactly one video")
	}
	route, err := s.recognitionRoute(ctx)
	if err != nil {
		return recognitionPlan{}, err
	}
	plan := recognitionPlan{request: request, directory: directory, files: files, groups: recognitionGroups(files), unknownRoute: route}
	s.buildRecognitionRows(ctx, &plan)
	if err := s.markRecognitionConflicts(ctx, &plan); err != nil {
		return recognitionPlan{}, err
	}
	plan.digest = recognitionPlanDigest(plan, selection, request)
	return plan, nil
}

// buildRecognitionRows renders every group into rows and records the
// operations a submit would admit. A file with no episode identity is the
// renderer's call — the unknown variant under the route, otherwise left alone
// — because whether it is a movie is a naming-fact decision.
func (s *Service) buildRecognitionRows(ctx context.Context, plan *recognitionPlan) {
	for groupIndex, group := range plan.groups {
		switch {
		case group.video >= 0:
			episode := recognitionEpisode(plan.files[group.video].identity, plan.request.Match)
			videoRowIndex := len(plan.rows)
			videoRow := s.recognitionRenderRow(ctx, plan, groupIndex, group.video, episode)
			plan.rows = append(plan.rows, videoRow)
			if recognitionActionable(videoRow) {
				s.recordRecognitionOperation(plan, videoRowIndex, groupIndex, videoRowIndex, filekinds.KindVideo, group.video, videoRow.TargetPath, "", "")
			}
			for _, index := range group.subtitles {
				subtitleRowIndex := len(plan.rows)
				var row RecognitionRow
				switch {
				case videoRow.Disposition == RecognitionSkip:
					// The subtitle follows its video: it is left alone too.
					file := plan.files[index]
					row = RecognitionRow{
						Path: file.inspection.absolute, Name: file.inspection.detail.Entry.Name,
						Kind: filekinds.KindSubtitle, Group: group.stem,
						Revision: file.inspection.detail.Revision, Disposition: RecognitionSkip,
					}
				case videoRow.TargetPath == "":
					file := plan.files[index]
					row = RecognitionRow{
						Path: file.inspection.absolute, Name: file.inspection.detail.Entry.Name,
						Kind: filekinds.KindSubtitle, Group: group.stem,
						Revision: file.inspection.detail.Revision, Disposition: RecognitionError,
						Blockers: []RecognitionBlocker{{Code: RecognitionBlockerVideoFailed}},
					}
				default:
					row = s.recognitionSubtitleRow(ctx, plan, groupIndex, index, episode, videoRow.TargetPath)
				}
				plan.rows = append(plan.rows, row)
				if recognitionActionable(row) {
					s.recordRecognitionOperation(plan, subtitleRowIndex, groupIndex, videoRowIndex, filekinds.KindSubtitle, index, row.TargetPath, row.subtitleSuffix, row.subtitleExtension)
				}
			}
		case len(group.subtitles) > 0:
			// A subtitle whose video is not selected cannot be named: its target
			// is the video's name, which this operation does not have.
			for _, index := range group.subtitles {
				plan.rows = append(plan.rows, RecognitionRow{
					Path: plan.files[index].inspection.absolute, Name: plan.files[index].inspection.detail.Entry.Name,
					Kind: filekinds.KindSubtitle, Group: group.stem,
					Revision:    plan.files[index].inspection.detail.Revision,
					Disposition: RecognitionError,
					Blockers:    []RecognitionBlocker{{Code: RecognitionBlockerVideoMissing}},
				})
			}
		default:
			for _, index := range group.others {
				rowIndex := len(plan.rows)
				row := s.recognitionRenderRow(ctx, plan, groupIndex, index, nil)
				plan.rows = append(plan.rows, row)
				if recognitionActionable(row) {
					s.recordRecognitionOperation(plan, rowIndex, groupIndex, -1, filekinds.KindOther, index, row.TargetPath, "", "")
				}
			}
		}
	}
}

// recognitionActionable reports a row this plan would move or rename.
func recognitionActionable(row RecognitionRow) bool {
	return row.Disposition == RecognitionRename || row.Disposition == RecognitionMove
}

// recordRecognitionOperation remembers one file a submit would move.
func (s *Service) recordRecognitionOperation(plan *recognitionPlan, row, group, videoRow int, role string, fileIndex int, target, suffix, extension string) {
	file := plan.files[fileIndex]
	plan.operations = append(plan.operations, recognitionOperation{
		row: row, group: group, videoRow: videoRow, fileIndex: fileIndex, role: role,
		source: file.inspection.absolute, sourceSpellings: file.inspection.spellings,
		target: target, suffix: suffix, extension: extension,
	})
}

// recognitionRenderRow renders one file's target with the operation's own
// naming rules and resolves it against the instance's writable space.
func (s *Service) recognitionRenderRow(ctx context.Context, plan *recognitionPlan, groupIndex, fileIndex int, episode *RecognitionEpisode) RecognitionRow {
	file := plan.files[fileIndex]
	row := RecognitionRow{
		Path: file.inspection.absolute, Name: file.inspection.detail.Entry.Name,
		Kind: file.kind, Group: plan.groups[groupIndex].stem,
		Revision: file.inspection.detail.Revision,
		Episode:  episode, Disposition: RecognitionRename,
	}
	if file.runtimeFactsErr != nil {
		row.Disposition = RecognitionError
		row.Blockers = []RecognitionBlocker{{Code: RecognitionBlockerRuntimeFacts}}
		return row
	}
	rendered, blocker, err := s.renderRecognition(ctx, recognitionRenderInput(plan, file, episode))
	if err != nil {
		row.Disposition = RecognitionError
		row.Blockers = []RecognitionBlocker{{Code: RecognitionBlockerTemplateInvalid}}
		return row
	}
	if blocker != nil {
		row.Disposition = RecognitionError
		row.Blockers = []RecognitionBlocker{*blocker}
		return row
	}
	if rendered.Skipped {
		row.Disposition = RecognitionSkip
		return row
	}
	if plan.request.LoadRuntimeFacts {
		row.EmptyFacts = append([]string(nil), rendered.EmptyFacts...)
	}
	target := joinAbsolute(rendered.Directory, rendered.Filename)
	problem, err := s.recognitionTargetProblem(ctx, target)
	if err != nil {
		row.Disposition = RecognitionError
		row.Blockers = []RecognitionBlocker{{Code: RecognitionBlockerTemplateInvalid}}
		return row
	}
	if problem != nil {
		row.Disposition = RecognitionError
		row.Blockers = []RecognitionBlocker{*problem}
		return row
	}
	row.TargetName = rendered.Filename
	row.TargetPath = target
	// A target in another directory is a move; the same absolute path the file
	// already has still reads as unchanged.
	if filepath.Dir(row.TargetPath) != filepath.Dir(file.inspection.absolute) {
		row.Disposition = RecognitionMove
	}
	if row.TargetPath == file.inspection.absolute {
		row.Disposition = RecognitionUnchanged
	}
	return row
}

// recognitionSubtitleRow renders a subtitle's suffix over the same facts as its
// video and composes the target from the video's rendered path.
func (s *Service) recognitionSubtitleRow(ctx context.Context, plan *recognitionPlan, groupIndex, fileIndex int, episode *RecognitionEpisode, videoTarget string) RecognitionRow {
	file := plan.files[fileIndex]
	row := RecognitionRow{
		Path: file.inspection.absolute, Name: file.inspection.detail.Entry.Name,
		Kind: filekinds.KindSubtitle, Group: plan.groups[groupIndex].stem,
		Revision: file.inspection.detail.Revision, Episode: episode,
		Disposition: RecognitionRename,
	}
	input := recognitionRenderInput(plan, file, episode)
	// The suffix follows the video's execution-time media facts; a subtitle is
	// never probed as if it were a video of its own.
	input.Media = plan.files[plan.groups[groupIndex].video].media
	suffix, blocker, err := s.renderRecognitionSuffix(ctx, input)
	if err != nil {
		row.Disposition = RecognitionError
		row.Blockers = []RecognitionBlocker{{Code: RecognitionBlockerTemplateInvalid}}
		return row
	}
	if blocker != nil {
		row.Disposition = RecognitionError
		row.Blockers = []RecognitionBlocker{*blocker}
		return row
	}
	// The subtitle follows its video: the sidecar name composes onto the
	// video's basename inside the video's own target directory.
	stem := strings.TrimSuffix(path.Base(videoTarget), path.Ext(videoTarget))
	extension := strings.TrimPrefix(filepath.Ext(file.inspection.detail.Entry.Name), ".")
	name := naming.CompanionSidecarFilename(stem, suffix, extension)
	target := joinAbsolute(filepath.Dir(videoTarget), name)
	if problem, err := s.recognitionTargetProblem(ctx, target); err != nil {
		row.Disposition = RecognitionError
		row.Blockers = []RecognitionBlocker{{Code: RecognitionBlockerTemplateInvalid}}
		return row
	} else if problem != nil {
		row.Disposition = RecognitionError
		row.Blockers = []RecognitionBlocker{*problem}
		return row
	}
	row.TargetName = name
	row.TargetPath = target
	row.subtitleSuffix = suffix
	row.subtitleExtension = extension
	if filepath.Dir(row.TargetPath) != filepath.Dir(file.inspection.absolute) {
		row.Disposition = RecognitionMove
	}
	if row.TargetPath == file.inspection.absolute {
		row.Disposition = RecognitionUnchanged
	}
	return row
}

// recognitionTargetProblem checks that a rendered target is a destination this
// instance may write: an absolute path outside the denial zone and the
// executor namespaces, inside one of the file manager's authorized roots. A
// path outside the roots is the row's own blocker; any other refusal is a
// system fault.
func (s *Service) recognitionTargetProblem(ctx context.Context, target string) (*RecognitionBlocker, error) {
	if target == "" || strings.IndexByte(target, 0) >= 0 || strings.ContainsRune(target, '\\') {
		return &RecognitionBlocker{Code: RecognitionBlockerTargetInvalid}, nil
	}
	if !filepath.IsAbs(target) || path.Base(target) == "." || path.Base(target) == "/" {
		return &RecognitionBlocker{Code: RecognitionBlockerTargetInvalid}, nil
	}
	if refuseDenied(s.zone, target) != nil || internalNamespace(target) {
		return &RecognitionBlocker{Code: RecognitionBlockerTargetInvalid}, nil
	}
	err := s.authorize(ctx, target)
	if err == nil {
		return nil, nil
	}
	var refused *Error
	if errors.As(err, &refused) && refused.Code == CodePathOutsideRoots {
		return &RecognitionBlocker{Code: RecognitionBlockerOutsideRoots}, nil
	}
	return nil, err
}

// markRecognitionConflicts checks every target this request would write: an
// existing file, a path an owner holds, or another row of the same request.
func (s *Service) markRecognitionConflicts(ctx context.Context, plan *recognitionPlan) error {
	claimed := map[string]int{}
	for index := range plan.rows {
		row := &plan.rows[index]
		if row.Disposition != RecognitionRename && row.Disposition != RecognitionMove {
			continue
		}
		if other, taken := claimed[row.TargetPath]; taken {
			// Two rows render one target: both conflict, so neither silently wins.
			if plan.rows[other].Conflict == nil {
				plan.rows[other].Conflict = &RecognitionConflict{OccupiedBy: OccupantDuplicate}
			}
			row.Conflict = &RecognitionConflict{OccupiedBy: OccupantDuplicate}
			continue
		}
		occupant, err := s.occupant(ctx, resolvedTarget{absolute: row.TargetPath, spellings: absoluteSpellings(row.TargetPath)})
		if err != nil {
			return err
		}
		if occupant != "" {
			row.Conflict = &RecognitionConflict{OccupiedBy: occupant}
			continue
		}
		claimed[row.TargetPath] = index
	}
	used := map[string]bool{}
	for target := range claimed {
		used[target] = true
	}
	for index := range plan.rows {
		row := &plan.rows[index]
		if row.Conflict == nil {
			continue
		}
		candidate, err := s.recognitionVersionCandidate(ctx, row.TargetPath, used)
		if err != nil {
			return err
		}
		row.Conflict.VersionPath = candidate
		if candidate != "" {
			used[candidate] = true
		}
	}
	return nil
}

// recognitionVersionCandidate is the advisory free " (N)" sibling of one
// target, skipping what this request already claims.
func (s *Service) recognitionVersionCandidate(ctx context.Context, target string, used map[string]bool) (string, error) {
	name := filepath.Base(target)
	extension := path.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	directory := filepath.Dir(target)
	for index := 2; index <= maxVersionProbe; index++ {
		absolute := joinAbsolute(directory, stem+" ("+strconv.Itoa(index)+")"+extension)
		if used[absolute] {
			continue
		}
		occupant, err := s.occupant(ctx, resolvedTarget{absolute: absolute, spellings: absoluteSpellings(absolute)})
		if err != nil {
			return "", err
		}
		if occupant == "" {
			return absolute, nil
		}
	}
	return "", nil
}

// recognitionPlanDigest identifies the rendered plan a submit confirms. It
// covers the request and every row's source revision, target and disposition;
// it never covers the conflict choice, which settles the same plan differently.
func recognitionPlanDigest(plan recognitionPlan, selection parser.ParserSelection, request RecognitionRequest) string {
	type blockerDigest struct {
		Code  string
		Facts []string
	}
	type rowDigest struct {
		Path        string
		Revision    string
		Target      string
		Disposition string
		EmptyFacts  []string
		Blockers    []blockerDigest
	}
	rows := make([]rowDigest, 0, len(plan.rows))
	for _, row := range plan.rows {
		blockers := make([]blockerDigest, 0, len(row.Blockers))
		for _, blocker := range row.Blockers {
			blockers = append(blockers, blockerDigest{Code: blocker.Code, Facts: blocker.Facts})
		}
		rows = append(rows, rowDigest{Path: row.Path, Revision: row.Revision, Target: row.TargetPath, Disposition: string(row.Disposition), EmptyFacts: row.EmptyFacts, Blockers: blockers})
	}
	return requestDigest(struct {
		Directory        string
		ParserID         string
		ParserConfig     string
		Naming           naming.NamingRules
		UnknownRoute     string
		LoadRuntimeFacts bool
		Match            recognitionMatchDigest
		Rows             []rowDigest
	}{plan.directory, selection.ID, string(selection.Config), request.Naming, plan.unknownRoute, request.LoadRuntimeFacts, digestRecognitionMatch(request.Match), rows})
}

type recognitionMatchDigest struct {
	Anime   string
	Season  *int
	Episode *int
	Offset  int
	Draft   recognitionAnimeDigest
}

type recognitionAnimeDigest struct {
	Title      string
	MediaKind  string
	Year       *int
	BGM        bool
	BGMWorkID  string
	TMDB       bool
	TMDBWorkID string
	TMDBKind   string
	TMDBSeason int
}

func digestRecognitionMatch(match *RecognitionMatch) recognitionMatchDigest {
	if match == nil {
		return recognitionMatchDigest{}
	}
	digest := recognitionMatchDigest{Anime: match.ManagedAnimeID, Season: match.Season, Episode: match.Episode, Offset: match.Offset}
	if match.Anime != nil {
		digest.Draft = recognitionAnimeDigest{
			Title: match.Anime.Title, MediaKind: match.Anime.MediaKind, Year: match.Anime.Year,
		}
		if match.Anime.BGM != nil {
			digest.Draft.BGM, digest.Draft.BGMWorkID = true, match.Anime.BGM.WorkID
		}
		if match.Anime.TMDB != nil {
			digest.Draft.TMDB = true
			digest.Draft.TMDBWorkID, digest.Draft.TMDBKind, digest.Draft.TMDBSeason = match.Anime.TMDB.WorkID, match.Anime.TMDB.MediaKind, match.Anime.TMDB.Season
		}
	}
	return digest
}

// recognitionRequestDigest is the idempotency identity of one recognition
// request; it does not depend on what the filesystem looked like when it ran.
func recognitionRequestDigest(request SubmitRecognitionRequest) string {
	return requestDigest(struct {
		Paths            []string
		ParserID         string
		ParserConfig     string
		Naming           naming.NamingRules
		LoadRuntimeFacts bool
		Match            recognitionMatchDigest
		Conflict         RecognitionConflictChoice
	}{request.Paths, request.Parser.ID, string(request.Parser.Config), request.Naming, request.LoadRuntimeFacts, digestRecognitionMatch(request.Match), request.Conflict})
}

// PreviewRecognition renders the plan of one recognition request and writes
// nothing.
func (s *Service) PreviewRecognition(ctx context.Context, request RecognitionRequest) (RecognitionPreview, error) {
	if err := s.requireRecognition(); err != nil {
		return RecognitionPreview{}, err
	}
	plan, err := s.recognitionPlan(ctx, request)
	if err != nil {
		return RecognitionPreview{}, err
	}
	preview := RecognitionPreview{Directory: plan.directory, Rows: plan.rows, UnknownRoute: plan.unknownRoute, PlanDigest: plan.digest}
	// The match's title candidate: the title the parser read from the first
	// video's own name, the same read a search seed's title hint carries.
	for _, file := range plan.files {
		if file.kind != filekinds.KindVideo {
			continue
		}
		if title, ok := file.rt.Get("Title"); ok && title != "" {
			preview.TitleHint = title
			break
		}
	}
	for _, row := range plan.rows {
		if row.Conflict != nil {
			preview.Conflicts = append(preview.Conflicts, row.TargetPath)
		}
	}
	return preview, nil
}

// SubmitRecognition admits the confirmed plan as one batch of ordinary
// move/rename user operations. The whole batch is admitted in one transaction;
// block admits nothing, skip drops the conflicting rows (and a conflicting
// video's subtitles), and version assigns each row a free sibling name before
// admission.
func (s *Service) SubmitRecognition(ctx context.Context, request SubmitRecognitionRequest) (RecognitionOutcome, error) {
	if err := s.requireCommands(); err != nil {
		return RecognitionOutcome{}, err
	}
	if err := s.requireRecognition(); err != nil {
		return RecognitionOutcome{}, err
	}
	if err := validKey(request.IdempotencyKey); err != nil {
		return RecognitionOutcome{}, err
	}
	// Per-file keys are the batch key plus ":" plus a digest; a batch key that
	// already carries one would make the batch's operations ambiguous.
	if strings.Contains(request.IdempotencyKey, ":") {
		return RecognitionOutcome{}, invalid(CodeIdempotencyKeyInvalid, "idempotency key is required")
	}
	choice := request.Conflict
	if choice == "" {
		choice = RecognitionConflictBlock
	}
	switch choice {
	case RecognitionConflictBlock, RecognitionConflictSkip, RecognitionConflictVersion, RecognitionConflictOverwrite:
	default:
		return RecognitionOutcome{}, invalid(CodeRecognitionConflictInvalid, "conflict choice is not valid")
	}
	request.Conflict = choice
	digest := recognitionRequestDigest(request)
	if operations, stored, err := s.commands.OperationBatch(ctx, request.IdempotencyKey); err != nil {
		return RecognitionOutcome{}, err
	} else if len(operations) > 0 {
		if stored != digest {
			return RecognitionOutcome{}, conflict(CodeIdempotencyConflict, "idempotency key was used for another request")
		}
		return recognitionOutcome(operations), nil
	}
	plan, err := s.recognitionPlan(ctx, request.RecognitionRequest)
	if err != nil {
		return RecognitionOutcome{}, err
	}
	if request.ExpectedPlanDigest == "" || request.ExpectedPlanDigest != plan.digest {
		return RecognitionOutcome{}, conflict(CodeRecognitionPlanChanged, "the plan changed since the preview")
	}
	for _, row := range plan.rows {
		if row.Disposition == RecognitionError {
			return RecognitionOutcome{}, conflict(CodeRecognitionNotRenderable, "a file cannot be rendered: "+row.Name)
		}
	}
	admissions, createdDirs, err := s.recognitionAdmissions(ctx, plan, request, digest, choice)
	if err != nil {
		return RecognitionOutcome{}, err
	}
	if len(admissions) == 0 {
		return RecognitionOutcome{}, invalid(CodeRecognitionNothingToDo, "recognition would not move any file")
	}
	operations, err := s.commands.AdmitOperations(ctx, admissions)
	if err != nil {
		removeCreatedDirectories(createdDirs)
		return RecognitionOutcome{}, err
	}
	return recognitionOutcome(operations), nil
}

func recognitionOutcome(operations []UserOperation) RecognitionOutcome {
	outcome := RecognitionOutcome{Operations: operations}
	for _, operation := range operations {
		if operation.Operation == UserMove {
			outcome.Moved++
		} else {
			outcome.Renamed++
		}
	}
	return outcome
}

// recognitionAdmissions turns the plan's operations into frozen admissions.
// Under skip, a conflicting row is left out and a conflicting video also
// leaves out its subtitles. Under version, every target is made distinct
// before admission and a versioned video's subtitles follow its new stem.
// Under overwrite, only rows conflicting with a plain file are admitted, each
// with the overwrite policy; a managed, in-progress or duplicated target
// refuses the whole submit.
func (s *Service) recognitionAdmissions(ctx context.Context, plan recognitionPlan, request SubmitRecognitionRequest, digest string, choice RecognitionConflictChoice) ([]OperationAdmission, []string, error) {
	drop := map[int]bool{}
	switch choice {
	case RecognitionConflictSkip:
		for index, row := range plan.rows {
			if row.Conflict != nil {
				drop[index] = true
			}
		}
		for _, operation := range plan.operations {
			if operation.videoRow >= 0 && drop[operation.videoRow] {
				drop[operation.row] = true
			}
		}
	case RecognitionConflictBlock:
		var paths []string
		for _, row := range plan.rows {
			if row.Conflict != nil {
				paths = append(paths, row.TargetPath)
			}
		}
		if len(paths) > 0 {
			if len(paths) > maxLockedPaths {
				paths = paths[:maxLockedPaths]
			}
			refusal := conflict(CodeTargetExists, "a target is occupied")
			refusal.Paths = paths
			return nil, nil, refusal
		}
	case RecognitionConflictOverwrite:
		var paths []string
		for _, row := range plan.rows {
			if row.Conflict != nil && row.Conflict.OccupiedBy != OccupantFile {
				paths = append(paths, row.TargetPath)
			}
		}
		if len(paths) > 0 {
			if len(paths) > maxLockedPaths {
				paths = paths[:maxLockedPaths]
			}
			refusal := conflict(CodeTargetExists, "a managed, in-progress or duplicated target is never overwritten")
			refusal.Paths = paths
			return nil, nil, refusal
		}
	}
	// A move into a deeper directory names directories the executor never
	// creates, so the operation creates every missing level first.
	targetDirectories := []string{}
	seenDirectory := map[string]bool{}
	for _, operation := range plan.operations {
		if drop[operation.row] {
			continue
		}
		directory := filepath.Dir(operation.target)
		if directory != filepath.Dir(operation.source) && !seenDirectory[directory] {
			seenDirectory[directory] = true
			targetDirectories = append(targetDirectories, directory)
		}
	}
	sort.Strings(targetDirectories)
	createdDirs := []string{}
	for _, directory := range targetDirectories {
		created, err := s.ensureRecognitionDirectory(directory)
		if err != nil {
			removeCreatedDirectories(createdDirs)
			return nil, nil, err
		}
		createdDirs = append(createdDirs, created...)
	}
	claimed := map[string]bool{}
	final := map[int]string{}
	admissions := make([]OperationAdmission, 0, len(plan.operations))
	for _, operation := range plan.operations {
		if drop[operation.row] {
			continue
		}
		directory := filepath.Dir(operation.target)
		name := filepath.Base(operation.target)
		if choice == RecognitionConflictVersion && operation.role == filekinds.KindSubtitle {
			if videoFinal, ok := final[operation.group]; ok {
				// The subtitles follow the video's final stem, never its
				// originally rendered one.
				stem := strings.TrimSuffix(filepath.Base(videoFinal), path.Ext(filepath.Base(videoFinal)))
				name = naming.CompanionSidecarFilename(stem, operation.suffix, operation.extension)
			}
		}
		var target string
		var err error
		if choice == RecognitionConflictVersion || claimed[joinAbsolute(directory, name)] {
			target, err = s.freeRecognitionTarget(ctx, directory, name, claimed)
		} else {
			target = joinAbsolute(directory, name)
		}
		if err != nil {
			removeCreatedDirectories(createdDirs)
			return nil, nil, err
		}
		claimed[target] = true
		if operation.role == filekinds.KindVideo {
			final[operation.group] = target
		}
		kind := UserRename
		if filepath.Dir(target) != filepath.Dir(operation.source) {
			kind = UserMove
		}
		collision := ConflictBlock
		switch {
		case choice == RecognitionConflictVersion:
			collision = ConflictVersion
		case choice == RecognitionConflictOverwrite && plan.rows[operation.row].Conflict != nil:
			collision = ConflictOverwrite
		}
		admission := OperationAdmission{
			ID: uuid.NewString(), IdempotencyKey: recognitionOperationKey(request.IdempotencyKey, operation.source),
			Digest: digest, Actor: request.Actor, Operation: kind,
			SourcePath: operation.source, SourceSpellings: operation.sourceSpellings,
			TargetPath: target, TargetSpellings: absoluteSpellings(target),
			Collision: collision, RequireDetachedSource: true,
		}
		if info := plan.files[operation.fileIndex].inspection.info; info != nil {
			admission.ExpectedSize = info.Size()
		}
		admissions = append(admissions, admission)
	}
	return admissions, createdDirs, nil
}

// ensureRecognitionDirectory makes directory exist, creating every missing
// level (deepest last in the returned list) and verifying each level is a real
// directory, never a symlink. The target's authorization was decided when the
// plan rendered it; the walk only repeats the denial-zone and namespace refusal.
func (s *Service) ensureRecognitionDirectory(directory string) ([]string, error) {
	if !filepath.IsAbs(directory) || path.Clean(directory) == "/" || path.Clean(directory) == "." {
		return nil, invalid(CodeTargetInvalid, "target directory is not usable")
	}
	created := []string{}
	current := string(filepath.Separator)
	for _, segment := range strings.Split(strings.TrimPrefix(path.Clean(directory), "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return nil, invalid(CodeTargetInvalid, "target directory is not usable")
		}
		current = joinAbsolute(current, segment)
		if refuseDenied(s.zone, current) != nil || internalNamespace(current) {
			return nil, invalid(CodeTargetInvalid, "target directory is not usable")
		}
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return nil, fsError(err, current)
			}
			created = append(created, current)
			continue
		}
		if err != nil {
			return nil, fsError(err, current)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, invalid(CodeTargetInvalid, "target directory is not usable")
		}
	}
	return created, nil
}

// removeCreatedDirectories removes directories a submit created when a later
// step refused, deepest first; a level that took a file in the meantime stays.
func removeCreatedDirectories(created []string) {
	for index := len(created) - 1; index >= 0; index-- {
		_ = os.Remove(created[index])
	}
}

// freeRecognitionTarget returns the first free sibling of one rendered name.
func (s *Service) freeRecognitionTarget(ctx context.Context, directory, name string, claimed map[string]bool) (string, error) {
	extension := path.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	for index := 1; index <= maxVersionProbe+1; index++ {
		candidateName := name
		if index > 1 {
			candidateName = stem + " (" + strconv.Itoa(index) + ")" + extension
		}
		absolute := joinAbsolute(directory, candidateName)
		if claimed[absolute] {
			continue
		}
		occupant, err := s.occupant(ctx, resolvedTarget{absolute: absolute, spellings: absoluteSpellings(absolute)})
		if err != nil {
			return "", err
		}
		if occupant == "" {
			return absolute, nil
		}
	}
	return "", conflict(CodeTargetExists, "no free version of the target")
}

// recognitionOperationKey derives one file's idempotency key from the batch
// key and its source path.
func recognitionOperationKey(batch, source string) string {
	return batch + ":" + requestDigest(source)[:16]
}
