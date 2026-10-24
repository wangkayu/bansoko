package application

import (
	anime "bansoko/internal/anime/domain"
	"encoding/json"
	"errors"
)

// OwnerResolution freezes caller-owned resolution. No transport JSON can set it.
type OwnerResolution struct {
	resolved anime.ResolvedSections
	digest   string
}

func NewOwnerResolution(resolved anime.ResolvedSections) (OwnerResolution, error) {
	sections, err := json.Marshal(resolved.Sections)
	if err != nil {
		return OwnerResolution{}, err
	}
	versions, err := json.Marshal(resolved.Versions)
	if err != nil {
		return OwnerResolution{}, err
	}
	schemas, err := json.Marshal(resolved.SchemaVersions)
	if err != nil {
		return OwnerResolution{}, err
	}
	copy, err := anime.ResolvedSectionsFromStored(sections, versions, schemas)
	if err != nil {
		return OwnerResolution{}, err
	}
	digest, err := copy.EffectiveDigest()
	return OwnerResolution{resolved: copy, digest: digest}, err
}
func (r OwnerResolution) validate(policy json.RawMessage) error {
	digest, err := r.resolved.EffectiveDigest()
	if err != nil || digest != r.digest || r.digest == "" {
		return errors.New("caller resolution invalid")
	}
	var stored struct {
		EffectiveDigest   string            `json:"effective_digest"`
		EffectiveSections json.RawMessage   `json:"effective_sections"`
		SectionVersions   map[string]int64  `json:"section_versions"`
		SectionSchemas    map[string]string `json:"section_schemas"`
	}
	if err := json.Unmarshal(policy, &stored); err != nil {
		return err
	}
	versions, _ := json.Marshal(stored.SectionVersions)
	schemas, _ := json.Marshal(stored.SectionSchemas)
	resolved, err := anime.ResolvedSectionsFromStored(stored.EffectiveSections, versions, schemas)
	if err != nil {
		return err
	}
	got, err := resolved.EffectiveDigest()
	if err != nil || got != digest || stored.EffectiveDigest != digest {
		return errors.New("caller frozen policy resolution mismatch")
	}
	return nil
}
func captureSections(m qualificationMaterial) anime.ResolvedSections {
	if m.config.kind == "owner" && m.ownerResolution != nil {
		return m.ownerResolution.resolved
	}
	b := m.config.business
	return anime.ResolvedSections{Sections: b.Sections, Versions: b.SectionVersions, SchemaVersions: b.SectionSchemas}
}
