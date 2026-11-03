package filemaintenance

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"strings"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/namingvars"
	namingvarspersistence "bansoko/internal/namingvars/adapter/persistence"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/templatefacts"
)

// File manager recognition: a supplied video filename template (or subtitle
// suffix template) rendered over the facts of one local file. It is the naming
// core one-shot step two renders with, under a different input adapter: the
// episode comes from the caller's own parser evidence over the file name, and
// the release-level rt.* values come from parsing that same name. Nothing here
// reads a placement, an artifact or a stored naming rule: the template is the
// operation's own, and the match (when the user chose one) supplies only the
// anime facts the template asks for.
//
// The naming engine stays the only authority for what a target may be: a
// rendered filename must be one non-empty path segment (the file's own folder
// member), and a subtitle suffix must not escape the video's basename. A
// template that reads a fact this render does not have is a typed diagnostic,
// never a guessed value.

// Recognition render failure codes (RecognitionRenderError.Code).
const (
	// RecognitionRenderFactsMissing: the template reads a fact this render
	// does not carry (no match for an anime fact, an unknown Vars name).
	RecognitionRenderFactsMissing = "facts_missing"
	// RecognitionRenderTemplateInvalid: the template does not compile.
	RecognitionRenderTemplateInvalid = "template_invalid"
	// RecognitionRenderTargetInvalid: the rendered value is not a usable
	// member of the file's folder (empty, contains a separator, traversal).
	RecognitionRenderTargetInvalid = "target_invalid"
)

// RecognitionRenderError is a render that cannot produce a value. It is a
// typed diagnostic for the plan preview, never a system fault.
type RecognitionRenderError struct {
	Code string
	// Facts are the catalog paths a facts_missing render lacks.
	Facts []string
	cause error
}

func (e *RecognitionRenderError) Error() string {
	if e.cause != nil {
		return "recognition render " + e.Code + ": " + e.cause.Error()
	}
	return "recognition render " + e.Code
}

func (e *RecognitionRenderError) Unwrap() error { return e.cause }

// RecognitionRenderInput is one file's render input. Kind selects the
// template kind: naming.KindVideoFilename for a file's own name (video or
// other) or naming.KindSubtitleSuffix for a subtitle's suffix.
type RecognitionRenderInput struct {
	Kind     naming.TemplateKind
	Template string

	// FileName is the current base name, extension included.
	FileName  string
	SizeBytes int64
	// FileKind is the naming file kind: video, subtitle or other.
	FileKind string

	// ManagedAnimeID is the optional match; empty renders with no anime at
	// all, so every anime.* fact is undefined.
	ManagedAnimeID string
	// Season is the season chosen for the operation; nil keeps the matched
	// anime's owner-edited season.
	Season *int
	// AnimeDraft is the settings column's temporary draft: the anime facts
	// this render names with, initialized from the matched anime. nil reads
	// the anime's stored rows.
	AnimeDraft *RecognitionAnimeDraft

	// Episode facts resolved by the caller's parser. Episode is the canonical
	// final number, already offset. Empty Episode means no episode identity
	// (ep.* is then undefined).
	Episode        string
	EpisodeEnd     string
	EpisodeRaw     string
	EpisodeDisplay string
	EpisodeOffset  int
	EpisodeSpecial string
	// EpisodeSource overrides ep.source; empty reads "file_parser" (the
	// caller's parser produced the episode). A user-chosen episode passes
	// "manual".
	EpisodeSource string

	// Rt is the release-level namespace parsed from the file's own name.
	Rt templatefacts.Values
}

// RecognitionAnimeDraft is the anime facts one recognition render names with.
// Every value is the draft's own, so the render never mixes a draft value with
// a re-read of the anime; a false binding flag means this render has none.
type RecognitionAnimeDraft struct {
	Title         string
	MediaKind     string
	Year          *int
	BGM           bool
	BGMWorkID     string
	TMDB          bool
	TMDBWorkID    string
	TMDBMediaKind string
	TMDBSeason    int
}

