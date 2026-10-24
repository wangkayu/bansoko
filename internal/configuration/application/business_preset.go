package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"bansoko/internal/anime/animeconfig"
	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
)

// ReadBusinessPreset reads the complete persisted preset in one repeatable-read
// snapshot. It creates no candidate or instance and performs no script evaluation.
// Missing policy/program rows fail closed: the HTTP contract must expose this
// operation before the UI may promise an atomic complete Global load.
func (s *Service) ReadBusinessPreset(ctx context.Context) (animeconfig.BusinessSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return animeconfig.BusinessSnapshot{}, err
	}
	defer tx.Rollback()
	snapshot, err := readBusinessPresetTx(ctx, tx)
	if err != nil {
		return animeconfig.BusinessSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return animeconfig.BusinessSnapshot{}, err
	}
	return snapshot, nil
}

// ReadGlobalTagFilters reads the current Global tag_filters value, or the code
// default before it is first saved. It is the value a new draft copies (one-time
// input): it is not part of the business snapshot and its digest.
func (s *Service) ReadGlobalTagFilters(ctx context.Context) (json.RawMessage, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM global_config_sections WHERE section_name = 'tag_filters'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return json.Marshal(animedomain.DefaultTagFilterRules())
	}
	if err != nil {
		return nil, fmt.Errorf("read global tag_filters: %w", err)
	}
	return append(json.RawMessage(nil), raw...), nil
}

// ReadBusinessPresetTx freezes the complete preset in the caller's transaction.
func ReadBusinessPresetTx(ctx context.Context, tx *sql.Tx) (animeconfig.BusinessSnapshot, error) {
	if tx == nil {
		return animeconfig.BusinessSnapshot{}, errors.New("business preset transaction is nil")
	}
	return readBusinessPresetTx(ctx, tx)
}

func readBusinessPresetTx(ctx context.Context, tx *sql.Tx) (animeconfig.BusinessSnapshot, error) {
	result := animeconfig.BusinessSnapshot{SchemaVersion: animedomain.BusinessSnapshotSchemaVersion, Sections: map[string]json.RawMessage{}, SectionVersions: map[string]int64{}, SectionSchemas: map[string]string{}}
	// The business snapshot carries exactly the six frozen sections; the
	// live-only tag_filters and download_completion Global rows are not part of it.
	// download_timeouts is Global-only and frozen into the acquisition limits;
	// metadata_refresh only drives the background sweep.
	rows, err := tx.QueryContext(ctx, `SELECT section_name, schema_version, value, version FROM global_config_sections WHERE section_name NOT IN ('tag_filters', 'download_completion', 'retention', 'parser_policy', 'file_effect_policy', 'download_timeouts', 'metadata_refresh')`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var name, schema string
		var raw []byte
		var version int64
		if err := rows.Scan(&name, &schema, &raw, &version); err != nil {
			rows.Close()
			return result, err
		}
		result.Sections[name] = append(json.RawMessage(nil), raw...)
		result.SectionVersions[name] = version
		result.SectionSchemas[name] = schema
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	// Missing ordinary sections use their declared code defaults. Provenance 0
	// makes absence explicit, unlike inventing a persisted Global version.
	for _, name := range animedomain.EffectiveSectionOrder {
		if _, ok := result.Sections[name]; !ok {
			v, err := animedomain.DefaultSectionValue(name)
			if err != nil {
				return result, err
			}
			raw, err := json.Marshal(v)
			if err != nil {
				return result, err
			}
			result.Sections[name] = raw
			result.SectionVersions[name] = 0
			result.SectionSchemas[name] = animedomain.SectionSchemaVersions[name]
		}
	}
	result.Parser, err = animeapplication.LoadGlobalParserPolicyEnvelopeTx(ctx, tx, result.SectionVersions[animedomain.SectionParser])
	if err != nil {
		return result, err
	}
	err = tx.QueryRowContext(ctx, `SELECT value->>'admission_mode', value->'quality_profile', value->'cleanup_policy', version FROM global_config_sections WHERE section_name='retention'`).Scan(&result.Retention.AdmissionMode, &result.Retention.QualityProfile, &result.Retention.CleanupPolicy, &result.RetentionVersion)
	if err != nil {
		return result, fmt.Errorf("read complete retention preset: %w", err)
	}
	err = tx.QueryRowContext(ctx, `SELECT value->>'unclassified_route', version FROM global_config_sections WHERE section_name='file_effect_policy'`).Scan(&result.UnclassifiedRoute, &result.FileEffectVersion)
	if errors.Is(err, sql.ErrNoRows) {
		// A fresh install has no persisted default yet. Every reader treats
		// absence as the code default route; provenance 0 records the absence,
		// exactly like a missing ordinary section above.
		result.UnclassifiedRoute, result.FileEffectVersion, err = "skip", 0, nil
	}
	if err != nil {
		return result, fmt.Errorf("read complete file effect preset: %w", err)
	}
	var varsContent, attrContent []byte
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,content FROM naming_vars_versions WHERE active`).Scan(&result.Programs.Vars.Digest, &varsContent)
	if err != nil {
		return result, fmt.Errorf("read Vars program reference: %w", err)
	}
	result.Programs.Vars.Version = namingvars.ProfileVersion(result.Programs.Vars.Digest)
	var source string
	if err := json.Unmarshal(varsContent, &source); err != nil {
		return result, err
	}
	if _, err := namingvars.NewProgram(result.Programs.Vars, source); err != nil {
		return result, err
	}
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,content FROM attr_rule_versions WHERE active`).Scan(&result.Programs.AttrRules.Digest, &attrContent)
	if err != nil {
		return result, fmt.Errorf("read attrrules reference: %w", err)
	}
	var rules attrrules.Rules
	if err := json.Unmarshal(attrContent, &rules); err != nil {
		return result, err
	}
	fp, err := attrrules.Fingerprint(rules)
	if err != nil {
		return result, err
	}
	if fp != result.Programs.AttrRules.Digest {
		return result, fmt.Errorf("attrrules source digest mismatch")
	}
	result.Programs.AttrRules.Version = attrrules.ProfileVersion(fp)
	return result.Freeze()
}
