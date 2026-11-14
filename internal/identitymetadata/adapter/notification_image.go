package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	identitymetadatapersistence "bansoko/internal/identitymetadata/adapter/persistence"
	plugintargetport "bansoko/internal/plugintarget/port"
)

type notificationCover struct {
	provider string
	rawURL   string
}

type notificationCoverStore interface {
	NotificationCover(context.Context, string) (notificationCover, bool, error)
}

type sqlNotificationCoverStore struct{ db *sql.DB }

func (s sqlNotificationCoverStore) NotificationCover(ctx context.Context, managedAnimeID string) (notificationCover, bool, error) {
	var provider string
	var entryRef sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT origin, origin_entry_ref FROM subscriptions WHERE managed_anime_id=$1`, managedAnimeID).Scan(&provider, &entryRef)
	if errors.Is(err, sql.ErrNoRows) {
		return notificationCover{}, false, nil
	}
	if err != nil {
		return notificationCover{}, false, err
	}
	if (provider != CoverProviderMikan && provider != CoverProviderAniBT) || !entryRef.Valid || entryRef.String == "" {
		return notificationCover{}, false, nil
	}
	var rawCover sql.NullString
	err = s.db.QueryRowContext(ctx, `
SELECT NULLIF(source_hint->>'cover', '')
FROM provider_catalog_entries
WHERE provider=$1 AND provider_entry_ref=$2 AND state='present'
ORDER BY observed_at DESC
LIMIT 1`, provider, entryRef.String).Scan(&rawCover)
	if errors.Is(err, sql.ErrNoRows) {
		return notificationCover{}, false, nil
	}
	if err != nil || !rawCover.Valid || strings.TrimSpace(rawCover.String) == "" {
		return notificationCover{}, false, err
	}
	return notificationCover{provider: provider, rawURL: rawCover.String}, true, nil
}

// NotificationImageResolver implements the shared subscription/one-shot image
// policy: provider cover, bound BGM poster, bound TMDB poster. It never guesses
// from a title and never exposes the session-authenticated Bansoko image proxy.
type NotificationImageResolver struct {
	coverStore notificationCoverStore
	covers     *ConfiguredCoverProxy
	posters    *ConfiguredPosterResolver
	tmdb       ConfiguredCandidateSearcher
}

func NewNotificationImageResolver(db *sql.DB, connections ConnectionSource, clientResolver HTTPClientResolver, secrets SecretResolver) *NotificationImageResolver {
	return &NotificationImageResolver{
		coverStore: sqlNotificationCoverStore{db: db},
		covers: NewCoverProxy(
			connections, clientResolver, identitymetadatapersistence.NewProviderImageRepository(db),
		),
		posters: NewPosterResolver(
			connections, clientResolver, identitymetadatapersistence.NewRepository(db),
		),
		tmdb: ConfiguredCandidateSearcher{Connections: connections, Secrets: secrets, ClientResolver: clientResolver},
	}
}

var _ plugintargetport.NotificationImageResolver = (*NotificationImageResolver)(nil)

func (r *NotificationImageResolver) ResolveNotificationImage(ctx context.Context, request plugintargetport.NotificationImageRequest) (plugintargetport.NotificationImage, error) {
	var lastErr error
	if r != nil && r.coverStore != nil && r.covers != nil && request.ManagedAnimeID != "" {
		image, found, err := r.providerCover(ctx, request.ManagedAnimeID)
		if err != nil {
			lastErr = err
		} else if found {
			return image, nil
		}
	}
	if r != nil && r.posters != nil && request.BGMID != "" {
		imageURL, err := r.posters.PosterSourceURL(ctx, request.BGMID)
		if err == nil && imageURL != "" {
			return plugintargetport.NotificationImage{Source: "bgm", URL: imageURL}, nil
		}
		lastErr = err
	}
	if r != nil && request.TMDBID != "" && (request.TMDBKind == "tv" || request.TMDBKind == "movie") {
		imageURL, err := r.tmdb.TMDBPosterURL(ctx, request.TMDBKind, request.TMDBID)
		if err == nil && imageURL != "" {
			return plugintargetport.NotificationImage{Source: "tmdb", URL: imageURL}, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return plugintargetport.NotificationImage{}, fmt.Errorf("resolve notification image: %w", lastErr)
	}
	return plugintargetport.NotificationImage{}, nil
}

func (r *NotificationImageResolver) providerCover(ctx context.Context, managedAnimeID string) (plugintargetport.NotificationImage, bool, error) {
	cover, found, err := r.coverStore.NotificationCover(ctx, managedAnimeID)
	if err != nil || !found {
		return plugintargetport.NotificationImage{}, false, err
	}
	source := notificationCoverRendition(cover.provider, cover.rawURL)
	parsed, canonical := canonicalCoverURL(source)
	if parsed == nil {
		return plugintargetport.NotificationImage{}, false, nil
	}
	policy, err := r.covers.policy(ctx, cover.provider)
	if err != nil {
		return plugintargetport.NotificationImage{}, false, err
	}
	if !policy.allows(parsed) {
		return plugintargetport.NotificationImage{}, false, nil
	}
	return plugintargetport.NotificationImage{Source: cover.provider, URL: canonical}, true, nil
}

// notificationCoverRendition shares the card's uncropped Mikan width but keeps
// the original JPEG/PNG representation: Telegram does not reliably accept the
// card-specific WebP rendition through either URL or multipart delivery.
func notificationCoverRendition(provider, raw string) string {
	if provider != CoverProviderMikan {
		return raw
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" {
		return raw
	}
	query := parsed.Query()
	query.Set("width", "600")
	query.Del("height")
	query.Del("format")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
