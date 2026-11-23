package application

import (
	"context"
	"testing"

	"bansoko/internal/testutil/animetest"
)

func TestPatchRSSSourceSpanOnlyCreatesReleaseReconciliationBatch(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribed := ownershipSubscribeTestDraft(t, service, "subscribe-key-span-only")
	ctx := context.Background()
	sourceID := subscribed.RssSources[0].Id
	if _, err := db.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('span-new', $1, 'span-new', '{}', '{}', 'discovered', 'new')`, sourceID); err != nil {
		t.Fatal(err)
	}

	patched, err := service.PatchRSSSourceOffset(ctx, subscribed.ManagedAnime.Id, sourceID, RSSSourceOffsetPatchInput{
		ExpectedVersion: 1, EpisodeOffset: 0, EpisodeSpan: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Version != 2 || patched.EpisodeOffset != 0 || patched.EpisodeSpan != 2 {
		t.Fatalf("patched source = %+v, want version 2 and offset/span 0/2", patched)
	}
	var oldOffset, newOffset, oldSpan, newSpan, rekeyed int
	var state string
	if err := db.QueryRowContext(ctx, `
SELECT old_offset, new_offset, old_span, new_span, rekeyed_count, state
FROM offset_alignment_changes WHERE rss_source_id = $1 AND source_version = 2`, sourceID).Scan(
		&oldOffset, &newOffset, &oldSpan, &newSpan, &rekeyed, &state); err != nil {
		t.Fatal(err)
	}
	if oldOffset != 0 || newOffset != 0 || oldSpan != 1 || newSpan != 2 || rekeyed != 0 || state != "recorded" {
		t.Fatalf("span alignment change = offsets %d/%d spans %d/%d rekeyed %d state %s", oldOffset, newOffset, oldSpan, newSpan, rekeyed, state)
	}
	var scheduled bool
	if err := db.QueryRowContext(ctx, `SELECT candidates_scheduled_at IS NOT NULL FROM offset_alignment_changes WHERE rss_source_id = $1 AND source_version = 2`, sourceID).Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if scheduled {
		t.Fatal("the span change must wait for the existing subscription batch barrier")
	}
}
