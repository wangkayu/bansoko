package application

import (
	"bansoko/internal/anime/metadatabinding"
	"context"
	"database/sql"
	"reflect"
)

func captureMountMetadataTx(ctx context.Context, tx *sql.Tx, m *qualificationMaterial) error {
	var bgm, tmdb, kind, override sql.NullString
	var season sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT bgm_work_id,tmdb_work_id,tmdb_media_kind,tmdb_season,media_kind_override,version FROM anime_metadata WHERE managed_anime_id=$1`, m.management.managedID).Scan(&bgm, &tmdb, &kind, &season, &override, &m.metadataVersion); err != nil {
		return materialError("metadata", err)
	}
	b, t, err := metadatabinding.BindingsFromColumns(bgm, tmdb, kind, season, m.management.managedID)
	if err != nil {
		return materialError("metadata", err)
	}
	if !reflect.DeepEqual(b, m.bgm) || !reflect.DeepEqual(t, m.tmdb) || !reflect.DeepEqual(nullableMetadataOverride(override), m.mediaKindOverride) {
		return acceptError(500, "INTERNAL_INVARIANT", "mount_metadata_binding_mismatch", nil)
	}
	return nil
}

// nullableMetadataOverride projects the nullable media_kind_override column
// into the optional owner value (nil means unset).
func nullableMetadataOverride(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	trimmed := value.String
	return &trimmed
}
