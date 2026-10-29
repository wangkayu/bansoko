package application

import (
	"time"

	animedomain "bansoko/internal/anime/domain"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

// This file holds the owner-shape types that more than one use case reads.
// They stay in the kernel package because draft results, ownership mutations
// and read-model projections all carry them; keeping them here is what lets
// the four use-case buckets move without referencing each other.

// ManagedAnime is the identity/version projection of one anime instance.
type ManagedAnime struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	TitleOverride *string `json:"title_override"`
	Version       int64   `json:"version"`
}

// Subscription is the state/version projection of one anime subscription.
type Subscription struct {
	ID             string                               `json:"id"`
	ManagedAnimeID string                               `json:"managed_anime_id"`
	Origin         string                               `json:"origin"`
	OriginEntryRef *string                              `json:"origin_entry_ref"`
	InstanceLabel  *string                              `json:"instance_label"`
	State          subscriptiondomain.SubscriptionState `json:"state"`
	LifecycleState string                               `json:"lifecycle_state"`
	LifecycleEpoch int64                                `json:"lifecycle_epoch"`
	RetiredAt      *time.Time                           `json:"retired_at"`
	Version        int64                                `json:"version"`
	// NewReleasesOnly and StartBaselineAt are the frozen new-releases-only option:
	// StartBaselineAt is the instance creation time, set exactly when the
	// option is on.
	NewReleasesOnly bool       `json:"new_releases_only"`
	StartBaselineAt *time.Time `json:"start_baseline_at"`
}

type AnimeMetadata struct {
	ManagedAnimeID string       `json:"managed_anime_id"`
	BGM            *BGMBinding  `json:"bgm"`
	TMDB           *TMDBBinding `json:"tmdb"`
	TitleOverride  *string      `json:"title_override"`
	TotalEpisodes  *int         `json:"total_episodes"`
	// MediaKind pins the naming branch selector (tv | movie); nil means
	// unset. The TMDB tuple above stays exactly the binding the matcher wrote.
	MediaKind *string `json:"media_kind"`
	// Year is the owner-edited naming year (anime.year); nil means unset.
	Year *int `json:"year"`
	// Season is the owner-edited naming season (anime.season; 0 = specials);
	// nil means unset. It is independent of TMDB.Season.
	Season  *int  `json:"season"`
	Version int64 `json:"version"`
	// TitleSuggestions are the workspace read's title candidates; other
	// responses leave it empty.
	TitleSuggestions []TitleSuggestion `json:"title_suggestions,omitempty"`
	// ReevaluatedReleases is set by the metadata patch only: how many
	// unsubmitted releases it scheduled for re-evaluation.
	ReevaluatedReleases *int `json:"-"`
}

// Title suggestion sources, in the order a view lists them.
const (
	TitleSourceTMDBCN       = "tmdb_cn"
	TitleSourceTMDBOriginal = "tmdb_original"
	TitleSourceBGMCN        = "bgm_cn"
	TitleSourceBGM          = "bgm"
	TitleSourceProvider     = "provider"
)

// TitleSuggestion is one candidate for the owner-fixed title: a name the bound
// works' cached titles or the provider catalog carry. It is read-only.
type TitleSuggestion struct {
	Source string `json:"source"`
	Text   string `json:"text"`
}

type BGMBinding struct {
	WorkID string `json:"work_id"`
}

type TMDBBinding struct {
	WorkID    string `json:"work_id"`
	MediaKind string `json:"media_kind"`
	Season    int    `json:"season"`
}

// RSSSource is the owner projection of one configured RSS source.
type RSSSource struct {
	AnibtAuxMode          *animedomain.RSSSourceDraftAnibtAuxMode `json:"anibt_aux_mode"`
	GroupName             string                                  `json:"group_name"`
	HttpFeedConnectionRef *string                                 `json:"http_feed_connection_ref"`
	ID                    string                                  `json:"id"`
	LocatorDisplay        string                                  `json:"locator_display"`
	NetworkRouteRef       *string                                 `json:"network_route_ref"`
	EpisodeOffset         int                                     `json:"episode_offset"`
	ProviderProvenance    *animedomain.ProviderProvenance         `json:"provider_provenance"`
	SourceKind            animedomain.SourceKind                  `json:"source_kind"`
	SubscriptionID        string                                  `json:"subscription_id"`
	Title                 string                                  `json:"title"`
	Version               int64                                   `json:"version"`
}

// MetadataHold is the persisted metadata-binding hold one anime currently has.
// The ownership precheck and the list projection both report it.
type MetadataHold struct {
	Held    bool
	Missing []string
}

// RSSSourceRuntime is the owner runtime projection of one RSS source: the
// ownership readers produce it and the workspace read model renders it.
type RSSSourceRuntime struct {
	Cursor           *RSSCursor
	FailedWorkItemID *string
	Health           string
	LastGoodFacts    *RSSLastGoodFacts
	LastRefreshAt    *time.Time
	LastSuccessAt    *time.Time
	ReasonCode       *string
	ReasonDetail     *string
	RSSSourceID      string
	Version          int64
}

type RSSCursor struct {
	Strategy string
	Value    string
}

type RSSLastGoodFacts struct {
	FeedTitle           *string
	ItemCount           int
	NewestStableItemKey string
}

// NullableBindingsForDomain converts the owner binding values into the domain
// nullable bindings the metadata patch and the read-model hold projection both
// validate against. It stays in the kernel so the ownership writer and the read
// model share one conversion instead of the read model importing the ownership
// input shape.
func NullableBindingsForDomain(bgm *BGMBinding, tmdb *TMDBBinding) (*animedomain.NullableBGMBinding, *animedomain.NullableTMDBBinding) {
	var domainBGM *animedomain.NullableBGMBinding
	if bgm != nil {
		domainBGM = &animedomain.NullableBGMBinding{WorkId: bgm.WorkID}
	}
	var domainTMDB *animedomain.NullableTMDBBinding
	if tmdb != nil {
		domainTMDB = &animedomain.NullableTMDBBinding{
			WorkId:    tmdb.WorkID,
			MediaKind: animedomain.TMDBMediaKind(tmdb.MediaKind),
			Season:    tmdb.Season,
		}
	}
	return domainBGM, domainTMDB
}

// RulesPreviewInput is the unsaved owner rules patch the owner_draft read mode
// renders. It stays in the kernel because its only producer is the transport
// boundary and its only consumer is the read model, and the read model may not
// import the owner that will eventually supply the saved form of the same rules.
type RulesPreviewInput struct {
	Sections animedomain.RulesPreviewSections
}
