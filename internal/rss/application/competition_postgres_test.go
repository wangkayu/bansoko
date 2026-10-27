package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	"bansoko/internal/batchplan"
	"bansoko/internal/filemaintenance"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/subscription/competition"
	"bansoko/internal/workitems"
)

// competitionProfile ranks the subgroup score first, then the resolution.
const competitionProfile = `{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"release.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]}]}`

// competitionFixture is one AniBT subscription instance with two subgroup
// sources (alpha: source-rss-test, beta: source-beta), both selected with
// score 50, the given admission mode, and the production wiring: the target
// admission, the replacement decision owner, the subscription competition and
// a recorder of release_discovered notices. No source has refreshed yet: each
// scenario states which refreshes happened (markRefreshed) or are still
// queued (queueRefresh).
type competitionFixture struct {
	t         *testing.T
	db        *sql.DB
	store     *workitems.Store
	service   *Service
	fetcher   *rssFakeSourceFetcher
	decisions *filemaintenance.ReplacementDecisionService
	notices   *discoveryNotices
	sizes     int64
}

func newCompetitionFixture(t *testing.T, mode string) *competitionFixture {
	t.Helper()
	initTestNamingVars(t)
	db := openRSSDatabase(t)
	sourceID, subscriptionID := seedRSSOwner(t, db, false, true)
	if _, err := db.Exec(`
UPDATE rss_sources
SET source_kind = 'provider_group', provider = 'anibt', provider_entry_ref = '517057', provider_group_ref = 'alpha',
    discovery_snapshot_version = 'v1', group_name = 'Alpha',
    provenance = '{"provider":"anibt","provider_entry_ref":"517057","provider_group_ref":"alpha","discovery_snapshot_version":"v1"}'::jsonb
WHERE id = $1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title,
                        provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version, provenance)
VALUES ('source-beta', $1, 'provider_group', 'https://feeds.example.test/beta.xml', 'https://feeds.example.test/beta.xml', 'Beta', 'Test Show',
        'anibt', '517057', 'beta', 'v1',
        '{"provider":"anibt","provider_entry_ref":"517057","provider_group_ref":"beta","discovery_snapshot_version":"v1"}'::jsonb)`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO rss_source_runtime(rss_source_id) VALUES ('source-beta')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO provider_group_selections(id, subscription_id, provider, group_ref, display_name, selection_mode, effective_selected, effective_score, available, availability_generation)
VALUES ('sel-alpha', $1, 'anibt', 'alpha', 'Alpha', 'auto', TRUE, 50, TRUE, 1),
       ('sel-beta', $1, 'anibt', 'beta', 'Beta', 'auto', TRUE, 50, TRUE, 1)`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
UPDATE anime_business_configs
SET admission_mode = $2, quality_profile = $3::jsonb,
    replacement_source_default_version = 1, replacement_version = 1
WHERE managed_anime_id = $1`, "anime-rss-test", mode, competitionProfile); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &rssFakeSourceFetcher{}
	service, err := newRSSService(t, db, store, fetcher)
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
	service.SetReplacementAdmission(decisions)
	service.SetSubscriptionCompetition(decisions)
	notices := &discoveryNotices{}
	service.SetBusinessDeliveryEnqueuer(notices)
	return &competitionFixture{t: t, db: db, store: store, service: service, fetcher: fetcher, decisions: decisions, notices: notices, sizes: 1024}
}

// markRefreshed records a completed refresh of each source.
func (f *competitionFixture) markRefreshed(sourceIDs ...string) {
	f.t.Helper()
	for _, id := range sourceIDs {
		if _, err := f.db.Exec(`UPDATE rss_source_runtime SET last_refresh_at = CURRENT_TIMESTAMP, last_success_at = CURRENT_TIMESTAMP, health = 'ready' WHERE rss_source_id = $1`, id); err != nil {
			f.t.Fatal(err)
		}
	}
}

