package application

import (
	"encoding/json"

	animedomain "bansoko/internal/anime/domain"
	downloadadmission "bansoko/internal/download/admission"
	"bansoko/internal/namingvars"
	"bansoko/internal/parser"
)

// This file holds the frozen execution shapes shared by the download owners.
// Ownership freezes them into a DownloadTask, oneshot carries them in its
// marker/recovery records, and the read models project them, so they live in
// the kernel instead of any one use-case bucket.

type SelectedReleaseIdentity struct {
	ReleaseID      string          `json:"release_id"`
	RSSSourceID    string          `json:"rss_source_id"`
	StableItemKey  string          `json:"stable_item_key"`
	RawFacts       json.RawMessage `json:"raw_facts"`
	SourceFacts    json.RawMessage `json:"source_facts"`
	InternalFacts  json.RawMessage `json:"internal_facts"`
	ReleaseVersion int64           `json:"release_version"`
	SubscriptionID string          `json:"subscription_id"`
	ManagedAnimeID string          `json:"managed_anime_id"`
}

type FrozenDownloadInput struct {
	InfoHashV1           *string                       `json:"info_hash_v1,omitempty"`
	InfoHashV2           *string                       `json:"info_hash_v2,omitempty"`
	AdoptionProvenance   *downloadadmission.Provenance `json:"adoption_provenance,omitempty"`
	ReleaseFiles         json.RawMessage               `json:"release_files,omitempty"`
	ReleaseStableItemKey string                        `json:"release_stable_item_key"`
	SelectedRelease      *SelectedReleaseIdentity      `json:"selected_release,omitempty"`
	SelectedSeed         json.RawMessage               `json:"selected_seed,omitempty"`
	SelectedURLDigest    *string                       `json:"selected_url_digest,omitempty"`
	SourceLocatorDisplay string                        `json:"source_locator_display"`
	TargetFacts          json.RawMessage               `json:"target_facts,omitempty"`
	TorrentFilename      *string                       `json:"torrent_filename,omitempty"`
	TorrentInputID       *string                       `json:"torrent_input_id,omitempty"`
	TorrentParserVersion *string                       `json:"torrent_parser_version,omitempty"`
	TorrentPayloadDigest *string                       `json:"torrent_payload_digest,omitempty"`
	TorrentVersion       *string                       `json:"torrent_version,omitempty"`
}

type ConfigRevisionIdentity = animedomain.ConfigRevisionIdentity

type FrozenPolicySnapshot struct {
	ConfigIdentity    ConfigRevisionIdentity       `json:"config_identity"`
	EffectiveDigest   string                       `json:"effective_digest"`
	EffectiveSections json.RawMessage              `json:"effective_sections"`
	EvaluatorVersion  *string                      `json:"evaluator_version,omitempty"`
	MetadataVersion   int64                        `json:"metadata_version"`
	Parser            *parser.Provenance           `json:"parser"`
	RulesVersion      int64                        `json:"rules_version"`
	Vars              *namingvars.FrozenEvaluation `json:"vars,omitempty"`
	SectionSchemas    map[string]string            `json:"section_schemas"`
	SectionVersions   map[string]int64             `json:"section_versions"`
}
