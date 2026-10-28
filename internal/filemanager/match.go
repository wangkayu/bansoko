package filemanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"

	"bansoko/internal/episodetitle"
	"bansoko/internal/filekinds"
	"bansoko/internal/filemaintenance"
)

// Match: a detached video is matched to one season and episode of
// one anime. The server renders the target with that anime's current TV naming
// template (filemaintenance.RenderMatch), shows it as a side-effect-free
// preview, and on confirmation admits one `match` user operation: a fenced
// move to the rendered absolute path that creates the missing directories,
// never overwrites, and leaves the file detached (no placement, no coverage).
// The source may lie anywhere the container can read; the destination is
// decided by the work's own naming target. The chosen episode is final: no
// offset applies and ep.source reads "manual". Subtitles beside the video stay
// where they are.

// Match error codes (each stable, none carries a private path).
const (
	CodeMatchRequiresDetached = "FILE_MANAGER_MATCH_REQUIRES_DETACHED"
	CodeMatchAnimeRequired    = "FILE_MANAGER_MATCH_ANIME_REQUIRED"
	CodeMatchAnimeUnavailable = "FILE_MANAGER_MATCH_ANIME_UNAVAILABLE"
	CodeMatchTargetChanged    = "FILE_MANAGER_MATCH_TARGET_CHANGED"
	CodeMatchNotRenderable    = "FILE_MANAGER_MATCH_NOT_RENDERABLE"
	CodeMatchInvalid          = "FILE_MANAGER_MATCH_INVALID"
	CodeTMDBBindingRequired   = "TMDB_BINDING_REQUIRED"
	CodeMetadataUnavailable   = "METADATA_PROVIDER_UNAVAILABLE"
)

// MatchMediaKind is the media kind of a match. Only tv exists; movie naming is
// a later slice and the field is reserved for it.
type MatchMediaKind string

const MatchMediaTV MatchMediaKind = "tv"

// Bounds of a chosen season and episode (the migration checks the same).
const (
	maxMatchSeason  = 9999
	maxMatchEpisode = 99999
)

// MatchAnimeFact is what the file manager needs of an anime to match into it.
type MatchAnimeFact struct {
	ID                string
	Title             string
	SubscriptionState string
	Retired           bool
	OneShot           bool
	TMDBWorkID        string
	TMDBMediaKind     string
	// MediaKindOverride is the owner media kind (tv | movie); empty is tv.
	MediaKindOverride string
}

// Selectable reports whether new files may be matched into the anime: its
// subscription is active or stopped, it is not retired and it is no one-shot's
// temporary anime.
func (a MatchAnimeFact) Selectable() bool {
	return !a.Retired && !a.OneShot && (a.SubscriptionState == "active" || a.SubscriptionState == "stopped")
}

// MediaKind is the owner media kind (unset is tv), the kind the identity
// chain resolves with (FrozenMediaBinding); the TMDB kind is never read for it.
func (a MatchAnimeFact) MediaKind() string {
	if a.MediaKindOverride == "movie" {
		return "movie"
	}
	return "tv"
}

// MatchAnimeReader reads an anime's match facts. It returns nil for an anime
// that does not exist.
type MatchAnimeReader interface {
	MatchAnime(ctx context.Context, managedAnimeID string) (*MatchAnimeFact, error)
}

// MatchRenderer renders the target of a match (filemaintenance.RenderMatch).
type MatchRenderer interface {
	RenderMatch(ctx context.Context, input filemaintenance.MatchRenderInput) (filemaintenance.MatchRender, error)
}

// SeasonCatalog reads a provider's seasons and episode lists
// (episodetitle.Lookup).
type SeasonCatalog interface {
	Seasons(ctx context.Context, source episodetitle.Source, workID string) ([]episodetitle.Season, error)
	Season(ctx context.Context, key episodetitle.Key, bypassCache bool) (episodetitle.List, error)
}

// SetMatch wires the match ports. Unwired, match requests are refused.
func (s *Service) SetMatch(renderer MatchRenderer, animes MatchAnimeReader, catalog SeasonCatalog) {
	if s != nil {
		s.matchRenderer, s.matchAnimes, s.seasons = renderer, animes, catalog
	}
}

// MatchRequest names the file and the episode to match it to.
type MatchRequest struct {
	// Path is the absolute path of the file to match.
	Path string
	// ManagedAnimeID is the anime to match into; empty takes the anime the
	// file came from.
	ManagedAnimeID string
	MediaKind      MatchMediaKind
	Season         int
	Episode        int
}

