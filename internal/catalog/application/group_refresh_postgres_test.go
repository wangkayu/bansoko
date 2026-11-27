package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	provider "bansoko/internal/provider/domain"
	"bansoko/internal/provider/outbound"
)

type testGroupReadFunc func(context.Context, string) (provider.GroupReadResult, error)

func (f testGroupReadFunc) ReadGroups(ctx context.Context, entry string) (provider.GroupReadResult, error) {
	return f(ctx, entry)
}

func TestGroupRefreshFailuresKeepLastSuccess(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const entry = "123"
	complete := provider.GroupReadResult{Provider: provider.ProviderAniBT, CatalogEntryRef: entry,
		SchemaVersion: "anibt-anime-groups-v1", Complete: true, CompletenessEvidence: "anibt-entry-total-v1", ObservedAt: now}
	service := NewService(db, nil)
	service.SetGroupReaders(map[string]GroupReader{"anibt": fakeGroupReader{result: complete}})
	initial, err := service.DiscoverProviderEntryGroups(ctx, "anibt", entry)
	if err != nil || initial.UpstreamStatus.State != provider.StateEmpty || initial.SnapshotVersion != 1 {
		t.Fatalf("complete empty discovery = %+v, %v", initial, err)
	}
	var previousAttempt time.Time
	if err := db.QueryRow(`SELECT last_attempt_at FROM provider_group_refresh_health WHERE catalog_entry_ref=$1`, entry).Scan(&previousAttempt); err != nil {
		t.Fatal(err)
	}
	incomplete := complete
	incomplete.Complete = false
	deadline := outbound.CooldownError{ReasonCode: outbound.ReasonBucketCooldown, CheckedAt: now, CooldownUntil: now.Add(time.Minute), RetryAfter: time.Minute}
	for _, tc := range []struct {
		name   string
		reader fakeGroupReader
		state  provider.State
		reason provider.ReasonCode
		wait   bool
	}{
		{"incomplete", fakeGroupReader{result: incomplete}, provider.StateError, provider.ReasonObservationIncomplete, false},
		{"not indexed", fakeGroupReader{err: &provider.ReadFailure{Status: provider.Status{State: provider.StateNotIndexed, ReasonCode: provider.ReasonAnimeNotFound}}}, provider.StateNotIndexed, provider.ReasonAnimeNotFound, false},
		{"rate limited", fakeGroupReader{err: &outbound.ThrottleError{CooldownError: deadline, StatusCode: 429}}, provider.StateRateLimited, provider.ReasonRateLimited, true},
		{"cooldown", fakeGroupReader{err: &deadline}, provider.StateCooldown, provider.ReasonBucketCooldown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service.SetGroupReaders(map[string]GroupReader{"anibt": tc.reader})
			got, err := service.DiscoverProviderEntryGroups(ctx, "anibt", entry)
			if (err == nil) != (tc.state == provider.StateNotIndexed) || got.UpstreamStatus.State != tc.state || got.UpstreamStatus.ReasonCode != tc.reason || got.SnapshotVersion != 0 {
				t.Fatalf("outcome = %+v, %v", got, err)
			}
			var state provider.State
			var reason provider.ReasonCode
			var attempted, success, checked time.Time
			var until sql.NullTime
			var count int
			if err := db.QueryRow(`SELECT state,error_code,last_attempt_at,last_success_at,checked_at,cooldown_until,
                (SELECT count(*) FROM provider_group_snapshots WHERE provider='anibt' AND catalog_entry_ref=$1)
                FROM provider_group_refresh_health WHERE provider='anibt' AND catalog_entry_ref=$1`, entry).
				Scan(&state, &reason, &attempted, &success, &checked, &until, &count); err != nil {
				t.Fatal(err)
			}
			if state != tc.state || reason != tc.reason || !attempted.After(previousAttempt) || !success.Equal(now) || count != 1 || checked.IsZero() || until.Valid != tc.wait || (tc.wait && !until.Time.Equal(deadline.CooldownUntil)) {
				t.Fatalf("health = %s/%s attempt=%v success=%v checked=%v until=%v snapshots=%d", state, reason, attempted, success, checked, until, count)
			}
			previousAttempt = attempted
		})
	}
}

func TestGroupRefreshOlderFailureDoesNotReplaceNewerOutcome(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := t.Context()
	older := testGroupReadFunc(func(ctx context.Context, entry string) (provider.GroupReadResult, error) {
		var attempted time.Time
		var state sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT last_attempt_at,state FROM provider_group_refresh_health WHERE provider='anibt' AND catalog_entry_ref=$1`, entry).Scan(&attempted, &state); err != nil || attempted.IsZero() || state.Valid {
			t.Fatalf("initial attempt = %v, state = %v, err = %v", attempted, state, err)
		}
		limited := &outbound.ThrottleError{StatusCode: 429, CooldownError: outbound.CooldownError{
			CheckedAt: attempted, CooldownUntil: attempted.Add(time.Minute), RetryAfter: time.Minute,
		}}
		if _, status, err := ReadProviderGroups(ctx, db, fakeGroupReader{err: limited}, "anibt", entry); err == nil || status.State != provider.StateRateLimited {
			t.Fatalf("newer attempt = %+v, %v", status, err)
		}
		return provider.GroupReadResult{}, errors.New("older read failed")
	})
	if _, status, err := ReadProviderGroups(ctx, db, older, "anibt", "123"); err == nil || status.State != provider.StateError {
		t.Fatalf("older attempt = %+v, %v", status, err)
	}
	var state provider.State
	if err := db.QueryRowContext(ctx, `SELECT state FROM provider_group_refresh_health WHERE provider='anibt' AND catalog_entry_ref='123'`).Scan(&state); err != nil || state != provider.StateRateLimited {
		t.Fatalf("persisted state = %q, %v", state, err)
	}
}
