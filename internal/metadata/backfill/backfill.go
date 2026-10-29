// Package backfill is the single fill-if-empty rule shared by the scheduled
// metadata_match automation and the on-demand POST /anime/{id}/metadata/match.
// A matcher result fills only fields that are still empty; a stored value is
// never overwritten; a partial result (for example BGM alone) is persisted.
package backfill

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	workTitles "bansoko/internal/identitymetadata/adapter/persistence"
	"bansoko/internal/metadata/matcher"
)

// Outcome is the shared result classification both paths record.
type Outcome string

const (
	OutcomeComplete Outcome = "complete"
	OutcomePartial  Outcome = "partial"
	OutcomeNoResult Outcome = "no_result"
)

// Stored is the backfillable part of anime_metadata. The TMDB tuple is
// present only when all three members are.
type Stored struct {
	BGMWorkID     *string
	TMDBWorkID    *string
	TMDBKind      *string
	TMDBSeason    *int
	TotalEpisodes *int
	Year          *int
	// Season is the owner-edited naming season. It is filled from the matched
	// TMDB season only when this merge writes the TMDB tuple (the one-time
	// prefill at binding) and only while it is unset.
	Season *int
	// MediaKind is the owner media kind (media_kind_override; unset is tv). It
	// is filled from the matched TMDB kind only when this merge writes the
	// TMDB tuple and only while it is unset.
	MediaKind *string
}

func (s Stored) HasBGM() bool { return s.BGMWorkID != nil && *s.BGMWorkID != "" }

func (s Stored) HasTMDB() bool {
	return s.TMDBWorkID != nil && *s.TMDBWorkID != "" && s.TMDBSeason != nil && *s.TMDBSeason >= 0 &&
		s.TMDBKind != nil && (*s.TMDBKind == "tv" || *s.TMDBKind == "movie")
}

// Complete reports whether the BGM binding and the whole TMDB tuple exist.
func (s Stored) Complete() bool { return s.HasBGM() && s.HasTMDB() }

// Plan is the result of applying a match to stored metadata.
type Plan struct {
	Merged                                Stored
	NewBGM, NewTMDB, NewEpisodes, NewYear bool
	NewSeason, NewMediaKind               bool
}

// Changed reports whether any field would be written.
func (p Plan) Changed() bool {
	return p.NewBGM || p.NewTMDB || p.NewEpisodes || p.NewYear || p.NewSeason || p.NewMediaKind
}

// Merge fills each empty field from the match. TMDB is written as a tuple or
// not at all. Year comes from the matched TMDB work and only while empty;
// Season and MediaKind come from the TMDB tuple this merge binds and only
// while empty.
func Merge(current Stored, match *matcher.MatchResult) Plan {
	plan := Plan{Merged: current}
	if match == nil {
		return plan
	}
	m := &plan.Merged
	if !current.HasBGM() && match.BgmID != nil && *match.BgmID != "" {
		id := *match.BgmID
		m.BGMWorkID, plan.NewBGM = &id, true
	}
	if !current.HasTMDB() && match.TmdbWorkID != nil && match.TmdbKind != nil && match.TmdbSeason != nil &&
		*match.TmdbWorkID != "" && (*match.TmdbKind == "tv" || *match.TmdbKind == "movie") && *match.TmdbSeason >= 0 {
		id, kind, season := *match.TmdbWorkID, *match.TmdbKind, *match.TmdbSeason
		m.TMDBWorkID, m.TMDBKind, m.TMDBSeason, plan.NewTMDB = &id, &kind, &season, true
		// The naming season is prefilled once, when the TMDB tuple is bound,
		// and only while unset; afterwards the two are independent. A movie
		// binding names no season.
		if current.Season == nil && kind == "tv" {
			owner := season
			m.Season, plan.NewSeason = &owner, true
		}
		// The owner media kind is backfilled once from the bound TMDB kind,
		// and only while unset; replacing the tuple later never changes it.
		if current.MediaKind == nil {
			owner := kind
			m.MediaKind, plan.NewMediaKind = &owner, true
		}
	}
	if current.TotalEpisodes == nil && match.TotalEpisodes != nil {
		n := *match.TotalEpisodes
		m.TotalEpisodes, plan.NewEpisodes = &n, true
	}
	if current.Year == nil && match.TmdbYear != nil &&
		*match.TmdbYear >= animedomain.MinAnimeYear && *match.TmdbYear <= animedomain.MaxAnimeYear {
		y := *match.TmdbYear
		m.Year, plan.NewYear = &y, true
	}
	return plan
}