// MatchBlockerCode names why a preview cannot be submitted.
type MatchBlockerCode string

const (
	// MatchBlockedFactsMissing: the template reads facts the evidence lacks.
	MatchBlockedFactsMissing MatchBlockerCode = "facts_missing"
	// MatchBlockedTemplateInvalid: the template does not render.
	MatchBlockedTemplateInvalid MatchBlockerCode = "template_invalid"
	// MatchBlockedTargetInvalid: the rendered path is not a usable target.
	MatchBlockedTargetInvalid MatchBlockerCode = "target_invalid"
	// MatchBlockedTargetIsSource: the file is already at the rendered path.
	MatchBlockedTargetIsSource MatchBlockerCode = "target_is_source"
)

// MatchBlocker is one reason a preview cannot be submitted. Facts name the
// catalog paths of a facts_missing blocker.
type MatchBlocker struct {
	Code  MatchBlockerCode
	Facts []string
}

// MatchOccupant says what holds an occupied target.
type MatchOccupant string

const (
	OccupantFile       MatchOccupant = "file"
	OccupantManaged    MatchOccupant = "managed"
	OccupantInProgress MatchOccupant = "in_progress"
)

// MatchConflict is an occupied target. VersionPath is the absolute path the
// file would take under the version choice (advisory: the worker picks the
// first free suffix when it runs).
type MatchConflict struct {
	OccupiedBy  MatchOccupant
	VersionPath string
}

// MatchPreview is the side-effect-free outcome of a match request. Target is
// empty when a blocker stops the render or the path.
type MatchPreview struct {
	Revision  string
	Anime     AnimeRef
	MediaKind MatchMediaKind
	Season    int
	Episode   int
	// TargetPath is the absolute rendered target.
	TargetPath string
	// DirectoriesToCreate are the absolute directories the match would create,
	// parents first.
	DirectoriesToCreate []string
	Conflict            *MatchConflict
	Blockers            []MatchBlocker
	// SubtitlesLeftBehind are subtitle files beside the video that follow its
	// name; they do not move with it.
	SubtitlesLeftBehind []string
	// FromLineage reports that release-level facts came from the file's own
	// acquisition.
	FromLineage bool
}

// SubmitMatchRequest confirms one previewed match.
type SubmitMatchRequest struct {
	IdempotencyKey string
	Actor          string
	// Path is the absolute path of the file to match.
	Path             string
	ExpectedRevision string
	ManagedAnimeID   string
	MediaKind        MatchMediaKind
	Season           int
	Episode          int
	// ExpectedTarget is the absolute target the preview showed; the submit is
	// refused when the render now gives another.
	ExpectedTarget string
	Conflict       ConflictChoice
}

// MatchAdmission is the match part of an operation admission.
type MatchAdmission struct {
	ManagedAnimeID string
	Season         int
	Episode        int
}

// matchSource is a checked match source file and its resolved anime.
type matchSource struct {
	inspection inspection
	anime      *MatchAnimeFact
}

func (s *Service) requireMatch() error {
	if s.matchRenderer == nil || s.matchAnimes == nil {
		return conflict(CodeCommandsUnavailable, "file manager match is not available")
	}
	return nil
}

func validMatchEpisode(mediaKind MatchMediaKind, season, episode int) error {
	if mediaKind != "" && mediaKind != MatchMediaTV {
		return invalid(CodeMatchInvalid, "only tv matches exist")
	}
	if season < 0 || season > maxMatchSeason || episode < 1 || episode > maxMatchEpisode {
		return invalid(CodeMatchInvalid, "season or episode is out of range")
	}
	return nil
}

// matchDefaultAnime is the anime a file's lineage placement belongs to when
// it still takes new files.
func matchDefaultAnime(derived derivation) *AnimeRef {
	placement := derived.placement
	if placement == nil || derived.role != RoleVideo || !placement.AnimeExists {
		return nil
	}
	fact := MatchAnimeFact{
		SubscriptionState: placement.SubscriptionState, Retired: placement.Retired, OneShot: placement.OneShotID != "",
	}
	if !fact.Selectable() {
		return nil
	}
	return &AnimeRef{ID: placement.ManagedAnimeID, Title: placement.AnimeTitle, SubscriptionState: placement.SubscriptionState}
}

