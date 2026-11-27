package application

import (
	"database/sql"
	"testing"
	"time"

	provider "bansoko/internal/provider/domain"
	"bansoko/internal/provider/outbound"
)

func TestOlderGroupSuccessPreservesNewerQuota(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := t.Context()
	for _, tc := range []struct {
		entry string
		reuse bool
	}{{"123", false}, {"124", true}} {
		t.Run(tc.entry, func(t *testing.T) {
			complete := provider.GroupReadResult{Provider: provider.ProviderAniBT, CatalogEntryRef: tc.entry,
				SchemaVersion: "anibt-anime-groups-v1", CompletenessEvidence: "anibt-entry-total-v1", Complete: true}
			publish := func(result provider.GroupReadResult, reuse bool) {
				t.Helper()
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if reuse {
					_, reused, err := ReuseUnchangedGroupObservationTx(ctx, tx, result)
					if err != nil || !reused {
						t.Fatalf("reuse=%t err=%v", reused, err)
					}
				} else if _, err := PersistGroupObservationTx(ctx, tx, result); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			read := func(at time.Time) provider.GroupReadResult {
				t.Helper()
				result := complete
				result.ObservedAt = at
				got, _, err := ReadProviderGroups(ctx, db, fakeGroupReader{result: result}, "anibt", tc.entry)
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			now := time.Now().UTC().Truncate(time.Microsecond)
			publish(read(now), false)
			delayed := read(now.Add(time.Second))
			checked := now.Add(2 * time.Second)
			until := checked.Add(time.Minute)
			limited := &outbound.ThrottleError{StatusCode: 429, CooldownError: outbound.CooldownError{CheckedAt: checked, CooldownUntil: until, RetryAfter: time.Minute}}
			if _, status, err := ReadProviderGroups(ctx, db, fakeGroupReader{err: limited}, "anibt", tc.entry); err == nil || status.State != provider.StateRateLimited {
				t.Fatalf("quota status=%+v err=%v", status, err)
			}
			var latestAttempt time.Time
			if err := db.QueryRowContext(ctx, `SELECT last_attempt_at FROM provider_group_refresh_health WHERE catalog_entry_ref=$1`, tc.entry).Scan(&latestAttempt); err != nil {
				t.Fatal(err)
			}
			publish(delayed, tc.reuse)
			var state, reason string
			var attempted, success, outcomeAt, cooldown time.Time
			if err := db.QueryRowContext(ctx, `SELECT state,error_code,last_attempt_at,last_success_at,checked_at,cooldown_until FROM provider_group_refresh_health WHERE catalog_entry_ref=$1`, tc.entry).Scan(&state, &reason, &attempted, &success, &outcomeAt, &cooldown); err != nil {
				t.Fatal(err)
			}
			if state != "rate_limited" || reason != "PROVIDER_RATE_LIMITED" || !attempted.Equal(latestAttempt) || !success.Equal(delayed.ObservedAt) || !outcomeAt.Equal(checked) || !cooldown.Equal(until) {
				t.Fatalf("late success changed latest outcome: %s/%s attempt=%v success=%v checked=%v cooldown=%v", state, reason, attempted, success, outcomeAt, cooldown)
			}
			newest := read(now.Add(3 * time.Second))
			publish(newest, tc.reuse)
			var cleared sql.NullTime
			if err := db.QueryRowContext(ctx, `SELECT state,last_success_at,cooldown_until FROM provider_group_refresh_health WHERE catalog_entry_ref=$1`, tc.entry).Scan(&state, &success, &cleared); err != nil || state != "empty" || !success.Equal(newest.ObservedAt) || cleared.Valid {
				t.Fatalf("newest success state=%s success=%v cooldown=%v err=%v", state, success, cleared, err)
			}
		})
	}
}
