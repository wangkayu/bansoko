package frozenpolicy

import (
	"encoding/json"

	domain "bansoko/internal/anime/domain"
	"bansoko/internal/qualityprofile"
)

// Business returns a complete submitted snapshot for the current schema.
func Business() (domain.BusinessSnapshot, error) {
	b := domain.BusinessSnapshot{SchemaVersion: domain.BusinessSnapshotSchemaVersion, Sections: map[string]json.RawMessage{}, SectionVersions: map[string]int64{}, SectionSchemas: map[string]string{}, Parser: domain.ParserPolicyEnvelope{SchemaVersion: domain.ParserPolicyEnvelopeSchemaVersion, Kind: "regex", SourceOwner: "global", Provenance: "global", PolicyVersion: 1, ProfileVersion: 1, RulesOwnerVersion: 1}, ReplacementVersion: 1, FileEffectVersion: 1, UnclassifiedRoute: "skip"}
	for _, name := range domain.EffectiveSectionOrder {
		value, err := domain.DefaultSectionValue(name)
		if err != nil {
			return b, err
		}
		b.Sections[name], err = json.Marshal(value)
		if err != nil {
			return b, err
		}
		b.SectionVersions[name] = 1
		b.SectionSchemas[name] = domain.SectionSchemaVersions[name]
	}
	b.Replacement = domain.ReplacementValue{AdmissionMode: "all", QualityProfile: qualityprofile.DefaultJSON()}
	return b.Freeze()
}
