package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"bansoko/internal/apperrors"
	"bansoko/internal/episodetitle/namingfacts"
	"bansoko/internal/filemaintenance/planner"
	"bansoko/internal/postprocessing/naming"
)

// NamingRulesSource supplies the current effective rules-owner value for one
// anime to every render (alignment, path diff, offset migration and match).
// The source is typed and subscription-neutral; the renderer never reads
// configuration tables directly.
type NamingRulesSource interface {
	EffectiveNamingRules(ctx context.Context, animeID string) (naming.NamingRules, error)
}

// NamingTargetRenderer renders the frozen target path for one snapshot
// observation through the shared naming engine. It performs no filesystem,
// provider or metadata I/O: the anime facts come from the persisted owner
// rows, the Parsed/Derived values are resolved by the naming registry from
// the frozen file name, and template errors fail the candidate (never guessed).
// The one provider lookup the owner decree of 2026-10-22 whitelists here is
// the provider episode title read (bgm.ep_title / tmdb.ep_title) in
// RenderTarget (the alignment candidate render) and RenderMatch — both
// user-initiated renders; every other entry point (path diff previews, the
// readmodel placement targets, offset migration) never queries.
type NamingTargetRenderer struct {
	db          *sql.DB
	rulesSource NamingRulesSource
	// episodeTitles is the shared persistent episode title Querier; nil means
	// not wired and every referenced lookup surfaces a transient failure.
	episodeTitles *namingfacts.Querier
}

// NewNamingTargetRenderer wires the renderer ports.
func NewNamingTargetRenderer(db *sql.DB, rulesSource NamingRulesSource) *NamingTargetRenderer {
	return &NamingTargetRenderer{db: db, rulesSource: rulesSource}
}

// SetEpisodeTitleQueries wires the persistent provider episode title Querier
// (migration 046) for the two whitelisted renders. The composition root
// shares one Querier with the library rename render.
func (r *NamingTargetRenderer) SetEpisodeTitleQueries(queries *namingfacts.Querier) {
	if r != nil {
		r.episodeTitles = queries
	}
}

// compileTitleTemplates compiles the effective rules with the same engine and
// renderer version the render itself uses, and returns the variant templates
// the render actually renders. Only these references decide whether an
// ep_title lookup runs.
func (r *NamingTargetRenderer) compileTitleTemplates(rules naming.NamingRules, facts naming.NamingContext, rendererVersion string) ([]naming.CompiledTemplate, error) {
	engine, err := naming.NewEngine().ForRenderer(rendererVersion)
	if err != nil {
		return nil, renderFactsUnavailable(err.Error())
	}
	compiled, err := engine.CompileRulesWithVars(rules, facts.Vars)
	if err != nil {
		return nil, fmt.Errorf("compile naming rules: %w", err)
	}
	variant := compiled.TV
	if naming.MediaKind(facts.Derived.EffectiveMediaKind) == naming.MediaKindMovie {
		variant = compiled.Movie
	}
	return []naming.CompiledTemplate{variant.Directory, variant.VideoFilename}, nil
}

// resolveEpisodeTitlesInto fills ctx.EpisodeTitles for one whitelisted render.
// Every maintenance render is cache-first: the only bypass of the stored rows
// is the 刷新元数据 re-pull behind the alignment variant, never this path.
// A nil Querier is fine for a render whose templates never reference the
// facts — only a referenced lookup needs the store, and only then a missing
// wiring is the transient failure the resolver reports.
func (r *NamingTargetRenderer) resolveEpisodeTitlesInto(ctx context.Context, templates []naming.CompiledTemplate, context *naming.NamingContext) error {
	resolution, err := r.episodeTitles.Resolve(ctx, templates, *context)
	if err != nil {
		return err
	}
	context.EpisodeTitles = resolution.Facts
	return nil
}

// CalibratedRenderFacts carries the reconciliation facts used to render the
// expected target path of an existing file row. Facts are authoritative: the
// renderer never re-parses the filename (same philosophy as offset migration),
// so the preview cannot drift from the calibration snapshot.
type CalibratedRenderFacts struct {
	// EpisodeDisplay is the canonical episode presentation (from coverage).
	EpisodeDisplay string
	Resolution     string
	Language       string
	PlacementID    string
}

