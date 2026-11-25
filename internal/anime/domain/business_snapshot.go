package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const BusinessSnapshotSchemaVersion = "business-snapshot-v3"

// BusinessSnapshot is the complete configuration copied into a draft or instance.
// Identity, sources, manual group intent and runtime governance are excluded.
type BusinessSnapshot struct {
	SchemaVersion      string                     `json:"schema_version"`
	Sections           map[string]json.RawMessage `json:"sections"`
	SectionVersions    map[string]int64           `json:"section_versions"`
	SectionSchemas     map[string]string          `json:"section_schemas"`
	Parser             ParserPolicyEnvelope       `json:"parser"`
	Replacement        ReplacementValue           `json:"replacement"`
	ReplacementVersion int64                      `json:"replacement_version"`
	UnclassifiedRoute  string                     `json:"unclassified_route"`
	FileEffectVersion  int64                      `json:"file_effect_version"`
}
type ReplacementValue struct {
	AdmissionMode  string          `json:"admission_mode"`
	QualityProfile json.RawMessage `json:"quality_profile"`
}

// Freeze copies every map/JSON level through serialization and verifies the
// immutable value. Saving this result must never resolve Global again.
func (s BusinessSnapshot) Freeze() (BusinessSnapshot, error) {
	if s.SchemaVersion != BusinessSnapshotSchemaVersion {
		return BusinessSnapshot{}, errors.New("unsupported business snapshot schema")
	}
	if len(s.Sections) != len(EffectiveSectionOrder) || len(s.SectionSchemas) != len(EffectiveSectionOrder) || len(s.SectionVersions) != len(EffectiveSectionOrder) {
		return BusinessSnapshot{}, errors.New("business sections must be complete and contain no retired fields")
	}
	if _, err := ResolvedSectionsFromStored(mustJSON(s.Sections), mustJSON(s.SectionVersions), mustJSON(s.SectionSchemas)); err != nil {
		return BusinessSnapshot{}, err
	}
	if err := s.Parser.Validate(); err != nil {
		return BusinessSnapshot{}, err
	}
	if s.Replacement.AdmissionMode != "all" && s.Replacement.AdmissionMode != "per_group" && s.Replacement.AdmissionMode != "single_copy" {
		return BusinessSnapshot{}, errors.New("invalid retention admission mode")
	}
	if raw := s.Replacement.QualityProfile; !json.Valid(raw) || string(raw) == "null" {
		return BusinessSnapshot{}, errors.New("invalid retention document")
	}
	if err := validateBusinessReplacement(s.Replacement); err != nil {
		return BusinessSnapshot{}, err
	}
	if s.UnclassifiedRoute != "skip" && s.UnclassifiedRoute != "route" {
		return BusinessSnapshot{}, errors.New("invalid unclassified route")
	}
	b, err := json.Marshal(s)
	if err != nil {
		return BusinessSnapshot{}, err
	}
	var copy BusinessSnapshot
	err = json.Unmarshal(b, &copy)
	return copy, err
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func (s BusinessSnapshot) Digest() (string, error) {
	copy, err := s.Freeze()
	if err != nil {
		return "", err
	}
	// Re-marshal JSON documents so JSONB whitespace/key ordering is immaterial.
	for k, raw := range copy.Sections {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", err
		}
		copy.Sections[k] = mustJSON(v)
	}
	{
		var v any
		if err := json.Unmarshal(copy.Replacement.QualityProfile, &v); err != nil {
			return "", err
		}
		copy.Replacement.QualityProfile = mustJSON(v)
	}
	b, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	// Canonical object-key order matches JSONB and the typed client digest.
	b, err = businessDigestBytes(b)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
