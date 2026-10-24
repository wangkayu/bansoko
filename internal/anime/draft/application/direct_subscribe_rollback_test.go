package application

import (
	"bansoko/internal/testutil/presetdraft"
	"context"
	"database/sql"
	"fmt"
	"testing"

	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// TestDirectSubscribeOwnedWriteFailuresRollBackEverything asserts that a
// failure at any owned-write stage of the direct (non-candidate) Subscribe
// transaction rolls back every row: no anime, subscription, metadata, config
// owner/override, source/runtime, work item, idempotency record, or activity.
// The direct path shares subscribeOnce with candidate activation; this pins
// its rollback guarantee independently so a future fork of the two paths
// cannot silently lose protection.
func TestDirectSubscribeOwnedWriteFailuresRollBackEverything(t *testing.T) {
	cases := []struct {
		name  string
		table string
	}{
		{name: "early/managed_anime", table: "managed_anime"},
		{name: "early/subscription", table: "subscriptions"},
		{name: "early/metadata", table: "anime_metadata"},
		{name: "mid/business_config", table: "anime_business_configs"},
		{name: "mid/override", table: "anime_config_overrides"},
		{name: "mid/rss_source", table: "rss_sources"},
		{name: "mid/rss_runtime", table: "rss_source_runtime"},
		{name: "mid/work_item", table: "work_items"},
		{name: "late/idempotency", table: "idempotency_records"},
		{name: "late/subscription_history", table: "subscription_log_entries"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := animetest.OpenTestDB(t)
			service := draftNewTestService(t, db)
			installDirectSubscribeFailureTrigger(t, db, tc.table)

			if _, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, draftTestDraft(t)), Start: new(true)}, "direct-rollback-"+tc.name); err == nil {
				t.Fatal("direct subscribe unexpectedly succeeded despite injected owned-write failure")
			}
			assertNoProductRows(t, db)
		})
	}
}

// TestDirectSubscribeLateCommitFailureRollsBackEverything injects a deferred
// AFTER-trigger so the failure only fires at COMMIT time, proving no partial
// row escapes even when every insert inside the transaction succeeded.
func TestDirectSubscribeLateCommitFailureRollsBackEverything(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()
	if _, err := animetest.OwnerTestDB(t).ExecContext(ctx, `
CREATE FUNCTION reject_direct_subscribe_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'injected late direct subscribe failure';
END;
$$;
CREATE CONSTRAINT TRIGGER reject_direct_subscribe_commit
AFTER INSERT ON subscription_log_entries
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION reject_direct_subscribe_commit();`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, draftTestDraft(t)), Start: new(true)}, "direct-rollback-commit"); err == nil {
		t.Fatal("direct subscribe unexpectedly succeeded despite deferred commit failure")
	}
	assertNoProductRows(t, db)
}

func installDirectSubscribeFailureTrigger(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	trigger := fmt.Sprintf(`
CREATE FUNCTION reject_direct_subscribe_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'injected direct subscribe write failure';
END;
$$;
CREATE TRIGGER reject_direct_subscribe_write
BEFORE INSERT ON %s
FOR EACH ROW EXECUTE FUNCTION reject_direct_subscribe_write();`, table)
	if _, err := animetest.OwnerTestDB(t).ExecContext(context.Background(), trigger); err != nil {
		t.Fatalf("install failure trigger on %s: %v", table, err)
	}
}

// assertNoProductRows asserts every product owner table (including the
// subscription history written through the activity writer) is empty.
func assertNoProductRows(t *testing.T, db *sql.DB) {
	t.Helper()
	counts := animetest.ProductRowCounts(t, db)
	counts["subscription_log_entries"] = animetest.CountRows(t, db, "subscription_log_entries")
	for table, count := range counts {
		if count != 0 {
			t.Fatalf("table %s has %d rows after failed subscribe, want 0", table, count)
		}
	}
}
