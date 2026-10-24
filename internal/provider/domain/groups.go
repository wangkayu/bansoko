package domain

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Kind string

const (
	ProviderAniBT Kind = "anibt"
	ProviderMikan Kind = "mikan"
)

var aniBTGroupSlug = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// GroupRef is the provider-namespaced identity shared by snapshots, feed
// locators, translations and exact policy targets.
type GroupRef struct {
	Provider   Kind   `json:"provider"`
	ExternalID string `json:"external_ref"`
}

type CatalogEntryRef struct {
	Provider   Kind
	ExternalID string
}

func NewGroupRef(provider Kind, externalID string) (GroupRef, error) {
	externalID = strings.TrimSpace(externalID)
	switch provider {
	case ProviderMikan:
		value, err := strconv.ParseUint(externalID, 10, 64)
		if err != nil || value == 0 {
			return GroupRef{}, errors.New("Mikan subgroup ID must be a positive integer")
		}
		externalID = strconv.FormatUint(value, 10)
	case ProviderAniBT:
		if !aniBTGroupSlug.MatchString(externalID) {
			return GroupRef{}, errors.New("AniBT group slug must be 1-128 ASCII letters, numbers, dots, underscores or hyphens")
		}
	default:
		return GroupRef{}, fmt.Errorf("unsupported provider %q", provider)
	}
	return GroupRef{Provider: provider, ExternalID: externalID}, nil
}

func (r GroupRef) Validate() error {
	normalized, err := NewGroupRef(r.Provider, r.ExternalID)
	if err != nil {
		return err
	}
	if normalized != r {
		return errors.New("provider group reference is not normalized")
	}
	return nil
}

func (r GroupRef) Key() string {
	return string(r.Provider) + ":" + r.ExternalID
}

// GroupRefOrZero normalizes a stored (provider, external_ref) pair. It returns
// the zero GroupRef — "no provider identity" — for an unsupported provider or a
// missing/unnormalizable external id, so a read path never fails on a legacy or
// partial row: a source that records no usable group identity simply exposes
// none.
func GroupRefOrZero(provider Kind, externalID string) GroupRef {
	ref, err := NewGroupRef(provider, externalID)
	if err != nil {
		return GroupRef{}
	}
	return ref
}

