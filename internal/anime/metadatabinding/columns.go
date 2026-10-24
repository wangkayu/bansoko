// Package metadatabinding owns the existing persisted-column decoding semantics.
package metadatabinding

import (
	animedomain "bansoko/internal/anime/domain"
	"database/sql"
	"fmt"
	"strings"
)

func BindingsFromColumns(bgmWorkID, tmdbWorkID, tmdbMediaKind sql.NullString, tmdbSeason sql.NullInt64, managedAnimeID string) (*animedomain.BGMBinding, *animedomain.TMDBBinding, error) {
	var bgm *animedomain.BGMBinding
	if bgmWorkID.Valid {
		if strings.TrimSpace(bgmWorkID.String) == "" {
			return nil, nil, fmt.Errorf("metadata BGM binding is empty for anime %q", managedAnimeID)
		}
		bgm = &animedomain.BGMBinding{WorkId: bgmWorkID.String}
	}

	tmdbFieldsPresent := tmdbWorkID.Valid || tmdbMediaKind.Valid || tmdbSeason.Valid
	if !tmdbFieldsPresent {
		return bgm, nil, nil
	}
	if !tmdbWorkID.Valid || !tmdbMediaKind.Valid || !tmdbSeason.Valid ||
		strings.TrimSpace(tmdbWorkID.String) == "" ||
		(tmdbMediaKind.String != string(animedomain.TMDBMediaKindTv) && tmdbMediaKind.String != string(animedomain.TMDBMediaKindMovie)) ||
		tmdbSeason.Int64 < 0 {
		return nil, nil, fmt.Errorf("metadata TMDB binding is incomplete or invalid for anime %q", managedAnimeID)
	}
	return bgm, &animedomain.TMDBBinding{
		WorkId:    tmdbWorkID.String,
		MediaKind: animedomain.TMDBMediaKind(tmdbMediaKind.String),
		Season:    int(tmdbSeason.Int64),
	}, nil
}
