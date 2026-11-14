package adapter

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
)

// PosterSourceURL returns Bangumi's upstream poster URL without downloading the
// image bytes. It uses the bound subject ID and the existing configured BGM
// connection; Telegram can fetch that URL directly, with multipart as fallback.
func (r *ConfiguredPosterResolver) PosterSourceURL(ctx context.Context, bgmID string) (string, error) {
	bgmID = strings.TrimSpace(bgmID)
	if !bgmIDPattern.MatchString(bgmID) {
		return "", ErrPosterNotFound
	}
	if cached, ok := r.cachedURL(bgmID); ok && validPosterSourceURL(cached) {
		return cached, nil
	}
	if r.PersistentCache != nil {
		if entry, found, err := r.PersistentCache.Get(ctx, bgmID); err == nil && found && validPosterSourceURL(entry.ImageURL) {
			r.storePosterURL(bgmID, entry.ImageURL, entry.ExpiresAt)
			return entry.ImageURL, nil
		}
	}
	connection, client, err := r.bgmClient(ctx)
	if err != nil {
		return "", err
	}
	urls, err := r.subjectImageURLs(ctx, client, connection.Endpoint, bgmID)
	if err != nil {
		return "", err
	}
	for _, candidate := range urls {
		if validPosterSourceURL(candidate) {
			r.storePosterURL(bgmID, candidate, time.Time{})
			return candidate, nil
		}
	}
	return "", errors.New("Bangumi subject has no usable poster URL")
}

// storePosterURL caches only the upstream lookup result. It deliberately does
// not invent poster bytes or a persistent-cache entry for an image not fetched.
func (r *ConfiguredPosterResolver) storePosterURL(bgmID, imageURL string, expiresAt time.Time) {
	if expiresAt.IsZero() {
		expiresAt = time.Now().UTC().Add(r.posterCacheTTL())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = map[string]posterCacheEntry{}
	}
	entry := r.cache[bgmID]
	entry.url = imageURL
	entry.urlExpiresAt = expiresAt
	r.cache[bgmID] = entry
}

func validPosterSourceURL(raw string) bool {
	if raw == "" || len(raw) > 2048 {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.User == nil && parsed.Hostname() != "" && (parsed.Scheme == "https" || parsed.Scheme == "http")
}
