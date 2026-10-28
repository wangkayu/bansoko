package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

// The records state and search filters select whole Release runs: an entry
// of a run takes the state of the settlement that closes it, and a search
// matching any entry of a run selects every entry of that run.

type filterRecord struct {
	recordType, id, source string
}

type filterFixture struct {
	db      *sql.DB
	service *Service
}

func seedRecordFilterFixture(t *testing.T) filterFixture {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed records filter fixture: %v\n%s", err, query)
		}
	}
	exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-alpha', 'Alpha Saga')`)
	exec(`INSERT INTO managed_anime(id, title, title_override) VALUES ('anime-beta', 'Beta Original', 'Beta Override')`)
	exec(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
		VALUES ('sub-alpha', 'anime-alpha', 'custom', 'alpha', 'active')`)
	exec(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
		VALUES ('sub-beta', 'anime-beta', 'oneshot', 'beta one-shot', 'stopped')`)
	exec(`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
		VALUES ('src-alpha', 'sub-alpha', 'custom_rss', 'https://feeds.example.test/alpha.xml', 'https://feeds.example.test/alpha.xml', 'GrpX', 'Alpha Saga'),
		       ('src-beta', 'sub-beta', 'custom_rss', 'https://feeds.example.test/beta.xml', 'https://feeds.example.test/beta.xml', 'GrpY', 'Beta Original')`)
	exec(`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
		VALUES ('rel-1', 'src-alpha', 'rel-1', '{}', '{}', 'discovered', 'discovered'),
		       ('rel-2', 'src-beta', 'rel-2', '{}', '{}', 'discovered', 'discovered')`)

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	at := func(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }
	entry := func(id, subscription, anime, kind, outcome, code, release, details string, occurred time.Time) {
		t.Helper()
		var releaseID, detailsJSON any
		if release != "" {
			releaseID = release
		}
		if details != "" {
			detailsJSON = details
		}
		exec(`INSERT INTO subscription_log_entries(
			id, subscription_id, managed_anime_id, kind, outcome, occurred_at, message_code, message_params,
			release_id, details, dedupe_key, retention_until
		) VALUES ($1, $2, $3, $4, $5, $6, $7, '{}'::jsonb, $8, $9::jsonb, $1, $6::timestamptz + INTERVAL '30 days')`,
			id, subscription, anime, kind, outcome, occurred, code, releaseID, detailsJSON)
	}

	// Release rel-1: a failed run, then the retried run that succeeded.
	entry("r1-download", "sub-alpha", "anime-alpha", "download_changed", "info", "download_submitted", "rel-1",
		`{"release_title":"[GrpX] Alpha Saga - 01"}`, at(0))
	entry("r1-import-failed", "sub-alpha", "anime-alpha", "file_alignment_failed", "failure", "file_import_failed", "rel-1",
		`{"file_name":"alpha-ep01-broken.mkv"}`, at(1))
	entry("r1-not-replaced", "sub-alpha", "anime-alpha", "replacement_decided", "info", "replacement_not_replaced", "rel-1",
		`{"reason_code":"import_failed"}`, at(2))
	entry("r1-settled", "sub-alpha", "anime-alpha", "acquisition_settled", "failure", "acquisition_failed", "rel-1", "", at(2))
	entry("r1b-download", "sub-alpha", "anime-alpha", "download_changed", "info", "download_submitted", "rel-1",
		`{"release_title":"[GrpX] Alpha Saga - 01"}`, at(3))
	entry("r1b-imported", "sub-alpha", "anime-alpha", "file_alignment_completed", "success", "file_imported", "rel-1",
		`{"file_name":"alpha-ep01.mkv"}`, at(4))
	entry("r1b-settled", "sub-alpha", "anime-alpha", "acquisition_settled", "success", "acquisition_succeeded", "rel-1", "", at(5))
	// Release rel-2 of the one-shot: an open run.
	entry("r2-release", "sub-beta", "anime-beta", "release_processed", "info", "release_accepted", "rel-2",
		`{"release_title":"[GrpY] Beta Special"}`, at(6))
	entry("r2-download", "sub-beta", "anime-beta", "download_changed", "info", "download_submitted", "rel-2",
		`{"release_title":"[GrpY] Beta Special"}`, at(7))
	// Entries outside any run carry their own outcome.
	entry("n-created", "sub-alpha", "anime-alpha", "subscription_created", "info", "subscription_created", "", "", at(-10))
	entry("n-cleanup-failed", "sub-alpha", "anime-alpha", "cleanup_failed", "failure", "old_version_delete_failed", "",
		`{"file_name":"old-alpha.mkv"}`, at(8))

	exec(`INSERT INTO plugin_target_configs(id, name, plugin_kind, adapter_kind, enabled, trigger_kinds, config_schema_version, config, template_set)
		VALUES ('target-ops', 'Ops Webhook', 'notification', 'webhook', FALSE,
			'["download_failed"]'::jsonb, 1, '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, '{}'::jsonb)`)
	delivery := func(id, state, anime string, created time.Time) {
		t.Helper()
		var animeID, deliveredAt, errorCode any
		if anime != "" {
			animeID = anime
		}
		switch state {
		case "delivered":
			deliveredAt = created
		case "failed", "unknown":
			errorCode = "NETWORK_ERROR"
		}
		exec(`INSERT INTO plugin_deliveries(
			id, plugin_target_config_id, plugin_target_config_version, adapter_kind, adapter_config_snapshot,
			delivery_kind, trigger_kind, source_owner_type, source_owner_id, source_owner_version,
			source_result_key, managed_anime_id, state, idempotency_key, payload_schema_version, payload, payload_digest,
			template_language_version, template_catalog_version, title_template_snapshot,
			body_template_snapshot, target_display, attempt_count, last_error_code, delivered_at,
			created_at, updated_at, retention_until)
		VALUES ($1, 'target-ops', 1, 'webhook', '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb,
			'business', 'download_failed', 'acquisition', $1, 1, $1, $2, $3, $1, 1, '{}'::jsonb,
			'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'bansoko-template-1', 'notification-catalog-v1', 'title', 'body', 'Ops Webhook', 1, $4, $5,
			$6, $6, CURRENT_TIMESTAMP + INTERVAL '30 days')`,
			id, animeID, state, errorCode, deliveredAt, created)
	}
	delivery("delivery-unknown", "unknown", "", at(9))
	delivery("delivery-failed", "failed", "", at(10))
	delivery("delivery-ok", "delivered", "anime-beta", at(11))

	service, err := NewService(db)
	if err != nil {
		t.Fatalf("new observability service: %v", err)
	}
	return filterFixture{db: db, service: service}
}

