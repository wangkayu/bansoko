package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/events"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
	selectionapp "bansoko/internal/selection/application"
)

// ListGlobalSections returns every global config section from the single typed owner.
func (s *Service) ListGlobalSections(ctx context.Context) ([]configurationdomain.GlobalSectionView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT section_name, schema_version, value, version FROM global_config_sections ORDER BY section_name`)
	if err != nil {
		return nil, fmt.Errorf("list global config sections: %w", err)
	}
	defer rows.Close()
	saved := map[string]configurationdomain.GlobalSectionView{}
	for rows.Next() {
		var name, schema string
		var value []byte
		var version int64
		if err := rows.Scan(&name, &schema, &value, &version); err != nil {
			return nil, fmt.Errorf("scan global config section: %w", err)
		}
		if name == animedomain.SectionTagFilters {
			value = animedomain.NormalizeTagFilterRulesJSON(value)
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(value, &parsed); err != nil {
			return nil, fmt.Errorf("decode global config section %q value: %w", name, err)
		}
		saved[name] = configurationdomain.GlobalSectionView{SectionName: name, SchemaVersion: schema, Value: parsed, Version: version, Saved: true}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate global config sections: %w", err)
	}
	result := make([]configurationdomain.GlobalSectionView, 0, len(animedomain.SectionSchemaVersions))
	for _, name := range animedomain.GlobalSectionOrder {
		if view, ok := saved[name]; ok {
			result = append(result, view)
			continue
		}
		value, err := animedomain.DefaultSectionValue(name)
		if err != nil {
			return nil, fmt.Errorf("resolve default for section %q: %w", name, err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode default for section %q: %w", name, err)
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, err
		}
		result = append(result, configurationdomain.GlobalSectionView{SectionName: name, SchemaVersion: animedomain.SectionSchemaVersions[name], Value: parsed, Version: 1})
	}
	for _, name := range animedomain.GlobalOnlySectionOrder {
		view, err := globalOnlySectionView(saved, name)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	return result, nil
}

// globalOnlySectionView is the saved row of a Global-only section (no
// per-anime override), or its code default at version 1 when none is saved.
func globalOnlySectionView(saved map[string]configurationdomain.GlobalSectionView, name string) (configurationdomain.GlobalSectionView, error) {
	if view, ok := saved[name]; ok {
		return view, nil
	}
	value, err := animedomain.DefaultSectionValue(name)
	if err != nil {
		return configurationdomain.GlobalSectionView{}, fmt.Errorf("resolve default for section %q: %w", name, err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return configurationdomain.GlobalSectionView{}, fmt.Errorf("encode default for section %q: %w", name, err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return configurationdomain.GlobalSectionView{}, err
	}
	return configurationdomain.GlobalSectionView{SectionName: name, SchemaVersion: animedomain.SectionSchemaVersions[name], Value: parsed, Version: 1}, nil
}

// namingVarsSnapshot reads the loaded naming-vars snapshot for save-time
// template validation. The snapshot follows the active program version (set at
// start and on every activation), so a process without one is misconfigured:
// the save fails closed with the
// typed refusal rather than storing a template nobody can confirm.
func namingVarsSnapshot(field string) (map[string]map[string]string, error) {
	vars, _, err := namingvars.Current()
	if err != nil {
		return nil, &apperrors.Validation{
			Code:    naming.ErrCodeNamingVarsUnknown,
			Details: []apperrors.FieldViolation{{Field: stringPtr("value"), Code: naming.ErrCodeNamingVarsUnknown, Message: "naming vars are not loaded; check the naming variable program for section " + field}},
		}
	}
	return vars, nil
}

// PutGlobalSection saves one validated global section with expected-version CAS.
func (s *Service) PutGlobalSection(ctx context.Context, name string, expected int64, input configurationdomain.GlobalSectionInput) (configurationdomain.GlobalSectionView, error) {
	schema, ok := animedomain.SectionSchemaVersions[name]
	if !ok {
		return configurationdomain.GlobalSectionView{}, &apperrors.BadRequest{Code: "SECTION_NAME_INVALID", Message: "Unknown global config section."}
	}
	if input.SchemaVersion != schema {
		return configurationdomain.GlobalSectionView{}, &apperrors.Validation{Code: "SCHEMA_VERSION_MISMATCH", Details: []apperrors.FieldViolation{{Field: stringPtr("schema_version"), Code: "SCHEMA_VERSION_MISMATCH", Message: fmt.Sprintf("section %q requires schema_version %q", name, schema)}}}
	}
	raw, err := json.Marshal(input.Value)
	if err != nil {
		return configurationdomain.GlobalSectionView{}, err
	}
	// A naming-rules save validates `${vars.group.key}` references against the
	// loaded snapshot, and an unloaded snapshot is a misconfiguration rather than
	// a reason to skip the check.
	var vars map[string]map[string]string
	if name == animedomain.SectionNamingRules {
		loaded, err := namingVarsSnapshot(name)
		if err != nil {
			return configurationdomain.GlobalSectionView{}, err
		}
		vars = loaded
	}
	err = animedomain.ValidateSectionValueWithVars(name, raw, vars)
	if err == nil && name == animedomain.SectionNamingRules {
		err = animedomain.ValidateNamingSectionForSave(raw, vars)
	}
	if err != nil {
		var namingErr *naming.Error
		if errors.As(err, &namingErr) {
			return configurationdomain.GlobalSectionView{}, &apperrors.Validation{Code: namingErr.Code, Details: []apperrors.FieldViolation{{Field: stringPtr("value"), Code: namingErr.Code, Message: namingErr.Message}}}
		}
		var timeoutsErr *animedomain.DownloadTimeoutsError
		if errors.As(err, &timeoutsErr) && timeoutsErr.Field != "" {
			return configurationdomain.GlobalSectionView{}, &apperrors.Validation{Code: "SECTION_VALUE_INVALID", Details: []apperrors.FieldViolation{{Field: stringPtr("value." + timeoutsErr.Field), Code: "SECTION_VALUE_INVALID", Message: err.Error()}}}
		}
		var fileRuleErr *animedomain.FileRuleError
		if errors.As(err, &fileRuleErr) {
			return configurationdomain.GlobalSectionView{}, &apperrors.Validation{Code: fileRuleErr.Code, Details: []apperrors.FieldViolation{{Field: stringPtr("value." + fileRuleErr.Field), Code: fileRuleErr.Code, Message: err.Error()}}}
		}
		return configurationdomain.GlobalSectionView{}, &apperrors.Validation{Code: "SECTION_VALUE_INVALID", Details: []apperrors.FieldViolation{{Field: stringPtr("value"), Code: "SECTION_VALUE_INVALID", Message: err.Error()}}}
	}
	if name == animedomain.SectionTagFilters {
		raw = animedomain.NormalizeTagFilterRulesJSON(raw)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return configurationdomain.GlobalSectionView{}, fmt.Errorf("begin put global section: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "bansoko:global-config-section:"+name); err != nil {
		return configurationdomain.GlobalSectionView{}, err
	}
	var current int64
	var previous []byte
	err = tx.QueryRowContext(ctx, `SELECT version, value FROM global_config_sections WHERE section_name = $1 FOR UPDATE`, name).Scan(&current, &previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return configurationdomain.GlobalSectionView{}, err
	}
	if err == nil && current != expected {
		return configurationdomain.GlobalSectionView{}, &apperrors.VersionConflict{Owner: "global_config_section", CurrentVersion: current}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO global_config_sections(section_name, schema_version, value, version) VALUES ($1, $2, $3, 1) ON CONFLICT (section_name) DO UPDATE SET schema_version = EXCLUDED.schema_version, value = EXCLUDED.value, version = global_config_sections.version + 1, updated_at = NOW()`, name, input.SchemaVersion, raw); err != nil {
		return configurationdomain.GlobalSectionView{}, err
	}
	// The global naming is the default naming template's body, so this save
	// changed the template library too: bump its version so a library CAS
	// read before this save is refused.
	if name == animedomain.SectionNamingRules {
		if _, err := tx.ExecContext(ctx, `UPDATE naming_template_library SET version = version + 1, updated_at = NOW() WHERE singleton`); err != nil {
			return configurationdomain.GlobalSectionView{}, fmt.Errorf("bump naming template library version: %w", err)
		}
	}
	// A Global section is only the default a new draft copies ), with one exception: the site parts
	// of subgroups (provider_defaults, name_keywords) are live policy, so a
	// change to them recomputes every active instance's group selection in this
	// transaction. Its entries and max_rss stay new-draft defaults only.
	recomputed := false
	if name == animedomain.SectionSubgroups {
		changed, err := selectionapp.SitePolicyChanged(previous, raw)
		if err != nil {
			return configurationdomain.GlobalSectionView{}, err
		}
		if changed {
			if err := recomputeSiteSubgroupPolicyTx(ctx, s.db, tx); err != nil {
				return configurationdomain.GlobalSectionView{}, fmt.Errorf("recompute group selection for site policy: %w", err)
			}
			recomputed = true
		}
	}
	if err := tx.Commit(); err != nil {
		return configurationdomain.GlobalSectionView{}, err
	}
	if s.eventHub != nil {
		s.eventHub.Publish(events.Event{Target: events.TargetSubscribed})
		if recomputed {
			s.eventHub.Publish(events.Event{Target: events.TargetRSS})
		}
	}
	// JSONB must be scanned into []byte before Unmarshal: scanning directly
	// into a map type is refused by the driver ([]uint8 ->
	// *map[string]interface{} is unsupported).
	var nameOut, schemaOut string
	var rawValue []byte
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT section_name, schema_version, value, version FROM global_config_sections WHERE section_name = $1`, name).Scan(&nameOut, &schemaOut, &rawValue, &version); err != nil {
		return configurationdomain.GlobalSectionView{}, err
	}
	if name == animedomain.SectionTagFilters {
		rawValue = animedomain.NormalizeTagFilterRulesJSON(rawValue)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(rawValue, &parsed); err != nil {
		return configurationdomain.GlobalSectionView{}, fmt.Errorf("decode saved section %q value: %w", name, err)
	}
	return configurationdomain.GlobalSectionView{SectionName: nameOut, SchemaVersion: schemaOut, Value: parsed, Version: version, Saved: true}, nil
}

func stringPtr(value string) *string { return &value }
