package application

// MetadataMatchInput is the application input for the provider-backed metadata
// matching action. The transport layer owns the HTTP representation.
type MetadataMatchInput struct {
	ExpectedVersion int64 `json:"expected_version"`
}

// MetadataMatchOutcome is the stable result classification for one metadata
// matching attempt.
type MetadataMatchOutcome string

const (
	MetadataMatchOutcomeComplete      MetadataMatchOutcome = "complete"
	MetadataMatchOutcomeConfigError   MetadataMatchOutcome = "config_error"
	MetadataMatchOutcomeNoResult      MetadataMatchOutcome = "no_result"
	MetadataMatchOutcomePartial       MetadataMatchOutcome = "partial"
	MetadataMatchOutcomeProviderError MetadataMatchOutcome = "provider_error"
)

// MetadataMatchBGM is a matched BGM binding.
type MetadataMatchBGM struct {
	WorkID string `json:"work_id"`
}

// MetadataMatchTMDB is a matched TMDB binding.
type MetadataMatchTMDB struct {
	MediaKind string `json:"media_kind"`
	Season    int    `json:"season"`
	WorkID    string `json:"work_id"`
}

// MetadataMatch is the subset of bindings resolved by the matching attempt.
type MetadataMatch struct {
	BGM  *MetadataMatchBGM  `json:"bgm"`
	TMDB *MetadataMatchTMDB `json:"tmdb"`
}

// MetadataMatchMetadata is the metadata owner view returned with a match
// result. It deliberately retains the complete HTTP response shape while
// remaining independent of generated transport types.
type MetadataMatchMetadata struct {
	BGM               *MetadataMatchBGM  `json:"bgm"`
	ManagedAnimeID    string             `json:"managed_anime_id"`
	TitleOverride     *string            `json:"title_override"`
	TMDB              *MetadataMatchTMDB `json:"tmdb"`
	TotalEpisodes     *int               `json:"total_episodes"`
	MediaKindOverride *string            `json:"media_kind_override"`
	Year              *int               `json:"year"`
	Season            *int               `json:"season"`
	Version           int64              `json:"version"`
}

// MetadataMatchResult is the application result for a metadata matching
// action. Its JSON tags intentionally match the persisted idempotency snapshot
// and the public HTTP contract.
type MetadataMatchResult struct {
	ManagedAnimeID string                `json:"managed_anime_id"`
	Match          *MetadataMatch        `json:"match,omitempty"`
	Metadata       MetadataMatchMetadata `json:"metadata"`
	Outcome        MetadataMatchOutcome  `json:"outcome"`
	Reason         *string               `json:"reason,omitempty"`
}