// queueRefresh enqueues a source's refresh the way a subscription start does
// (the production idempotency key), without running it.
func (f *competitionFixture) queueRefresh(sourceID string) {
	f.t.Helper()
	ctx := context.Background()
	var version int64
	if err := f.db.QueryRow(`SELECT version FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&version); err != nil {
		f.t.Fatal(err)
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner: workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, Kind: workitems.KindRSSRefresh,
		IdempotencyKey: "rss_refresh:" + sourceID, InputSnapshot: json.RawMessage(`{"trigger":"subscription_started"}`),
		MaxAttempts: 3, ExpectedOwnerVersion: version,
	}); err != nil {
		_ = tx.Rollback()
		f.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatal(err)
	}
}

// runQueuedRefresh claims a queued refresh and applies its outcome the way
// the runtime does.
func (f *competitionFixture) runQueuedRefresh(sourceID string) {
	f.t.Helper()
	ctx := context.Background()
	claim, err := f.store.ClaimDue(ctx, "competition-test-refresher", []string{workitems.KindRSSRefresh}, time.Minute)
	if err != nil || claim.Owner.ID != sourceID {
		f.t.Fatalf("claim the refresh of %s: %+v %v", sourceID, claim, err)
	}
	outcome, err := f.service.Handle(ctx, claim)
	if err != nil {
		f.t.Fatal(err)
	}
	switch outcome.Kind {
	case worker.OutcomeRetry:
		if _, err := f.store.Retry(ctx, claim, outcome.ReasonCode, outcome.Reason, outcome.RetryAfter, outcome.WriteOwner); err != nil {
			f.t.Fatal(err)
		}
	case worker.OutcomeFinish:
		f.finish(claim, outcome)
	default:
		f.t.Fatalf("refresh outcome = %+v", outcome)
	}
}

// firstRoundDone reports whether the instance committed its first round.
func (f *competitionFixture) firstRoundDone() bool {
	f.t.Helper()
	var done bool
	if err := f.db.QueryRow(`SELECT competition_first_round_at IS NOT NULL FROM subscriptions WHERE id = 'sub-rss-test'`).Scan(&done); err != nil {
		f.t.Fatal(err)
	}
	return done
}

// addRelease stores one feed item of a source with its own torrent (one
// video of the given episode; every torrent has its own info hash).
func (f *competitionFixture) addRelease(id, sourceID, title string, episode int) {
	f.t.Helper()
	insertReleaseForEvaluation(f.t, f.db, sourceID, id, title, "https://downloads.example.test/"+id+".torrent")
	f.sizes++
	seedNativeTorrentFile(f.t, f.db, "torrent-"+id, id, fmt.Sprintf("show-%02d.mkv", episode), f.sizes, id+".torrent")
}

// claimEvaluation enqueues and claims the release_evaluate item of one
// release (the production idempotency key); several claims may be leased at
// once, like the light lane's concurrent evaluations.
func (f *competitionFixture) claimEvaluation(id string) workitems.Claim {
	f.t.Helper()
	ctx := context.Background()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	itemID, err := f.store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner: workitems.OwnerRef{Type: workitems.OwnerRelease, ID: id}, Kind: workitems.KindReleaseEvaluate,
		IdempotencyKey: "release_evaluate:" + id, InputSnapshot: json.RawMessage(`{"trigger":"refresh"}`),
		MaxAttempts: 5, ExpectedOwnerVersion: currentReleaseVersion(f.t, f.db, id),
	})
	if err != nil {
		_ = tx.Rollback()
		f.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatal(err)
	}
	claim, err := f.store.ClaimDue(ctx, "competition-test-evaluator", []string{workitems.KindReleaseEvaluate}, time.Minute)
	if err != nil || claim.ID != itemID {
		f.t.Fatalf("claim evaluation of %s: %+v %v", id, claim, err)
	}
	return claim
}

// handle runs the release evaluation handler of a leased claim.
func (f *competitionFixture) handle(claim workitems.Claim) worker.Outcome {
	f.t.Helper()
	outcome, err := f.service.Handle(context.Background(), claim)
	if err != nil || outcome.Kind != worker.OutcomeFinish {
		f.t.Fatalf("evaluation of %s: %+v %v", claim.Owner.ID, outcome, err)
	}
	return outcome
}

// finish commits a handled outcome the way the runtime does (owner write,
// terminal transition, post-transition hook in one transaction).
func (f *competitionFixture) finish(claim workitems.Claim, outcome worker.Outcome) {
	f.t.Helper()
	var err error
	if outcome.PostTransition != nil {
		err = f.store.FinishWithResultPostTransition(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner, outcome.PostTransition)
	} else {
		err = f.store.FinishWithResult(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner)
	}
	if err != nil {
		f.t.Fatalf("finish %s: %v", claim.Owner.ID, err)
	}
}

func (f *competitionFixture) evaluate(ids ...string) {
	f.t.Helper()
	for _, id := range ids {
		claim := f.claimEvaluation(id)
		f.finish(claim, f.handle(claim))
	}
}

// claimCompetition claims the instance's due competition item (ok is false
// when none is due).
func (f *competitionFixture) claimCompetition() (workitems.Claim, bool) {
	f.t.Helper()
	claim, err := f.store.ClaimDue(context.Background(), "competition-test-worker", []string{workitems.KindSubscriptionCompetition}, time.Minute)
	if errors.Is(err, workitems.ErrNoDueWork) {
		return workitems.Claim{}, false
	}
	if err != nil {
		f.t.Fatal(err)
	}
	if claim.Owner.Type != workitems.OwnerFileMaintenance || claim.Owner.ID != "anime-rss-test" {
		f.t.Fatalf("competition owner = %+v", claim.Owner)
	}
	return claim, true
}

// handleCompetition runs the competition handler of a claim; the handler
// commits its round itself and returns a committed receipt.
func (f *competitionFixture) handleCompetition(claim workitems.Claim) (worker.Outcome, error) {
	ctx := context.Background()
	outcome, err := f.service.CompetitionHandler().Handle(ctx, claim)
	if err != nil {
		return outcome, err
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.CommittedClaim == nil || outcome.CommittedClaim.WorkItemID != claim.ID {
		return outcome, fmt.Errorf("competition outcome = %+v", outcome)
	}
	if outcome.AfterCommit != nil {
		outcome.AfterCommit(ctx)
	}
	return outcome, nil
}

// runCompetition claims the instance's live competition item and runs it the
// way the runtime does. ran is false when no item is due.
func (f *competitionFixture) runCompetition() (workitems.OwnerResult, bool) {
	f.t.Helper()
	claim, ok := f.claimCompetition()
	if !ok {
		return workitems.OwnerResult{}, false
	}
	outcome, err := f.handleCompetition(claim)
	if err != nil {
		f.t.Fatalf("competition: %v", err)
	}
	return outcome.OwnerResult, true
}

// mustRunCompetition runs the due competition and requires its reason.
func (f *competitionFixture) mustRunCompetition(reason string) {
	f.t.Helper()
	result, ran := f.runCompetition()
	if !ran {
		f.t.Fatalf("no competition is due (want %s)", reason)
	}
	if result.ReasonCode != reason {
		f.t.Fatalf("competition result = %+v, want %s", result, reason)
	}
}

func (f *competitionFixture) liveCompetitions() int {
	f.t.Helper()
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE owner_type = 'file_maintenance' AND owner_id = 'anime-rss-test' AND kind = 'subscription_competition'`).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count
}

