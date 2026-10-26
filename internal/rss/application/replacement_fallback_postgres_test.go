package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	"bansoko/internal/batchplan"
	"bansoko/internal/filemaintenance"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/workitems"
)

const fallbackResolutionProfile = `{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]}]}`

// newFallbackFixture seeds the RSS test owner in single_copy with one release
// per title (same episode, same torrent manifest) and wires the real
// replacement decision owner into release evaluation.
func newFallbackFixture(t *testing.T, titles map[string]string) (*sql.DB, *workitems.Store, *Service, *filemaintenance.ReplacementDecisionService) {
	t.Helper()
	initTestNamingVars(t)
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	for id, title := range titles {
		insertReleaseForEvaluation(t, db, sourceID, id, title, "https://downloads.example.test/"+id+".torrent")
		seedNativeTorrent(t, db, "torrent-"+id, id)
	}
	if _, err := db.Exec(`
UPDATE anime_business_configs
SET admission_mode = 'single_copy', quality_profile = $2::jsonb,
    retention_source_default_version = 1, retention_version = 1
WHERE managed_anime_id = $1`, "anime-rss-test", fallbackResolutionProfile); err != nil {
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
	mediaFacts := facts.NewStoreWithRepository(mediapersistence.NewRepository(db))
	service.SetCandidateFactsRepository(facts.NewPGRepository(db))
	service.SetMediaFactsStore(mediaFacts)
	service.SetTargetAcquisition(missingTargetProbe{})
	service.SetTargetStore(target.NewStore(targetpersistence.NewRepository(db)))
	decisions := filemaintenance.NewReplacementDecisionService(db)
	decisions.SetMediaFactsStore(mediaFacts)
	decisions.SetWorkItems(store)
	service.SetReplacementAdmission(decisions)
	return db, store, service, decisions
}

// latestPlanOutcomes reads the video outcomes of a release's latest frozen
// batch plan.
func latestPlanOutcomes(t *testing.T, db *sql.DB, releaseID string) []batchplan.Outcome {
	t.Helper()
	var raw []byte
	if err := db.QueryRow(`
SELECT release_facts->'batch_plan' FROM media_fact_snapshots
WHERE release_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, releaseID).Scan(&raw); err != nil {
		t.Fatalf("read plan of %s: %v", releaseID, err)
	}
	var plan batchplan.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	out := []batchplan.Outcome{}
	for _, file := range plan.Files {
		if file.Kind == "video" {
			out = append(out, file.Outcome)
		}
	}
	return out
}

func requireOutcome(t *testing.T, db *sql.DB, releaseID string, want batchplan.Outcome) {
	t.Helper()
	outcomes := latestPlanOutcomes(t, db, releaseID)
	if len(outcomes) != 1 || outcomes[0] != want {
		t.Fatalf("release %s video outcomes = %v, want [%s]", releaseID, outcomes, want)
	}
}

func liveFallbackItems(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE owner_type = 'file_maintenance' AND owner_id = 'anime-rss-test' AND kind = 'replacement_fallback' AND state IN ('available','leased')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func promotedIDs(promotions []filemaintenance.FallbackPromotion) []string {
	out := make([]string, 0, len(promotions))
	for _, promotion := range promotions {
		out = append(out, promotion.ReleaseID)
	}
	return out
}

// runFallback claims the live replacement_fallback item and finishes it.
func runFallback(t *testing.T, store *workitems.Store, decisions *filemaintenance.ReplacementDecisionService) workitems.OwnerResult {
	t.Helper()
	ctx := context.Background()
	claim, err := store.ClaimDue(ctx, "fallback-test-worker", []string{workitems.KindReplacementFallback}, time.Minute)
	if err != nil {
		t.Fatalf("claim fallback: %v", err)
	}
	outcome, err := decisions.FallbackHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatalf("fallback handler: %v", err)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	return outcome.OwnerResult
}

// runReleaseEvaluation claims the live release_evaluate item of one release
// and finishes it; it reports the claimed input trigger.
func runReleaseEvaluation(t *testing.T, store *workitems.Store, service *Service, releaseID string) string {
	t.Helper()
	ctx := context.Background()
	claim, err := store.ClaimDue(ctx, "fallback-test-worker", []string{workitems.KindReleaseEvaluate}, time.Minute)
	if err != nil {
		t.Fatalf("claim release evaluation of %s: %v", releaseID, err)
	}
	if claim.Owner.ID != releaseID {
		t.Fatalf("claimed release evaluation of %s, want %s", claim.Owner.ID, releaseID)
	}
	finishReleaseClaim(t, store, service, claim)
	return releaseEvaluationTrigger(claim.InputSnapshot)
}

// A failed download frees its pool: the fallback query promotes the best
// candidate the running download kept out (B over C), never the failed
// release itself. The promoted release runs normal acceptance and downloads.
// The failed release, retried while B downloads, is covered in flight and
// re-enters the query once B settles. A stopped subscription is not
// triggered; resuming it runs the query.
func TestReplacementFallbackPromotesNextCandidatePostgres(t *testing.T) {
	ctx := context.Background()
	db, store, service, decisions := newFallbackFixture(t, map[string]string{
		"fb-a": "[Group] Test Show - 01 [1080p]",
		"fb-b": "[Group] Test Show - 01 [720p]",
		"fb-c": "[Group] Test Show - 01 [480p]",
	})
	evaluateAutomatically(t, db, store, service, "fb-a")
	assertReleaseState(t, db, "fb-a", "accepted_pending_download", "accepted", true)
	for _, id := range []string{"fb-c", "fb-b"} {
		evaluateAutomatically(t, db, store, service, id)
		assertReleaseState(t, db, id, "filtered", batchplan.ReasonNoPlaceableFiles, false)
		requireOutcome(t, db, id, batchplan.OutcomeCoveredInFlight)
	}
	promotions, reason, err := decisions.FallbackCandidates(ctx, "anime-rss-test")
	if err != nil || len(promotions) != 0 || reason != "pools_claimed" {
		t.Fatalf("a claimed pool must promote nothing: %v %q %v", promotedIDs(promotions), reason, err)
	}

	// A stopped subscription: the settle triggers nothing, resume does.
	if _, err := db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE managed_anime_id = 'anime-rss-test'`); err != nil {
		t.Fatal(err)
	}
	failCurrentDownload(t, "fb-a")
	if count := liveFallbackItems(t, db); count != 0 {
		t.Fatalf("a stopped subscription must not trigger the fallback: %d items", count)
	}
	if promotions, reason, err := decisions.FallbackCandidates(ctx, "anime-rss-test"); err != nil || len(promotions) != 0 || reason != "subscription_inactive" {
		t.Fatalf("stopped fallback = %v %q %v", promotedIDs(promotions), reason, err)
	}
	if _, err := db.Exec(`UPDATE subscriptions SET state = 'active', version = version + 1 WHERE managed_anime_id = 'anime-rss-test'`); err != nil {
		t.Fatal(err)
	}
	if err := filemaintenance.NewReplacementTriggerService(db, store).TriggerSubscriptionResumed(ctx, "anime-rss-test"); err != nil {
		t.Fatal(err)
	}
	if count := liveFallbackItems(t, db); count != 1 {
		t.Fatalf("resume must enqueue the fallback: %d items", count)
	}
	// The replacement_evaluate item the resume also merged is not under test.
	if _, err := ownerTestDB(t).Exec(`DELETE FROM work_items WHERE kind = 'replacement_evaluate'`); err != nil {
		t.Fatal(err)
	}

	promotions, _, err = decisions.FallbackCandidates(ctx, "anime-rss-test")
	if err != nil || strings.Join(promotedIDs(promotions), ",") != "fb-b" {
		t.Fatalf("the failed release's pool must promote B only: %v %v", promotedIDs(promotions), err)
	}
	if result := runFallback(t, store, decisions); result.ReasonCode != "fallback_promoted" {
		t.Fatalf("fallback result = %+v", result)
	}
	if trigger := runReleaseEvaluation(t, store, service, "fb-b"); trigger != releaseTriggerReplacementFallback {
		t.Fatalf("promoted evaluation trigger = %q", trigger)
	}
	assertReleaseState(t, db, "fb-b", "accepted_pending_download", "accepted", true)
	if count := liveFallbackItems(t, db); count != 0 {
		t.Fatalf("a promoted release that downloads must not query again: %d items", count)
	}

	// The failed A stays out until retried; its retry is covered in flight by
	// the running B and re-enters the query once B settles.
	result, err := service.RetryRelease(ctx, "fb-a", "retry-key-fallback-a")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != RetryOutcomeFiltered {
		t.Fatalf("retry while B downloads = %+v, want filtered", result)
	}
	// The retry detached the failed task (it stays as history); no new task.
	var verdict, retryReason string
	var taskID sql.NullString
	var tasks int
	if err := db.QueryRow(`SELECT verdict, reason_code, download_task_id, (SELECT COUNT(*) FROM download_tasks WHERE release_id = 'fb-a') FROM releases WHERE id = 'fb-a'`).Scan(&verdict, &retryReason, &taskID, &tasks); err != nil {
		t.Fatal(err)
	}
	if verdict != "filtered" || retryReason != batchplan.ReasonNoPlaceableFiles || taskID.Valid || tasks != 1 {
		t.Fatalf("retried A = %s/%s task=%v tasks=%d", verdict, retryReason, taskID.Valid, tasks)
	}
	requireOutcome(t, db, "fb-a", batchplan.OutcomeCoveredInFlight)
	if promotions, reason, err := decisions.FallbackCandidates(ctx, "anime-rss-test"); err != nil || len(promotions) != 0 || reason != "pools_claimed" {
		t.Fatalf("B's running claim must hold the pool: %v %q %v", promotedIDs(promotions), reason, err)
	}
	completeCurrentDownload(t, "fb-b", "succeeded")
	if count := liveFallbackItems(t, db); count != 1 {
		t.Fatalf("B's settle must trigger the fallback: %d items", count)
	}
	promotions, _, err = decisions.FallbackCandidates(ctx, "anime-rss-test")
	if err != nil || strings.Join(promotedIDs(promotions), ",") != "fb-a" {
		t.Fatalf("the retried A must re-enter the query (over C): %v %v", promotedIDs(promotions), err)
	}
}

// After the first download succeeds, the release it kept out is evaluated
// against the placed file: a better one replaces it, one that is not better
// is covered by the present file, the query runs once more and ends.
func TestReplacementFallbackAfterSuccessPostgres(t *testing.T) {
	for _, tc := range []struct {
		name   string
		titleB string
		better bool
	}{
		{name: "better", titleB: "[Group] Test Show - 01 [1080p]", better: true},
		{name: "not_better", titleB: "[Group] Test Show - 01 [480p]", better: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, store, service, decisions := newFallbackFixture(t, map[string]string{
				"fs-a": "[Group] Test Show - 01 [720p]",
				"fs-b": tc.titleB,
			})
			evaluateAutomatically(t, db, store, service, "fs-a")
			assertReleaseState(t, db, "fs-a", "accepted_pending_download", "accepted", true)
			evaluateAutomatically(t, db, store, service, "fs-b")
			requireOutcome(t, db, "fs-b", batchplan.OutcomeCoveredInFlight)

			// A succeeds and its file is a managed placement of the episode.
			var key string
			if err := db.QueryRow(`SELECT coverage_keys->>0 FROM release_candidate_facts WHERE release_id = 'fs-a' ORDER BY release_version DESC LIMIT 1`).Scan(&key); err != nil || key == "" {
				t.Fatalf("read A's coverage: %q %v", key, err)
			}
			owner := ownerTestDB(t)
			if _, err := owner.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, snapshot_generation,
 verification_state, state, replacement_eligible, version, created_at, updated_at)
VALUES ('plc-fs-a', '/library/fallback/a.mkv', 'anime-rss-test', 'reconciliation', 'obs-fs-a', 'fp-fs-a', 'fp-fs-a', 'fs-a', 'facts-v1', 1, 'verified', 'active', true, 1, now(), now())`); err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Exec(`INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref) VALUES ('plc-fs-a', $1, 0, 'facts-v1', 'obs-fs-a')`, key); err != nil {
				t.Fatal(err)
			}
			completeCurrentDownload(t, "fs-a", "succeeded")
			if count := liveFallbackItems(t, db); count != 1 {
				t.Fatalf("A's settle must trigger the fallback: %d items", count)
			}
			promotions, _, err := decisions.FallbackCandidates(ctx, "anime-rss-test")
			if err != nil || strings.Join(promotedIDs(promotions), ",") != "fs-b" {
				t.Fatalf("B must be promoted after A succeeds: %v %v", promotedIDs(promotions), err)
			}
			runFallback(t, store, decisions)
			runReleaseEvaluation(t, store, service, "fs-b")
			if tc.better {
				assertReleaseState(t, db, "fs-b", "accepted_pending_download", "accepted", true)
				var verdict string
				if err := db.QueryRow(`SELECT verdict FROM replacement_decisions WHERE candidate_release_id = 'fs-b' AND state = 'admitted'`).Scan(&verdict); err != nil || verdict != "replace" {
					t.Fatalf("B must be admitted to replace A: %q %v", verdict, err)
				}
				if count := liveFallbackItems(t, db); count != 0 {
					t.Fatalf("a downloading B must not query again: %d", count)
				}
				return
			}
			assertReleaseState(t, db, "fs-b", "filtered", batchplan.ReasonNoPlaceableFiles, false)
			requireOutcome(t, db, "fs-b", batchplan.OutcomeCoveredExisting)
			if count := liveFallbackItems(t, db); count != 1 {
				t.Fatalf("a promoted B that did not download must query again: %d", count)
			}
			if result := runFallback(t, store, decisions); result.ReasonCode != "no_candidate" {
				t.Fatalf("the loop must end with no candidate: %+v", result)
			}
			var pending int
			if err := db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE kind IN ('release_evaluate','replacement_fallback') AND state IN ('available','leased')`).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("nothing may remain queued: %d %v", pending, err)
			}
		})
	}
}
