package filemaintenance

import (
	"context"
	"database/sql"
	"testing"

	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/batchplan"
	"bansoko/internal/qualityprofile"
)

// seedScoredSelections gives the subscription two AniBT groups: group-high
// (90) and group-low (10), and switches the instance to single_copy with the
// default profile (subgroup.score first).
func seedScoredSelections(t *testing.T, db *sql.DB) (high, low *qualityprofile.SubgroupSource) {
	t.Helper()
	if _, err := db.Exec(`
UPDATE anime_business_configs
SET admission_mode = 'single_copy', quality_profile = $1::jsonb, replacement_version = replacement_version + 1
WHERE managed_anime_id = 'anime-1'`, string(qualityprofile.DefaultJSON())); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, ref string
		score   int
	}{{"sel-high", "group-high", 90}, {"sel-low", "group-low", 10}} {
		if _, err := db.Exec(`
INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,effective_selected,effective_score,available,availability_generation)
VALUES ($1,'sub-1','anibt',$2,$2,'auto',TRUE,$3,TRUE,1)`, row.id, row.ref, row.score); err != nil {
			t.Fatal(err)
		}
	}
	return &qualityprofile.SubgroupSource{Provider: "anibt", GroupRef: "group-high"},
		&qualityprofile.SubgroupSource{Provider: "anibt", GroupRef: "group-low"}
}

func setGroupScore(t *testing.T, db *sql.DB, groupRef string, score int) {
	t.Helper()
	if _, err := db.Exec(`UPDATE provider_group_selections SET effective_score = $2, version = version + 1 WHERE subscription_id = 'sub-1' AND group_ref = $1`, groupRef, score); err != nil {
		t.Fatal(err)
	}
}

func decisionStateByID(t *testing.T, db *sql.DB, id string) (state, reason string) {
	t.Helper()
	var staleReason sql.NullString
	if err := db.QueryRow(`SELECT state, stale_reason FROM replacement_decisions WHERE id = $1`, id).Scan(&state, &staleReason); err != nil {
		t.Fatal(err)
	}
	return state, staleReason.String
}

// A placed decision that the subgroup score decided does not survive a score
// edit: settle stales it (subgroup_score_changed) with no effect, and the
// next evaluation decides again with the new scores.
func TestSettleStalesWhenSubgroupScoreChanged(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	high, low := seedScoredSelections(t, db)
	old := seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "GroupA", Subgroup: low}, "2026-07-01T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "GroupB", Subgroup: high}, "2026-08-01T00:00:00Z")

	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(NewCleanupService(db))
	view, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if view.Verdict != "replace" || view.State != "admitted" {
		t.Fatalf("the higher group score must replace: %+v", view)
	}

	// The winner's group is rescored below the loser's.
	setGroupScore(t, db, "group-high", 5)
	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if state, reason := decisionStateByID(t, db, view.Id); state != "stale" || reason != staleSubgroupScoreChanged {
		t.Fatalf("decision = %s/%s, want stale/%s", state, reason, staleSubgroupScoreChanged)
	}
	if state := placementState(t, db, old); state != "active" {
		t.Fatalf("the existing file must stay active, got %s", state)
	}
	if _, deletions, superseded := decisionCounts(t, db, "anime-1"); deletions != 0 || superseded != 0 {
		t.Fatalf("a stale decision must have no effect: deletions=%d superseded=%d", deletions, superseded)
	}

	// Decided again with the current scores: the existing file is kept.
	again, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
	if err != nil {
		t.Fatalf("re-evaluate: %v", err)
	}
	if again.Id == view.Id || again.Verdict != "keep" {
		t.Fatalf("re-evaluation must decide again with the new scores: %+v", again)
	}
}

// A loser's score edit stales the decision as well: every score the pool
// compared is frozen.
func TestSettleStalesWhenLoserSubgroupScoreChanged(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	high, low := seedScoredSelections(t, db)
	old := seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "GroupA", Subgroup: low}, "2026-07-01T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "GroupB", Subgroup: high}, "2026-08-01T00:00:00Z")
	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(NewCleanupService(db))
	view, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
	if err != nil || view.Verdict != "replace" {
		t.Fatalf("evaluate: %+v %v", view, err)
	}
	setGroupScore(t, db, "group-low", 95)
	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if state, reason := decisionStateByID(t, db, view.Id); state != "stale" || reason != staleSubgroupScoreChanged {
		t.Fatalf("decision = %s/%s, want stale/%s", state, reason, staleSubgroupScoreChanged)
	}
	if state := placementState(t, db, old); state != "active" {
		t.Fatalf("the existing file must stay active, got %s", state)
	}
}