// checkMatchSource re-derives the file and applies the match rules: a detached
// regular video, not in progress, at any browsable absolute path. It returns
// the anime to match into.
func (s *Service) checkMatchSource(ctx context.Context, absolutePath, animeID string) (matchSource, error) {
	source, err := s.inspect(ctx, absolutePath)
	if err != nil {
		return matchSource{}, err
	}
	if source.detail.Entry.Kind != KindFile || internalNamespace(source.absolute) {
		return matchSource{}, conflict(CodeOperationNotAllowed, "operation does not apply to this entry")
	}
	switch source.detail.Entry.State {
	case StateManaged:
		return matchSource{}, conflict(CodeMatchRequiresDetached, "a managed file is aligned, not matched")
	case StateInProgress:
		return matchSource{}, conflict(CodeOperationNotAllowed, "operation is not allowed: "+string(BlockInProgress))
	}
	if source.derived.role != RoleNone && source.derived.role != RoleVideo ||
		filekinds.Classify(filekinds.Version, source.detail.Entry.Name) != filekinds.KindVideo {
		return matchSource{}, conflict(CodeOperationNotAllowed, "only a video is matched")
	}
	if animeID == "" {
		if source.derived.placement != nil {
			animeID = source.derived.placement.ManagedAnimeID
		}
		if animeID == "" {
			return matchSource{}, conflict(CodeMatchAnimeRequired, "choose the anime to match into")
		}
	}
	anime, err := s.matchAnimes.MatchAnime(ctx, animeID)
	if err != nil {
		return matchSource{}, err
	}
	switch {
	case anime == nil || anime.OneShot || anime.SubscriptionState != "active" && anime.SubscriptionState != "stopped":
		return matchSource{}, conflict(CodeMatchAnimeUnavailable, "the anime does not take matched files")
	case anime.Retired:
		return matchSource{}, conflict(CodeAnimeRetired, "the anime no longer admits changes")
	}
	return matchSource{inspection: source, anime: anime}, nil
}

// renderMatch renders the target of the request. A render that cannot produce
// a path is a blocker (the preview shows it), not an error.
func (s *Service) renderMatch(ctx context.Context, source matchSource, season, episode int) (filemaintenance.MatchRender, *MatchBlocker, error) {
	lineage := ""
	if placement := source.inspection.derived.placement; placement != nil && source.inspection.derived.role == RoleVideo {
		lineage = placement.ID
	}
	size := int64(0)
	if source.inspection.info != nil {
		size = source.inspection.info.Size()
	}
	rendered, err := s.matchRenderer.RenderMatch(ctx, filemaintenance.MatchRenderInput{
		ManagedAnimeID: source.anime.ID, LineagePlacementID: lineage, LineagePath: source.inspection.absolute,
		FileName:  source.inspection.detail.Entry.Name,
		SizeBytes: size, Season: season, Episode: episode,
	})
	var renderErr *filemaintenance.MatchRenderError
	switch {
	case err == nil:
		return rendered, nil, nil
	case errors.As(err, &renderErr):
		code := MatchBlockedTemplateInvalid
		switch renderErr.Code {
		case filemaintenance.MatchRenderFactsMissing:
			code = MatchBlockedFactsMissing
		case filemaintenance.MatchRenderPathInvalid:
			code = MatchBlockedTargetInvalid
		}
		return filemaintenance.MatchRender{}, &MatchBlocker{Code: code, Facts: renderErr.Facts}, nil
	}
	return filemaintenance.MatchRender{}, nil, err
}

const (
	maxPathSegmentBytes = 255
	maxVersionProbe     = 50
)

// matchTarget is a rendered target checked against the library: where it is,
// which directories it needs, and who holds it.
type matchTarget struct {
	resolvedTarget
	create   []string
	occupant MatchOccupant
}