// RenderRecognition renders one supplied template over the input's facts and
// returns the rendered value. For KindVideoFilename the value is the target
// file name; for KindSubtitleSuffix it is the suffix the caller composes onto
// the rendered video basename.
func (r *NamingTargetRenderer) RenderRecognition(ctx context.Context, in RecognitionRenderInput) (string, error) {
	fileName := path.Base(strings.ReplaceAll(strings.TrimSpace(in.FileName), "\\", "/"))
	if fileName == "" || fileName == "." || fileName == ".." || fileName == "/" {
		return "", errors.New("recognition render requires a file name")
	}
	if in.Kind != naming.KindVideoFilename && in.Kind != naming.KindSubtitleSuffix {
		return "", errors.New("recognition render kind is not video_filename or subtitle_suffix")
	}
	if strings.TrimSpace(in.Template) == "" && in.Kind == naming.KindVideoFilename {
		return "", &RecognitionRenderError{Code: RecognitionRenderTemplateInvalid, cause: errors.New("video filename template is empty")}
	}

	// Presentation Vars belong to the deployment, not to the file: a
	// recognition render uses the current snapshot like a template preview.
	// An uninitialized snapshot leaves an empty Vars object, so a template
	// reading vars.* reports it unknown while every other template renders.
	vars := map[string]map[string]string{}
	if current, _, err := namingvars.Current(); err == nil {
		vars = current
	}
	engine := naming.NewEngine()
	compiled, err := engine.CompileWithVars(in.Kind, in.Template, vars)
	if err != nil {
		return "", classifyRecognitionRender(err)
	}

	namingContext, err := r.recognitionContext(ctx, in, fileName)
	if err != nil {
		return "", err
	}
	namingContext, err = naming.WithVars(namingContext, vars)
	if err != nil {
		return "", &RecognitionRenderError{Code: RecognitionRenderFactsMissing, cause: err}
	}
	resolved := naming.DefaultRegistry().ResolveAll(naming.ResolverInput{Facts: namingContext})
	// The special classification is the resolver's reading of the file's own
	// name; a caller with its own parser evidence may state it instead.
	if in.EpisodeSpecial != "" {
		resolved.Context.Parsed.Episode.SpecialKind = in.EpisodeSpecial
	}
	value, err := engine.Render(compiled, resolved.Context)
	if err != nil {
		return "", classifyRecognitionRender(err)
	}
	return value, nil
}

// recognitionContext assembles the naming context of one file. Only the
// operation's own inputs and, for a match, the anime's current rows are read.
func (r *NamingTargetRenderer) recognitionContext(ctx context.Context, in RecognitionRenderInput, fileName string) (naming.NamingContext, error) {
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(fileName)), ".")
	namingContext := naming.NamingContext{
		File: naming.FileNamingFacts{
			OriginalName: fileName,
			Basename:     strings.TrimSuffix(fileName, filepath.Ext(fileName)),
			Extension:    extension,
			SizeBytes:    in.SizeBytes,
			Kind:         in.FileKind,
		},
		Release: naming.ReleaseNamingFacts{
			// The file's own name is the only release-level evidence this
			// render has; it is what the parser read the episode and the
			// rt.* values from.
			OriginalTitle: strings.TrimSuffix(fileName, filepath.Ext(fileName)),
		},
		AnimeAbsent: in.ManagedAnimeID == "" && in.AnimeDraft == nil,
	}
	if episode := strings.TrimSpace(in.Episode); episode != "" {
		display := strings.TrimSpace(in.EpisodeDisplay)
		if display == "" {
			display = episode
		}
		source := strings.TrimSpace(in.EpisodeSource)
		if source == "" {
			source = string(episodeidentity.EpisodeSourceFileParser)
		}
		namingContext.Release.FrozenEpisode = &naming.FrozenEpisodeNamingFacts{
			Raw:       in.EpisodeRaw,
			Number:    episode,
			Offset:    in.EpisodeOffset,
			Display:   display,
			NumberEnd: in.EpisodeEnd,
			Source:    source,
		}
	}
	if in.Rt != nil {
		namingContext = naming.WithTemplateFacts(namingContext, templatefacts.Facts{Reltitle: in.Rt.Clone()})
	}
	if in.ManagedAnimeID != "" {
		anime, err := r.recognitionAnimeFacts(ctx, in.ManagedAnimeID, in.Season, in.AnimeDraft)
		if err != nil {
			return naming.NamingContext{}, err
		}
		namingContext.Anime = anime
	} else if in.AnimeDraft != nil {
		// A draft-only match names with the draft alone: no stored anime is
		// read, exactly as one-shot's task draft names its files.
		anime, err := r.recognitionDraftFacts(ctx, in.AnimeDraft, in.Season)
		if err != nil {
			return naming.NamingContext{}, err
		}
		namingContext.Anime = anime
	}
	return namingContext, nil
}