// RenderCalibratedTarget renders the expected canonical target for one
// reconciliation calibration row: it resolves the naming context but then
// overrides Parsed.Episode / File.Media.Resolution / Parsed.Language with the
// frozen calibration facts before rendering. The release and file facts stay
// as acquisition froze them; the title, the owner season, year and media kind
// and the metadata binding are the anime's current configuration. A missing
// binding or an undefined template variable fails the render with a typed
// config-incomplete error; an excluded placement fails with
// ErrPlacementExcluded. Nothing falls back to the frozen binding.
func (r *NamingTargetRenderer) RenderCalibratedTarget(ctx context.Context, animeID, filePath string, sizeBytes int64, facts CalibratedRenderFacts) (string, error) {
	if r.db == nil {
		return "", errors.New("naming target renderer is not wired")
	}
	if strings.TrimSpace(filePath) == "" || strings.TrimSpace(animeID) == "" {
		return "", errors.New("naming target renderer requires anime id and file path")
	}
	lineage, found, err := r.placedRenderFacts(ctx, animeID, filePath, facts.PlacementID)
	if err != nil {
		return "", err
	}
	if !found || !lineage.lineage {
		return "", renderFactsUnavailable("placement has no file-operation lineage")
	}
	rules, err := r.renderRules(ctx, animeID)
	if err != nil {
		return "", err
	}
	resolved, base, err := r.renderNamingContext(ctx, animeID, filePath, sizeBytes, facts.PlacementID, nil)
	if err != nil {
		return "", err
	}
	resolved.Context.Parsed.Episode.Display = facts.EpisodeDisplay
	resolved.Context.Parsed.Episode.Raw = facts.EpisodeDisplay
	if facts.Resolution != "" {
		resolved.Context.File.Media.Resolution = facts.Resolution
	}
	if facts.Language != "" {
		resolved.Context.Parsed.Language.Code = facts.Language
	}
	return renderResolvedTarget(base, rules, resolved.Context, lineage.renderFacts(), lineage.namingRenderer())
}

// RenderTarget computes the canonical target for one observation.
func (r *NamingTargetRenderer) RenderTarget(ctx context.Context, observation planner.SnapshotObservation) (string, error) {
	if r.db == nil {
		return "", errors.New("naming target renderer database is not wired")
	}
	if strings.TrimSpace(observation.FilePath) == "" || strings.TrimSpace(observation.AnimeID) == "" {
		return "", errors.New("naming target renderer requires anime id and file path")
	}
	lineage, found, err := r.placedRenderFacts(ctx, observation.AnimeID, observation.FilePath, observation.PlacementID)
	if err != nil {
		return "", err
	}
	if !found || !lineage.lineage {
		return "", renderFactsUnavailable("placement has no file-operation lineage")
	}
	rules, err := r.renderRules(ctx, observation.AnimeID)
	if err != nil {
		return "", err
	}
	resolved, base, err := r.renderNamingContext(ctx, observation.AnimeID, observation.FilePath, observation.SizeBytes, observation.PlacementID, nil)
	if err != nil {
		return "", err
	}
	// Completed offset migration atomically switches Placement coverage, while
	// the admission identity remains immutable. Match the calibration preview:
	// use the snapshot's canonical episode, never a live offset or filename.
	if episode := episodeFromCoverage(observation.Coverage); episode != "" {
		resolved.Context.Parsed.Episode.Display = episode
		resolved.Context.Parsed.Episode.Raw = episode
	}
	if observation.Resolution != "" {
		resolved.Context.File.Media.Resolution = observation.Resolution
	}
	if observation.Language != "" {
		resolved.Context.Parsed.Language.Code = observation.Language
	}
	// The alignment candidate render is on the episode-title whitelist: fill
	// the facts before rendering. A transient lookup failure fails this
	// candidate (the planner excludes it with a typed reason); a definite
	// absence renders the empty fact.
	titleTemplates, err := r.compileTitleTemplates(rules, resolved.Context, lineage.namingRenderer())
	if err != nil {
		return "", err
	}
	if err := r.resolveEpisodeTitlesInto(ctx, titleTemplates, &resolved.Context); err != nil {
		return "", err
	}
	return renderResolvedTarget(base, rules, resolved.Context, lineage.renderFacts(), lineage.namingRenderer())
}

// renderResolvedTarget renders with the naming renderer version the placement
// recorded (rendererVersion; "" is naming-render-1), so a frozen target is
// replayed with the semantics it was produced with.
func renderResolvedTarget(base string, rules naming.NamingRules, facts naming.NamingContext, recorded map[string]string, rendererVersion string) (string, error) {
	engine, err := naming.NewEngine().ForRenderer(rendererVersion)
	if err != nil {
		return "", renderFactsUnavailable(err.Error())
	}
	compiled, err := engine.CompileRulesWithVars(rules, facts.Vars)
	if err != nil {
		return "", fmt.Errorf("compile naming rules: %w", err)
	}
	variant := compiled.TV
	if naming.MediaKind(facts.Derived.EffectiveMediaKind) == naming.MediaKindMovie {
		variant = compiled.Movie
	}
	if missing := missingTemplateFacts(engine, variant.RequiredFacts(), recorded, facts); len(missing) > 0 {
		return "", &missingFactsError{facts: missing}
	}
	directory, filename, err := compiled.RenderVideo(engine, facts)
	if err != nil {
		return "", templateVariableUndefined(fmt.Errorf("render naming target for %s: %w", base, err))
	}
	if strings.TrimSpace(filename) == "" {
		return "", fmt.Errorf("naming render produced an empty filename for %s", base)
	}
	if strings.TrimSpace(directory) == "" {
		return filename, nil
	}
	return filepath.Join(directory, filename), nil
}