// BuildProviderRSSURL constructs the public RSS feed URL for a provider entry
// and group. It mirrors the URL format used by the acquisition fetchers
// (HTTPMikanRSSFetcher and HTTPAniBTRSSFetcher) so a provider_group source
// can store a fetchable URL as its locator, identical to custom_rss.
func BuildProviderRSSURL(provider Kind, endpoint, catalogEntryRef, groupExternalID string) (string, error) {
	switch provider {
	case ProviderMikan:
		if _, err := normalizedPositiveID(catalogEntryRef, "Mikan bangumi ID"); err != nil {
			return "", err
		}
		if _, err := normalizedPositiveID(groupExternalID, "Mikan subgroup ID"); err != nil {
			return "", err
		}
		base, err := parseProviderEndpoint(endpoint, "Mikan")
		if err != nil {
			return "", err
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/RSS/Bangumi"
		query := base.Query()
		query.Set("bangumiId", catalogEntryRef)
		query.Set("subgroupid", groupExternalID)
		base.RawQuery = query.Encode()
		return base.String(), nil
	case ProviderAniBT:
		if _, err := strconv.ParseInt(strings.TrimSpace(catalogEntryRef), 10, 64); err != nil || strings.TrimSpace(catalogEntryRef) == "" {
			return "", errors.New("AniBT BGM ID must be a positive integer")
		}
		if !aniBTGroupSlug.MatchString(strings.TrimSpace(groupExternalID)) {
			return "", errors.New("AniBT group slug is invalid")
		}
		base, err := parseProviderEndpoint(endpoint, "AniBT")
		if err != nil {
			return "", err
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/rss/anime.xml"
		query := base.Query()
		query.Set("bgmId", catalogEntryRef)
		query.Set("groupSlug", groupExternalID)
		base.RawQuery = query.Encode()
		return base.String(), nil
	default:
		return "", fmt.Errorf("unsupported provider %q", provider)
	}
}

func normalizedPositiveID(value, label string) (string, error) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed == 0 {
		return "", fmt.Errorf("%s must be a positive integer", label)
	}
	return strconv.FormatUint(parsed, 10), nil
}

func parseProviderEndpoint(endpoint, label string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil || base.Fragment != "" {
		return nil, fmt.Errorf("%s endpoint must be an absolute HTTP URL without credentials", label)
	}
	return base, nil
}

type FeedLocator struct {
	Provider        Kind     `json:"provider"`
	CatalogEntryRef string   `json:"catalog_entry_ref"`
	Group           GroupRef `json:"group_ref"`
}

func (l FeedLocator) Validate() error {
	if l.Provider != l.Group.Provider {
		return errors.New("feed locator provider and group provider must match")
	}
	if err := l.Group.Validate(); err != nil {
		return err
	}
	value, err := strconv.ParseUint(strings.TrimSpace(l.CatalogEntryRef), 10, 64)
	if err != nil || value == 0 || strconv.FormatUint(value, 10) != l.CatalogEntryRef {
		return errors.New("provider catalog entry reference must be a normalized positive integer")
	}
	return nil
}

type GroupSnapshotEntry struct {
	Ref         GroupRef
	DisplayName string
	FeedLocator FeedLocator
	ObservedAt  time.Time
	// Active is optional only for older internal test readers; nil cannot prove
	// authoritative availability. Adapters supply an explicit status.
	Active *bool
}

type GroupSnapshot struct {
	ID              string
	Provider        Kind
	CatalogEntryRef string
	ObservedAt      time.Time
	SourceVersion   string
	SchemaVersion   string
	Version         int64
	Groups          []GroupSnapshotEntry
}

type GroupReadResult struct {
	Provider             Kind
	CatalogEntryRef      string
	ObservedAt           time.Time
	SourceVersion        string
	SchemaVersion        string
	Groups               []GroupSnapshotEntry
	Complete             bool
	CompletenessEvidence string
}

func (s GroupSnapshot) Validate() error {
	if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.SchemaVersion) == "" || s.Version < 1 || s.ObservedAt.IsZero() {
		return errors.New("provider group snapshot identity, schema version, time and version are required")
	}
	seen := make(map[string]struct{}, len(s.Groups))
	for _, group := range s.Groups {
		if group.Ref.Provider != s.Provider {
			return errors.New("snapshot group provider does not match snapshot provider")
		}
		if group.FeedLocator.CatalogEntryRef != s.CatalogEntryRef {
			return errors.New("snapshot group feed locator does not match catalog entry")
		}
		if err := group.FeedLocator.Validate(); err != nil {
			return err
		}
		if _, ok := seen[group.Ref.Key()]; ok {
			return fmt.Errorf("duplicate provider group %q", group.Ref.Key())
		}
		seen[group.Ref.Key()] = struct{}{}
	}
	return nil
}

type Translation struct {
	CanonicalKey string     `json:"canonical_key"`
	DisplayName  string     `json:"display_name"`
	Aliases      []string   `json:"aliases"`
	ProviderRefs []GroupRef `json:"provider_refs"`
}

type TranslationVersion struct {
	SchemaVersion string        `json:"schema_version"`
	DataVersion   string        `json:"data_version"`
	Items         []Translation `json:"items"`
}

type Availability string

const (
	AvailabilityObserved    Availability = "observed"
	AvailabilityNotObserved Availability = "not_observed"
	AvailabilityUnavailable Availability = "unavailable"
)

type LocalGroupCatalogItem struct {
	Ref                  GroupRef
	DisplayName          string
	CanonicalKey         string
	TranslatedName       string
	Aliases              []string
	Availability         Availability
	SnapshotID           string
	SnapshotVersion      int64
	LastObservedAt       *time.Time
	LatestSelectionState string
	PolicyMode           string
	PolicyScore          *int
	PolicyCountInLimit   bool
	PolicySource         string
	PolicyVersion        int64
}

type LocalGroupCatalogView struct {
	TranslationSchemaVersion string
	TranslationDataVersion   string
	Items                    []LocalGroupCatalogItem
	RefreshStates            []GroupRefreshState
	AsOf                     time.Time
}

type GroupRefreshState struct {
	Provider        Kind
	CatalogEntryRef string
	State           string
	LastAttemptAt   time.Time
	LastSuccessAt   *time.Time
	ErrorCode       string
	ErrorMessage    string
	Version         int64
}