// recognitionDraftFacts builds the anime facts of a draft-only match: the
// draft stands on its own, with the bound works' titles filled from the
// metadata cache when one is wired. No anime row is read or written.
func (r *NamingTargetRenderer) recognitionDraftFacts(ctx context.Context, draft *RecognitionAnimeDraft, season *int) (naming.AnimeNamingFacts, error) {
	var evidence namingvars.AnimeEvidence
	applyRecognitionAnimeDraft(&evidence, draft)
	if r != nil && r.db != nil {
		if err := namingvarspersistence.FillWorkTitles(ctx, r.db, &evidence); err != nil {
			return naming.AnimeNamingFacts{}, err
		}
	}
	facts := naming.AnimeNamingFacts{
		Title:         evidence.AnimeTitle,
		TitleOverride: evidence.AnimeTitleOverride,
		Year:          ownerYearValue(evidence.AnimeYear),
		BGM: naming.BGMNamingFacts{
			WorkID: evidence.BgmID, Title: evidence.BgmTitle, TitleCN: evidence.BgmTitleCN,
			Year: providerYear(evidence.BgmYear),
		},
		TMDB: naming.TMDBNamingFacts{
			WorkID: evidence.TmdbID, MediaKind: draft.MediaKind,
			TitleCN: evidence.TmdbTitleCN, OriginalTitle: evidence.TmdbOriginalTitle,
			Year: providerYear(evidence.TmdbYear),
		},
	}
	if season != nil {
		chosen := *season
		facts.Season = &chosen
	}
	if draft.TMDB {
		facts.TMDB.Season, facts.TMDB.SeasonPresent = draft.TMDBSeason, true
	}
	return facts, nil
}

// recognitionAnimeFacts reads the anime facts a matched render exposes. It is
// the match render's evidence: the anime's current rows, with the operation's
// temporary draft standing in for them when it has one. A retired or ending
// anime still names its files; recognition never writes to it.
func (r *NamingTargetRenderer) recognitionAnimeFacts(ctx context.Context, animeID string, season *int, draft *RecognitionAnimeDraft) (naming.AnimeNamingFacts, error) {
	if r == nil || r.db == nil {
		return naming.AnimeNamingFacts{}, errors.New("recognition render requires a database for a matched anime")
	}
	evidence, err := namingvarspersistence.LoadAnimeEvidence(ctx, r.db, animeID)
	if err != nil {
		return naming.AnimeNamingFacts{}, err
	}
	if draft != nil {
		applyRecognitionAnimeDraft(&evidence, draft)
		if err := namingvarspersistence.FillWorkTitles(ctx, r.db, &evidence); err != nil {
			return naming.AnimeNamingFacts{}, err
		}
	}
	facts := naming.AnimeNamingFacts{
		ID:            animeID,
		Title:         evidence.AnimeTitle,
		TitleOverride: evidence.AnimeTitleOverride,
		ProviderTitle: evidence.AnimeProviderTitle,
		Year:          ownerYearValue(evidence.AnimeYear),
		Season:        ownerSeasonValue(evidence.AnimeSeason),
		BGM: naming.BGMNamingFacts{
			WorkID: evidence.BgmID, Title: evidence.BgmTitle, TitleCN: evidence.BgmTitleCN,
			Year: providerYear(evidence.BgmYear),
		},
		TMDB: naming.TMDBNamingFacts{
			WorkID: evidence.TmdbID, MediaKind: evidence.TmdbKind,
			TitleCN: evidence.TmdbTitleCN, OriginalTitle: evidence.TmdbOriginalTitle,
			Year: providerYear(evidence.TmdbYear),
		},
	}
	// The draft's kind is the kind this render resolves with (anime.kind).
	if draft != nil {
		facts.TMDB.MediaKind = draft.MediaKind
	}
	// The chosen season is the season this file is named with; without a choice
	// the anime's current season stands. A draft carries the TMDB season with
	// its binding, so the chosen season only stands in for an anime without one.
	if season != nil {
		chosen := *season
		facts.Season = &chosen
		if draft == nil {
			facts.TMDB.Season, facts.TMDB.SeasonPresent = chosen, true
		}
	}
	if draft == nil && evidence.TmdbSeason != nil {
		facts.TMDB.Season, facts.TMDB.SeasonPresent = int(*evidence.TmdbSeason), true
	} else if draft != nil && draft.TMDB {
		facts.TMDB.Season, facts.TMDB.SeasonPresent = draft.TMDBSeason, true
	}
	return facts, nil
}

