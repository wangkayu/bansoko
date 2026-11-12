// Package animeconfig reads one instance's complete effective configuration:
// the section values the instance stores for itself (every per-anime section
// is one-time input, copied from Global at creation and never re-read), plus
// the Global-only download completion setting.
//
// It exists so every consumer that must apply the SAME selected profile — RSS
// release evaluation and Artifact per-file recognition — resolves from one
// implementation instead of each package growing its own query. Resolution is
// side-effect free and never reads the mutable draft.
package animeconfig

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	animedomain "bansoko/internal/anime/domain"
)

// Querier is the minimal read surface needed to resolve an instance config. It
// accepts both *sql.DB and *sql.Tx (and txscope wrappers) so the loader can run
// inside an owner transaction.
type Querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// LoadEffective resolves the complete effective configuration for one instance
// from the values it stores. A missing section fails closed. It performs no
// writes.
func LoadEffective(ctx context.Context, q Querier, managedAnimeID string) (animedomain.EffectiveConfig, error) {
	_, effective, err := LoadResolvedEffective(ctx, q, managedAnimeID)
	return effective, err
}

// LoadResolvedEffective resolves the complete effective configuration and its
// digest-ready ResolvedSections in one pass. It eliminates the need for callers
// to apply live sections after decoding: the returned EffectiveConfig already
// carries the instance's real tag filters and the Global download completion.
func LoadResolvedEffective(ctx context.Context, q Querier, managedAnimeID string) (animedomain.ResolvedSections, animedomain.EffectiveConfig, error) {
	resolved, live, err := loadResolvedWithLive(ctx, q, managedAnimeID)
	if err != nil {
		return animedomain.ResolvedSections{}, animedomain.EffectiveConfig{}, err
	}
	decoded, err := animedomain.DecodeEffectiveSections(resolved)
	if err != nil {
		return animedomain.ResolvedSections{}, animedomain.EffectiveConfig{}, err
	}
	effective, err := animedomain.NewEffectiveConfig(decoded, live.TagFilters, live.DownloadCompletion)
	if err != nil {
		return animedomain.ResolvedSections{}, animedomain.EffectiveConfig{}, err
	}
	return resolved, effective, nil
}

// liveSections are the sections resolved next to the six frozen ones:
// the instance's stored tag_filters and the Global-only download_completion.
// They never enter ResolvedSections, so frozen envelopes and digests keep
// their shapes. This type is an internal intermediate; callers should use
// LoadResolvedEffective or LoadEffective instead of applying live sections
// manually.
type liveSections struct {
	TagFilters         animedomain.ResolvedTagFilters
	DownloadCompletion animedomain.ResolvedDownloadCompletion
}

// loadResolvedWithLive is LoadResolved plus the instance's tag_filters and the
// Global-only download_completion setting.
func loadResolvedWithLive(ctx context.Context, q Querier, managedAnimeID string) (animedomain.ResolvedSections, liveSections, error) {
	subgroupsOverride, rulesOverrides, err := LoadStoredSections(ctx, q, managedAnimeID)
	if err != nil {
		return animedomain.ResolvedSections{}, liveSections{}, err
	}
	resolved, err := animedomain.ResolveEffectiveSections(subgroupsOverride, rulesOverrides)
	if err != nil {
		return animedomain.ResolvedSections{}, liveSections{}, err
	}
	tagFilters, err := animedomain.ResolveTagFilters(rulesOverrides[animedomain.SectionTagFilters])
	if err != nil {
		return animedomain.ResolvedSections{}, liveSections{}, err
	}
	downloadCompletion, err := LoadDownloadCompletion(ctx, q)
	if err != nil {
		return animedomain.ResolvedSections{}, liveSections{}, err
	}
	return resolved, liveSections{TagFilters: tagFilters, DownloadCompletion: downloadCompletion}, nil
}

// LoadResolved resolves the complete effective configuration from the
// instance's stored sections while retaining every section's source version and
// schema. Callers that admit asynchronous work should persist this result.
func LoadResolved(ctx context.Context, q Querier, managedAnimeID string) (animedomain.ResolvedSections, error) {
	subgroupsOverride, rulesOverrides, err := LoadStoredSections(ctx, q, managedAnimeID)
	if err != nil {
		return animedomain.ResolvedSections{}, err
	}
	return animedomain.ResolveEffectiveSections(subgroupsOverride, rulesOverrides)
}

