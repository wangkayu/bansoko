package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	catalogdomain "bansoko/internal/catalog/domain"
	providerdomain "bansoko/internal/provider/domain"
)

// ReadProviderGroups records the attempt before provider I/O. Only the caller's
// publication transaction may record a success; failed reads stop here.
func ReadProviderGroups(ctx context.Context, db *sql.DB, reader GroupReader, provider, entry string) (providerdomain.GroupReadResult, providerdomain.Status, error) {
	var attemptedAt time.Time
	if err := db.QueryRowContext(ctx, `
INSERT INTO provider_group_refresh_health(provider,catalog_entry_ref,last_attempt_at)
VALUES ($1,$2,statement_timestamp())
ON CONFLICT (provider,catalog_entry_ref) DO UPDATE SET
    last_attempt_at=GREATEST(provider_group_refresh_health.last_attempt_at + INTERVAL '1 microsecond',EXCLUDED.last_attempt_at),
    version=provider_group_refresh_health.version+1, updated_at=statement_timestamp()
RETURNING last_attempt_at`, provider, entry).Scan(&attemptedAt); err != nil {
		return providerdomain.GroupReadResult{}, providerdomain.Status{}, fmt.Errorf("record group refresh attempt: %w", err)
	}
	result, err := reader.ReadGroups(ctx, entry)
	if err == nil {
		if string(result.Provider) != provider || result.CatalogEntryRef != entry {
			err = errors.New("provider discovery identity mismatch")
		} else {
			err = validateCompleteGroupRead(result)
		}
	}
	if err == nil {
		result.AttemptedAt = attemptedAt
		return result, providerdomain.GroupReadStatus(result), nil
	}
	status := providerdomain.ClassifyReadError(err, time.Now().UTC())
	var deadline *time.Time
	if !status.CooldownUntil.IsZero() {
		deadline = &status.CooldownUntil
	}
	// A canceled caller may still have received a response. Record that outcome
	// with a bounded context, but do not overwrite a newer attempt's outcome.
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, healthErr := db.ExecContext(persist, `
UPDATE provider_group_refresh_health SET state=$3, error_code=$4,
    error_message=NULL, checked_at=$5, cooldown_until=$6,
    version=version+1, updated_at=statement_timestamp()
WHERE provider=$1 AND catalog_entry_ref=$2 AND last_attempt_at=$7`,
		provider, entry, status.State, status.ReasonCode, status.CheckedAt, deadline, attemptedAt)
	if healthErr != nil {
		err = errors.Join(err, fmt.Errorf("record group refresh outcome: %w", healthErr))
	}
	return result, status, &providerdomain.ReadFailure{Status: status, Err: err}
}

func validateCompleteGroupRead(result providerdomain.GroupReadResult) error {
	schema, evidence := catalogdomain.GroupEvidence(result.Provider)
	if !result.Complete || evidence == "" || result.CompletenessEvidence != evidence || result.SchemaVersion != schema {
		return &providerdomain.ReadFailure{Status: providerdomain.Status{
			State: providerdomain.StateError, ReasonCode: providerdomain.ReasonObservationIncomplete,
		}, Err: errors.New("provider group refresh response is incomplete")}
	}
	for _, group := range result.Groups {
		if group.Active == nil {
			return &providerdomain.ReadFailure{Status: providerdomain.Status{
				State: providerdomain.StateError, ReasonCode: providerdomain.ReasonObservationIncomplete,
			}, Err: errors.New("provider group availability is unknown")}
		}
	}
	// Use the same identity and locator validation as the publication path.
	return (providerdomain.GroupSnapshot{ID: "validation", Version: 1, Provider: result.Provider,
		CatalogEntryRef: result.CatalogEntryRef, ObservedAt: result.ObservedAt,
		SchemaVersion: result.SchemaVersion, Groups: result.Groups}).Validate()
}
