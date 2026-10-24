package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
)

// maxWorkTitleBytes bounds one recorded title (metadata_work_titles).
const maxWorkTitleBytes = 512

// maxWorkOverviewBytes bounds the recorded TMDB work summary (metadata_work_
// titles.overview). A TMDB overview may exceed a title's length; the frozen
// input member and the database CHECK share this bound.
const maxWorkOverviewBytes = 2048

// RecordWorkTitles upserts the latest native titles observed for each work.
func (r *Repository) RecordWorkTitles(ctx context.Context, titles []identitymetadatadomain.WorkTitles) error {
	if r == nil || r.db == nil {
		return errors.New("metadata work titles database is nil")
	}
	return UpsertWorkTitles(ctx, r.db, titles)
}

// Execer is a database handle or transaction.
type Execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// UpsertWorkTitles records titles through exec (inside the caller's
// transaction when exec is one). A title that is blank, oversized or not
// valid UTF-8 is stored as unknown; a year outside the supported range is
// stored as NULL. A work with no usable title, overview and year is skipped.
func UpsertWorkTitles(ctx context.Context, exec Execer, titles []identitymetadatadomain.WorkTitles) error {
	for _, work := range titles {
		provider, kind, id := strings.TrimSpace(work.Provider), strings.TrimSpace(work.MediaKind), strings.TrimSpace(work.WorkID)
		if id == "" || len(id) > 128 || !((provider == "bgm" && kind == "subject") || (provider == "tmdb" && (kind == "tv" || kind == "movie"))) {
			continue
		}
		title, titleCN, original := workTitle(work.Title), workTitle(work.TitleCN), workTitle(work.OriginalTitle)
		overview := workOverview(work.Overview)
		year := identitymetadatadomain.NormalizeYear(work.Year)
		if provider == "bgm" {
			original = nil
			overview = nil
		} else {
			// A tmdb row stores the request-language title in title_cn and
			// leaves title NULL: the recorded value is TMDB's zh-CN name, and
			// original_title keeps the native one. overview is the work
			// summary (TMDB only; BGM has no equivalent surface).
			title = nil
		}
		if title == nil && titleCN == nil && original == nil && overview == nil && year == nil {
			continue
		}
		var yearArg any
		if year != nil {
			yearArg = *year
		}
		if _, err := exec.ExecContext(ctx, `
INSERT INTO metadata_work_titles(provider, media_kind, work_id, title, title_cn, original_title, overview, year, observed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, CURRENT_TIMESTAMP)
ON CONFLICT (provider, media_kind, work_id) DO UPDATE
SET title = EXCLUDED.title, title_cn = EXCLUDED.title_cn, original_title = EXCLUDED.original_title, overview = EXCLUDED.overview, year = EXCLUDED.year, observed_at = EXCLUDED.observed_at`,
			provider, kind, id, title, titleCN, original, overview, yearArg); err != nil {
			return fmt.Errorf("record metadata work titles: %w", err)
		}
	}
	return nil
}

func workTitle(value string) any {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxWorkTitleBytes || !utf8.ValidString(value) {
		return nil
	}
	return value
}

func workOverview(value string) any {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxWorkOverviewBytes || !utf8.ValidString(value) {
		return nil
	}
	return value
}