// planMatchTarget checks a rendered absolute target: a path outside the denial
// zone that is not an executor namespace, whose existing directories are real
// directories (never a symlink), plus the directories still to be created. An
// occupied target is reported in occupant.
func (s *Service) planMatchTarget(ctx context.Context, source inspection, rendered string) (matchTarget, *MatchBlocker, error) {
	badTarget := &MatchBlocker{Code: MatchBlockedTargetInvalid}
	if refuseDenied(s.zone, rendered) != nil {
		return matchTarget{}, badTarget, nil
	}
	absolute, ok := cleanAbsolute(rendered)
	if !ok || absolute == string(filepath.Separator) || internalNamespace(absolute) {
		return matchTarget{}, badTarget, nil
	}
	segments := splitAbsolute(absolute)
	if len(segments) == 0 {
		return matchTarget{}, badTarget, nil
	}
	for _, segment := range segments {
		if len(segment) > maxPathSegmentBytes {
			return matchTarget{}, badTarget, nil
		}
	}
	if absolute == source.absolute {
		return matchTarget{}, &MatchBlocker{Code: MatchBlockedTargetIsSource}, nil
	}
	current := string(filepath.Separator)
	var create []string
	for index, segment := range segments[:len(segments)-1] {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			for rest := index; rest < len(segments)-1; rest++ {
				create = append(create, absoluteFromSegments(segments[:rest+1]))
			}
			break
		}
		if err != nil {
			return matchTarget{}, nil, fsError(err, current)
		}
		if !info.IsDir() {
			return matchTarget{}, badTarget, nil
		}
	}
	resolved := resolvedTarget{absolute: absolute, spellings: absoluteSpellings(absolute), conflict: ConflictBlock}
	occupant, err := s.occupant(ctx, resolved)
	if err != nil {
		return matchTarget{}, nil, err
	}
	return matchTarget{resolvedTarget: resolved, create: create, occupant: occupant}, nil, nil
}

// occupant reports what holds a target: a file on disk, a path an owner
// manages, or a path an unfinished owner holds ("" when free).
func (s *Service) occupant(ctx context.Context, target resolvedTarget) (MatchOccupant, error) {
	// Owners first: a managed file is on disk too, and must read as managed.
	facts, err := s.store.PathFacts(ctx, target.spellings)
	if err != nil {
		return "", err
	}
	for _, variant := range target.spellings {
		if len(facts.InProgress[variant]) > 0 {
			return OccupantInProgress, nil
		}
		for _, placement := range facts.Placements[variant] {
			if placement.State == placementActive {
				return OccupantManaged, nil
			}
		}
		for _, companion := range facts.Companions[variant] {
			if companion.State == companionActive {
				return OccupantManaged, nil
			}
		}
	}
	if _, err := os.Lstat(target.absolute); err == nil {
		return OccupantFile, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		if errors.Is(err, os.ErrPermission) {
			return "", fsError(err, target.absolute)
		}
		// A parent that is not a directory reads as an unusable target.
		return OccupantFile, nil
	}
	return "", nil
}

// versionCandidate is the path the version choice would take: the first free
// " (N)" sibling of the target, advisory.
func (s *Service) versionCandidate(ctx context.Context, target resolvedTarget) (string, error) {
	name := filepath.Base(target.absolute)
	extension := path.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	directory := filepath.Dir(target.absolute)
	for index := 2; index <= maxVersionProbe; index++ {
		absolute := joinAbsolute(directory, stem+" ("+fmt.Sprint(index)+")"+extension)
		candidate := resolvedTarget{absolute: absolute, spellings: absoluteSpellings(absolute)}
		occupant, err := s.occupant(ctx, candidate)
		if err != nil {
			return "", err
		}
		if occupant == "" {
			return absolute, nil
		}
	}
	return "", nil
}

// leftBehindSubtitles lists the subtitle files in the video's directory that
// follow its name; a match does not move them.
func leftBehindSubtitles(source inspection) []string {
	absolute, info, err := walkAbsolute(filepath.Dir(source.absolute), false)
	if err != nil {
		return nil
	}
	entries, _, err := readDirectory(absolute, info)
	if err != nil {
		return nil
	}
	stem := strings.TrimSuffix(source.detail.Entry.Name, path.Ext(source.detail.Entry.Name))
	var result []string
	for _, entry := range entries {
		if entry.kind != KindFile || filekinds.Classify(filekinds.Version, entry.name) != filekinds.KindSubtitle {
			continue
		}
		if filekinds.BindSubtitle(entry.name, []string{stem}) == 0 {
			result = append(result, joinAbsolute(absolute, entry.name))
		}
	}
	sort.Strings(result)
	if len(result) > 20 {
		result = result[:20]
	}
	return result
}

