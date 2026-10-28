package application

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestListRecordsKindFilterIsRepeatable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed record fixture: %v", err)
		}
	}
	exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-records', 'Records Test Anime')`)
	exec(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
		VALUES ('subscription-records', 'anime-records', 'custom', 'records', 'active')`)
	exec(`INSERT INTO routine_activities(
		id, kind, scope_kind, scope_id, managed_anime_id, outcome, last_executed_at,
		last_result_code, summary
	) VALUES ('routine-records', 'rss_refresh', 'subscription', 'subscription-records',
		'anime-records', 'success', CURRENT_TIMESTAMP, 'success', 'routine fixture')`)
	exec(`INSERT INTO subscription_log_entries(
		id, subscription_id, managed_anime_id, kind, outcome, occurred_at, message_code, message_params, dedupe_key
	) VALUES ('log-records', 'subscription-records', 'anime-records', 'rss_new_content', 'info',
		CURRENT_TIMESTAMP, 'rss_new_content', '{"count":1}', 'records-fixture')`)

	service, err := NewService(db)
	if err != nil {
		t.Fatalf("new observability service: %v", err)
	}

	recordTypes := func(t *testing.T, params RecordQuery) []string {
		t.Helper()
		page, err := service.ListRecords(ctx, params)
		if err != nil {
			t.Fatalf("list records: %v", err)
		}
		var types []string
		for _, record := range page.Records {
			raw := record.Raw
			var shape struct {
				RecordType string `json:"record_type"`
			}
			if err := json.Unmarshal(raw, &shape); err != nil {
				t.Fatalf("decode record union: %v", err)
			}
			types = append(types, shape.RecordType)
		}
		return types
	}

	assertTypes := func(t *testing.T, params RecordQuery, want map[string]int) {
		t.Helper()
		got := map[string]int{}
		for _, recordType := range recordTypes(t, params) {
			got[recordType]++
		}
		if len(got) != len(want) {
			t.Fatalf("record types: got %v, want %v", got, want)
		}
		for recordType, count := range want {
			if got[recordType] != count {
				t.Fatalf("record types: got %v, want %v", got, want)
			}
		}
	}

	// No kind parameter returns the full union.
	assertTypes(t, RecordQuery{}, map[string]int{
		"routine": 1, "business": 1,
	})

	// A single kind narrows to that source.
	businessOnly := []string{"business"}
	assertTypes(t, RecordQuery{Kind: &businessOnly}, map[string]int{"business": 1})

	// Repeated kinds return the union of the requested sources.
	businessAndRoutine := []string{"business", "routine"}
	assertTypes(t, RecordQuery{Kind: &businessAndRoutine}, map[string]int{"routine": 1, "business": 1})
}

// TestListRecordsPaginationIsStrictAcrossKindTies proves the union keyset is a
// strict total order: rows from different sources sharing one record_time and
// one id used to collide on the old (record_time, id) cursor, which skipped or
// repeated rows across pages. The (record_time, source_rank, id) cursor must
// visit every row exactly once in deterministic rank order.
func TestListRecordsPaginationIsStrictAcrossKindTies(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed tie fixture: %v", err)
		}
	}
	exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-ties', 'Ties Anime')`)
	exec(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
		VALUES ('subscription-ties', 'anime-ties', 'custom', 'ties', 'active')`)
	// One identical (record_time, id) triple across three sources.
	tieTime := time.Date(2026, 9, 12, 8, 30, 0, 0, time.UTC)
	exec(`INSERT INTO routine_activities(
		id, kind, scope_kind, scope_id, managed_anime_id, outcome, last_executed_at, last_result_code, summary
	) VALUES ('tie-break', 'rss_refresh', 'subscription', 'subscription-ties',
		'anime-ties', 'success', $1, 'success', 'routine tie')`, tieTime)
	// retention_until is explicit: its column default is CURRENT_TIMESTAMP + 30
	// days, and the table checks retention_until <= occurred_at + 30 days, so a
	// fixed occurred_at in the past would violate the check once the wall clock
	// passes it.
	exec(`INSERT INTO subscription_log_entries(
		id, subscription_id, managed_anime_id, kind, outcome, occurred_at, message_code, message_params, dedupe_key, retention_until
	) VALUES ('tie-break', 'subscription-ties', 'anime-ties', 'rss_new_content', 'info',
		$1, 'rss_new_content', '{"count":1}', 'tie-business', $1::timestamptz + INTERVAL '30 days')`, tieTime)
	exec(`INSERT INTO automation_dispatch_runs(
		id, kind, trigger, state, target_count, enqueued_count, created_at, started_at, finished_at, retention_until
	) VALUES ('tie-break', 'rss_refresh', 'scheduled', 'completed', 1, 1, $1, $1, $1, $1::timestamptz + INTERVAL '1 day')`, tieTime)

	service, err := NewService(db)
	if err != nil {
		t.Fatalf("new observability service: %v", err)
	}

	collect := func(t *testing.T) []string {
		t.Helper()
		var order []string
		seen := map[string]int{}
		cursor := ""
		pages := 0
		for {
			pages++
			if pages > 10 {
				t.Fatalf("pagination did not terminate after 10 pages: %v", order)
			}
			params := RecordQuery{Limit: intPtr(1)}
			if cursor != "" {
				params.Cursor = &cursor
			}
			page, err := service.ListRecords(ctx, params)
			if err != nil {
				t.Fatalf("list records page %d: %v", pages, err)
			}
			if len(page.Records) == 0 {
				if page.NextCursor != nil {
					t.Fatalf("empty page must not carry a next cursor")
				}
				break
			}
			for _, record := range page.Records {
				var shape struct {
					RecordType string `json:"record_type"`
				}
				if err := json.Unmarshal(record.Raw, &shape); err != nil {
					t.Fatalf("decode record union: %v", err)
				}
				order = append(order, shape.RecordType)
				seen[shape.RecordType]++
			}
			if page.NextCursor == nil {
				break
			}
			cursor = *page.NextCursor
		}
		for recordType, count := range seen {
			if count != 1 {
				t.Fatalf("record type %q visited %d times, want exactly once (order %v)", recordType, count, order)
			}
		}
		return order
	}

	first := collect(t)
	second := collect(t)
	// Rank order is deterministic: automation (3) before business (2) before
	// routine (1) at an identical record_time.
	want := []string{"automation", "business", "routine"}
	if len(first) != len(want) {
		t.Fatalf("visited %d rows, want %d: %v", len(first), len(want), first)
	}
	for i := range want {
		if first[i] != want[i] {
			t.Fatalf("tie order = %v, want %v", first, want)
		}
	}
	if fmt.Sprint(first) != fmt.Sprint(second) {
		t.Fatalf("replay changed the visit order: %v vs %v", first, second)
	}
}

func intPtr(value int) *int { return &value }
