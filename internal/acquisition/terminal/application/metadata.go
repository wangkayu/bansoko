package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	anime "bansoko/internal/anime/domain"
)

func metadataVersionOf(m qualificationMaterial) any {
	if m.config.kind == "owner" {
		return m.metadataVersion
	}
	return nil
}

// Held is a captured fact. Only a one-shot (a temporary owner that never
// enters the library) bypasses its release verdict; the automatic entry and the
// manual retry both respect it unless the Global release-without-metadata
// switch releases the hold. Missing bindings stay missing in the frozen native
// input either way.
func admissionMetadataHold(c admissionCapture, allowMissingLinkage bool) anime.MetadataHold {
	return anime.AdmittedMetadataHold(anime.MetadataHoldFromBindings(c.material.bgm, c.material.tmdb), allowMissingLinkage)
}
func bypassMetadataHold(mode string) bool {
	return mode == "one_shot_seed" || mode == "one_shot_snapshot"
}

// readMetadataHoldAllow reads the Global release-without-metadata switch in the
// accepting transaction, so admission decides with the same value the entry
// recorded. A missing row is the code default (off); an unreadable or
// unversioned saved row fails the acceptance closed instead of guessing.
func readMetadataHoldAllow(ctx context.Context, tx *sql.Tx) (bool, error) {
	var schema string
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT schema_version, value FROM global_config_sections WHERE section_name = $1`, anime.SectionMetadataHold).Scan(&schema, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return anime.DefaultMetadataHoldRules().AllowMissingLinkage, nil
	}
	if err != nil {
		return false, fmt.Errorf("read metadata_hold setting: %w", err)
	}
	if schema != anime.MetadataHoldSchemaVersion {
		return false, fmt.Errorf("metadata_hold: unsupported schema version %q", schema)
	}
	rules, err := anime.DecodeMetadataHold(raw)
	if err != nil {
		return false, fmt.Errorf("metadata_hold: %w", err)
	}
	return rules.AllowMissingLinkage, nil
}
