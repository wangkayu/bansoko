package domain

import (
	"fmt"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

type SnapshotStatus string

const (
	StatusFresh      SnapshotStatus = "fresh"
	StatusStale      SnapshotStatus = "stale"
	StatusWithdrawn  SnapshotStatus = "withdrawn"
	StatusOutage     SnapshotStatus = "outage"
	StatusZeroResult SnapshotStatus = "zero_result"
)

type HealthState string

const (
	HealthUnknown  HealthState = "unknown"
	HealthReady    HealthState = "ready"
	HealthDegraded HealthState = "degraded"
	HealthFailed   HealthState = "failed"
)

type EntryState string

const (
	EntryPresent   EntryState = "present"
	EntryWithdrawn EntryState = "withdrawn"
)

type Entry struct {
	ID               string
	SnapshotID       string
	ProviderEntryRef string
	BGMID            string
	Title            string
	OriginalTitle    string
	SourceHint       map[string]any
	State            EntryState
	ObservedAt       time.Time
}

type ItemKind string

const (
	ItemAniBTEntry   ItemKind = "anibt_entry"
	ItemMikanEntry   ItemKind = "mikan_entry"
	ItemManagedAnime ItemKind = "managed_anime"
)

type ItemPreference string

const (
	PreferenceNeutral     ItemPreference = "neutral"
	PreferenceWatchlisted ItemPreference = "watchlisted"
	PreferenceIgnored     ItemPreference = "ignored"
)

// Provider-stated release availability of a catalog entry. Unknown is the
// empty string and is never exposed.
const (
	ReleaseStateHasReleases = "has_releases"
	ReleaseStateNoReleases  = "no_releases"
)

type SourceHealthSummary struct {
	State         string
	LastSuccessAt *time.Time
}

// InstanceLinkage is one lossless association from an external provider entry
// to an already-existing formal owner. Provider ref is only the external browse
// attribute; ManagedAnimeRef references managed_anime.id, the sole formal owner
// identity. One provider entry may associate with several owners, and a repeated
// ref/locator does not create or identify an owner. SubscriptionVersion is that
// owner's own Subscription CAS version; LifecycleState/LifecycleEpoch are the
// owner's separate managed lifecycle boundary, so a consumer can tell a
// retired-from-history association apart from a live one without inspecting
// Subscription.state.
type InstanceLinkage struct {
	ManagedAnimeRef     string
	SubscriptionRef     string
	SubscriptionState   string
	SubscriptionVersion int64
	LifecycleState      string
	LifecycleEpoch      int64
}

type Item struct {
	Kind             ItemKind
	Origin           string
	Provider         string
	Title            string
	OriginalTitle    string
	AniBTEntryRef    string
	ProviderEntryRef string
	BGMID            string
	PosterURL        string
	// CoverURL is the provider cover external URL (held server-side only;
	// transport rewrites it to the same-origin /images/cover proxy path before
	// responding, so the browser never sees the external URL). The card renders
	// it first, PosterURL (the BGM poster proxy) as fallback. BroadcastDay is
	// the stable broadcast-day key (mon...sun/tbd/none) the mapping layer
	// derives from provider schedule facts; display copy belongs to the
	// frontend.
	CoverURL     string
	BroadcastDay string
	// ReleaseFormat is the provider-stated release format (tv, movie, ova,
	// ona); empty when the provider states none.
	ReleaseFormat     string
	SourceHint        map[string]any
	CatalogState      string
	ObservedAt        *time.Time
	ManagedAnimeRef   string
	SubscriptionRef   string
	SubscriptionState string
	// Instances is the authoritative lossless association list from this one
	// external provider entry to zero or more existing formal owners. The
	// singular ManagedAnimeRef/SubscriptionRef/State above are only a
	// compatibility summary and are populated ONLY when exactly one association
	// exists; with several associations the array is authoritative and the
	// singular fields stay empty rather than silently picking the first. External
	// refs and locators are never owner identity.
	Instances         []InstanceLinkage
	Preference        ItemPreference
	PreferenceVersion int64
	SourceHealth      *SourceHealthSummary
	AllowedActions    []string
	// ReleaseState is the provider's own "has any release" fact for the entry
	// (AniBT hasRelease, Mikan greyout); empty means unknown and stays absent.
	// ReleaseCount is AniBT's rssReleaseCount; UpdateText is Mikan's raw
	// update-date display string. Both are nil/empty when the provider did not
	// state them.
	ReleaseState string
	ReleaseCount *int
	UpdateText   string
	// Score is AniBT's own 0-10 catalog rating; nil when the provider did not
	// state a usable number. It is never filled from metadata providers.
	Score *float64
}

// Suppression hides a provider catalog entry from the workspace. It is not
// season-scoped: an entry stays suppressed across seasons until it is removed.
type Suppression struct {
	ID               string
	Provider         string
	ProviderEntryRef string
	TitleSnapshot    string
	SuppressedAt     time.Time
	Version          int64
}

type ManagedPreference struct {
	ManagedAnimeID string
	Preference     ItemPreference
	Version        int64
	UpdatedAt      time.Time
}

func (p ItemPreference) Validate() error {
	switch p {
	case PreferenceNeutral, PreferenceWatchlisted, PreferenceIgnored:
		return nil
	default:
		return fmt.Errorf("unsupported catalog preference %q", p)
	}
}

type ReadHints struct {
	ETag         string
	LastModified string
	Cursor       string
}

// Stable failure classes for a provider read. They let the persisted snapshot
// tell a network or HTTP failure from a page that no longer matches the parser,
// so an operator does not have to guess from an English error string.
const (
	ReadFailureUnreachable    = "provider_unreachable"
	ReadFailureHTTPStatus     = "provider_http_status"
	ReadFailureSchemaMismatch = "provider_parser_schema_mismatch"
)

// ReadError is a provider read failure carrying one of the ReadFailure classes.
type ReadError struct {
	Code string
	Err  error
}

func (e *ReadError) Error() string { return e.Err.Error() }
func (e *ReadError) Unwrap() error { return e.Err }

type ReadResult struct {
	Entries         []Entry
	SourceVersion   string
	Cursor          string
	ETag            string
	LastModified    string
	ContractVersion string
	NotModified     bool
}

type Snapshot struct {
	ID              string
	Season          string
	Status          SnapshotStatus
	Provider        string
	ObservedAt      time.Time
	SourceVersion   string
	Cursor          string
	ETag            string
	LastModified    string
	ContractVersion string
	HealthState     HealthState
	HealthCode      string
	HealthMessage   string
	HealthCheckedAt time.Time
	ErrorCode       string
	ErrorMessage    string
	Version         int64
	RefreshID       string
	TraceID         string
	// LastSuccessAt is the observation time of the latest successful refresh
	// (fresh, withdrawn or zero_result); nil when none succeeded yet.
	LastSuccessAt *time.Time
	// Stale is the time-based freshness verdict, see CatalogSnapshotStale.
	Stale   bool
	Entries []Entry
}

// CatalogFreshFor is how long a successful catalog observation counts as
// fresh; it is twice the default 12-hour catalog_refresh interval.
const CatalogFreshFor = 24 * time.Hour

// CatalogSnapshotStale reports whether a provider season has no successful
// observation within CatalogFreshFor of now.
func CatalogSnapshotStale(lastSuccessAt *time.Time, now time.Time) bool {
	return lastSuccessAt == nil || now.Sub(*lastSuccessAt) > CatalogFreshFor
}

type Workspace struct {
	Season   string
	Snapshot Snapshot
	Items    []Item
}

type Preference struct {
	Season         string
	RefreshEnabled bool
	Schedule       string
	Version        int64
	UpdatedAt      time.Time
}

type ProviderGroupDiscoveryGroup struct {
	Provider    string
	GroupRef    string
	DisplayName string
	FeedURL     string
	ObservedAt  time.Time
}

type ProviderGroupDiscoveryResult struct {
	Groups           []ProviderGroupDiscoveryGroup
	ObservedAt       time.Time
	Provider         string
	ProviderEntryRef string
	SchemaVersion    string
	SnapshotVersion  int64
	SourceVersion    string
}

type ProviderRSSPreviewSeed struct {
	DownloadURL           string
	ExpectedPayloadDigest *string
	InfoHash              *string
	PublishedAt           time.Time
	SizeBytes             *int64
	StableSeedKey         string
	Title                 string
	// TorrentFilename is the replayable naming input captured with the seed.
	// It is internal to the process; the public contract exposes the
	// display facts, not this rendered-filename input.
	TorrentFilename string
	// LocatorDisplay is the redacted display form of the seed's source
	// location. The raw locator stays server-side so an ordinary read cannot
	// leak credentials.
	LocatorDisplay string
	// TorrentState is one of the TorrentAcquisition* values. A seed whose
	// torrent could not be acquired stays readable with a bounded reason
	// instead of disappearing from its group.
	TorrentState       string
	TorrentErrorCode   *string
	TorrentErrorReason *string
	// Manifest is the derived file projection of this seed's exact persisted
	// payload. It is present exactly when TorrentState is available, and it is
	// the authority for which files the read model renders.
	Manifest *TorrentManifest
	// AniBTTags is the frozen AniBT structured tag set the snapshot recorded.
	// nil means the snapshot carries no tag set (a non-AniBT provider, or a
	// snapshot written before the column existed); a non-nil set may be empty.
	AniBTTags  *acquisitiondomain.StoredAniBTTags
	SourceSite *acquisitiondomain.SeedSourceSite
}

type ProviderRSSPreviewResult struct {
	ContentHash      string
	FetchedAt        time.Time
	Freshness        FeedSnapshotFreshness
	GroupRef         string
	ObservedAt       time.Time
	Provider         string
	ProviderEntryRef string
	SnapshotID       string
	SnapshotVersion  int64
	Seeds            []ProviderRSSPreviewSeed
}
