package application

import (
	animedomain "bansoko/internal/anime/domain"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

// SubscribeAnimeInput is the transport-neutral input for direct subscription.
// A new subscription is pending (not started) unless Start asks to start it
// at once.
type SubscribeAnimeInput struct {
	Draft       animedomain.AnimeDraft
	RequestHash string
	Start       bool
}

// SubscribeCommand is the transport-neutral input to the shared Subscribe transaction.
type SubscribeCommand struct {
	Draft           animedomain.AnimeDraft
	IdempotencyKey  string
	RequestHash     string
	CandidateID     *string
	ExpectedVersion *int64
	Revision        *int64
	InitialState    subscriptiondomain.SubscriptionState
}

// CandidateSubscribeInput carries the exact immutable saved revision and the
// optional initial-state choice for activating a candidate. A nil state means
// pending (not started), or active when Start is set; revision zero is never
// inferred from the mutable candidate pointer.
type CandidateSubscribeInput struct {
	Revision     int64
	InitialState *subscriptiondomain.SubscriptionState
	Start        bool
}

// SubscribeResult is the application-owned result of creating the complete
// anime owner set from a saved candidate. HTTP projection belongs to transport.
type SubscribeResult struct {
	EffectiveConfigVersions EffectiveConfigVersions `json:"effective_config_versions"`
	ManagedAnime            ManagedAnime            `json:"managed_anime"`
	Metadata                AnimeMetadata           `json:"metadata"`
	RssSources              []RSSSource             `json:"rss_sources"`
	Subscription            Subscription            `json:"subscription"`
}

type EffectiveConfigVersions struct {
	FileEffect    int64 `json:"file_effect"`
	FileFilter    int64 `json:"file_filter"`
	Naming        int64 `json:"naming"`
	Parser        int64 `json:"parser"`
	ReleaseFilter int64 `json:"release_filter"`
	Subgroups     int64 `json:"subgroups"`
}