// LoadStoredSections reads the section values one instance stores: its
// subgroups section and its rules sections keyed by section name. A stored
// value is always complete; a row without one fails closed.
func LoadStoredSections(ctx context.Context, q Querier, managedAnimeID string) (animedomain.OverrideValue, map[string]animedomain.OverrideValue, error) {
	rows, err := q.QueryContext(ctx, `
SELECT owner_type, section_name, schema_version, value
FROM anime_config_overrides
WHERE managed_anime_id = $1`, managedAnimeID)
	if err != nil {
		return animedomain.OverrideValue{}, nil, fmt.Errorf("read anime config sections: %w", err)
	}
	defer rows.Close()
	var subgroupsOverride animedomain.OverrideValue
	rulesOverrides := map[string]animedomain.OverrideValue{}
	for rows.Next() {
		var owner, section string
		var schemaVersion sql.NullString
		var value []byte
		if err := rows.Scan(&owner, &section, &schemaVersion, &value); err != nil {
			return animedomain.OverrideValue{}, nil, fmt.Errorf("scan anime config section: %w", err)
		}
		if expectedOwner, ok := configSectionOwner(section); !ok || owner != expectedOwner {
			return animedomain.OverrideValue{}, nil, fmt.Errorf("anime config section %q has invalid owner %q", section, owner)
		}
		if !schemaVersion.Valid || schemaVersion.String != animedomain.SectionSchemaVersions[section] {
			return animedomain.OverrideValue{}, nil, fmt.Errorf("anime config section %q has unsupported schema version %q", section, schemaVersion.String)
		}
		if len(value) == 0 || string(value) == "null" {
			return animedomain.OverrideValue{}, nil, fmt.Errorf("anime config section %q has no stored value", section)
		}
		override := animedomain.OverrideValue{Value: append(json.RawMessage(nil), value...)}
		if section == animedomain.SectionSubgroups {
			subgroupsOverride = override
		} else {
			rulesOverrides[section] = override
		}
	}
	if err := rows.Err(); err != nil {
		return animedomain.OverrideValue{}, nil, fmt.Errorf("iterate anime config sections: %w", err)
	}
	return subgroupsOverride, rulesOverrides, nil
}

// LoadDownloadCompletion resolves the Global-only download_completion setting:
// the saved Global row, otherwise the code default.
func LoadDownloadCompletion(ctx context.Context, q Querier) (animedomain.ResolvedDownloadCompletion, error) {
	rows, err := q.QueryContext(ctx, `
SELECT schema_version, value, version
FROM global_config_sections
WHERE section_name = 'download_completion'`)
	if err != nil {
		return animedomain.ResolvedDownloadCompletion{}, fmt.Errorf("read global download completion: %w", err)
	}
	defer rows.Close()
	globals := map[string]animedomain.GlobalSection{}
	for rows.Next() {
		var schemaVersion string
		var value []byte
		var version int64
		if err := rows.Scan(&schemaVersion, &value, &version); err != nil {
			return animedomain.ResolvedDownloadCompletion{}, fmt.Errorf("scan global download completion: %w", err)
		}
		if schemaVersion != animedomain.SectionSchemaVersions[animedomain.SectionDownloadCompletion] {
			return animedomain.ResolvedDownloadCompletion{}, fmt.Errorf("global download_completion has unsupported schema version %q", schemaVersion)
		}
		globals[animedomain.SectionDownloadCompletion] = animedomain.GlobalSection{SchemaVersion: schemaVersion, Value: append(json.RawMessage(nil), value...), Version: version}
	}
	if err := rows.Err(); err != nil {
		return animedomain.ResolvedDownloadCompletion{}, fmt.Errorf("iterate global download completion: %w", err)
	}
	return animedomain.ResolveDownloadCompletion(globals)
}

func configSectionOwner(section string) (string, bool) {
	if section == animedomain.SectionSubgroups {
		return "subgroups", true
	}
	// download_completion is a Global-only setting: no anime may hold a row.
	if _, ok := animedomain.SectionSchemaVersions[section]; ok && section != animedomain.SectionDownloadCompletion {
		return "rules", true
	}
	return "", false
}
