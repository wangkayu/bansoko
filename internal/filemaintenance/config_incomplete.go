package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/namingvars"
	namingvarspersistence "bansoko/internal/namingvars/adapter/persistence"
	"bansoko/internal/postprocessing/naming"
)

// Exclusion reasons of a maintenance render that cannot produce a target. They
// are recorded as the candidate's exclusion reason and shown by the path diff
// and the file list, so the owner sees why a file stays where it is.
const (
	// ConfigIncompletePrefix starts every reason of a render whose current
	// configuration lacks an input: "config_incomplete:<item>".
	ConfigIncompletePrefix = "config_incomplete:"
	// ConfigItemBindingMissingBGM and ConfigItemBindingMissingTMDB: the anime
	// has no BGM / TMDB binding, the same basis as the metadata hold.
	ConfigItemBindingMissingBGM  = "binding_missing:bgm"
	ConfigItemBindingMissingTMDB = "binding_missing:tmdb"
	// ConfigItemTemplateVariableUndefinedPrefix is followed by the variable
	// names the current template reads and the render does not define.
	ConfigItemTemplateVariableUndefinedPrefix = "template_variable_undefined:"
	// ReasonPlacementExcluded: the placement is kept as it is (it belongs to
	// another season or kind) and never takes part in a render.
	ReasonPlacementExcluded = "placement_excluded"
)

// ConfigIncompleteError is a render refused because the anime's current
// configuration lacks an input. It fails closed: there is no fallback to the
// values the file was acquired with and no provider request.
type ConfigIncompleteError struct{ Item string }

func (e *ConfigIncompleteError) Error() string {
	return "current configuration is incomplete: " + e.Item
}

// ExcludeReasonCode is the typed reason recorded for the candidate.
func (e *ConfigIncompleteError) ExcludeReasonCode() string { return ConfigIncompletePrefix + e.Item }

type placementExcludedError struct{}

func (placementExcludedError) Error() string { return "placement is excluded from rendering" }

// ExcludeReasonCode is the typed reason recorded for the candidate.
func (placementExcludedError) ExcludeReasonCode() string { return ReasonPlacementExcluded }

// ErrPlacementExcluded reports that a placement is excluded (active, verified
// and not replacement-eligible): it stays put and has no target.
var ErrPlacementExcluded error = placementExcludedError{}

// RenderExclusionReason is the typed exclusion reason a render error carries
// (config_incomplete:<item> or placement_excluded), or "" for any other error.
func RenderExclusionReason(err error) string {
	var typed interface{ ExcludeReasonCode() string }
	if errors.As(err, &typed) {
		return typed.ExcludeReasonCode()
	}
	return ""
}

// isConfigIncompleteReason reports a well-formed config_incomplete:<item>
// reason, the form the exclusion reason counters keep.
func isConfigIncompleteReason(value string) bool {
	item, ok := strings.CutPrefix(value, ConfigIncompletePrefix)
	if !ok {
		return false
	}
	switch item {
	case ConfigItemBindingMissingBGM, ConfigItemBindingMissingTMDB:
		return true
	}
	names, ok := strings.CutPrefix(item, ConfigItemTemplateVariableUndefinedPrefix)
	return ok && names != "" && len(names) <= 200
}

// templateVariableUndefined converts the engine's undefined-variable refusal
// into the config-incomplete error naming the variables.
func templateVariableUndefined(err error) error {
	var named *naming.Error
	if errors.As(err, &named) && named.Code == naming.ErrCodeNamingFactUndefined && len(named.Missing) > 0 {
		return &ConfigIncompleteError{Item: ConfigItemTemplateVariableUndefinedPrefix + strings.Join(named.Missing, ",")}
	}
	return err
}

