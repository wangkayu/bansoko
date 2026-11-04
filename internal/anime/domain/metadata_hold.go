package domain

import (
	"encoding/json"
	"fmt"
)

// SectionMetadataHold is the Global-only release-without-metadata switch. It is
// edited only in the settings page, read live by every evaluation and
// admission (there is no per-anime override, no draft field and no frozen
// copy), exactly like metadata_refresh. When on, a subscription whose BGM or
// TMDB binding is missing keeps its missing bindings as a fact but is no
// longer held: releases are admitted and named from the owner's manual values
// only. It is off by default, which is the historical behavior: an unlinked
// subscription waits for its metadata.
const SectionMetadataHold = "metadata_hold"

// MetadataHoldSchemaVersion pins the stored metadata_hold shape.
const MetadataHoldSchemaVersion = "metadata-hold-v1"

// MetadataHoldRules is the complete metadata_hold section value.
type MetadataHoldRules struct {
	// AllowMissingLinkage admits releases of an unlinked subscription.
	AllowMissingLinkage bool `json:"allow_missing_linkage"`
}

// DefaultMetadataHoldRules is the code default: held (no release without
// metadata), the historical behavior.
func DefaultMetadataHoldRules() MetadataHoldRules {
	return MetadataHoldRules{AllowMissingLinkage: false}
}

// ValidateMetadataHoldJSON enforces the typed shape of a stored or submitted
// metadata_hold value.
func ValidateMetadataHoldJSON(raw json.RawMessage) error {
	_, err := DecodeMetadataHold(raw)
	return err
}

// DecodeMetadataHold decodes and validates one stored value.
// allow_missing_linkage is required, so a partial value or an unknown member
// fails closed.
func DecodeMetadataHold(raw json.RawMessage) (MetadataHoldRules, error) {
	var probe struct {
		AllowMissingLinkage *bool `json:"allow_missing_linkage"`
	}
	if err := decodeStrictJSON(raw, &probe); err != nil || probe.AllowMissingLinkage == nil {
		return MetadataHoldRules{}, fmt.Errorf("metadata_hold value must carry a boolean allow_missing_linkage")
	}
	return MetadataHoldRules{AllowMissingLinkage: *probe.AllowMissingLinkage}, nil
}
