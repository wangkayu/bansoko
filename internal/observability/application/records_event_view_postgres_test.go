package application

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The primary event view is a read projection of the business source: it hides
// exactly the repeated release filtering process (release_processed, info,
// awaiting_competition or no_placeable_files), and the filter runs before
// pagination so a page of primary records is not pushed behind noise.

type eventViewFixture struct {
	db      *sql.DB
	service *Service
}

// entry inserts one business journal entry; an empty reason stores NULL.
func (f eventViewFixture) entry(t *testing.T, id, kind, outcome, code, reason string, occurred time.Time) {
	t.Helper()
	var reasonCode any
	if reason != "" {
		reasonCode = reason
	}
	if _, err := f.db.ExecContext(context.Background(), `
INSERT INTO subscription_log_entries(
    id, subscription_id, managed_anime_id, kind, outcome, message_code, message_params,
    release_id, details, dedupe_key, reason_code, occurred_at, retention_until
) VALUES ($1, 'sub-ev', 'anime-ev', $2, $3, $4, '{}'::jsonb, NULL, '{}'::jsonb, $1, $5, $6,
          $6::timestamptz + INTERVAL '30 days')`,
		id, kind, outcome, code, reasonCode, occurred); err != nil {
		t.Fatalf("seed event view entry %s: %v", id, err)
	}
}

