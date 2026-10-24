package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/catalog/domain"
	"bansoko/internal/events"
	"github.com/google/uuid"
)

const suppressionColumns = `id, provider, provider_entry_ref, title_snapshot, suppressed_at, version`

// ErrSuppressionInvalid marks a rejected suppression request so the transport
// can answer 422 instead of letting a validation failure fall through as 500.
var ErrSuppressionInvalid = errors.New("catalog suppression request is invalid")

// ListSuppressions returns every suppression across providers and seasons.
// Suppressions are not season-scoped, so the caller gets the whole list and
// filters client-side if it needs a subset.
func (s *Service) ListSuppressions(ctx context.Context) ([]domain.Suppression, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("catalog database is nil")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+suppressionColumns+` FROM catalog_suppressions ORDER BY suppressed_at DESC, provider, provider_entry_ref`)
	if err != nil {
		return nil, fmt.Errorf("list catalog suppressions: %w", err)
	}
	defer rows.Close()
	result := make([]domain.Suppression, 0)
	for rows.Next() {
		var suppression domain.Suppression
		if err := rows.Scan(&suppression.ID, &suppression.Provider, &suppression.ProviderEntryRef, &suppression.TitleSnapshot, &suppression.SuppressedAt, &suppression.Version); err != nil {
			return nil, fmt.Errorf("scan catalog suppression: %w", err)
		}
		result = append(result, suppression)
	}
	return result, rows.Err()
}

// SuppressEntry is idempotent: suppressing an already suppressed entry refreshes
// the stored title and returns the existing record instead of failing.
func (s *Service) SuppressEntry(ctx context.Context, provider, providerEntryRef, titleSnapshot string) (domain.Suppression, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	providerEntryRef = strings.TrimSpace(providerEntryRef)
	titleSnapshot = strings.TrimSpace(titleSnapshot)
	if s == nil || s.db == nil {
		return domain.Suppression{}, errors.New("catalog database is nil")
	}
	if err := validateCatalogProvider(provider); err != nil {
		return domain.Suppression{}, fmt.Errorf("%w: %s", ErrSuppressionInvalid, err)
	}
	if err := validateProviderCatalogEntryRef(provider, providerEntryRef); err != nil {
		return domain.Suppression{}, fmt.Errorf("%w: %s", ErrSuppressionInvalid, err)
	}
	if titleSnapshot == "" || len(titleSnapshot) > 512 {
		return domain.Suppression{}, fmt.Errorf("%w: title snapshot is required and must be at most 512 characters", ErrSuppressionInvalid)
	}
	var suppression domain.Suppression
	// The version only advances when the stored title actually changes, so
	// re-submitting the same suppression is a true no-op rather than churn.
	err := s.db.QueryRowContext(ctx, `INSERT INTO catalog_suppressions(id, provider, provider_entry_ref, title_snapshot)
VALUES ($1, $2, $3, $4)
ON CONFLICT (provider, provider_entry_ref) DO UPDATE
SET title_snapshot = EXCLUDED.title_snapshot,
    version = catalog_suppressions.version + (CASE WHEN catalog_suppressions.title_snapshot <> EXCLUDED.title_snapshot THEN 1 ELSE 0 END),
    updated_at = NOW()
RETURNING `+suppressionColumns, uuid.NewString(), provider, providerEntryRef, titleSnapshot).
		Scan(&suppression.ID, &suppression.Provider, &suppression.ProviderEntryRef, &suppression.TitleSnapshot, &suppression.SuppressedAt, &suppression.Version)
	if err != nil {
		return domain.Suppression{}, fmt.Errorf("suppress catalog entry: %w", err)
	}
	if s.eventHub != nil {
		s.eventHub.Publish(events.Event{Target: events.TargetCatalog})
	}
	return suppression, nil
}

// UnsuppressEntry is idempotent: removing a suppression that does not exist
// succeeds, so a stale client never sees a spurious failure.
func (s *Service) UnsuppressEntry(ctx context.Context, provider, providerEntryRef string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	providerEntryRef = strings.TrimSpace(providerEntryRef)
	if s == nil || s.db == nil {
		return errors.New("catalog database is nil")
	}
	if err := validateCatalogProvider(provider); err != nil {
		return fmt.Errorf("%w: %s", ErrSuppressionInvalid, err)
	}
	if err := validateProviderCatalogEntryRef(provider, providerEntryRef); err != nil {
		return fmt.Errorf("%w: %s", ErrSuppressionInvalid, err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM catalog_suppressions WHERE provider = $1 AND provider_entry_ref = $2`, provider, providerEntryRef); err != nil {
		return fmt.Errorf("unsuppress catalog entry: %w", err)
	}
	if s.eventHub != nil {
		s.eventHub.Publish(events.Event{Target: events.TargetCatalog})
	}
	return nil
}

// suppressedProviderRefs returns the suppressed entry refs of one provider.
// Suppressions span seasons and stay small, so the whole provider set is loaded
// rather than parameterising on the season's refs.
func (s *Service) suppressedProviderRefs(ctx context.Context, provider string) (map[string]struct{}, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("catalog database is nil")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT provider_entry_ref FROM catalog_suppressions WHERE provider = $1`, provider)
	if err != nil {
		return nil, fmt.Errorf("read provider catalog suppressions: %w", err)
	}
	defer rows.Close()
	refs := map[string]struct{}{}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, fmt.Errorf("scan provider catalog suppression: %w", err)
		}
		refs[ref] = struct{}{}
	}
	return refs, rows.Err()
}
