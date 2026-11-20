package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	animedomain "bansoko/internal/anime/domain"
)

// SettleInitialTitleTx consumes the one-time choice only once both bindings
// exist. The caller owns the anime row and the transaction that publishes the
// bindings or starts the subscription. It never fetches a title from a provider.
func SettleInitialTitleTx(ctx context.Context, tx *sql.Tx, id string) (settled bool, replacement *string, err error) {
	var candidate sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT CASE pref.value->>'source'
         WHEN $3 THEN tmdb.title_cn
         WHEN $4 THEN tmdb.original_title
         WHEN $5 THEN bgm.title_cn
         WHEN $6 THEN bgm.title
       END
FROM managed_anime a
JOIN anime_metadata m ON m.managed_anime_id = a.id
LEFT JOIN global_config_sections pref ON pref.section_name = $2
LEFT JOIN metadata_work_titles tmdb ON tmdb.provider = 'tmdb' AND tmdb.media_kind = m.tmdb_media_kind AND tmdb.work_id = m.tmdb_work_id
LEFT JOIN metadata_work_titles bgm ON bgm.provider = 'bgm' AND bgm.media_kind = 'subject' AND bgm.work_id = m.bgm_work_id
WHERE a.id = $1 AND a.initial_title_pending AND m.bgm_work_id <> '' AND m.tmdb_work_id <> ''
  AND m.tmdb_media_kind IN ('tv', 'movie') AND m.tmdb_season >= 0`, id, animedomain.SectionSubscriptionTitle,
		animedomain.TitleSourceTMDBCN, animedomain.TitleSourceTMDBOriginal, animedomain.TitleSourceBGMCN, animedomain.TitleSourceBGM).Scan(&candidate)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("read initial subscription title: %w", err)
	}
	if title := strings.TrimSpace(candidate.String); title != "" {
		replacement = &title
	}
	if _, err := tx.ExecContext(ctx, `UPDATE managed_anime SET initial_title_pending = false, title_override = COALESCE($2, title_override) WHERE id = $1`, id, replacement); err != nil {
		return false, nil, fmt.Errorf("settle initial subscription title: %w", err)
	}
	return true, replacement, nil
}
