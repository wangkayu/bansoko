package application

import (
	"context"
	"testing"
	"time"

	"bansoko/internal/catalog/adapter/persistence"
)

// TestPreviewFreshWindowFollowsRSSRefreshInterval proves a provider feed
// preview snapshot is fresh for exactly one RSS refresh interval.
func TestPreviewFreshWindowFollowsRSSRefreshInterval(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	service := NewService(db, nil)
	service.SetProviderFeedFetcher(&countingFeedFetcher{items: feedItems()})
	service.SetFeedSnapshotStore(persistence.NewRepository(db))
	freshFor := func(entryRef string) time.Duration {
		t.Helper()
		if _, err := service.RefreshProviderEntryRSS(ctx, "mikan", entryRef, "678"); err != nil {
			t.Fatal(err)
		}
		var window float64
		if err := db.QueryRowContext(ctx, `SELECT EXTRACT(EPOCH FROM fresh_until - fetched_at) FROM provider_feed_snapshots WHERE provider = 'mikan' AND provider_entry_ref = $1 ORDER BY version DESC LIMIT 1`, entryRef).Scan(&window); err != nil {
			t.Fatal(err)
		}
		return time.Duration(window * float64(time.Second)).Round(time.Second)
	}
	if got := freshFor("70001"); got != 15*time.Minute {
		t.Fatalf("default fresh window = %s, want the shipped 15-minute RSS interval", got)
	}
	if _, err := db.ExecContext(ctx, `UPDATE automation_schedules SET interval_value = 2, interval_unit = 'hour' WHERE kind = 'rss_refresh'`); err != nil {
		t.Fatal(err)
	}
	if got := freshFor("70002"); got != 2*time.Hour {
		t.Fatalf("fresh window = %s, want the 2-hour RSS interval", got)
	}
}
