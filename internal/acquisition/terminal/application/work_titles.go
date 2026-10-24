package application

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// workTitles are the native titles, the provider's year and (TMDB only) the
// work summary recorded for one bound work. A blank value is unknown; year is
// nil when never observed.
type workTitles struct {
	title, titleCN, originalTitle string
	overview                      string
	year                          *int
}

// captureWorkTitlesTx reads the recorded native titles, year and TMDB overview
// of the works the material binds and the release's frozen source facts,
// inside the acceptance transaction. A work never observed by metadata search
// or matching has no titles and no year; nothing is derived.
func captureWorkTitlesTx(ctx context.Context, tx *sql.Tx, m *qualificationMaterial) error {
	read := func(provider, kind, id string) (*workTitles, error) {
		if strings.TrimSpace(id) == "" {
			return nil, nil
		}
		var title, titleCN, original, overview sql.NullString
		var year sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT title, title_cn, original_title, overview, year FROM metadata_work_titles WHERE provider=$1 AND media_kind=$2 AND work_id=$3`, provider, kind, id).Scan(&title, &titleCN, &original, &overview, &year)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		titles := &workTitles{title: title.String, titleCN: titleCN.String, originalTitle: original.String, overview: overview.String}
		if year.Valid {
			value := int(year.Int64)
			titles.year = &value
		}
		return titles, nil
	}
	var err error
	m.bgmTitles, m.tmdbTitles, m.releaseSourceFacts = nil, nil, nil
	if strings.TrimSpace(m.release.id) != "" {
		var facts []byte
		err = tx.QueryRowContext(ctx, `SELECT source_facts FROM releases WHERE id=$1`, m.release.id).Scan(&facts)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		m.releaseSourceFacts = facts
	}
	if m.bgm != nil {
		if m.bgmTitles, err = read("bgm", "subject", m.bgm.WorkId); err != nil {
			return err
		}
	}
	if m.tmdb != nil {
		if m.tmdbTitles, err = read("tmdb", string(m.tmdb.MediaKind), m.tmdb.WorkId); err != nil {
			return err
		}
	}
	return nil
}

// frozenWorkTitles is the acquisition input's frozen titles value: known with
// the provider's native names/year/summary (null where unknown), or absent
// when the work was never observed and nothing is known. overview is present
// only for the provider that actually records one (TMDB).
func frozenWorkTitles(titles *workTitles, fields map[string]string) any {
	if titles == nil {
		return frozenValue("absent", nil)
	}
	value := map[string]any{"year": nil}
	known := false
	if titles.year != nil {
		value["year"], known = int64(*titles.year), true
	}
	for key, raw := range fields {
		if strings.TrimSpace(raw) == "" {
			value[key] = nil
			continue
		}
		value[key], known = raw, true
	}
	if !known {
		return frozenValue("absent", nil)
	}
	return frozenValue("known", value)
}