func seedEventViewFixture(t *testing.T) eventViewFixture {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed event view fixture: %v\n%s", err, query)
		}
	}
	exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-ev', 'Event View Anime')`)
	exec(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
		VALUES ('sub-ev', 'anime-ev', 'custom', 'event-view', 'active')`)
	exec(`INSERT INTO plugin_target_configs(id, name, plugin_kind, adapter_kind, enabled, trigger_kinds, config_schema_version, config, template_set)
		VALUES ('target-ev', 'Event View Webhook', 'notification', 'webhook', FALSE,
			'["download_failed"]'::jsonb, 1, '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, '{}'::jsonb)`)
	exec(`INSERT INTO plugin_deliveries(
		id, plugin_target_config_id, plugin_target_config_version, adapter_kind, adapter_config_snapshot,
		delivery_kind, trigger_kind, source_owner_type, source_owner_id, source_owner_version,
		source_result_key, managed_anime_id, state, idempotency_key, payload_schema_version, payload, payload_digest,
		template_language_version, template_catalog_version, title_template_snapshot,
		body_template_snapshot, target_display, attempt_count, delivered_at, created_at, updated_at, retention_until)
		VALUES ('delivery-ev', 'target-ev', 1, 'webhook', '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb,
			'business', 'download_failed', 'acquisition', 'delivery-ev', 1, 'delivery-ev', NULL, 'delivered', 'delivery-ev', 1, '{}'::jsonb,
			'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'bansoko-template-1', 'notification-catalog-v1', 'title', 'body', 'Event View Webhook', 1,
			CURRENT_TIMESTAMP - INTERVAL '1 day', CURRENT_TIMESTAMP - INTERVAL '1 day', CURRENT_TIMESTAMP - INTERVAL '1 day',
			CURRENT_TIMESTAMP + INTERVAL '29 days')`)

	service, err := NewService(db)
	if err != nil {
		t.Fatalf("new observability service: %v", err)
	}
	return eventViewFixture{db: db, service: service}
}

func listEventViewIDs(t *testing.T, fixture eventViewFixture, query RecordQuery) string {
	t.Helper()
	limit := 200
	query.Limit = &limit
	page, err := fixture.service.ListRecords(context.Background(), query)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if page.NextCursor != nil {
		t.Fatalf("fixture does not fit one page")
	}
	return recordIDs(recordsToFilterRecords(t, page.Records))
}

// TestListRecordsPrimaryEventViewExactExclusion proves only the exact set is
// hidden: the two reasons on release_processed info rows. Another reason, a
// NULL reason, a failure carrying the same reason, another kind, another
// outcome and the plugin source all stay visible.
func TestListRecordsPrimaryEventViewExactExclusion(t *testing.T) {
	fixture := seedEventViewFixture(t)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	at := func(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }

	fixture.entry(t, "noise-awaiting", "release_processed", "info", "release_awaiting_competition", "awaiting_competition", at(0))
	fixture.entry(t, "noise-no-placeable", "release_processed", "info", "release_filtered", "no_placeable_files", at(1))
	fixture.entry(t, "other-reason", "release_processed", "info", "release_parse_failed", "parse_failed", at(2))
	fixture.entry(t, "no-reason", "release_processed", "info", "release_accepted", "", at(3))
	fixture.entry(t, "failure-same-reason", "release_processed", "failure", "release_filtered", "no_placeable_files", at(4))
	fixture.entry(t, "other-kind", "subscription_created", "info", "subscription_created", "no_placeable_files", at(5))
	fixture.entry(t, "success-same-reason", "release_processed", "success", "release_accepted", "awaiting_competition", at(6))
	fixture.entry(t, "awaiting-other-code", "release_processed", "info", "release_accepted", "awaiting_competition", at(7))

	// The API default (nil) and all return the same records.
	defaultIDs := listEventViewIDs(t, fixture, RecordQuery{})
	allIDs := listEventViewIDs(t, fixture, RecordQuery{EventView: textParam(RecordEventViewAll)})
	if defaultIDs != allIDs {
		t.Fatalf("default view differs from all:\n default %s\n     all %s", defaultIDs, allIDs)
	}

	wantPrimary := expectIDs(
		"plugin:delivery-ev", "business:other-reason", "business:no-reason",
		"business:failure-same-reason", "business:other-kind", "business:success-same-reason",
		"business:awaiting-other-code",
	)
	if got := listEventViewIDs(t, fixture, RecordQuery{EventView: textParam(RecordEventViewPrimary)}); got != wantPrimary {
		t.Fatalf("primary records\n got %s\nwant %s", got, wantPrimary)
	}

	// entry_kind and the view intersect: narrowing to release_processed still
	// hides the two rows, while a plugin-only query is unaffected.
	releaseKinds := []string{"release_processed"}
	got := listEventViewIDs(t, fixture, RecordQuery{EventView: textParam(RecordEventViewPrimary), EntryKind: &releaseKinds})
	want := expectIDs("business:other-reason", "business:no-reason", "business:failure-same-reason",
		"business:success-same-reason", "business:awaiting-other-code")
	if got != want {
		t.Fatalf("primary with entry_kind\n got %s\nwant %s", got, want)
	}
	pluginKinds := []string{"plugin"}
	got = listEventViewIDs(t, fixture, RecordQuery{EventView: textParam(RecordEventViewPrimary), Kind: &pluginKinds})
	if want := expectIDs("plugin:delivery-ev"); got != want {
		t.Fatalf("primary with kind=plugin\n got %s\nwant %s", got, want)
	}
}

// TestListRecordsPrimaryEventViewPaginatesAfterFilter proves the predicate
// runs before each source's LIMIT: the newest rows are all noise, so the first
// primary page already returns the older primary events; paging both views
// neither repeats nor drops a record.
func TestListRecordsPrimaryEventViewPaginatesAfterFilter(t *testing.T) {
	fixture := seedEventViewFixture(t)
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	at := func(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }

	// 25 primary events, then 60 noise rows newer than all of them.
	for i := 0; i < 25; i++ {
		fixture.entry(t, fmt.Sprintf("main-%02d", i), "subscription_created", "info", "subscription_created", "", at(i))
	}
	for i := 0; i < 60; i++ {
		reason := "awaiting_competition"
		if i%2 == 0 {
			reason = "no_placeable_files"
		}
		fixture.entry(t, fmt.Sprintf("noise-%02d", i), "release_processed", "info", "release_filtered", reason, at(100+i))
	}

	limit := 20
	primary := RecordEventViewPrimary
	page, err := fixture.service.ListRecords(context.Background(), RecordQuery{EventView: &primary, Limit: &limit})
	if err != nil {
		t.Fatalf("primary first page: %v", err)
	}
	first := recordsToFilterRecords(t, page.Records)
	if len(first) != limit {
		t.Fatalf("primary first page holds %d records, want %d", len(first), limit)
	}
	for _, record := range first {
		// The plugin delivery sits a day back and never reaches the first page;
		// a noise row would mean the filter ran after the page was cut.
		if record.recordType != "business" || !strings.HasPrefix(record.id, "main-") {
			t.Fatalf("primary first page leaked %s:%s", record.recordType, record.id)
		}
	}
	if page.NextCursor == nil {
		t.Fatal("25 primary records need a second page")
	}

	// The primary view continues from its own cursor without repeats or gaps.
	primarySeen := map[string]bool{}
	cursor := page.NextCursor
	for _, record := range first {
		primarySeen[record.recordType+":"+record.id] = true
	}
	for pages := 0; cursor != nil; pages++ {
		if pages > 5 {
			t.Fatal("primary view did not terminate")
		}
		next, err := fixture.service.ListRecords(context.Background(), RecordQuery{EventView: &primary, Limit: &limit, Cursor: cursor})
		if err != nil {
			t.Fatalf("primary view page %d: %v", pages+1, err)
		}
		for _, record := range recordsToFilterRecords(t, next.Records) {
			key := record.recordType + ":" + record.id
			if primarySeen[key] {
				t.Fatalf("primary view repeated %s", key)
			}
			primarySeen[key] = true
		}
		cursor = next.NextCursor
	}
	if len(primarySeen) != 26 { // 25 primary events + the plugin delivery
		t.Fatalf("primary view saw %d records, want 26", len(primarySeen))
	}

	// The all view pages the whole history without repeats or gaps.
	allView := RecordEventViewAll
	seen := map[string]bool{}
	cursor = nil
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("all view did not terminate")
		}
		allPage, err := fixture.service.ListRecords(context.Background(), RecordQuery{EventView: &allView, Limit: &limit, Cursor: cursor})
		if err != nil {
			t.Fatalf("all view page %d: %v", pages, err)
		}
		for _, record := range recordsToFilterRecords(t, allPage.Records) {
			key := record.recordType + ":" + record.id
			if seen[key] {
				t.Fatalf("all view repeated %s", key)
			}
			seen[key] = true
		}
		cursor = allPage.NextCursor
		if cursor == nil {
			break
		}
	}
	if len(seen) != 86 { // 25 primary + 60 noise + 1 plugin delivery
		t.Fatalf("all view saw %d records, want 86", len(seen))
	}
}

// TestListRecordsPrimaryEventViewCombinesWithState proves the view and the
// existing filters compose: a failure that shares a hidden reason still appears
// under the failure state in the primary view.
func TestListRecordsPrimaryEventViewCombinesWithState(t *testing.T) {
	fixture := seedEventViewFixture(t)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	fixture.entry(t, "noise-awaiting", "release_processed", "info", "release_awaiting_competition", "awaiting_competition", base)
	fixture.entry(t, "failure-shown", "release_processed", "failure", "release_filtered", "no_placeable_files", base.Add(time.Minute))

	primary := RecordEventViewPrimary
	state := RecordStateFailure
	got := listEventViewIDs(t, fixture, RecordQuery{EventView: &primary, State: &state})
	if want := expectIDs("business:failure-shown"); got != want {
		t.Fatalf("primary with state=failure\n got %s\nwant %s", got, want)
	}
}
