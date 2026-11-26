package domain

import "encoding/json"

// FrozenInput is the persisted download_tasks.frozen_input contract shared by
// the admission writers and the downloader. Entry-specific evidence remains
// opaque to download, while download-owned fields are typed.
type FrozenInput struct {
	ReleaseStableItemKey string          `json:"release_stable_item_key"`
	SelectedRelease      json.RawMessage `json:"selected_release,omitempty"`
	SelectedSeed         json.RawMessage `json:"selected_seed,omitempty"`
	TargetFacts          json.RawMessage `json:"target_facts,omitempty"`
	TargetSetDigest      string          `json:"target_set_digest,omitempty"`
	SourceLocatorDisplay string          `json:"source_locator_display"`
	TorrentInputID       string          `json:"torrent_input_id"`
	TorrentPayloadDigest string          `json:"torrent_payload_digest"`
	TorrentFilename      string          `json:"torrent_filename"`
	TorrentParserVersion string          `json:"torrent_parser_version"`
	TorrentVersion       string          `json:"torrent_version"`
	InfoHash             string          `json:"info_hash,omitempty"`
	InfoHashV1           string          `json:"info_hash_v1,omitempty"`
	InfoHashV2           string          `json:"info_hash_v2,omitempty"`
	// Path freezing compares the rest of the input as JSONB. Preserve an
	// explicit empty array (or null) separately from an omitted inventory.
	ReleaseFiles       json.RawMessage     `json:"release_files,omitempty"`
	PathSnapshot       *PathSnapshot       `json:"path_snapshot,omitempty"`
	PartialDownload    *PartialDownload    `json:"partial_download,omitempty"`
	PartialNamespace   string              `json:"partial_namespace,omitempty"`
	PartialStartSent   bool                `json:"partial_start_sent,omitempty"`
	AdoptionProvenance *AdoptionProvenance `json:"adoption_provenance,omitempty"`
}

// AdoptionProvenance records the provider match admitted for this task.
type AdoptionProvenance struct {
	Kind                string `json:"kind"`
	ProviderTaskID      string `json:"provider_task_id"`
	ProviderInfoHash    string `json:"provider_info_hash"`
	OperationKeyChecked string `json:"operation_key_checked"`
}
