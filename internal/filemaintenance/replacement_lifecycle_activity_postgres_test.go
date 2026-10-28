package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/observability/businesslog"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

// The replacement lifecycle in the journal: a replace decision writes nothing
// when it is frozen and exactly one line when it ends (replaced at
// settlement, or not_replaced when it never takes effect); keep, retain,
// queue and blocked verdicts are final when frozen and write their line then.

type lifecycleLine struct {
	subject, reason, message, cause, dedupe string
}

func lifecycleLines(t *testing.T, db *sql.DB) []lifecycleLine {
	t.Helper()
	rows, err := db.Query(`
SELECT COALESCE(subject_id,''), COALESCE(reason_code,''), message_code, COALESCE(details->>'reason_code',''), dedupe_key
FROM subscription_log_entries WHERE kind='replacement_decided' ORDER BY occurred_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []lifecycleLine
	for rows.Next() {
		var line lifecycleLine
		if err := rows.Scan(&line.subject, &line.reason, &line.message, &line.cause, &line.dedupe); err != nil {
			t.Fatal(err)
		}
		out = append(out, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func lifecycleService(t *testing.T, db *sql.DB) (*ReplacementDecisionService, *observabilityapplication.Service) {
	t.Helper()
	writer, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(NewCleanupService(db))
	service.SetActivityWriter(writer)
	return service, writer
}

func supersededLine(decisionID string) lifecycleLine {
	return lifecycleLine{decisionID, "not_replaced", "replacement_not_replaced", "superseded", "replacement:" + decisionID + ":not_replaced"}
}

func TestReplaceDecisionLogsOnlyReplacedAtSettlement(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:s1:e1"},
		Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")
	service, _ := lifecycleService(t, db)

	views, err := service.EvaluateApplication(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"})
	if err != nil || len(views) != 1 || views[0].Verdict != "replace" || views[0].State != "admitted" {
		t.Fatalf("evaluate = %+v, %v; want one admitted replace decision", views, err)
	}
	if got := lifecycleLines(t, db); len(got) != 0 {
		t.Fatalf("a frozen replace decision wrote %+v; it writes nothing until it ends", got)
	}
	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	want := lifecycleLine{views[0].ID, "replaced", "replacement_replaced", "", ""}
	got := lifecycleLines(t, db)
	if len(got) != 1 {
		t.Fatalf("replacement lines after settle = %+v, want one replaced", got)
	}
	got[0].dedupe = ""
	if got[0] != want {
		t.Fatalf("replacement line = %+v, want %+v", got[0], want)
	}
	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if again := lifecycleLines(t, db); len(again) != 1 {
		t.Fatalf("a repeated settle wrote more lines: %+v", again)
	}
}

func TestFinalVerdictLogsItsLineWhenFrozen(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:s1:e1"},
		Facts{Resolution: "1080p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-08-01T00:00:00Z")
	service, _ := lifecycleService(t, db)

	views, err := service.EvaluateApplication(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"})
	if err != nil || len(views) != 1 || views[0].Verdict == "replace" {
		t.Fatalf("evaluate = %+v, %v; want one final (non-replace) decision for a worse candidate", views, err)
	}
	message, final := admissionReplacementMessage(views[0].Verdict)
	if !final {
		t.Fatalf("verdict %q is not final at admission", views[0].Verdict)
	}
	got := lifecycleLines(t, db)
	if len(got) != 1 || got[0].subject != views[0].ID || got[0].message != string(message) || got[0].reason != views[0].Verdict {
		t.Fatalf("replacement lines = %+v, want the %s line of %s", got, message, views[0].ID)
	}
}

func TestStaleAtSettlementLogsOneSuperseded(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	service := settleStaleFixture(t, db)
	writer, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service.SetActivityWriter(writer)
	if _, err := db.Exec(`UPDATE subscriptions SET version = version + 1, updated_at = CURRENT_TIMESTAMP WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}
	if err := service.SettleDecision(ctx, "dec-frozen"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	assertStaleWithZeroEffects(t, db, staleSubscriptionVersionChanged, "active", "active", 1, 1)
	if got := lifecycleLines(t, db); len(got) != 1 || got[0] != supersededLine("dec-frozen") {
		t.Fatalf("replacement lines = %+v, want [%+v]", got, supersededLine("dec-frozen"))
	}
	if err := service.SettleDecision(ctx, "dec-frozen"); err != nil {
		t.Fatalf("settle again: %v", err)
	}
	if got := lifecycleLines(t, db); len(got) != 1 {
		t.Fatalf("a repeated settle of a stale decision wrote more lines: %+v", got)
	}
}

// A replace decision without frozen winner evidence is staled by markStale in
// its own transaction, which writes the superseded line with it.
func TestLegacyReplaceWithoutWinnerLogsSuperseded(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedPlacement(t, db, "plc-winner", "anime-1", "/library/W/EP01.mkv", "fp-w", []string{"tv:s1:e1"}, "2026-07-01T00:00:00Z")
	seedPlacement(t, db, "plc-loser", "anime-1", "/library/L/EP01.mkv", "fp-l", []string{"tv:s1:e1"}, "2026-08-01T00:00:00Z")
	seedFrozenDecision(t, db, "dec-legacy", "anime-1", "plc-winner", 1, 1, ModeSingleCopy, nil,
		[]PlacementRef{{PlacementID: "plc-loser", Version: 1, Fingerprint: "fp-l"}}, "admitted")
	service, _ := lifecycleService(t, db)
	if err := service.SettleDecision(ctx, "dec-legacy"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	var state, reason string
	if err := db.QueryRow(`SELECT state, COALESCE(stale_reason,'') FROM replacement_decisions WHERE id = 'dec-legacy'`).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "stale" || reason != staleLegacyEvidenceIncomplete {
		t.Fatalf("legacy decision %s/%s, want stale/%s", state, reason, staleLegacyEvidenceIncomplete)
	}
	if got := lifecycleLines(t, db); len(got) != 1 || got[0] != supersededLine("dec-legacy") {
		t.Fatalf("replacement lines = %+v, want [%+v]", got, supersededLine("dec-legacy"))
	}
}

// A candidate whose first acquisition failed has its planned decision's
// not_replaced line; when the retried acquisition places the file, the fresh
// decision it froze settles and writes replaced. The two lines name different
// decisions and neither key blocks the other.
func TestRetriedCandidateLogsReplacedAfterNotReplaced(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	owner := disposabledb.Owner(t)
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:s1:e1"},
		Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")
	service, writer := lifecycleService(t, db)

	// The first acquisition's planned decision, staled when it failed, with
	// the line the settlement consumer writes for it.
	pools, _ := json.Marshal([]any{map[string]any{"pool_key": "tv:s1:e1", "verdict": "replace", "winner": map[string]any{"placement_id": "release:release-new", "version": 1, "fingerprint": "candidate"}}})
	if _, err := owner.ExecContext(ctx, `
INSERT INTO replacement_decisions(id,idempotency_key,managed_anime_id,candidate_release_id,policy_version,profile_version,candidate_facts_ref,coverage_key,coverage_keys,admission_mode,pools,verdict,state,stale_reason,subscription_version,input_digest,version)
VALUES ('dec-first','dec-first','anime-1','release-new',1,'profile-v1','release:1','tv:s1:e1','["tv:s1:e1"]','per_group',$1,'replace','stale','candidate_acquisition_failed',1,'digest',2)`, string(pools)); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := businesslog.WriteReplacementNotReplacedTx(ctx, txscope.Wrap(tx), writer, businesslog.NotReplaced{
		SubscriptionID: "sub-1", ManagedAnimeID: "anime-1", DecisionID: "dec-first", DecisionVersion: 2,
		ReleaseID: "release-new", Reason: businesslog.NotReplacedDownloadFailed,
		CorrelationID: "acq-release-new", CausationType: "acquisition", CausationID: "acq-release-new",
	}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	views, err := service.EvaluateApplication(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"})
	if err != nil || len(views) != 1 || views[0].Verdict != "replace" || views[0].ID == "dec-first" {
		t.Fatalf("retried evaluate = %+v, %v; want a fresh replace decision", views, err)
	}
	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	got := lifecycleLines(t, db)
	if len(got) != 2 {
		t.Fatalf("replacement lines = %+v, want not_replaced then replaced", got)
	}
	wantFirst := lifecycleLine{"dec-first", "not_replaced", "replacement_not_replaced", "download_failed", "replacement:dec-first:not_replaced"}
	if got[0] != wantFirst {
		t.Fatalf("first line = %+v, want %+v", got[0], wantFirst)
	}
	if got[1].subject != views[0].ID || got[1].reason != "replaced" || got[1].message != "replacement_replaced" {
		t.Fatalf("second line = %+v, want replaced for %s", got[1], views[0].ID)
	}
}
