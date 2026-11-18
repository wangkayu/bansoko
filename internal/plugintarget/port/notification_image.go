package port

import "context"

// NotificationImageRequest contains only trusted server/frozen-fact identity.
// In particular it has no title search and no caller-supplied image URL.
type NotificationImageRequest struct {
	ManagedAnimeID string
	// CatalogEntry identifies an exact catalog selection made without a subscription.
	CatalogEntry  NotificationCatalogEntry
	ReleaseSource string
	BGMID         string
	TMDBID        string
	TMDBKind      string
	// PreferredSource is empty for the automatic priority chain, or one of
	// bgm/tmdb/anibt/mikan for an explicit image(...) variable.
	PreferredSource string
}

// NotificationCatalogEntry is a provider-qualified catalog identity, not a URL.
type NotificationCatalogEntry struct {
	Provider string
	EntryRef string
}

// NotificationImage is a validated provider image URL. Source identifies the
// provider selected by the resolver's fixed priority chain.
type NotificationImage struct {
	Source string
	URL    string
}

// NotificationImageResolver selects a notification image without searching by
// title. A missing image is not a delivery failure; the sender uses its own
// built-in placeholder when no candidate can be resolved.
type NotificationImageResolver interface {
	ResolveNotificationImage(context.Context, NotificationImageRequest) (NotificationImage, error)
}
