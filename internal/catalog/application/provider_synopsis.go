package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"bansoko/internal/catalog/domain"
)

// ErrSynopsisEntryInvalid reports a provider or entry reference outside the
// catalog-entry contract. The transport maps it to a typed 422.
var ErrSynopsisEntryInvalid = errors.New("catalog entry synopsis reference is invalid")

// ErrSynopsisUnsupported reports a configured provider that has no anime page
// synopsis surface. Both current discovery providers implement it; this only
// guards a future reader that does not.
var ErrSynopsisUnsupported = errors.New("provider does not support entry synopsis")

const (
	// synopsisCacheTTL is deliberately short: a page synopsis is a display fact,
	// and a corrected provider page should appear without a restart.
	synopsisCacheTTL = 10 * time.Minute
	// synopsisCacheLimit bounds the per-process entry cache; the browse wall can
	// show hundreds of cards, but only opened dialogs are read.
	synopsisCacheLimit = 256
)

type synopsisCacheEntry struct {
	value   domain.EntrySynopsis
	expires time.Time
}

// ProviderEntrySynopsis returns one catalog entry's whole synopsis and the
// facts its provider page states. It is side-effect free and reads the page
// only on demand (the reader dialog's open), never during list loads. Answers
// are cached briefly per (provider, entry) and concurrent reads of one entry
// collapse into one page request.
func (s *Service) ProviderEntrySynopsis(ctx context.Context, provider, providerEntryRef string) (domain.EntrySynopsis, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	providerEntryRef = strings.TrimSpace(providerEntryRef)
	if s == nil {
		return domain.EntrySynopsis{}, errors.New("catalog service is nil")
	}
	if err := validateCatalogProvider(provider); err != nil {
		return domain.EntrySynopsis{}, fmt.Errorf("%w: %s", ErrSynopsisEntryInvalid, err)
	}
	if err := validateProviderCatalogEntryRef(provider, providerEntryRef); err != nil {
		return domain.EntrySynopsis{}, fmt.Errorf("%w: %s", ErrSynopsisEntryInvalid, err)
	}
	key := provider + "\x00" + providerEntryRef
	now := s.synopsisNow()
	if cached, ok := s.cachedSynopsis(key, now); ok {
		return cached, nil
	}
	reader, err := s.providerReader(provider)
	if err != nil {
		return domain.EntrySynopsis{}, err
	}
	synopsisReader, ok := reader.(SynopsisReader)
	if !ok {
		return domain.EntrySynopsis{}, fmt.Errorf("%w: %s", ErrSynopsisUnsupported, provider)
	}
	value, err, _ := s.synopsisFlights.Do(key, func() (any, error) {
		// Another caller may have filled the cache while this one waited for
		// the flight.
		if cached, ok := s.cachedSynopsis(key, s.synopsisNow()); ok {
			return cached, nil
		}
		result, readErr := synopsisReader.ReadSynopsis(ctx, providerEntryRef)
		if readErr != nil {
			return domain.EntrySynopsis{}, readErr
		}
		s.storeSynopsis(key, result, s.synopsisNow())
		return result, nil
	})
	if err != nil {
		return domain.EntrySynopsis{}, err
	}
	result, ok := value.(domain.EntrySynopsis)
	if !ok {
		return domain.EntrySynopsis{}, errors.New("catalog synopsis flight returned an unexpected value")
	}
	return result, nil
}

func (s *Service) synopsisNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

func (s *Service) cachedSynopsis(key string, now time.Time) (domain.EntrySynopsis, bool) {
	s.synopsisMu.Lock()
	defer s.synopsisMu.Unlock()
	entry, ok := s.synopsisCache[key]
	if !ok || !now.Before(entry.expires) {
		return domain.EntrySynopsis{}, false
	}
	return entry.value, true
}

func (s *Service) storeSynopsis(key string, value domain.EntrySynopsis, now time.Time) {
	s.synopsisMu.Lock()
	defer s.synopsisMu.Unlock()
	if s.synopsisCache == nil {
		s.synopsisCache = make(map[string]synopsisCacheEntry)
	}
	if len(s.synopsisCache) >= synopsisCacheLimit {
		for candidate, entry := range s.synopsisCache {
			if !now.Before(entry.expires) {
				delete(s.synopsisCache, candidate)
			}
		}
	}
	if len(s.synopsisCache) >= synopsisCacheLimit {
		var oldestKey string
		var oldestExpiry time.Time
		for candidate, entry := range s.synopsisCache {
			if oldestKey == "" || entry.expires.Before(oldestExpiry) {
				oldestKey, oldestExpiry = candidate, entry.expires
			}
		}
		delete(s.synopsisCache, oldestKey)
	}
	s.synopsisCache[key] = synopsisCacheEntry{value: value, expires: now.Add(synopsisCacheTTL)}
}
