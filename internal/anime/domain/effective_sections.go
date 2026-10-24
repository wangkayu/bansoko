package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// EffectiveSectionOrder is the canonical section order for resolving and
// digesting effective configuration. It is the single ordered list every
// resolved-configuration producer must follow so a revision digest does not
// depend on map iteration order.
var EffectiveSectionOrder = []string{
	SectionSubgroups,
	SectionParser,
	SectionReleaseRules,
	SectionFileRules,
	SectionNamingRules,
	SectionFileEffect,
}

// ResolvedSections is the complete resolved effective configuration as typed
// whole-section values plus their provenance. It is the storage- and
// digest-ready form of what ResolveEffective decodes into EffectiveConfig.
type ResolvedSections struct {
	// Sections maps section_name to the complete resolved typed value.
	Sections map[string]json.RawMessage
	// Versions maps section_name to the resolved source version: 0 for a value
	// the draft or instance stores itself; a frozen business snapshot keeps the
	// Global version it copied the value from.
	Versions map[string]int64
	// SchemaVersions maps section_name to the typed schema version that pins
	// the stored value shape.
	SchemaVersions map[string]string
}

// ConfigRevisionIdentity is the immutable configuration identity frozen into
// admitted work. Candidate revisions use candidate_id + revision; materialized
// instances use managed_anime_id. The CAS version is intentionally absent.
type ConfigRevisionIdentity struct {
	CandidateID     *string `json:"candidate_id,omitempty"`
	EffectiveDigest string  `json:"effective_digest"`
	IdentityKind    string  `json:"identity_kind"`
	ManagedAnimeID  *string `json:"managed_anime_id,omitempty"`
	Revision        *int64  `json:"revision,omitempty"`
}

// ResolveDraftEffectiveSections resolves the complete section values a draft
// stores without decoding them into EffectiveConfig: the draft's subgroups
// section and its five rules sections, each a complete value. Global is never
// read; a missing section fails closed.
func ResolveDraftEffectiveSections(draft AnimeDraft) (ResolvedSections, error) {
	return ResolveDraftEffectiveSectionsWithVars(draft, nil)
}

// ResolveDraftEffectiveSectionsWithVars is the save-time form: naming templates
// are validated against the injected Vars snapshot.
func ResolveDraftEffectiveSectionsWithVars(draft AnimeDraft, vars map[string]map[string]string) (ResolvedSections, error) {
	subgroupsOverride, violations := SubgroupsSectionOverride(draft.Subgroups)
	if len(violations) > 0 {
		return ResolvedSections{}, fmt.Errorf("subgroups override: %s", violations[0].Message)
	}
	rulesOverrides, violations := RulesSectionOverridesWithVars(draft.Rules.Sections, vars)
	if len(violations) > 0 {
		violation := violations[0]
		if violation.Code != "OVERRIDE_MODE_INVALID" {
			return ResolvedSections{}, &SectionValidationError{Code: violation.Code, Field: violation.Field, Message: violation.Message}
		}
		return ResolvedSections{}, fmt.Errorf("rules override %s: %s", violation.Field, violation.Message)
	}
	return ResolveEffectiveSections(subgroupsOverride, rulesOverrides)
}

// ResolveEffectiveSections resolves every section in canonical order from the
// stored values and keeps the raw typed value plus its provenance. Callers that
// need to freeze an effective configuration should retain this result.
func ResolveEffectiveSections(subgroupsOverride OverrideValue, rulesOverrides map[string]OverrideValue) (ResolvedSections, error) {
	resolved := ResolvedSections{
		Sections:       make(map[string]json.RawMessage, len(EffectiveSectionOrder)),
		Versions:       make(map[string]int64, len(EffectiveSectionOrder)),
		SchemaVersions: make(map[string]string, len(EffectiveSectionOrder)),
	}
	for _, section := range EffectiveSectionOrder {
		override := rulesOverrides[section]
		if section == SectionSubgroups {
			override = subgroupsOverride
		}
		raw, version, err := storedSectionValue(section, override)
		if err != nil {
			return ResolvedSections{}, err
		}
		// The stored value must still be a valid typed section. This keeps a
		// persisted revision from silently carrying a shape DecodeEffectiveConfig
		// cannot read back.
		if err := ValidateSectionValue(section, raw); err != nil {
			return ResolvedSections{}, fmt.Errorf("resolved section %s is not a valid typed value: %w", section, err)
		}
		resolved.Sections[section] = append(json.RawMessage(nil), raw...)
		resolved.Versions[section] = version
		resolved.SchemaVersions[section] = SectionSchemaVersions[section]
	}
	return resolved, nil
}

