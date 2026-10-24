package nativeinput

import (
	anime "bansoko/internal/anime/domain"
	"encoding/json"
)

// OwnerPolicy freezes the same resolved columns the production task encoder uses.
func OwnerPolicy(resolved anime.ResolvedSections) (json.RawMessage, error) {
	digest, err := resolved.EffectiveDigest()
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"effective_digest": digest, "effective_sections": resolved.Sections, "section_versions": resolved.Versions, "section_schemas": resolved.SchemaVersions})
}