// PreviewMatch shows what a match would do and writes nothing: the target
// path, the directories it would create, an occupied target, the subtitles
// that stay behind and any blocker. A file the match does not apply to, a
// download-root file and an anime that no longer takes files are typed 409s.
func (s *Service) PreviewMatch(ctx context.Context, request MatchRequest) (MatchPreview, error) {
	if err := s.requireMatch(); err != nil {
		return MatchPreview{}, err
	}
	if err := validMatchEpisode(request.MediaKind, request.Season, request.Episode); err != nil {
		return MatchPreview{}, err
	}
	source, err := s.checkMatchSource(ctx, request.Path, request.ManagedAnimeID)
	if err != nil {
		return MatchPreview{}, err
	}
	preview := MatchPreview{
		Revision: source.inspection.detail.Revision, MediaKind: MatchMediaTV, Season: request.Season, Episode: request.Episode,
		Anime:               AnimeRef{ID: source.anime.ID, Title: source.anime.Title, SubscriptionState: source.anime.SubscriptionState},
		SubtitlesLeftBehind: leftBehindSubtitles(source.inspection),
	}
	rendered, blocker, err := s.renderMatch(ctx, source, request.Season, request.Episode)
	if err != nil {
		return MatchPreview{}, err
	}
	if blocker != nil {
		preview.Blockers = append(preview.Blockers, *blocker)
		return preview, nil
	}
	preview.FromLineage = rendered.Lineage
	target, blocker, err := s.planMatchTarget(ctx, source.inspection, rendered.Path)
	if err != nil {
		return MatchPreview{}, err
	}
	if blocker != nil {
		preview.Blockers = append(preview.Blockers, *blocker)
		return preview, nil
	}
	preview.TargetPath = target.absolute
	preview.DirectoriesToCreate = target.create
	if target.occupant != "" {
		preview.Conflict = &MatchConflict{OccupiedBy: target.occupant}
		if preview.Conflict.VersionPath, err = s.versionCandidate(ctx, target.resolvedTarget); err != nil {
			return MatchPreview{}, err
		}
	}
	return preview, nil
}

// SubmitMatch admits one confirmed match as a `match` user operation. It
// re-derives the file, compares the revision and the previewed target, renders
// again (the provider episode titles are looked up again and frozen on the
// operation) and hands the frozen request to the command store, which
// re-checks owner facts and enqueues the fenced work in one transaction.
func (s *Service) SubmitMatch(ctx context.Context, request SubmitMatchRequest) (UserOperation, error) {
	if err := s.requireCommands(); err != nil {
		return UserOperation{}, err
	}
	if err := s.requireMatch(); err != nil {
		return UserOperation{}, err
	}
	if err := validKey(request.IdempotencyKey); err != nil {
		return UserOperation{}, err
	}
	if err := validMatchEpisode(request.MediaKind, request.Season, request.Episode); err != nil {
		return UserOperation{}, err
	}
	choice := request.Conflict
	if choice == "" {
		choice = ConflictBlock
	}
	if choice != ConflictBlock && choice != ConflictVersion {
		return UserOperation{}, invalid(CodeTargetInvalid, "conflict choice is not valid")
	}
	digest := requestDigest(struct {
		Operation                           UserOperationKind
		Path, Revision, Anime, ExpectedPath string
		Season, Episode                     int
		Conflict                            ConflictChoice
	}{UserMatch, request.Path, request.ExpectedRevision, request.ManagedAnimeID, request.ExpectedTarget,
		request.Season, request.Episode, choice})
	if existing, existingDigest, err := s.commands.OperationByKey(ctx, request.IdempotencyKey); err != nil {
		return UserOperation{}, err
	} else if existing != nil {
		if existingDigest != digest {
			return UserOperation{}, conflict(CodeIdempotencyConflict, "idempotency key was used for another request")
		}
		return *existing, nil
	}
	source, err := s.checkMatchSource(ctx, request.Path, request.ManagedAnimeID)
	if err != nil {
		return UserOperation{}, err
	}
	if source.inspection.detail.Revision != request.ExpectedRevision {
		return UserOperation{}, conflict(CodeEntryChanged, "entry changed since it was read")
	}
	rendered, blocker, err := s.renderMatch(ctx, source, request.Season, request.Episode)
	if err != nil {
		return UserOperation{}, err
	}
	if blocker != nil {
		return UserOperation{}, conflict(CodeMatchNotRenderable, "the naming template cannot name this file: "+string(blocker.Code))
	}
	target, blocker, err := s.planMatchTarget(ctx, source.inspection, rendered.Path)
	if err != nil {
		return UserOperation{}, err
	}
	if blocker != nil {
		if blocker.Code == MatchBlockedTargetIsSource {
			return UserOperation{}, invalid(CodeTargetInvalid, "target is the file itself")
		}
		return UserOperation{}, invalid(CodeTargetInvalid, "target is not a valid path")
	}
	if target.absolute != request.ExpectedTarget {
		return UserOperation{}, conflict(CodeMatchTargetChanged, "the target changed since the preview")
	}
	if target.occupant != "" && choice == ConflictBlock {
		return UserOperation{}, conflict(CodeTargetExists, "target is occupied")
	}
	admission := OperationAdmission{
		ID: uuid.NewString(), IdempotencyKey: request.IdempotencyKey, Digest: digest, Actor: request.Actor,
		Operation:  UserMatch,
		SourcePath: source.inspection.absolute, SourceSpellings: source.inspection.spellings,
		TargetPath: target.absolute, TargetSpellings: target.spellings, Collision: choice,
		RequireDetachedSource: true,
		Match: &MatchAdmission{
			ManagedAnimeID: source.anime.ID, Season: request.Season, Episode: request.Episode,
		},
	}
	if info := source.inspection.info; info != nil {
		admission.ExpectedSize = info.Size()
	}
	return s.commands.AdmitOperation(ctx, admission)
}

