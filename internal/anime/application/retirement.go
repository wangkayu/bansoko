package application

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"bansoko/internal/apperrors"
)

// ManagedAnimeRetiredError reports that the owner lifecycle no longer admits new work.
func ManagedAnimeRetiredError() error {
	return &apperrors.Business{
		Status:  409,
		Code:    "MANAGED_ANIME_RETIRED",
		Message: "The managed anime is retired and admits no new work.",
	}
}

func managedAnimeRetiredError() error { return ManagedAnimeRetiredError() }

var retirementFenceMessages = map[string]bool{
	"MANAGED_ANIME_RETIRED":                  true,
	"MANAGED_ANIME_RETIREMENT_FENCE_STALE":   true,
	"MANAGED_ANIME_RETIREMENT_FENCE_INVALID": true,
	"WORK_ITEM_OWNER_CHAIN_CHANGED":          true,
}

// MapRetirementDBError maps known lifecycle-boundary violations to their typed
// business errors. After local cleanup the owner row is gone, so a late child
// write fails with a foreign-key violation on these paths; it is the same
// closed boundary as retirement and maps to the retired error.
func MapRetirementDBError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "23503":
		return ManagedAnimeRetiredError()
	case "23514":
		if retirementFenceMessages[pgErr.Message] {
			return ManagedAnimeRetiredError()
		}
	}
	return err
}

func mapRetirementDBError(err error) error { return MapRetirementDBError(err) }

// SubscriptionNotStartedError is the typed refusal of a command that a pending
// (never started) Subscription does not admit: it does no work until its
// first start, and it can only be started.
func SubscriptionNotStartedError() error {
	return &apperrors.Business{
		Status:  409,
		Code:    "SUBSCRIPTION_NOT_STARTED",
		Message: "The subscription has not been started.",
	}
}
