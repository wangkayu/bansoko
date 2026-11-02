package application

import (
	"context"
	"encoding/json"
	"testing"
)

// TestListReleaseBatchPagesDetailEntries drives the batch detail feed over real
// rows: the newest-first page, the keyset cursor across it, the batch key every
// returned entry carries, and the empty page of a batch whose key matches no
// detail row. The scan of the outer projection is only exercised by a live
// query: a fake row source cannot prove the selected columns match the scan.
func TestListReleaseBatchPagesDetailEntries(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed release batch fixture: %v", err)
		}
	}
	exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-batch', 'Batch Test Anime')`)
	exec(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
		VALUES ('subscription-batch', 'anime-batch', 'custom', 'batch', 'active')`)
	exec(`INSERT INTO subscription_log_entries(
		id, subscription_id, managed_anime_id, kind, outcome, occurred_at, message_code, message_params,
		dedupe_key, subject_type, subject_id
	) VALUES ('log-batch-head', 'subscription-batch', 'anime-batch', 'release_batch_processed', 'info',
		CURRENT_TIMESTAMP + interval '10 seconds', 'release_batch_reevaluated', '{"count":4}',
		'batch-fixture-head', 'release_batch', 'batch-fixture-key')`)
	for index, id := range []string{"log-batch-1", "log-batch-2", "log-batch-3", "log-batch-4"} {
		exec(`INSERT INTO subscription_log_entries(
			id, subscription_id, managed_anime_id, kind, outcome, occurred_at, message_code, message_params,
			dedupe_key, batch_key
		) VALUES ($1, 'subscription-batch', 'anime-batch', 'release_processed', 'info',
			CURRENT_TIMESTAMP + make_interval(secs => $2), 'release_awaiting_competition', '{}',
			$3, 'batch-fixture-key')`,
			id, index, "batch-fixture-"+id)
	}

	service, err := NewService(db)
	if err != nil {
		t.Fatalf("new observability service: %v", err)
	}

	type entryShape struct {
		RecordType string `json:"record_type"`
		Entry      struct {
			ID         string  `json:"id"`
			BatchKey   *string `json:"batch_key"`
			Kind       string  `json:"kind"`
			AnimeTitle string  `json:"anime_title"`
		} `json:"entry"`
	}
	decode := func(t *testing.T, page RecordPage) []entryShape {
		t.Helper()
		entries := make([]entryShape, 0, len(page.Records))
		for _, record := range page.Records {
			var shape entryShape
			if err := json.Unmarshal(record.Raw, &shape); err != nil {
				t.Fatalf("decode batch detail record: %v", err)
			}
			if shape.RecordType != "business" || shape.Entry.Kind != "release_processed" {
				t.Fatalf("unexpected batch detail record: %+v", shape)
			}
			if shape.Entry.BatchKey == nil || *shape.Entry.BatchKey != "batch-fixture-key" {
				t.Fatalf("batch detail lost its batch key: %+v", shape)
			}
			if shape.Entry.AnimeTitle != "Batch Test Anime" {
				t.Fatalf("batch detail lost the anime title: %+v", shape)
			}
			entries = append(entries, shape)
		}
		return entries
	}

	limit := 3
	first, err := service.ListReleaseBatch(ctx, "batch-fixture-key", RecordQuery{Limit: &limit})
	if err != nil {
		t.Fatalf("list release batch: %v", err)
	}
	entries := decode(t, first)
	if len(entries) != 3 || first.NextCursor == nil {
		t.Fatalf("first page = %d entries, cursor %v", len(entries), first.NextCursor)
	}
	wantNewestFirst := []string{"log-batch-4", "log-batch-3", "log-batch-2"}
	for index, id := range wantNewestFirst {
		if entries[index].Entry.ID != id {
			t.Fatalf("first page order = %v", entries)
		}
	}

	second, err := service.ListReleaseBatch(ctx, "batch-fixture-key", RecordQuery{Limit: &limit, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("list release batch second page: %v", err)
	}
	entries = decode(t, second)
	if len(entries) != 1 || entries[0].Entry.ID != "log-batch-1" || second.NextCursor != nil {
		t.Fatalf("second page = %+v, cursor %v", entries, second.NextCursor)
	}

	empty, err := service.ListReleaseBatch(ctx, "batch-fixture-missing", RecordQuery{Limit: &limit})
	if err != nil {
		t.Fatalf("list release batch of an unknown key: %v", err)
	}
	if len(empty.Records) != 0 || empty.NextCursor != nil {
		t.Fatalf("unknown key page = %+v, cursor %v", empty.Records, empty.NextCursor)
	}
}