func (f *competitionFixture) taskCount() int {
	f.t.Helper()
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM download_tasks`).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count
}

func (f *competitionFixture) requireAwaiting(ids ...string) {
	f.t.Helper()
	for _, id := range ids {
		assertReleaseState(f.t, f.db, id, competition.VerdictAwaiting, competition.VerdictAwaiting, false)
	}
}

func (f *competitionFixture) requireAdmitted(ids ...string) {
	f.t.Helper()
	for _, id := range ids {
		assertReleaseState(f.t, f.db, id, "accepted_pending_download", "accepted", true)
	}
}

func (f *competitionFixture) requireCoveredInFlight(ids ...string) {
	f.t.Helper()
	for _, id := range ids {
		assertReleaseState(f.t, f.db, id, "filtered", batchplan.ReasonNoPlaceableFiles, false)
		requireOutcome(f.t, f.db, id, batchplan.OutcomeCoveredInFlight)
	}
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

// Three releases of one episode from two subgroup sources are evaluated
// concurrently (all three leased at once, as the light lane runs them). Each
// evaluation only plans and waits; the competition does not run while any of
// them is unfinished, then admits per mode: single_copy one release, per_group
// one per subgroup, all every release. The others are kept out as
// covered_in_flight, exactly as a running download keeps them out.
func TestCompetitionAdmitsOneReleasePerEpisodePostgres(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		admitted []string
		kept     []string
	}{
		{mode: "single_copy", admitted: []string{"a-1080"}, kept: []string{"a-720", "b-480"}},
		{mode: "per_group", admitted: []string{"a-1080", "b-480"}, kept: []string{"a-720"}},
		{mode: "all", admitted: []string{"a-1080", "a-720", "b-480"}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newCompetitionFixture(t, tc.mode)
			f.markRefreshed("source-rss-test", "source-beta")
			f.addRelease("a-1080", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
			f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
			f.addRelease("b-480", "source-beta", "[Beta] Test Show - 01 [480p]", 1)
			claims := []workitems.Claim{f.claimEvaluation("a-1080"), f.claimEvaluation("a-720"), f.claimEvaluation("b-480")}
			outcomes := make([]worker.Outcome, len(claims))
			for index, claim := range claims {
				outcomes[index] = f.handle(claim)
			}
			// The first evaluation lands: the competition is enqueued and
			// waits for the two still running.
			f.finish(claims[0], outcomes[0])
			f.requireAwaiting("a-1080")
			if f.liveCompetitions() != 1 {
				t.Fatalf("the waiting release must enqueue the competition: %d", f.liveCompetitions())
			}
			f.mustRunCompetition(competitionWaitingEvaluations)
			if f.taskCount() != 0 {
				t.Fatalf("nothing may be admitted while evaluations run: %d tasks", f.taskCount())
			}
			f.finish(claims[1], outcomes[1])
			f.finish(claims[2], outcomes[2])
			f.requireAwaiting("a-1080", "a-720", "b-480")
			if f.liveCompetitions() != 1 {
				t.Fatalf("the triggers merge into one competition: %d", f.liveCompetitions())
			}
			f.mustRunCompetition("competition_decided")
			f.requireAdmitted(tc.admitted...)
			f.requireCoveredInFlight(tc.kept...)
			if got := f.taskCount(); got != len(tc.admitted) {
				t.Fatalf("download tasks = %d, want %d", got, len(tc.admitted))
			}
			// Every candidate is decided: nothing is due any more.
			if _, ran := f.runCompetition(); ran {
				t.Fatal("a decided round must not enqueue another")
			}
		})
	}
}

// A loser of the round is picked once the admitted download fails: the
// failure's settle enqueues the competition (SQL twin), which admits the next
// best candidate and never the failed release. The settle trigger writes the
// same work item as the Go enqueue.
func TestCompetitionFallbackAfterFailedDownloadPostgres(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("a-1080", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
	f.addRelease("b-480", "source-beta", "[Beta] Test Show - 01 [480p]", 1)
	f.evaluate("a-1080", "a-720", "b-480")
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("a-1080")
	f.requireCoveredInFlight("a-720", "b-480")

	failCurrentDownload(t, "a-1080")
	settled := competitionItemShape(t, f.db)
	if settled.trigger != competition.TriggerAcquisitionSettled {
		t.Fatalf("the settle must enqueue the competition: %+v", settled)
	}
	// Go twin: the same owner, kind, key, budget and owner-version
	// expectation as the settle trigger (both merge through
	// subscription_competition_merge).
	if _, err := ownerTestDB(t).Exec(`DELETE FROM work_items WHERE kind = 'subscription_competition'`); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := competition.EnqueueTx(context.Background(), tx, "anime-rss-test", competition.TriggerAcquisitionSettled, competition.Always); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if enqueued := competitionItemShape(t, f.db); enqueued != settled {
		t.Fatalf("Go enqueue %+v differs from the settle trigger %+v", enqueued, settled)
	}

	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("a-720")
	f.requireCoveredInFlight("b-480")
	var failedTask sql.NullString
	if err := f.db.QueryRow(`SELECT download_task_id FROM releases WHERE id = 'a-1080'`).Scan(&failedTask); err != nil || !failedTask.Valid {
		t.Fatalf("the failed release keeps its failed task until a retry: %v %v", failedTask, err)
	}
	if f.taskCount() != 2 {
		t.Fatalf("download tasks = %d, want the failed one and the fallback", f.taskCount())
	}
}

type competitionShape struct {
	ownerType, ownerID, kind, key, trigger string
	maxAttempts, retention                 int
	expectedVersion                        int64
	managedAnimeID                         sql.NullString
}

func competitionItemShape(t *testing.T, db *sql.DB) competitionShape {
	t.Helper()
	var shape competitionShape
	if err := db.QueryRow(`
SELECT owner_type, owner_id, kind, idempotency_key, input_snapshot->>'trigger', max_attempts, retention_days, expected_owner_version, managed_anime_id
FROM work_items WHERE kind = 'subscription_competition'`).Scan(&shape.ownerType, &shape.ownerID, &shape.kind, &shape.key, &shape.trigger,
		&shape.maxAttempts, &shape.retention, &shape.expectedVersion, &shape.managedAnimeID); err != nil {
		t.Fatalf("read the competition item: %v", err)
	}
	return shape
}

// The first round waits until every source in scope has refreshed once; a
// source whose first refresh failed counts as refreshed. The failed refresh
// itself enqueues the competition.
func TestCompetitionFirstRoundWaitsForEveryFirstRefreshPostgres(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	// The start queued both first refreshes; alpha's has run.
	f.markRefreshed("source-rss-test")
	f.queueRefresh("source-beta")
	f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
	f.evaluate("a-720")
	f.requireAwaiting("a-720")
	f.mustRunCompetition(competitionWaitingFirstRefresh)
	f.requireAwaiting("a-720")
	if item := f.competitionItem(); item.trigger != competition.TriggerGateWait || item.due {
		t.Fatalf("a waiting round only looks again later: %+v", item)
	}

	if f.firstRoundDone() {
		t.Fatal("a waiting round is not the first round")
	}
	// beta's first refresh fails: it is done, and the round runs.
	f.fetcher.err = errors.New("feed unreachable")
	f.runQueuedRefresh("source-beta")
	var refreshed sql.NullTime
	if err := f.db.QueryRow(`SELECT last_refresh_at FROM rss_source_runtime WHERE rss_source_id = 'source-beta'`).Scan(&refreshed); err != nil || !refreshed.Valid {
		t.Fatalf("a failed refresh records the refresh: %v %v", refreshed, err)
	}
	if f.liveCompetitions() != 1 {
		t.Fatalf("the failed first refresh must enqueue the competition: %d", f.liveCompetitions())
	}
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("a-720")
	if !f.firstRoundDone() {
		t.Fatal("the decided round is the instance's first round")
	}
}

// After the first round, a release that arrives on one source is decided as
// soon as its evaluation ends, without waiting for any other source to refresh
// again; it is compared with the running download and kept out of its
// episode. A torrent fetch failure is a failed verdict of its own release and
// holds no round.
func TestCompetitionIncrementalRoundWaitsOnlyForEvaluationsPostgres(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
	f.evaluate("a-720")
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("a-720")

	// Later: beta brings episode 1 (better) and episode 2; a third item's
	// torrent could not be fetched.
	f.addRelease("b-1080", "source-beta", "[Beta] Test Show - 01 [1080p]", 1)
	f.addRelease("b-02", "source-beta", "[Beta] Test Show - 02 [1080p]", 2)
	insertReleaseForEvaluation(t, f.db, "source-beta", "b-03", "[Beta] Test Show - 03 [1080p]", "https://downloads.example.test/b-03.torrent")
	if _, err := f.db.Exec(`UPDATE releases SET internal_facts = internal_facts || '{"torrent_error_code":"torrent_metadata_unavailable","torrent_error_reason":"fetch failed"}'::jsonb WHERE id = 'b-03'`); err != nil {
		t.Fatal(err)
	}
	f.evaluate("b-03")
	assertReleaseState(t, f.db, "b-03", "parse_failed", "torrent_metadata_unavailable", false)
	f.evaluate("b-1080", "b-02")
	f.requireAwaiting("b-1080", "b-02")
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("b-02")
	f.requireCoveredInFlight("b-1080")
	assertReleaseState(t, f.db, "b-03", "parse_failed", "torrent_metadata_unavailable", false)

	// The better release takes the episode once the running download is
	// placed: the settle's round admits it as a replacement.
	placeCurrentDownload(t, f.db, "a-720")
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("b-1080")
	var verdict string
	if err := f.db.QueryRow(`SELECT verdict FROM replacement_decisions WHERE candidate_release_id = 'b-1080' AND state = 'admitted'`).Scan(&verdict); err != nil || verdict != "replace" {
		t.Fatalf("the better release must be admitted to replace the placed one: %q %v", verdict, err)
	}
}

// A new release that is not better than the placed file is covered by it;
// one that is better is admitted as a replacement (洗版放行) in the round of
// its own evaluation.
func TestCompetitionReplacementAdmissionPostgres(t *testing.T) {
	for _, tc := range []struct {
		name   string
		title  string
		better bool
	}{
		{name: "better", title: "[Beta] Test Show - 01 [1080p]", better: true},
		{name: "not_better", title: "[Beta] Test Show - 01 [480p]", better: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCompetitionFixture(t, "single_copy")
			f.markRefreshed("source-rss-test", "source-beta")
			f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
			f.evaluate("a-720")
			f.mustRunCompetition("competition_decided")
			f.requireAdmitted("a-720")
			placeCurrentDownload(t, f.db, "a-720")
			// The settle's round finds nothing to decide.
			f.mustRunCompetition("no_candidate")

			f.addRelease("b-new", "source-beta", tc.title, 1)
			f.evaluate("b-new")
			f.requireAwaiting("b-new")
			f.mustRunCompetition("competition_decided")
			if tc.better {
				f.requireAdmitted("b-new")
				var verdict string
				if err := f.db.QueryRow(`SELECT verdict FROM replacement_decisions WHERE candidate_release_id = 'b-new' AND state = 'admitted'`).Scan(&verdict); err != nil || verdict != "replace" {
					t.Fatalf("the better release must be admitted to replace: %q %v", verdict, err)
				}
				return
			}
			assertReleaseState(t, f.db, "b-new", "filtered", batchplan.ReasonNoPlaceableFiles, false)
			requireOutcome(t, f.db, "b-new", batchplan.OutcomeCoveredExisting)
			if _, ran := f.runCompetition(); ran {
				t.Fatal("a covered release must not enqueue another round")
			}
		})
	}
}

// A site policy change re-ranks only the releases still waiting: a waiting
// release decides by the new scores, while the admitted download of another
// episode is left alone.
func TestCompetitionSitePolicyReRanksOnlyWaitingReleasesPostgres(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("a-02", "source-rss-test", "[Alpha] Test Show - 02 [1080p]", 2)
	f.evaluate("a-02")
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("a-02")

	// Episode 1 arrives from both groups; the two releases wait for the
	// round their evaluations enqueued.
	f.addRelease("a-01", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	f.addRelease("b-01", "source-beta", "[Beta] Test Show - 01 [720p]", 1)
	f.evaluate("a-01", "b-01")
	f.requireAwaiting("a-01", "b-01")

	// The site policy now scores beta above alpha (the recompute writes the
	// effective scores and enqueues the waiting instance's competition).
	if _, err := f.db.Exec(`UPDATE provider_group_selections SET effective_score = CASE group_ref WHEN 'beta' THEN 90 ELSE 10 END, version = version + 1`); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if enqueued, err := competition.EnqueueTx(context.Background(), tx, "anime-rss-test", competition.TriggerSitePolicy, competition.WhenAwaiting); err != nil || !enqueued {
		t.Fatalf("a site policy change must enqueue the waiting instance: %v %v", enqueued, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("b-01")
	f.requireCoveredInFlight("a-01")
	// The admitted episode 2 keeps its one download.
	var tasks int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM download_tasks WHERE release_id = 'a-02'`).Scan(&tasks); err != nil || tasks != 1 {
		t.Fatalf("the admitted download must be left alone: %d %v", tasks, err)
	}
	// With nothing waiting, a policy change enqueues nothing.
	tx, err = f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if enqueued, err := competition.EnqueueTx(context.Background(), tx, "anime-rss-test", competition.TriggerSitePolicy, competition.WhenAwaiting); err != nil || enqueued {
		t.Fatalf("no waiting release, no competition: %v %v", enqueued, err)
	}
}

