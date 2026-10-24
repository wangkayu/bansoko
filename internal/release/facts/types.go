// Package facts owns the release-derived candidate and media fact contracts:
// versioned candidate facts, append-only Release-level snapshots, and
// deterministic per-file EffectiveFileFacts projections. Downstream owners
// reference stable IDs instead of reparsing titles, filenames, or final paths.
// Historical rows never get invented provenance.
package facts

import (
	"bansoko/internal/batchplan"
	"encoding/json"
	"strings"
	"time"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/parser"
)

// SchemaVersion identifies this facts schema contract. The frozen
// EpisodeIdentity carries no alternatives or conflicts and a fractional
// episode is never resolved.
const SchemaVersion = "media-facts-v1"

// MappingVersion identifies the normalized vocabulary mapping contract.
const MappingVersion = "facts-map-v1"

// Typed field states: value | missing | unknown | invalid | conflict. Missing
// and unknown are distinct: missing means no evidence existed, unknown means
// evidence existed but could not be normalized.
const (
	StateValue    = "value"
	StateMissing  = "missing"
	StateUnknown  = "unknown"
	StateInvalid  = "invalid"
	StateConflict = "conflict"
)

// ReleaseFacts is the normalized release-level evidence from the selected
// parser and provider context. Quality fields are display/scoring evidence;
// only the frozen EpisodeIdentity owns pool identity. Evidence carries the
// raw pre-offset episode evidence so single-video files may fall back to the
// Release identity without re-parsing the title.
type ReleaseFacts struct {
	Title           string                           `json:"title"`
	GroupName       string                           `json:"group_name,omitempty"`
	Resolution      string                           `json:"resolution,omitempty"`
	Quality         string                           `json:"quality,omitempty"`
	Languages       []string                         `json:"languages,omitempty"`
	Special         bool                             `json:"special,omitempty"`
	Warnings        []string                         `json:"warnings,omitempty"`
	EpisodeEvidence *episodeidentity.EpisodeEvidence `json:"episode_evidence,omitempty"`
	// MediaKind/Season/SeasonBound and the metadata work IDs are frozen
	// together at release evaluation. Downstream naming uses this evidence and
	// never re-reads current anime_metadata after submission.
	MediaKind   string `json:"media_kind,omitempty"`
	Season      int    `json:"season,omitempty"`
	SeasonBound bool   `json:"season_bound,omitempty"`
	BGMWorkID   string `json:"bgm_work_id,omitempty"`
	TMDBWorkID  string `json:"tmdb_work_id,omitempty"`
	// ParserProvenance is the frozen selected-parser evidence (kind, config
	// fingerprint, descriptor/normalizer/resolver versions, effective offset,
	// policy/profile versions, parse digests) that produced this evidence. It
	// lets Artifact and the file-effect chain rebuild/explain the selection
	// without reading current instance parser policy.
	ParserProvenance *parser.Provenance `json:"parser_provenance,omitempty"`
	// FileKindsVersion is the product file kind list version (internal/
	// filekinds) the release was evaluated with. Which files are videos or
	// subtitles is decided by that frozen list, so retry, recovery and preview
	// do not change when the product list does.
	FileKindsVersion string `json:"file_kinds_version,omitempty"`
	// BatchPlan is the per-file plan conclusion this Release was accepted
	// with: every manifest file's outcome, the placeable coverage keys and the
	// frozen unknown-file route. Prepare reads it instead of the live instance
	// switch, and the post-download file rows must agree with it.
	BatchPlan *batchplan.Plan `json:"batch_plan,omitempty"`
}

// FileEvidence is the normalized per-file parser evidence. Sibling files
// never supply facts to each other.
type FileEvidence struct {
	Name       string   `json:"name"`
	MediaKind  string   `json:"media_kind"`
	SizeBytes  int64    `json:"size_bytes"`
	GroupName  string   `json:"group_name,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
	Languages  []string `json:"languages,omitempty"`
	Revision   string   `json:"revision,omitempty"`
	Special    bool     `json:"special,omitempty"`
}

// QualityFacts is the normalized typed quality snapshot for one video. Every
// field carries its own typed state so missing, unknown, invalid, and
// conflict stay distinguishable; replacement scores only declared dimensions.
type QualityFacts struct {
	Group      string   `json:"group,omitempty"`
	Languages  []string `json:"languages,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
	Source     string   `json:"source,omitempty"`
	Revision   string   `json:"revision,omitempty"`
	SizeBytes  int64    `json:"size_bytes"`
	// States carries one typed state per field name ("group", "languages",
	// "resolution", "source", "revision", "size"). Absent field = value state.
	States map[string]string `json:"states,omitempty"`
}

// EffectiveFileFacts is the deterministic projection for one video file:
// its frozen EpisodeIdentity plus normalized QualityFacts.
type EffectiveFileFacts struct {
	Identity       episodeidentity.EpisodeIdentity `json:"identity"`
	Quality        QualityFacts                    `json:"quality"`
	MappingVersion string                          `json:"mapping_version"`
}

// Snapshot is one append-only Release-level facts record.
type Snapshot struct {
	ID                      string
	ReleaseID               string
	SchemaVersion           string
	ResolverVersion         string
	ParserKind              string
	ParserConfigFingerprint string
	// ParserConfig is the exact parser configuration the frozen evidence was
	// produced with. Downstream per-file recognition rebuilds the selection
	// from it instead of reading current instance policy. NULL for legacy rows
	// and for a profile that carried no config.
	ParserConfig    json.RawMessage
	InputDigest     string
	ReleaseIdentity episodeidentity.EpisodeIdentity
	ReleaseFacts    ReleaseFacts
	CreatedAt       time.Time
}

// FileRow is one per-file facts row inside a snapshot.
type FileRow struct {
	SnapshotID        string
	FileRef           string
	MediaKind         string
	SizeBytes         int64
	SourceFingerprint string
	FileEvidence      FileEvidence
	EffectiveFacts    EffectiveFileFacts
	CreatedAt         time.Time
}

// MarshalIdentity encodes a frozen identity for JSONB storage.
func MarshalIdentity(identity episodeidentity.EpisodeIdentity) (json.RawMessage, error) {
	return json.Marshal(identity)
}

// UnmarshalIdentity decodes a frozen identity from JSONB storage.
func UnmarshalIdentity(raw json.RawMessage) (episodeidentity.EpisodeIdentity, error) {
	var identity episodeidentity.EpisodeIdentity
	if len(raw) == 0 {
		return identity, nil
	}
	err := json.Unmarshal(raw, &identity)
	return identity, err
}

// DecodeFrozenMediaBinding reads the frozen metadata media kind/season binding
// from a snapshot's release_facts JSONB. A legacy row written before the
// binding was frozen (or a snapshot for an unbound instance) decodes to the
// historical TV default ("tv", season 0, not season-bound); consumers must not
// re-read current anime_metadata as a substitute for missing frozen
// evidence.
func DecodeFrozenMediaBinding(raw json.RawMessage) (mediaKind string, season int, seasonBound bool) {
	mediaKind = "tv"
	if len(raw) == 0 || string(raw) == "null" {
		return mediaKind, 0, false
	}
	var frozen struct {
		MediaKind   string `json:"media_kind"`
		Season      int    `json:"season"`
		SeasonBound bool   `json:"season_bound"`
	}
	if err := json.Unmarshal(raw, &frozen); err != nil {
		return mediaKind, 0, false
	}
	if kind := strings.TrimSpace(frozen.MediaKind); kind == "tv" || kind == "movie" {
		mediaKind = kind
	}
	return mediaKind, frozen.Season, frozen.SeasonBound
}