// applyRecognitionAnimeDraft replaces the stored anime facts with the
// operation's own copy: a value the draft leaves empty stays empty, never
// falling back to the stored row.
func applyRecognitionAnimeDraft(evidence *namingvars.AnimeEvidence, draft *RecognitionAnimeDraft) {
	evidence.AnimeTitle = draft.Title
	// The draft title is the effective title, which outranks every other
	// source exactly as an edited subscription title does.
	evidence.AnimeTitleOverride = draft.Title
	evidence.AnimeYear = nil
	if draft.Year != nil {
		year := int64(*draft.Year)
		evidence.AnimeYear = &year
	}
	evidence.BgmID = ""
	if draft.BGM {
		evidence.BgmID = draft.BGMWorkID
	}
	evidence.TmdbID, evidence.TmdbKind, evidence.TmdbSeason = "", "", nil
	if draft.TMDB {
		tmdbSeason := int64(draft.TMDBSeason)
		evidence.TmdbID, evidence.TmdbKind, evidence.TmdbSeason = draft.TMDBWorkID, draft.TMDBMediaKind, &tmdbSeason
	}
}

// ownerYearValue is the owner-edited year as naming reads it (nil when unset).
func ownerYearValue(value *int64) *int {
	if value == nil {
		return nil
	}
	year := int(*value)
	return &year
}

// ownerSeasonValue is the owner-edited season column as naming reads it (nil
// when unset).
func ownerSeasonValue(value *int64) *int {
	if value == nil {
		return nil
	}
	season := int(*value)
	return &season
}

// classifyRecognitionRender maps a naming engine failure to its typed
// diagnostic.
func classifyRecognitionRender(err error) error {
	var named *naming.Error
	if errors.As(err, &named) {
		switch {
		case len(named.Missing) > 0 && naming.IsFactError(named.Code):
			return &RecognitionRenderError{Code: RecognitionRenderFactsMissing, Facts: append([]string(nil), named.Missing...), cause: err}
		case named.Code == naming.ErrCodeNamingVarsUnknown:
			return &RecognitionRenderError{Code: RecognitionRenderFactsMissing, cause: err}
		case named.Code == naming.ErrCodeNamingTargetInvalid,
			named.Code == naming.ErrCodeNamingTargetNotAbsolute,
			named.Code == naming.ErrCodeSubtitleSuffixInvalid:
			return &RecognitionRenderError{Code: RecognitionRenderTargetInvalid, cause: err}
		}
	}
	if errors.Is(err, naming.ErrRenderFactsUnavailable) {
		return &RecognitionRenderError{Code: RecognitionRenderFactsMissing, cause: err}
	}
	return &RecognitionRenderError{Code: RecognitionRenderTemplateInvalid, cause: err}
}