// An explicit retry in an active subscription puts the release back into the
// competition; in a stopped subscription it keeps the direct admission.
func TestCompetitionRetryAwaitsInActiveSubscriptionPostgres(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
	f.evaluate("a-720")
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("a-720")
	failCurrentDownload(t, "a-720")
	f.mustRunCompetition("no_candidate")

	result, err := f.service.RetryRelease(context.Background(), "a-720", "retry-key-competition-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != RetryOutcomeAwaiting || result.Verdict != competition.VerdictAwaiting {
		t.Fatalf("retry in an active subscription = %+v, want awaiting", result)
	}
	if f.liveCompetitions() != 1 {
		t.Fatalf("the retry must enqueue the competition: %d", f.liveCompetitions())
	}
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("a-720")
	if f.taskCount() != 2 {
		t.Fatalf("download tasks = %d, want the failed one and the retry", f.taskCount())
	}
}

// placeCurrentDownload completes a release's download and records its file
// as the verified active placement of its episode, the state a finished
// import leaves.
func placeCurrentDownload(t *testing.T, db *sql.DB, releaseID string) {
	t.Helper()
	var key string
	if err := db.QueryRow(`SELECT coverage_keys->>0 FROM release_candidate_facts WHERE release_id = $1 ORDER BY release_version DESC LIMIT 1`, releaseID).Scan(&key); err != nil || key == "" {
		t.Fatalf("read the coverage of %s: %q %v", releaseID, key, err)
	}
	owner := ownerTestDB(t)
	if _, err := owner.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, snapshot_generation,
 verification_state, state, replacement_eligible, version, created_at, updated_at)
VALUES ('plc-'||$1, '/library/competition/'||$1||'.mkv', 'anime-rss-test', 'reconciliation', 'obs-'||$1, 'fp-'||$1, 'fp-'||$1, $1, 'facts-v1', 1, 'verified', 'active', true, 1, now(), now())`, releaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(`INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref) VALUES ('plc-'||$1, $2, 0, 'facts-v1', 'obs-'||$1)`, releaseID, key); err != nil {
		t.Fatal(err)
	}
	completeCurrentDownload(t, releaseID, "succeeded")
}