// missingFactsError is renderFactsUnavailable for a template that needs facts
// the evidence lacks, keeping the paths so a preview can name them.
type missingFactsError struct{ facts []string }

func (e *missingFactsError) Error() string {
	return naming.ErrRenderFactsUnavailable.Error() + ": effective template needs facts the frozen evidence lacks: " + strings.Join(e.facts, ", ")
}

func (e *missingFactsError) Unwrap() error { return naming.ErrRenderFactsUnavailable }

type animeRenderFacts struct {
	title         string
	titleOverride string
	providerTitle string
	bgm           naming.BGMNamingFacts
	tmdb          naming.TMDBNamingFacts
}

// animeFacts reads only the managed display titles. The metadata binding and
// the owner season, year and media kind come from currentAnimeConfig.
func (r *NamingTargetRenderer) animeFacts(ctx context.Context, animeID string) (animeRenderFacts, error) {
	var facts animeRenderFacts
	var title, titleOverride, providerTitle sql.NullString
	err := r.db.QueryRowContext(ctx, `
SELECT title, title_override, provider_title
FROM managed_anime
WHERE id = $1`, animeID).Scan(&title, &titleOverride, &providerTitle)
	if err != nil {
		return facts, fmt.Errorf("read managed anime naming facts: %w", err)
	}
	facts.title = title.String
	if titleOverride.Valid {
		facts.titleOverride = titleOverride.String
	}
	if providerTitle.Valid {
		facts.providerTitle = providerTitle.String
	}
	return facts, nil
}

// renderRules returns the anime's CURRENT effective naming rules: a template
// change on the anime moves its existing files to the new paths through
// alignment, the per-anime files dialog shows that target, and offset
// migration renders with the same rules. Only the rules come from the current
// owner value, as do the title, the owner season, year and media kind and the
// metadata binding (currentAnimeConfig); every release-level fact (Vars output,
// rt/anibt values, release title, group, probed media facts, frozen episode
// identity and torrent facts) still comes from the placement's lineage.
// An anime without a current rules owner (or an unwired source) fails closed
// as render-facts-unavailable; the frozen rules are never a fallback.
func (r *NamingTargetRenderer) renderRules(ctx context.Context, animeID string) (naming.NamingRules, error) {
	if r.rulesSource == nil {
		return naming.NamingRules{}, renderFactsUnavailable("current naming rules source is not wired")
	}
	rules, err := r.rulesSource.EffectiveNamingRules(ctx, animeID)
	if err != nil {
		var notFound *apperrors.NotFound
		if errors.As(err, &notFound) {
			return naming.NamingRules{}, renderFactsUnavailable("anime has no current naming rules")
		}
		return naming.NamingRules{}, fmt.Errorf("read current naming rules: %w", err)
	}
	if !naming.SupportedLanguage(rules.Language) {
		return naming.NamingRules{}, renderFactsUnavailable("current naming rules version is unavailable")
	}
	return rules, nil
}

// RenderTargetWithFrozenEpisode renders the canonical target for one file
// using a frozen EpisodeIdentity instead of re-parsing the basename. This is
// the offset-correction path: the raw episode and season come from the media
// fact snapshot, and the new offset is applied exactly once by the caller
// before rendering.
func (r *NamingTargetRenderer) RenderTargetWithFrozenEpisode(ctx context.Context, animeID, filePath string, sizeBytes int64, frozen *naming.FrozenEpisodeNamingFacts) (string, error) {
	return r.renderTargetWithFrozenEpisode(ctx, animeID, filePath, sizeBytes, "", frozen)
}

func (r *NamingTargetRenderer) renderTargetWithFrozenEpisode(ctx context.Context, animeID, filePath string, sizeBytes int64, placementID string, frozen *naming.FrozenEpisodeNamingFacts) (string, error) {
	if r.db == nil {
		return "", errors.New("naming target renderer is not wired")
	}
	if strings.TrimSpace(filePath) == "" || strings.TrimSpace(animeID) == "" || frozen == nil {
		return "", errors.New("naming target renderer requires anime id, file path and frozen episode")
	}
	lineage, found, err := r.placedRenderFacts(ctx, animeID, filePath, placementID)
	if err != nil {
		return "", err
	}
	if !found || !lineage.lineage {
		return "", renderFactsUnavailable("placement has no file-operation lineage")
	}
	rules, err := r.renderRules(ctx, animeID)
	if err != nil {
		return "", err
	}
	resolved, base, err := r.renderNamingContext(ctx, animeID, filePath, sizeBytes, placementID, frozen)
	if err != nil {
		return "", err
	}
	return renderResolvedTarget(base, rules, resolved.Context, lineage.renderFacts(), lineage.namingRenderer())
}