// Classify gives the outcome of a successful matcher call.
func Classify(plan Plan, match *matcher.MatchResult) Outcome {
	switch {
	case plan.Merged.Complete():
		return OutcomeComplete
	case plan.Changed() || match.HasAny():
		return OutcomePartial
	default:
		return OutcomeNoResult
	}
}

// ApplyTx writes the plan guarded by a CAS on the metadata version. It
// returns false and writes nothing when the version moved (the caller skips
// the round; the next run retries) or when the plan changes nothing. A stored
// value is never overwritten: only the planned empty fields appear in SET.
// The native names of the newly bound works are recorded in the same tx.
//
// A media-kind fill that would change the effective kind (unset is tv) of an
// instance whose identity is in use (active files or acquisitions that have
// not settled) is
// dropped from the plan before the write: the kind stays unset and every other
// planned field is still written. The caller reads the plan afterwards for
// what was written.
func ApplyTx(ctx context.Context, tx *sql.Tx, managedAnimeID string, expectedVersion int64, plan *Plan, match *matcher.MatchResult) (bool, error) {
	if plan.NewMediaKind {
		// Merge fills the kind only while it is unset, so the stored kind is
		// NULL here.
		allowed, _, err := animeapplication.IdentityKindChangeAllowedTx(ctx, tx, managedAnimeID, nil, plan.Merged.MediaKind)
		if err != nil {
			return false, err
		}
		if !allowed {
			plan.Merged.MediaKind, plan.NewMediaKind = nil, false
		}
	}
	if !plan.Changed() {
		return false, nil
	}
	var sets []string
	args := []any{managedAnimeID, expectedVersion}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if plan.NewBGM {
		add("bgm_work_id", *plan.Merged.BGMWorkID)
	}
	if plan.NewTMDB {
		add("tmdb_work_id", *plan.Merged.TMDBWorkID)
		add("tmdb_media_kind", *plan.Merged.TMDBKind)
		add("tmdb_season", *plan.Merged.TMDBSeason)
	}
	if plan.NewEpisodes {
		add("total_episodes", *plan.Merged.TotalEpisodes)
	}
	if plan.NewYear {
		add("year", *plan.Merged.Year)
	}
	if plan.NewSeason {
		add("season", *plan.Merged.Season)
	}
	if plan.NewMediaKind {
		add("media_kind_override", *plan.Merged.MediaKind)
	}
	res, err := tx.ExecContext(ctx, `UPDATE anime_metadata SET `+strings.Join(sets, ", ")+
		`, version = version + 1, updated_at = NOW() WHERE managed_anime_id = $1 AND version = $2`, args...)
	if err != nil {
		return false, fmt.Errorf("save matched metadata: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	if err := workTitles.UpsertWorkTitles(ctx, tx, match.WorkTitles(plan.NewBGM, plan.NewTMDB)); err != nil {
		return false, err
	}
	// A filled binding or owner value may complete the configuration an
	// import waits for.
	if err := animeapplication.WakeConfigWaitingPlansTx(ctx, tx, managedAnimeID); err != nil {
		return false, err
	}
	// The bindings, year, season and media kind are render inputs: the files
	// re-render under the filled values. The total episodes never reach a path.
	if plan.NewBGM || plan.NewTMDB || plan.NewYear || plan.NewSeason || plan.NewMediaKind {
		if err := animeapplication.RequestConfigScanTx(ctx, tx, managedAnimeID); err != nil {
			return false, err
		}
	}
	return true, nil
}
