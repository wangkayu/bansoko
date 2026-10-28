package application

import (
	"context"
	"database/sql"
	"fmt"

	"bansoko/internal/apperrors"
)

// IdentityKindChangeBlockedCode is the 409 a metadata write returns when it
// would change the effective media kind of an instance whose identity is in
// use.
const IdentityKindChangeBlockedCode = "IDENTITY_KIND_CHANGE_BLOCKED"

// IdentityKindBlockers counts what holds an instance's media kind: the media
// kind decides the episode key shape (tv:e<N> or movie:0), so a change
// between tv and movie would leave the instance's files and in-flight
// acquisitions keyed under a kind it no longer resolves with.
type IdentityKindBlockers struct {
	// ActivePlacements counts the instance's active library files.
	ActivePlacements int64
	// PendingAcquisitions counts the instance's accepted acquisitions that
	// have not settled.
	PendingAcquisitions int64
}

// Blocked reports whether the instance's media kind must stay as it is.
func (b IdentityKindBlockers) Blocked() bool {
	return b.ActivePlacements > 0 || b.PendingAcquisitions > 0
}

// EffectiveMediaKind is the media kind an identity resolves with: the owner
// kind, NULL (unset) being tv.
func EffectiveMediaKind(override *string) string {
	if override != nil && *override == "movie" {
		return "movie"
	}
	return "tv"
}

// LoadIdentityKindBlockersTx counts the blockers of one instance inside the
// caller's transaction. Every writer of media_kind_override reads it.
func LoadIdentityKindBlockersTx(ctx context.Context, tx *sql.Tx, managedAnimeID string) (IdentityKindBlockers, error) {
	var blockers IdentityKindBlockers
	// An acquisition belongs to the instance by its origin or by the
	// subscription whose RSS release it downloads; a one-shot acquisition never
	// enters the library and holds nothing. Every acquisition that has not
	// settled counts, whatever stage it is in — including one whose import
	// waits for configuration (plan waiting_config), which keeps its root
	// running until it completes or is cancelled.
	err := tx.QueryRowContext(ctx, `
SELECT
    (SELECT count(*) FROM placements p
     WHERE p.managed_anime_id = $1 AND p.state = 'active'),
    (SELECT count(*) FROM acquisitions r
     WHERE r.state NOT IN ('succeeded', 'failed', 'cancelled')
       AND r.entry_kind <> 'one_shot'
       AND (r.origin_managed_anime_ref = $1 OR EXISTS (
            SELECT 1 FROM download_tasks d
            JOIN releases rel ON rel.id = d.release_id
            JOIN rss_sources src ON src.id = rel.rss_source_id
            JOIN subscriptions sub ON sub.id = src.subscription_id
            WHERE d.acquisition_id = r.id AND sub.managed_anime_id = $1)))`,
		managedAnimeID).Scan(&blockers.ActivePlacements, &blockers.PendingAcquisitions)
	if err != nil {
		return IdentityKindBlockers{}, fmt.Errorf("count identity kind blockers: %w", err)
	}
	return blockers, nil
}

// IdentityKindChangeAllowedTx reports whether an instance's effective media
// kind may move from current to next. An unchanged kind is always allowed;
// a change is allowed only while nothing holds the identity. The blockers are
// returned so a refusal can name them.
func IdentityKindChangeAllowedTx(ctx context.Context, tx *sql.Tx, managedAnimeID string, current, next *string) (bool, IdentityKindBlockers, error) {
	if EffectiveMediaKind(current) == EffectiveMediaKind(next) {
		return true, IdentityKindBlockers{}, nil
	}
	blockers, err := LoadIdentityKindBlockersTx(ctx, tx, managedAnimeID)
	if err != nil {
		return false, IdentityKindBlockers{}, err
	}
	return !blockers.Blocked(), blockers, nil
}

// IdentityKindChangeBlockedError is the typed 409 of an owner write refused
// by IdentityKindChangeAllowedTx; details carry both counts.
func IdentityKindChangeBlockedError(blockers IdentityKindBlockers) error {
	placements, acquisitions := blockers.ActivePlacements, blockers.PendingAcquisitions
	return &apperrors.Business{
		Status:  409,
		Code:    IdentityKindChangeBlockedCode,
		Message: "The media kind cannot change while the anime has library files or acquisitions that have not settled.",
		Details: []apperrors.FieldViolation{
			{Code: "active_placements", Message: "Active library files of the anime.", Count: &placements},
			{Code: "pending_acquisitions", Message: "Acquisitions of the anime that have not settled.", Count: &acquisitions},
		},
	}
}