// TMDBSeason is one season of an anime's bound TMDB show.
type TMDBSeason struct {
	Number       int
	Name         string
	EpisodeCount int
	AirDate      string
}

// TMDBEpisode is one episode of a TMDB season.
type TMDBEpisode struct {
	Number  int
	Title   string
	AirDate string
}

// tmdbShow resolves the TMDB TV show an anime is bound to.
func (s *Service) tmdbShow(ctx context.Context, managedAnimeID string) (string, error) {
	if s.matchAnimes == nil || s.seasons == nil {
		return "", conflict(CodeCommandsUnavailable, "file manager match is not available")
	}
	anime, err := s.matchAnimes.MatchAnime(ctx, managedAnimeID)
	if err != nil {
		return "", err
	}
	if anime == nil {
		return "", notFound(CodeMatchAnimeUnavailable, "anime does not exist")
	}
	// The episode catalog is read from a TMDB TV work, and an episode match
	// needs a TV anime: the owner kind decides that, never the TMDB kind.
	if anime.TMDBWorkID == "" || anime.TMDBMediaKind != "tv" || anime.MediaKind() != "tv" {
		return "", conflict(CodeTMDBBindingRequired, "the anime has no TMDB TV binding")
	}
	return anime.TMDBWorkID, nil
}

func catalogError(err error) error {
	if errors.Is(err, episodetitle.ErrTransient) {
		return &Error{Kind: ErrorUnavailable, Code: CodeMetadataUnavailable, Message: "the metadata provider could not be reached"}
	}
	return err
}

// TMDBSeasons lists the seasons (season 0 included) of the TMDB show an anime
// is bound to.
func (s *Service) TMDBSeasons(ctx context.Context, managedAnimeID string) ([]TMDBSeason, error) {
	workID, err := s.tmdbShow(ctx, managedAnimeID)
	if err != nil {
		return nil, err
	}
	seasons, err := s.seasons.Seasons(ctx, episodetitle.SourceTMDB, workID)
	if err != nil {
		return nil, catalogError(err)
	}
	result := make([]TMDBSeason, 0, len(seasons))
	for _, season := range seasons {
		result = append(result, TMDBSeason{Number: season.Number, Name: season.Name, EpisodeCount: season.EpisodeCount, AirDate: season.AirDate})
	}
	return result, nil
}

// TMDBSeasonEpisodes lists one season's episodes (number, title, air date).
func (s *Service) TMDBSeasonEpisodes(ctx context.Context, managedAnimeID string, season int) ([]TMDBEpisode, error) {
	if season < 0 || season > maxMatchSeason {
		return nil, invalid(CodeMatchInvalid, "season is out of range")
	}
	workID, err := s.tmdbShow(ctx, managedAnimeID)
	if err != nil {
		return nil, err
	}
	list, err := s.seasons.Season(ctx, episodetitle.Key{Source: episodetitle.SourceTMDB, WorkID: workID, Season: season}, false)
	if err != nil {
		return nil, catalogError(err)
	}
	result := make([]TMDBEpisode, 0, len(list.Episodes))
	for _, episode := range list.Episodes {
		result = append(result, TMDBEpisode{Number: episode.Number, Title: episode.Title, AirDate: episode.AirDate})
	}
	return result, nil
}