// ResolvedSectionsFromStored rebuilds a ResolvedSections value from a persisted
// revision's JSON columns, validating that every canonical section is present.
func ResolvedSectionsFromStored(effective, sectionVersions, sectionSchemas json.RawMessage) (ResolvedSections, error) {
	resolved := ResolvedSections{
		Sections:       map[string]json.RawMessage{},
		Versions:       map[string]int64{},
		SchemaVersions: map[string]string{},
	}
	if err := json.Unmarshal(effective, &resolved.Sections); err != nil {
		return ResolvedSections{}, fmt.Errorf("decode stored effective sections: %w", err)
	}
	if err := json.Unmarshal(sectionVersions, &resolved.Versions); err != nil {
		return ResolvedSections{}, fmt.Errorf("decode stored section versions: %w", err)
	}
	if err := json.Unmarshal(sectionSchemas, &resolved.SchemaVersions); err != nil {
		return ResolvedSections{}, fmt.Errorf("decode stored section schemas: %w", err)
	}
	for _, section := range EffectiveSectionOrder {
		raw, ok := resolved.Sections[section]
		if !ok || len(raw) == 0 {
			return ResolvedSections{}, fmt.Errorf("stored effective sections are missing %q", section)
		}
	}
	return resolved, nil
}

// EffectiveConfigFromSections decodes the typed EffectiveConfig from resolved
// whole-section values. It mirrors ResolveEffective's decode targets exactly so
// a saved revision and a live resolution agree.
func EffectiveConfigFromSections(resolved ResolvedSections) (EffectiveConfig, error) {
	// The frozen six-section envelope carries no tag_filters or
	// download_completion: decode them as the code defaults (filter nothing,
	// keep seeding). Live instance resolution replaces them.
	result := EffectiveConfig{Versions: map[string]int64{}, TagFilters: DefaultResolvedTagFilters(), DownloadCompletion: DefaultResolvedDownloadCompletion()}
	decode := func(section string, target any) error {
		raw, ok := resolved.Sections[section]
		if !ok || len(raw) == 0 {
			return fmt.Errorf("resolved section %s is missing", section)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return fmt.Errorf("decode effective section %s: %w", section, err)
		}
		result.Versions[section] = resolved.Versions[section]
		return nil
	}
	if err := decode(SectionSubgroups, &result.Selection); err != nil {
		return EffectiveConfig{}, err
	}
	if err := decode(SectionParser, &result.Rules.Parser); err != nil {
		return EffectiveConfig{}, err
	}
	if err := decode(SectionReleaseRules, &result.Rules.ReleaseFilter); err != nil {
		return EffectiveConfig{}, err
	}
	if err := decode(SectionFileRules, &result.Rules.FileFilter); err != nil {
		return EffectiveConfig{}, err
	}
	if err := decode(SectionNamingRules, &result.Rules.Naming); err != nil {
		return EffectiveConfig{}, err
	}
	if err := decode(SectionFileEffect, &result.Rules.FileEffect); err != nil {
		return EffectiveConfig{}, err
	}
	return result, nil
}

// EffectiveDigest returns the canonical sha256 over the resolved section values
// and their provenance. Each raw value is re-normalized through encoding/json so
// the digest depends only on logical content, never on storage formatting or map
// iteration order. Numbers are decoded with UseNumber so an integer is never
// distorted by float64 conversion.
func (r ResolvedSections) EffectiveDigest() (string, error) {
	normalized := make(map[string]any, len(r.Sections))
	for section, raw := range r.Sections {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return "", fmt.Errorf("normalize resolved section %s: %w", section, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err == nil {
			return "", fmt.Errorf("resolved section %s contains more than one JSON value", section)
		}
		normalized[section] = value
	}
	payload := struct {
		Effective       map[string]any    `json:"effective"`
		SectionVersions map[string]int64  `json:"section_versions"`
		SectionSchemas  map[string]string `json:"section_schemas"`
	}{
		Effective:       normalized,
		SectionVersions: r.Versions,
		SectionSchemas:  r.SchemaVersions,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode effective digest payload: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