func (f filterFixture) list(t *testing.T, query RecordQuery) []filterRecord {
	t.Helper()
	limit := 200
	query.Limit = &limit
	page, err := f.service.ListRecords(context.Background(), query)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if page.NextCursor != nil {
		t.Fatalf("fixture does not fit one page")
	}
	envelopes := map[string]string{"business": "entry", "plugin": "delivery", "automation": "run", "routine": "routine"}
	out := []filterRecord{}
	for _, record := range page.Records {
		var shape map[string]json.RawMessage
		if err := json.Unmarshal(record.Raw, &shape); err != nil {
			t.Fatalf("decode record: %v", err)
		}
		var recordType string
		if err := json.Unmarshal(shape["record_type"], &recordType); err != nil {
			t.Fatalf("decode record_type: %v", err)
		}
		var payload struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		}
		if err := json.Unmarshal(shape[envelopes[recordType]], &payload); err != nil {
			t.Fatalf("decode %s payload: %v", recordType, err)
		}
		out = append(out, filterRecord{recordType: recordType, id: payload.ID, source: payload.Source})
	}
	return out
}

func recordIDs(records []filterRecord) string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.recordType+":"+record.id)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func expectIDs(ids ...string) string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

func textParam(value string) *string { return &value }

func TestListRecordsStateSelectsWholeRuns(t *testing.T) {
	fixture := seedRecordFilterFixture(t)
	cases := []struct {
		state string
		want  string
	}{
		// The failed run as a whole, including its replacement line and the
		// step that carried no failure itself; unknown deliveries are failures.
		{RecordStateFailure, expectIDs("business:r1-download", "business:r1-import-failed", "business:r1-not-replaced",
			"business:r1-settled", "business:n-cleanup-failed", "plugin:delivery-unknown", "plugin:delivery-failed")},
		{RecordStateSuccess, expectIDs("business:r1b-download", "business:r1b-imported", "business:r1b-settled", "plugin:delivery-ok")},
		// The open run of rel-2; its release_processed entry is not a run step.
		{RecordStateInProgress, expectIDs("business:r2-download")},
		{RecordStateInfo, expectIDs("business:n-created", "business:r2-release")},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			if got := recordIDs(fixture.list(t, RecordQuery{State: textParam(tc.state)})); got != tc.want {
				t.Fatalf("state=%s records\n got %s\nwant %s", tc.state, got, tc.want)
			}
		})
	}
}

