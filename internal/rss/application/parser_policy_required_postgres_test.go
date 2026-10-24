package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"bansoko/internal/acquisition/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// TestReleaseEvaluateRequiresInstanceParserPolicyPostgres deletes the instance
// parser policy of a live release and asserts the evaluation fails closed with
// the typed reason: the selected parser never runs, no Release verdict is
// written, and no download task, media snapshot or acquisition target appears.
func TestReleaseEvaluateRequiresInstanceParserPolicyPostgres(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	ctx := context.Background()
	const kind = "test-policy-required-release-parser"
	adapter := registerRecordingParser(t, kind, "42")
	seedInstanceParserPolicyKind(t, db, "anime-rss-test", kind)
	insertReleaseForEvaluation(t, db, sourceID, "release-policy-missing", "[Group] Test Show - 07 1080p", "https://downloads.example.test/show-07.torrent")
	if _, err := db.ExecContext(ctx, `UPDATE anime_business_configs SET parser_kind = NULL, parser_source_default_version = NULL, parser_version = NULL WHERE managed_anime_id = 'anime-rss-test'`); err != nil {
		t.Fatal(err)
	}

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-policy-missing"}, workitems.KindReleaseEvaluate, 1)
	outcome, err := service.Handle(ctx, claim)
	if err != nil {
		t.Fatalf("release evaluation returned an error instead of a typed failure: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("outcome=%+v, want a terminal failed finish", outcome)
	}
	if outcome.OwnerResult.ReasonCode != "parser_policy_required" {
		t.Fatalf("reason code=%q, want parser_policy_required", outcome.OwnerResult.ReasonCode)
	}
	if outcome.WriteOwner != nil {
		t.Fatalf("a missing parser policy must not write owner state")
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}

	var verdict, reasonCode string
	var parsedFacts []byte
	var version int64
	if err := db.QueryRowContext(ctx, `
SELECT verdict, reason_code, parsed_facts, version FROM releases WHERE id = 'release-policy-missing'`).
		Scan(&verdict, &reasonCode, &parsedFacts, &version); err != nil {
		t.Fatal(err)
	}
	if verdict != "discovered" || reasonCode != "discovered" || version != 1 || len(parsedFacts) != 0 {
		t.Fatalf("release mutated: verdict=%q reason=%q version=%d parsed=%s", verdict, reasonCode, version, parsedFacts)
	}
	for _, table := range []string{"download_tasks", "media_fact_snapshots", "acquisition_targets"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE release_id = 'release-policy-missing'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s has %d rows for a parser-policy-less release, want 0", table, count)
		}
	}
	if inputs := adapter.parsedInputs(); len(inputs) != 0 {
		t.Fatalf("the selected parser ran without its policy: %v", inputs)
	}
}

// TestRefreshRequiresInstanceParserPolicyPostgres is the refresh counterpart:
// the source runtime is not reloaded as ready, no release is created, and the
// selected parser never runs.
func TestRefreshRequiresInstanceParserPolicyPostgres(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	ctx := context.Background()
	const kind = "test-policy-required-refresh-parser"
	adapter := registerRecordingParser(t, kind, "42")
	seedInstanceParserPolicyKind(t, db, "anime-rss-test", kind)
	if _, err := db.ExecContext(ctx, `UPDATE anime_business_configs SET parser_kind = NULL, parser_source_default_version = NULL, parser_version = NULL WHERE managed_anime_id = 'anime-rss-test'`); err != nil {
		t.Fatal(err)
	}

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &rssFakeSourceFetcher{items: []domain.FeedItem{{GUID: "guid-policy-missing", Title: "[Group] Test Show - 07 1080p"}}}
	service, err := newRSSService(t, db, store, fake)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner: workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID},
		Kind:  workitems.KindRSSRefresh, IdempotencyKey: "rss_refresh:parser-policy-required",
		InputSnapshot: json.RawMessage(`{"trigger":"test"}`), MaxAttempts: 3, ExpectedOwnerVersion: 1,
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "rss-policy-required-worker", []string{workitems.KindRSSRefresh}, time.Minute)
	if err != nil || claim.ID != id {
		t.Fatalf("claim refresh: id=%s claim=%+v err=%v", id, claim, err)
	}
	outcome, err := service.Handle(ctx, claim)
	if err != nil {
		t.Fatalf("refresh returned an error instead of a typed failure: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("outcome=%+v, want a terminal failed finish", outcome)
	}
	if outcome.OwnerResult.ReasonCode != "parser_policy_required" {
		t.Fatalf("reason code=%q, want parser_policy_required", outcome.OwnerResult.ReasonCode)
	}
	if outcome.WriteOwner != nil {
		t.Fatalf("a missing parser policy must not write the source runtime")
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}

	var health string
	var version int64
	var lastRefreshAt *time.Time
	if err := db.QueryRowContext(ctx, `
SELECT health, version, last_refresh_at FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).
		Scan(&health, &version, &lastRefreshAt); err != nil {
		t.Fatal(err)
	}
	if health != "unknown" || version != 1 || lastRefreshAt != nil {
		t.Fatalf("source runtime mutated: health=%q version=%d last_refresh_at=%v", health, version, lastRefreshAt)
	}
	var releases int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	if releases != 0 {
		t.Fatalf("refresh created %d releases without a parser policy, want 0", releases)
	}
	if inputs := adapter.parsedInputs(); len(inputs) != 0 {
		t.Fatalf("the selected parser ran without its policy: %v", inputs)
	}
}