// A pre-acquisition decision is not reused after a score edit: the next
// candidate intent stales it and decides again with the current scores.
func TestUnboundDecisionNotReusedAfterSubgroupScoreChanged(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(`
UPDATE anime_business_configs
SET admission_mode = 'single_copy', quality_profile = $1::jsonb, replacement_version = replacement_version + 1
WHERE managed_anime_id = 'anime-1'`, `{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"release.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]}]}`); err != nil {
		t.Fatal(err)
	}
	// The incumbent's group has no selection row yet (no score): the
	// unscored candidate ties on subgroup.score and wins on resolution.
	low := &qualityprofile.SubgroupSource{Provider: "anibt", GroupRef: "group-low"}
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "GroupA", Subgroup: low}, "2026-07-01T00:00:00Z")
	seedCandidateRelease(t, db, "sub-1", "release-new", "[G] Show - 01 [1080p]")
	evidence := resolvedTVEvidence(t, "[G] Show - 01 [1080p]", "tv:e1", "1")
	service := NewReplacementDecisionService(db)
	latest := func() (id, verdict string) {
		t.Helper()
		if err := db.QueryRow(`
SELECT id, verdict FROM replacement_decisions
WHERE candidate_release_id = 'release-new' AND state = 'admitted'
ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&id, &verdict); err != nil {
			t.Fatal(err)
		}
		return id, verdict
	}

	intent, err := releaseIntent(service.CandidateIntents(ctx, "anime-1", "release-new", evidence))
	if err != nil || intent != acquisitiontarget.IntentReplace {
		t.Fatalf("intent = %q, %v; want replace", intent, err)
	}
	first, verdict := latest()
	if verdict != "replace" {
		t.Fatalf("verdict = %s, want replace", verdict)
	}
	// Unchanged scores reuse the live decision.
	if intent, err := releaseIntent(service.CandidateIntents(ctx, "anime-1", "release-new", evidence)); err != nil || intent != acquisitiontarget.IntentReplace {
		t.Fatalf("reuse intent = %q, %v", intent, err)
	}
	if again, _ := latest(); again != first {
		t.Fatalf("unchanged scores must reuse the decision: %s != %s", again, first)
	}

	// The incumbent's group gains a score: the decision is not reused.
	if _, err := db.Exec(`
INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,effective_selected,effective_score,available,availability_generation)
VALUES ('sel-low','sub-1','anibt','group-low','group-low','auto',TRUE,10,TRUE,1)`); err != nil {
		t.Fatal(err)
	}
	intent, err = releaseIntent(service.CandidateIntents(ctx, "anime-1", "release-new", evidence))
	if err != nil || intent == acquisitiontarget.IntentReplace {
		t.Fatalf("intent = %q, %v; the scored incumbent must be kept", intent, err)
	}
	if state, reason := decisionStateByID(t, db, first); state != "stale" || reason != staleSubgroupScoreChanged {
		t.Fatalf("decision = %s/%s, want stale/%s", state, reason, staleSubgroupScoreChanged)
	}
	// Before acquisition only a replacement freezes a decision: a kept
	// incumbent admits ordinarily, with no live decision left.
	var live int
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_decisions WHERE candidate_release_id = 'release-new' AND state <> 'stale'`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("the scored incumbent must leave no live decision, got %d", live)
	}
}

// RSS plan dedupe reads the subgroup scores inside a read-only transaction:
// the score read must not take a row lock (SELECT ... FOR SHARE fails there,
// which stopped every non-all-mode release before acquisition).
func TestCoverageVerdictsReadsSubgroupScoresReadOnly(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	_, low := seedScoredSelections(t, db)
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "GroupA", Subgroup: low}, "2026-07-01T00:00:00Z")
	seedCandidateRelease(t, db, "sub-1", "release-new", "[G] Show - 01 [720p]")
	verdicts, err := NewReplacementDecisionService(db).CoverageVerdicts(ctx, "anime-1", "release-new",
		acquisitiontarget.CandidateEvidence{Title: "[G] Show - 01 [720p]", Coverage: []string{"tv:e1", "tv:e2"}})
	if err != nil {
		t.Fatalf("coverage verdicts: %v", err)
	}
	if verdicts["tv:e1"] != batchplan.VerdictExisting || verdicts["tv:e2"] != batchplan.VerdictOpen {
		t.Fatalf("verdicts = %v", verdicts)
	}
}

// A required (manual include) group is written at effective_score 100, so in
// single_copy it never loses to an automatically picked group on
// subgroup.score: its file stays against a high-scored automatic candidate,
// and its candidate replaces an automatic group's file.
func TestSingleCopyRequiredGroupNeverLosesOnScore(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		incumbentRef, candidateRef string
		verdict                    string
	}{
		{"required_incumbent_stays", "group-required", "group-auto", "keep"},
		{"required_candidate_replaces", "group-auto", "group-required", "replace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openFrozenTestDB(t)
			defer db.Close()
			if _, err := db.Exec(`
UPDATE anime_business_configs
SET admission_mode = 'single_copy', quality_profile = $1::jsonb, replacement_version = replacement_version + 1
WHERE managed_anime_id = 'anime-1'`, string(qualityprofile.DefaultJSON())); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`
INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,manual_selected,effective_selected,effective_score,available,availability_generation)
VALUES ('sel-required','sub-1','anibt','group-required','Required','manual',TRUE,TRUE,100,TRUE,1),
       ('sel-auto','sub-1','anibt','group-auto','Auto','auto',NULL,TRUE,99,TRUE,1)`); err != nil {
				t.Fatal(err)
			}
			seedFilePlacement(t, db, "anime-1", "sub-1", "required-old", []string{"tv:e1"},
				Facts{Resolution: "1080p", Group: "Old", Subgroup: subgroup("anibt", tc.incumbentRef)}, "2026-07-01T00:00:00Z")
			seedFilePlacement(t, db, "anime-1", "sub-1", "required-new", []string{"tv:e1"},
				Facts{Resolution: "1080p", Group: "New", Subgroup: subgroup("anibt", tc.candidateRef)}, "2026-08-01T00:00:00Z")
			view, err := evaluateSingle(NewReplacementDecisionService(db).EvaluateServer(context.Background(), EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "required-new"}))
			if err != nil {
				t.Fatal(err)
			}
			if string(view.Verdict) != tc.verdict {
				t.Fatalf("verdict = %s, want %s: %+v", view.Verdict, tc.verdict, view.Pools)
			}
		})
	}
}