func TestListRecordsSearchSelectsWholeRuns(t *testing.T) {
	fixture := seedRecordFilterFixture(t)
	failedRun := expectIDs("business:r1-download", "business:r1-import-failed", "business:r1-not-replaced", "business:r1-settled")
	cases := []struct {
		name  string
		query RecordQuery
		want  string
	}{
		{"file name of a failed step", RecordQuery{Q: textParam("BROKEN")}, failedRun},
		{"file name of the retried run", RecordQuery{Q: textParam("alpha-ep01.mkv")},
			expectIDs("business:r1b-download", "business:r1b-imported", "business:r1b-settled")},
		{"release title shared by both runs", RecordQuery{Q: textParam("[GrpX]")},
			expectIDs("business:r1-download", "business:r1-import-failed", "business:r1-not-replaced", "business:r1-settled",
				"business:r1b-download", "business:r1b-imported", "business:r1b-settled")},
		{"anime title override", RecordQuery{Q: textParam("beta override")},
			expectIDs("business:r2-release", "business:r2-download", "plugin:delivery-ok")},
		{"anime title under its override", RecordQuery{Q: textParam("Beta Original")},
			expectIDs("business:r2-release", "business:r2-download", "plugin:delivery-ok")},
		{"notification target name", RecordQuery{Q: textParam("ops webhook")},
			expectIDs("plugin:delivery-unknown", "plugin:delivery-failed", "plugin:delivery-ok")},
		{"LIKE wildcards are literal", RecordQuery{Q: textParam("alpha%01")}, ""},
		{"state and search together", RecordQuery{State: textParam(RecordStateFailure), Q: textParam("alpha-ep01")}, failedRun},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := recordIDs(fixture.list(t, tc.query)); got != tc.want {
				t.Fatalf("records\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// The navigation badge reads failed settlements and failed or unconfirmed
// deliveries.
func TestListRecordsBadgeQuery(t *testing.T) {
	fixture := seedRecordFilterFixture(t)
	kinds := []string{"business", "plugin"}
	entryKinds := []string{"acquisition_settled"}
	got := recordIDs(fixture.list(t, RecordQuery{Kind: &kinds, EntryKind: &entryKinds, State: textParam(RecordStateFailure)}))
	if want := expectIDs("business:r1-settled", "plugin:delivery-unknown", "plugin:delivery-failed"); got != want {
		t.Fatalf("badge records\n got %s\nwant %s", got, want)
	}
}

func TestListRecordsBusinessEntriesCarryTheirSource(t *testing.T) {
	fixture := seedRecordFilterFixture(t)
	business := []string{"business"}
	records := fixture.list(t, RecordQuery{Kind: &business})
	if len(records) != 11 {
		t.Fatalf("business records = %d, want 11: %s", len(records), recordIDs(records))
	}
	for _, record := range records {
		want := "subscription"
		if strings.HasPrefix(record.id, "r2-") {
			want = "one_shot"
		}
		if record.source != want {
			t.Errorf("%s source = %q, want %q", record.id, record.source, want)
		}
	}
}
