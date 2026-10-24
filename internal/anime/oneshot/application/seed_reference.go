package application

import (
	"context"
	"errors"
	"strings"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	animeport "bansoko/internal/anime/port"
	"bansoko/internal/apperrors"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

// A selected seed comes in one of two forms. The locator form carries the
// torrent URL and the seed facts the caller copied from a feed snapshot (the
// frontend and HTTP path). The reference form carries no locator: the snapshot
// identity plus the stable seed key name one seed of one persisted snapshot,
// and the server resolves it into the locator form. Resolution happens at the
// admission entry point, before anything is hashed, compared or frozen, so the
// input snapshot, the replay identity and every later stage are exactly those
// of the locator form for the same seed. An agent therefore never needs (or
// sees) a download URL.

// isSeedReference reports whether the seed is in the reference form.
func isSeedReference(seed SelectedSeed) bool {
	return strings.TrimSpace(seed.Locator) == ""
}

// validateSeedReference requires the complete snapshot identity of a reference.
func validateSeedReference(seed SelectedSeed) error {
	if strings.TrimSpace(seed.SnapshotID) == "" || seed.SnapshotVersion < 1 ||
		strings.TrimSpace(seed.SnapshotContentHash) == "" || strings.TrimSpace(seed.StableSeedKey) == "" {
		return &apperrors.Business{
			Status: 422, Code: "SEED_INVALID",
			Message: "A seed without locator is a snapshot reference and needs snapshot_id, snapshot_version, snapshot_content_hash and stable_seed_key.",
		}
	}
	return nil
}

func seedReferenceStale(message string) error {
	return &apperrors.Business{Status: 422, Code: "SEED_PREVIEW_STALE", Message: message}
}

// resolveSeedReference reads the exact persisted snapshot a reference names and
// returns the seed in the locator form, built from that snapshot's facts. The
// snapshot must still be the fresh, current one for its provider entry and
// group; a missing, superseded, stale or mismatching snapshot, a key that is not
// in it, or a supplied fact that differs from it is SEED_PREVIEW_STALE with no
// writes. Admission then verifies the resolved seed exactly as it verifies a
// locator-form seed (persisted payload, strong identity, preview binding).
func (s *Service) resolveSeedReference(ctx context.Context, reference SelectedSeed) (SelectedSeed, error) {
	if err := validateSeedReference(reference); err != nil {
		return SelectedSeed{}, err
	}
	if s.seedPreviews == nil {
		return SelectedSeed{}, &apperrors.Business{Status: 503, Code: "SEED_PREVIEW_UNAVAILABLE", Message: "The persisted provider seed source is not configured."}
	}
	identity := animecatalogdomain.ProviderRSSPreviewIdentity{
		SnapshotID: reference.SnapshotID, SnapshotVersion: reference.SnapshotVersion, ContentHash: reference.SnapshotContentHash,
	}
	snapshot, err := s.seedPreviews.FetchEntryGroupSnapshotByID(ctx, string(reference.Provider), reference.ProviderEntryRef, reference.ProviderGroupRef, identity)
	if err != nil {
		if errors.Is(err, animeapplication.ErrProviderFeedSnapshotRefreshRequired) {
			return SelectedSeed{}, seedReferenceStale("The referenced feed snapshot is missing, superseded or stale. Refresh the feed snapshot and choose the seed again.")
		}
		var business *apperrors.Business
		if errors.As(err, &business) {
			return SelectedSeed{}, err
		}
		return SelectedSeed{}, &apperrors.Business{Status: 503, Code: "SEED_PREVIEW_UNAVAILABLE", Message: animeapplication.SafeReason(err.Error())}
	}
	if snapshot.Freshness != "fresh" || snapshot.Provider != string(reference.Provider) ||
		snapshot.ProviderEntryRef != reference.ProviderEntryRef || snapshot.ProviderGroupRef != reference.ProviderGroupRef ||
		snapshot.SnapshotID != reference.SnapshotID || snapshot.SnapshotVersion != reference.SnapshotVersion ||
		snapshot.ContentHash != reference.SnapshotContentHash {
		return SelectedSeed{}, seedReferenceStale("The referenced feed snapshot is not the current one for this group. Refresh the feed snapshot and choose the seed again.")
	}
	for index := range snapshot.Seeds {
		fact := snapshot.Seeds[index]
		if fact.StableSeedKey != reference.StableSeedKey {
			continue
		}
		resolved := seedFromSnapshotFact(reference, fact)
		if !referenceFactsAgree(reference, resolved) {
			return SelectedSeed{}, seedReferenceStale("A seed fact in the request differs from the referenced snapshot. Read the feed snapshot again and resend the reference.")
		}
		return resolved, nil
	}
	return SelectedSeed{}, seedReferenceStale("The referenced seed is not in that feed snapshot. Read the feed snapshot again and choose the seed.")
}

// seedFromSnapshotFact builds the locator-form seed for one persisted snapshot
// fact, carrying the reference's provenance and snapshot identity. The published
// time is the snapshot's instant in UTC (the catalog persists it in UTC), so the
// resolved seed does not depend on the database session time zone. The digest
// spelling is canonicalized exactly as a locator-form request is.
func seedFromSnapshotFact(reference SelectedSeed, fact animeapplication.SeedPreviewFact) SelectedSeed {
	seed := SelectedSeed{
		Provider:            animedomain.ProviderKind(reference.Provider),
		ProviderEntryRef:    reference.ProviderEntryRef,
		ProviderGroupRef:    reference.ProviderGroupRef,
		SnapshotID:          reference.SnapshotID,
		SnapshotVersion:     reference.SnapshotVersion,
		SnapshotContentHash: reference.SnapshotContentHash,
		StableSeedKey:       reference.StableSeedKey,
		Locator:             fact.DownloadURL,
		Title:               fact.Title,
	}
	if !fact.PublishedAt.IsZero() {
		seed.PublishedAt = fact.PublishedAt.UTC()
	}
	if fact.SizeBytes != nil {
		value := *fact.SizeBytes
		seed.SizeBytes = &value
	}
	if fact.InfoHash != "" {
		value := fact.InfoHash
		seed.InfoHash = &value
	}
	if fact.ExpectedPayloadDigest != "" {
		value := fact.ExpectedPayloadDigest
		seed.ExpectedPayloadDigest = &value
	}
	// The AniBT tag set is provider XML, never title text, so the snapshot is
	// its only source: without it the one-shot's frozen source facts would say
	// the item carried no tags and every `anibt.*` filter would fail a release
	// the subscribed path accepts.
	seed.AniBTTags = cloneStoredAniBTTags(fact.AniBTTags)
	return canonicalSelectedSeed(seed)
}

// referenceFactsAgree checks every seed fact the reference supplied against the
// resolved seed. Facts are optional in a reference; a supplied one must match.
func referenceFactsAgree(reference, resolved SelectedSeed) bool {
	if strings.TrimSpace(reference.Title) != "" && reference.Title != resolved.Title {
		return false
	}
	if !reference.PublishedAt.IsZero() && !reference.PublishedAt.Equal(resolved.PublishedAt) {
		return false
	}
	if reference.SizeBytes != nil && !optionalInt64Equal(reference.SizeBytes, resolved.SizeBytes) {
		return false
	}
	if reference.InfoHash != nil && !strings.EqualFold(*reference.InfoHash, optionalStringValue(resolved.InfoHash)) {
		return false
	}
	if reference.ExpectedPayloadDigest != nil &&
		normalizeDigestHex(*reference.ExpectedPayloadDigest) != normalizeDigestHex(optionalStringValue(resolved.ExpectedPayloadDigest)) {
		return false
	}
	return true
}

// seedReferenceMatchesFrozen reports whether a reference names the seed a
// marker froze: same provenance, snapshot identity and key, and every supplied
// fact equal. Replay resolves a reference against the marker's frozen seed, not
// the feed snapshot, so an exact replay returns the original handle even after
// the snapshot was superseded or went stale.
func seedReferenceMatchesFrozen(reference, frozen SelectedSeed) bool {
	return reference.Provider == frozen.Provider &&
		reference.ProviderEntryRef == frozen.ProviderEntryRef &&
		reference.ProviderGroupRef == frozen.ProviderGroupRef &&
		reference.SnapshotID == frozen.SnapshotID &&
		reference.SnapshotVersion == frozen.SnapshotVersion &&
		reference.SnapshotContentHash == frozen.SnapshotContentHash &&
		reference.StableSeedKey == frozen.StableSeedKey &&
		referenceFactsAgree(reference, frozen)
}

// frozenSeedForReference returns the seed a marker froze when a replayed
// reference names it; any other reference reuses the key for a different
// request.
func frozenSeedForReference(record animeport.OneShotMarkerRecord, reference SelectedSeed) (SelectedSeed, error) {
	snapshot := decodeOneShotInputSnapshot(record.InputSnapshot)
	if snapshot == nil {
		return SelectedSeed{}, &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The one-shot replay record has no valid frozen input snapshot."}
	}
	if !seedReferenceMatchesFrozen(reference, snapshot.SelectedSeed) {
		return SelectedSeed{}, &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different selected seed."}
	}
	return snapshot.SelectedSeed, nil
}
