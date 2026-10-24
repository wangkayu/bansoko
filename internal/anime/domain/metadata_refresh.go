package domain

import (
	"encoding/json"
	"fmt"
)

// SectionMetadataRefresh is the Global-only switch of the background metadata
// refresh sweep. It is edited only in the settings page: there is no per-anime
// override and no draft field. The sweep re-pulls the bound BGM/TMDB works of
// active subscriptions on a fixed cadence so the naming renders' caches follow
// upstream over time. It is off by default because a changed upstream TMDB
// title also changes what templates in use render.
const SectionMetadataRefresh = "metadata_refresh"

// MetadataRefreshSchemaVersion pins the stored metadata_refresh shape.
const MetadataRefreshSchemaVersion = "metadata-refresh-v1"

// MetadataRefreshRules is the complete metadata_refresh section value.
type MetadataRefreshRules struct {
	Enabled bool `json:"enabled"`
}

// DefaultMetadataRefreshRules is the code default: off (freshness is opt-in).
func DefaultMetadataRefreshRules() MetadataRefreshRules {
	return MetadataRefreshRules{Enabled: false}
}

// ValidateMetadataRefreshJSON enforces the typed shape of a stored or
// submitted metadata_refresh value.
func ValidateMetadataRefreshJSON(raw json.RawMessage) error {
	_, err := DecodeMetadataRefresh(raw)
	return err
}

// DecodeMetadataRefresh decodes and validates one stored value. enabled is
// required, so a partial value or an unknown member fails closed.
func DecodeMetadataRefresh(raw json.RawMessage) (MetadataRefreshRules, error) {
	var probe struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeStrictJSON(raw, &probe); err != nil || probe.Enabled == nil {
		return MetadataRefreshRules{}, fmt.Errorf("metadata_refresh value must carry a boolean enabled")
	}
	return MetadataRefreshRules{Enabled: *probe.Enabled}, nil
}
