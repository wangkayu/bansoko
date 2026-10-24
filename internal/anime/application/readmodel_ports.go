package application

import (
	"context"

	animedomain "bansoko/internal/anime/domain"
)

// This file is the kernel's read-model host contract: the side-effect-free
// reads and pure projections the extracted read model consumes through its own
// narrow ports. The candidate revision read the read model needs for a saved
// preview belongs to the draft owner (the package that stores the revision) and
// is injected separately.

// ReadOverrides reads persisted owner overrides for read models.
func (s *Service) ReadOverrides(ctx context.Context, managedAnimeID string) (animedomain.OverrideValue, map[string]animedomain.OverrideValue, error) {
	return s.readOverrides(ctx, managedAnimeID)
}

// CanonicalAnimeDraftHash returns the same validated canonical digest used by
// candidate save and revision integrity verification.
func CanonicalAnimeDraftHash(draft animedomain.AnimeDraft) (string, error) {
	return canonicalAnimeDraftHash(draft)
}

// CanonicalJSONHash returns the package's canonical JSON digest.
func CanonicalJSONHash(value any) (string, error) { return canonicalJSONHash(value) }

// DraftInvalidError converts domain field violations to the stable validation error.
func DraftInvalidError(violations []animedomain.FieldViolation) error {
	return draftInvalidError(violations)
}

// SectionResolutionError maps section-resolution failures to the established API error.
func SectionResolutionError(err error) error { return sectionResolutionError(err) }

// SafeReason returns a bounded, redacted reason string.
func SafeReason(raw string) string { return safeReason(raw) }
