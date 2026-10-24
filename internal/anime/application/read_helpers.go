package application

import (
	"context"
	"database/sql"
	"fmt"

	"bansoko/internal/anime/animeconfig"
	animedomain "bansoko/internal/anime/domain"
)

// RulesSectionOrder is the canonical owner-section write order.
var RulesSectionOrder = []string{
	animedomain.SectionParser,
	animedomain.SectionReleaseRules,
	animedomain.SectionFileRules,
	animedomain.SectionNamingRules,
	animedomain.SectionFileEffect,
}

var rulesSectionOrder = RulesSectionOrder

// UpsertOverride persists one complete per-anime section value. There is no
// inherit state: an empty value fails closed before anything is written.
func UpsertOverride(ctx context.Context, tx *sql.Tx, animeID, ownerType, section string, override animedomain.OverrideValue) error {
	if len(override.Value) == 0 {
		return fmt.Errorf("persist section %s: a complete value is required", section)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (managed_anime_id, section_name) DO UPDATE SET schema_version = EXCLUDED.schema_version, value = EXCLUDED.value, updated_at = NOW()`,
		animeID, ownerType, section, animedomain.SectionSchemaVersions[section], override.Value); err != nil {
		return fmt.Errorf("persist section %s: %w", section, err)
	}
	return nil
}

// NullableStringPointer projects a nullable database string into an owned pointer.
func NullableStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func nullableStringPointer(value sql.NullString) *string { return NullableStringPointer(value) }

// NullableIntPointer projects a nullable database integer into an owned pointer.
func NullableIntPointer(value sql.NullInt32) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int32)
	return &result
}

func nullableIntPointer(value sql.NullInt32) *int { return NullableIntPointer(value) }

func (s *Service) readOverrides(ctx context.Context, managedAnimeID string) (subgroups animedomain.OverrideValue, rules map[string]animedomain.OverrideValue, err error) {
	return readOverridesFrom(ctx, s.db, managedAnimeID)
}

// ReadOverridesFrom reads the section values an instance stores using a
// caller-owned queryer. A row without a complete value fails closed.
func ReadOverridesFrom(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, managedAnimeID string) (subgroups animedomain.OverrideValue, rules map[string]animedomain.OverrideValue, err error) {
	return animeconfig.LoadStoredSections(ctx, queryer, managedAnimeID)
}

func readOverridesFrom(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, managedAnimeID string) (animedomain.OverrideValue, map[string]animedomain.OverrideValue, error) {
	return ReadOverridesFrom(ctx, queryer, managedAnimeID)
}

func cloneNullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return cloneStringPointer(&value.String)
}

// CloneBool copies an optional boolean without sharing its storage.
func CloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneBool(value *bool) *bool { return CloneBool(value) }

func upsertOverride(ctx context.Context, tx *sql.Tx, animeID, ownerType, section string, override animedomain.OverrideValue) error {
	return UpsertOverride(ctx, tx, animeID, ownerType, section, override)
}