// currentAnimeConfig is the configuration a file of the anime renders with:
// the owner-edited season, year and media kind and the metadata binding with
// its bound works' titles. Nothing in it comes from the file's acquisition.
type currentAnimeConfig struct {
	bgmWorkID  string
	tmdbWorkID string
	// tmdbBindingKind is the kind of the bound TMDB work (the title row key).
	tmdbBindingKind string
	tmdbSeason      sql.NullInt64
	ownerSeason     sql.NullInt64
	ownerYear       sql.NullInt64
	// mediaKindOverride is the stored owner media kind; NULL reads as tv.
	mediaKindOverride sql.NullString
	titles            namingvars.AnimeEvidence
}

// effectiveMediaKind is the owner media kind: only movie selects the movie
// branch, an unset kind is tv.
func (c currentAnimeConfig) effectiveMediaKind() string {
	if strings.TrimSpace(c.mediaKindOverride.String) == "movie" {
		return "movie"
	}
	return "tv"
}

// check reports the first missing input: a missing binding is the metadata
// hold's own basis (BGM work id blank, TMDB work id absent).
func (c currentAnimeConfig) check() error {
	if strings.TrimSpace(c.bgmWorkID) == "" {
		return &ConfigIncompleteError{Item: ConfigItemBindingMissingBGM}
	}
	if strings.TrimSpace(c.tmdbWorkID) == "" {
		return &ConfigIncompleteError{Item: ConfigItemBindingMissingTMDB}
	}
	return nil
}

// apply writes the configuration into the render context, leaving every
// release and file fact alone. An unset owner season or year stays nil: the
// template renders it empty.
func (c currentAnimeConfig) apply(facts *naming.AnimeNamingFacts) {
	facts.BGM = naming.BGMNamingFacts{WorkID: c.bgmWorkID, Title: c.titles.BgmTitle, TitleCN: c.titles.BgmTitleCN, Year: providerYear(c.titles.BgmYear)}
	facts.TMDB = naming.TMDBNamingFacts{
		WorkID: c.tmdbWorkID, MediaKind: c.effectiveMediaKind(),
		TitleCN: c.titles.TmdbTitleCN, OriginalTitle: c.titles.TmdbOriginalTitle, Year: providerYear(c.titles.TmdbYear),
	}
	if c.tmdbSeason.Valid {
		season := int(c.tmdbSeason.Int64)
		facts.TMDB.Season, facts.TMDB.SeasonPresent, facts.TMDB.SeasonBound = season, true, season == 0
	}
	facts.Year, facts.Season = nil, nil
	if c.ownerYear.Valid {
		year := int(c.ownerYear.Int64)
		facts.Year = &year
	}
	if c.ownerSeason.Valid && c.ownerSeason.Int64 >= 0 {
		season := int(c.ownerSeason.Int64)
		facts.Season = &season
	}
}

// currentAnimeConfig reads the anime's current configuration from persisted
// rows only; it never asks a provider. An anime without a metadata row has
// neither binding.
func (r *NamingTargetRenderer) currentAnimeConfig(ctx context.Context, animeID string) (currentAnimeConfig, error) {
	var c currentAnimeConfig
	var bgm, tmdb, tmdbKind sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season, season, year, media_kind_override FROM anime_metadata WHERE managed_anime_id=$1`, animeID).
		Scan(&bgm, &tmdb, &tmdbKind, &c.tmdbSeason, &c.ownerSeason, &c.ownerYear, &c.mediaKindOverride)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("read current anime configuration: %w", err)
	}
	c.bgmWorkID, c.tmdbWorkID, c.tmdbBindingKind = strings.TrimSpace(bgm.String), strings.TrimSpace(tmdb.String), strings.TrimSpace(tmdbKind.String)
	if err := c.check(); err != nil {
		return c, err
	}
	c.titles = namingvars.AnimeEvidence{AnimeID: animeID, BgmID: c.bgmWorkID, TmdbID: c.tmdbWorkID, TmdbKind: c.tmdbBindingKind}
	if err := namingvarspersistence.FillWorkTitles(ctx, r.db, &c.titles); err != nil {
		return c, err
	}
	return c, nil
}
