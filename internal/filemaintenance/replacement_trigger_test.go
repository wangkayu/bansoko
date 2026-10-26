package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/library/domain"
	"bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	reconciliationapplication "bansoko/internal/reconciliation/application"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

// recordingRecorder captures observable diagnostic writes for trigger tests.
type recordingRecorder struct {
	mu        sync.Mutex
	failures  []port.DiagnosticFailureRequest
	successes []port.DiagnosticSuccessRequest
}

func (r *recordingRecorder) RecordDiagnosticFailure(_ context.Context, req port.DiagnosticFailureRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, req)
	return nil
}
func (r *recordingRecorder) RecordDiagnosticSuccess(_ context.Context, req port.DiagnosticSuccessRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.successes = append(r.successes, req)
	return nil
}
func (r *recordingRecorder) UpsertRoutine(context.Context, port.RoutineRequest) error { return nil }
func (r *recordingRecorder) UpsertRoutineTx(context.Context, txscope.Tx, port.RoutineRequest) error {
	return nil
}
func (r *recordingRecorder) RecordDiagnosticFailureTx(context.Context, txscope.Tx, port.DiagnosticFailureRequest) error {
	return nil
}
func (r *recordingRecorder) RecordDiagnosticSuccessTx(context.Context, txscope.Tx, port.DiagnosticSuccessRequest) error {
	return nil
}

func (r *recordingRecorder) failureCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.failures)
}

// newTriggerTestDB resets the disposable schema and seeds one active anime with
// its instance retention policy (per_group, ranks).
func strPtr(value string) *string {
	return &value
}

func intPtr(value int) *int {
	return &value
}

func newTriggerTestDB(t *testing.T) (*sql.DB, *workitems.Store, *ReplacementTriggerService) {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	seedAnimeSubscription(t, db, "anime-1", "sub-1", "active")
	seedInstanceRetention(t, db, "anime-1", ModePerGroup,
		`{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]}]}`, 1)
	store, err := workitems.NewStore(db, workitems.DefaultRetentionDays, port.NopRuntimeLogger{})
	if err != nil {
		t.Fatal(err)
	}
	trigger := NewReplacementTriggerService(db, store)
	return db, store, trigger
}

// liveEvaluateItem scans the one live replacement_evaluate item of an anime.
type liveEvaluateItem struct {
	ID                  string
	IdempotencyKey      string
	InputSnapshot       map[string]any
	Trigger             string
	Epoch               int64
	SubscriptionVersion int64
}

func loadLiveEvaluateItem(t *testing.T, db *sql.DB, animeID string) *liveEvaluateItem {
	t.Helper()
	var (
		item       liveEvaluateItem
		snapshot   []byte
		trigger    sql.NullString
		subVersion sql.NullInt64
	)
	err := db.QueryRow(`
SELECT id, idempotency_key, input_snapshot, input_snapshot->>'trigger', (input_snapshot->>'subscription_version')::bigint
FROM work_items
WHERE owner_type = 'file_maintenance' AND owner_id = $1 AND kind = 'replacement_evaluate'
  AND state IN ('available', 'leased')
ORDER BY created_at DESC LIMIT 1`, animeID).
		Scan(&item.ID, &item.IdempotencyKey, &snapshot, &trigger, &subVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	item.Trigger = trigger.String
	item.Epoch = subVersion.Int64 // the epoch equals the subscription version
	item.SubscriptionVersion = subVersion.Int64
	if err := json.Unmarshal(snapshot, &item.InputSnapshot); err != nil {
		t.Fatal(err)
	}
	return &item
}

func countLiveEvaluateItems(t *testing.T, db *sql.DB, animeID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`
SELECT COUNT(*) FROM work_items
WHERE owner_type = 'file_maintenance' AND owner_id = $1 AND kind = 'replacement_evaluate'
  AND state IN ('available', 'leased')`, animeID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// ---------------------------------------------------------------------------
// Placement after-commit trigger
// ---------------------------------------------------------------------------

func TestTriggerPlacementCommittedCreatesLiveEvaluateItem(t *testing.T) {
	db, _, trigger := newTriggerTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-r1", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")

	if err := trigger.TriggerPlacementCommitted(ctx, "anime-1", "release-r1"); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	item := loadLiveEvaluateItem(t, db, "anime-1")
	if item == nil {
		t.Fatal("active placement must create a live evaluate item")
	}
	if item.IdempotencyKey != "replacement_evaluate:anime-1" {
		t.Fatalf("stable idempotency key expected: %s", item.IdempotencyKey)
	}
	if item.Trigger != TriggerPlacementCommitted {
		t.Fatalf("trigger identity = %q, want %q", item.Trigger, TriggerPlacementCommitted)
	}
	if item.Epoch != 1 || item.SubscriptionVersion != 1 {
		t.Fatalf("epoch/subscription version must freeze at trigger time: %d/%d", item.Epoch, item.SubscriptionVersion)
	}
	if item.InputSnapshot["candidate_release_id"] != "release-r1" || item.InputSnapshot["trigger"] != TriggerPlacementCommitted {
		t.Fatalf("input snapshot must carry the candidate and reason: %+v", item.InputSnapshot)
	}
}

func TestTriggerPlacementCommittedStoppedSkips(t *testing.T) {
	db, _, trigger := newTriggerTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-r1", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")

	if err := trigger.TriggerPlacementCommitted(ctx, "anime-1", "release-r1"); err != nil {
		t.Fatalf("stopped trigger must be a safe no-op, got %v", err)
	}
	if item := loadLiveEvaluateItem(t, db, "anime-1"); item != nil {
		t.Fatalf("stopped subscription must not create an evaluate item: %+v", item)
	}
	placements, cleanups, superseded := decisionCounts(t, db, "anime-1")
	if placements != 1 || cleanups != 0 || superseded != 0 {
		t.Fatalf("stopped admission keeps the placement but zero replacement effects: placements=%d cleanups=%d superseded=%d", placements, cleanups, superseded)
	}
}

// ---------------------------------------------------------------------------
// Instance policy save trigger (through the HTTP adapter service)
// ---------------------------------------------------------------------------

func TestTriggerPolicySavedOnlyTargetAnime(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	// Two active instances with identical policy v1; only anime-1 is saved.
	seedAnimeSubscription(t, db, "anime-1", "sub-1", "active")
	seedInstanceRetention(t, db, "anime-1", ModePerGroup,
		`{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]}]}`, 1)
	seedAnimeSubscription(t, db, "anime-2", "sub-2", "active")
	seedInstanceRetention(t, db, "anime-2", ModePerGroup,
		`{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]}]}`, 1)
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-r1", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	seedFilePlacement(t, db, "anime-2", "sub-2", "release-r2", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")

	store, err := workitems.NewStore(db, workitems.DefaultRetentionDays, port.NopRuntimeLogger{})
	if err != nil {
		t.Fatal(err)
	}
	trigger := NewReplacementTriggerService(db, store)
	service := NewService(NewPolicyService(db), NewRetentionPolicyService(db), nil, nil, nil)
	service.SetReplacementTrigger(trigger)

	saved, err := service.SaveInstanceRetentionPolicy(ctx, "anime-1", generated.InstanceRetentionPolicyInput{
		AdmissionMode:   generated.RetentionAdmissionModeSingleCopy,
		QualityProfile:  generated.QualityProfile{Version: "profile-v1", Items: []generated.QualityCompareItem{{Key: "release.resolution", Ranks: [][]string{{"2160p"}, {"1080p"}, {"720p"}, {"480p"}}}}},
		ExpectedVersion: 1,
	}, "test", "http")
	if err != nil {
		t.Fatalf("policy save: %v", err)
	}
	if saved.Version != 2 {
		t.Fatalf("policy version must advance to 2, got %d", saved.Version)
	}
	// Everything of the target anime is placed: a policy change has no
	// unplaced candidate to re-decide and creates no item.
	if item := loadLiveEvaluateItem(t, db, "anime-1"); item != nil {
		t.Fatalf("a policy change without an unplaced candidate must not evaluate: %+v", item)
	}
	// A release still downloading is re-decided under the new policy.
	if _, err := disposabledb.Owner(t).Exec(`UPDATE acquisitions SET entry_kind = 'subscription_add', state = 'running' WHERE source_release_ref = 'release-r1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveInstanceRetentionPolicy(ctx, "anime-1", generated.InstanceRetentionPolicyInput{
		AdmissionMode:   generated.RetentionAdmissionModePerGroup,
		QualityProfile:  generated.QualityProfile{Version: "profile-v1", Items: []generated.QualityCompareItem{{Key: "release.resolution", Ranks: [][]string{{"2160p"}, {"1080p"}, {"720p"}, {"480p"}}}}},
		ExpectedVersion: 2,
	}, "test", "http"); err != nil {
		t.Fatalf("policy save: %v", err)
	}
	item := loadLiveEvaluateItem(t, db, "anime-1")
	if item == nil || item.Trigger != TriggerPolicySaved {
		t.Fatalf("target anime must carry a policy_saved item: %+v", item)
	}
	if item.InputSnapshot["candidate_release_id"] != "release-r1" {
		t.Fatalf("policy trigger must resolve the in-flight release: %+v", item.InputSnapshot)
	}
	// Non-target anime: policy, decision and WorkItem counts unchanged.
	if item := loadLiveEvaluateItem(t, db, "anime-2"); item != nil {
		t.Fatalf("non-target anime must not receive an item: %+v", item)
	}
	var policyVersion, decisions int
	if err := db.QueryRow(`SELECT retention_version FROM anime_business_configs WHERE managed_anime_id = 'anime-2'`).Scan(&policyVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_decisions WHERE managed_anime_id = 'anime-2'`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if policyVersion != 1 || decisions != 0 {
		t.Fatalf("non-target anime must be untouched: policy=%d decisions=%d", policyVersion, decisions)
	}
}

func TestTriggerPolicySavedStoppedKeepsPolicyThenResumeNewEpoch(t *testing.T) {
	db, store, trigger := newTriggerTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-r1", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	if _, err := db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}

	service := NewService(NewPolicyService(db), NewRetentionPolicyService(db), nil, nil, nil)
	service.SetReplacementTrigger(trigger)
	if _, err := service.SaveInstanceRetentionPolicy(ctx, "anime-1", generated.InstanceRetentionPolicyInput{
		AdmissionMode:   generated.RetentionAdmissionModeSingleCopy,
		QualityProfile:  generated.QualityProfile{Version: "profile-v1", Items: []generated.QualityCompareItem{}},
		ExpectedVersion: 1,
	}, "test", "http"); err != nil {
		t.Fatalf("stopped policy save: %v", err)
	}
	if item := loadLiveEvaluateItem(t, db, "anime-1"); item != nil {
		t.Fatalf("stopped policy save must create no evaluate item: %+v", item)
	}
	var policyVersion int64
	if err := db.QueryRow(`SELECT retention_version FROM anime_business_configs WHERE managed_anime_id = 'anime-1'`).Scan(&policyVersion); err != nil {
		t.Fatal(err)
	}
	if policyVersion != 2 {
		t.Fatalf("stopped policy save must still persist the owner truth: version=%d", policyVersion)
	}

	// Resume creates a fresh epoch (subscription version bumped by the stop).
	if _, err := db.Exec(`UPDATE subscriptions SET state = 'active', version = version + 1 WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}
	if err := trigger.TriggerSubscriptionResumed(ctx, "anime-1"); err != nil {
		t.Fatalf("resume trigger: %v", err)
	}
	item := loadLiveEvaluateItem(t, db, "anime-1")
	if item == nil {
		t.Fatal("resume must create the new epoch item")
	}
	if item.Trigger != TriggerSubscriptionResumed || item.Epoch != 3 || item.SubscriptionVersion != 3 {
		t.Fatalf("resume epoch must be the new subscription version: trigger=%s epoch=%d sub=%d", item.Trigger, item.Epoch, item.SubscriptionVersion)
	}
	if item.InputSnapshot["candidate_release_id"] != "release-r1" {
		t.Fatalf("resume must read the paused-period placement facts: %+v", item.InputSnapshot)
	}

	// Repeated resume notifications merge into the one live item.
	if err := trigger.TriggerSubscriptionResumed(ctx, "anime-1"); err != nil {
		t.Fatalf("replay resume trigger: %v", err)
	}
	if count := countLiveEvaluateItems(t, db, "anime-1"); count != 1 {
		t.Fatalf("repeated resume must keep exactly one live item: %d", count)
	}

	// Consume the item through the real worker path, then a later notification
	// creates a fresh single item again (never two live at once).
	service2 := NewReplacementDecisionService(db)
	cleanup := NewCleanupService(db)
	cleanup.SetWorkItems(store)
	service2.SetCleanupHandoff(cleanup)
	claim, err := store.ClaimDue(ctx, "trigger-test-worker", []string{workitems.KindReplacementEvaluate}, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	outcome, err := service2.EvaluateHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("resume evaluate must succeed: %+v", outcome)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := trigger.TriggerSubscriptionResumed(ctx, "anime-1"); err != nil {
		t.Fatalf("post-consumption trigger: %v", err)
	}
	if count := countLiveEvaluateItems(t, db, "anime-1"); count != 1 {
		t.Fatalf("post-consumption notification must create one new live item: %d", count)
	}
}

// ---------------------------------------------------------------------------
// Merge semantics: one live item, latest unexecuted input
// ---------------------------------------------------------------------------

func TestTriggerMergesLatestInputAcrossTriggers(t *testing.T) {
	db, _, trigger := newTriggerTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-r1", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-r2", []string{"tv:s1:e1"},
		Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")

	if err := trigger.TriggerPlacementCommitted(ctx, "anime-1", "release-r1"); err != nil {
		t.Fatal(err)
	}
	// A later placement trigger merges into the same live item and carries
	// the latest unexecuted input.
	if err := trigger.TriggerPlacementCommitted(ctx, "anime-1", "release-r2"); err != nil {
		t.Fatal(err)
	}
	if count := countLiveEvaluateItems(t, db, "anime-1"); count != 1 {
		t.Fatalf("independent triggers must merge into one live item: %d", count)
	}
	item := loadLiveEvaluateItem(t, db, "anime-1")
	if item.InputSnapshot["candidate_release_id"] != "release-r2" {
		t.Fatalf("merged item must carry the latest unexecuted input: %+v", item.InputSnapshot)
	}
	// A policy trigger never overwrites a pending item: it reads the new
	// policy when it runs, and its placed candidate keeps being evaluated.
	if _, err := disposabledb.Owner(t).Exec(`UPDATE acquisitions SET entry_kind = 'subscription_add', state = 'running' WHERE source_release_ref = 'release-r1'`); err != nil {
		t.Fatal(err)
	}
	if err := trigger.TriggerPolicySaved(ctx, "anime-1"); err != nil {
		t.Fatal(err)
	}
	if count := countLiveEvaluateItems(t, db, "anime-1"); count != 1 {
		t.Fatalf("a policy trigger must not add a live item: %d", count)
	}
	item = loadLiveEvaluateItem(t, db, "anime-1")
	if item.InputSnapshot["candidate_release_id"] != "release-r2" || item.Trigger != TriggerPlacementCommitted {
		t.Fatalf("a pending item must keep its input: %+v", item.InputSnapshot)
	}
}

func TestTriggerConcurrentNotificationsKeepSingleLiveItem(t *testing.T) {
	db, _, trigger := newTriggerTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-r1", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := trigger.TriggerPlacementCommitted(ctx, "anime-1", "release-r1"); err != nil {
				errs <- err
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := trigger.TriggerPolicySaved(ctx, "anime-1"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent trigger failed: %v", err)
	}
	if count := countLiveEvaluateItems(t, db, "anime-1"); count != 1 {
		t.Fatalf("concurrent notifications must merge into exactly one live item: %d", count)
	}
}

// ---------------------------------------------------------------------------
// Completed-generation replacement consumer gate
// ---------------------------------------------------------------------------

func TestTriggerGenerationCompletedGatedByActive(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	seedAnimeSubscription(t, db, "anime-stopped", "sub-s", "stopped")
	seedInstanceRetention(t, db, "anime-stopped", ModePerGroup, ``, 1)
	seedAnimeSubscription(t, db, "anime-active", "sub-a", "active")
	seedInstanceRetention(t, db, "anime-active", ModePerGroup, ``, 1)
	seedFilePlacement(t, db, "anime-stopped", "sub-s", "release-s", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	seedFilePlacement(t, db, "anime-active", "sub-a", "release-a", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")

	store, err := workitems.NewStore(db, workitems.DefaultRetentionDays, port.NopRuntimeLogger{})
	if err != nil {
		t.Fatal(err)
	}
	trigger := NewReplacementTriggerService(db, store)

	// The replacement consumer of a completed generation is gated: a stopped
	// instance gets no evaluate item...
	if err := trigger.TriggerGenerationCompleted(ctx, "anime-stopped"); err != nil {
		t.Fatalf("stopped generation consumer must be a no-op, got %v", err)
	}
	if item := loadLiveEvaluateItem(t, db, "anime-stopped"); item != nil {
		t.Fatalf("stopped replacement consumer must create nothing: %+v", item)
	}
	// ...while an active instance consumes the generation normally.
	if err := trigger.TriggerGenerationCompleted(ctx, "anime-active"); err != nil {
		t.Fatal(err)
	}
	item := loadLiveEvaluateItem(t, db, "anime-active")
	if item == nil || item.Trigger != TriggerGenerationCompleted {
		t.Fatalf("active generation consumer must merge an item: %+v", item)
	}

	// The neutral scan consumer is independent of the gate: a stopped anime
	// still schedules its reconciliation_scan WorkItem (zero replacement rows).
	reconService, err := reconciliationapplication.NewService(db, store, stubScanner{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconService.SubmitScan(ctx, reconciliationapplication.ScanSubmitInput{
		ManagedAnimeID: "anime-stopped", TreeTop: strPtr("/library"),
	}); err != nil {
		t.Fatalf("stopped scan consumer must not be gated: %v", err)
	}
	var scanItems int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE owner_id = 'anime-stopped' AND kind = 'reconciliation_scan' AND state IN ('available', 'leased')`).Scan(&scanItems); err != nil {
		t.Fatal(err)
	}
	if scanItems != 1 {
		t.Fatalf("neutral scan consumer must be ungated: scan items=%d", scanItems)
	}
	if count := countLiveEvaluateItems(t, db, "anime-stopped"); count != 0 {
		t.Fatalf("replacement counts must stay zero for the stopped instance: %d", count)
	}
}

// stubScanner is the read-only scan port for the neutral-consumer assertion.
type stubScanner struct{}

func (stubScanner) Scan(context.Context, domain.ScanRequest) (domain.ScanResult, error) {
	return domain.ScanResult{}, nil
}

// ---------------------------------------------------------------------------
// Observable trigger failure
// ---------------------------------------------------------------------------

func TestTriggerFailureRecordsObservableFailure(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	seedAnimeSubscription(t, db, "anime-1", "sub-1", "active")
	seedInstanceRetention(t, db, "anime-1", ModePerGroup, ``, 1)

	rec := &recordingRecorder{}
	trigger := NewReplacementTriggerService(db, nil) // store missing -> trigger cannot merge
	trigger.SetDiagnostics(rec, port.NopRuntimeLogger{})
	if err := trigger.TriggerPlacementCommitted(ctx, "anime-1", "release-r1"); err == nil {
		t.Fatal("unwired store must fail the trigger")
	}
	identity := port.ReplacementTriggerFailure()
	if len(rec.failures) == 0 || rec.failures[0].Identity != identity || rec.failures[0].Severity != port.SeverityError {
		t.Fatalf("diagnostic identity expected: %+v", rec.failures)
	}
	if rec.failures[0].Identity.ScopeKind != port.ScopeOwner || rec.failures[0].ScopeID != "anime-1" || rec.failures[0].ManagedAnimeID != "anime-1" {
		t.Fatalf("diagnostic owner scope expected: %+v", rec.failures[0])
	}
	store, err := workitems.NewStore(db, workitems.DefaultRetentionDays, port.NopRuntimeLogger{})
	if err != nil {
		t.Fatal(err)
	}
	trigger.workItems = store
	if err := trigger.TriggerPlacementCommitted(ctx, "anime-1", "release-r1"); err != nil {
		t.Fatalf("recovered trigger should merge successfully: %v", err)
	}
	if len(rec.successes) != 1 || rec.successes[0].Identity != identity || rec.successes[0].Identity.ScopeKind != port.ScopeOwner || rec.successes[0].ScopeID != "anime-1" {
		t.Fatalf("diagnostic recovery close expected same identity and owner scope: %+v", rec.successes)
	}
	if rec.failures[0].Identity.ScopeKind != port.ScopeOwner || rec.failures[0].ScopeID != "anime-1" || rec.failures[0].ManagedAnimeID != "anime-1" {
		t.Fatalf("diagnostic owner scope expected: %+v", rec.failures[0])
	}
	// Recovery admits the merged item after closing the original diagnostic identity.
	if item := loadLiveEvaluateItem(t, db, "anime-1"); item == nil {
		t.Fatal("recovered trigger must create a live evaluate item")
	}
	var decisions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_decisions`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 {
		t.Fatalf("failed trigger must not evaluate: %d", decisions)
	}
}

// ---------------------------------------------------------------------------
// Stop / evaluate / settle races
// ---------------------------------------------------------------------------

func TestEvaluateThenStopBeforeSettleMarksStaleZeroEffects(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	oldPlacement := seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:s1:e1"},
		Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")

	service := NewReplacementDecisionService(db)
	view, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if view.Verdict != "replace" || view.State != "admitted" {
		t.Fatalf("expected an admitted replace decision: %s/%s", view.Verdict, view.State)
	}

	// Stop between evaluate and settle: the version bump + state change make
	// the frozen decision stale; zero new destructive effects.
	if _, err := db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}
	cleanup := NewCleanupService(db)
	service.SetCleanupHandoff(cleanup)
	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatalf("settle after stop: %v", err)
	}
	var staleState, staleReason string
	if err := db.QueryRow(`SELECT state, stale_reason FROM replacement_decisions WHERE id = $1`, view.Id).Scan(&staleState, &staleReason); err != nil {
		t.Fatal(err)
	}
	if staleState != "stale" || staleReason != staleSubscriptionStopped {
		t.Fatalf("decision must be stale with %s, got %s/%s", staleSubscriptionStopped, staleState, staleReason)
	}
	if state := placementState(t, db, oldPlacement); state != "active" {
		t.Fatalf("old placement must stay active: %s", state)
	}
	if state := placementState(t, db, candidate); state != "active" {
		t.Fatalf("candidate placement must stay active: %s", state)
	}
	var cleanups int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cleanup_operations`).Scan(&cleanups); err != nil {
		t.Fatal(err)
	}
	if cleanups != 0 {
		t.Fatalf("stale settle must create zero cleanup operations: %d", cleanups)
	}
	// The stale decision is never re-settled after a resume: a new epoch
	// creates a NEW decision with the fresh subscription version instead.
	if _, err := db.Exec(`UPDATE subscriptions SET state = 'active', version = version + 1 WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}
	resumed, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
	if err != nil {
		t.Fatalf("resumed evaluate: %v", err)
	}
	if resumed.Id == view.Id {
		t.Fatalf("resume must never reuse the pre-stop decision id: %s", view.Id)
	}
	if err := db.QueryRow(`SELECT state FROM replacement_decisions WHERE id = $1`, view.Id).Scan(&staleState); err != nil {
		t.Fatal(err)
	}
	if staleState != "stale" {
		t.Fatalf("pre-stop decision must stay stale: %s", staleState)
	}
	if state := placementState(t, db, oldPlacement); state != "active" {
		t.Fatalf("old placement must never be superseded by the stale decision: %s", state)
	}
}

func TestStopRaceEvaluateSettleNeverProducesEffect(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:s1:e1"},
		Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")
	service := NewReplacementDecisionService(db)
	cleanup := NewCleanupService(db)
	service.SetCleanupHandoff(cleanup)

	for round := 0; round < 10; round++ {
		if _, err := db.Exec(`UPDATE subscriptions SET state = 'active', version = version + 1 WHERE managed_anime_id = 'anime-1'`); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		var evalErr error
		go func() {
			defer wg.Done()
			_, evalErr = evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
		}()
		go func() {
			defer wg.Done()
			if _, err := db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE managed_anime_id = 'anime-1'`); err != nil {
				t.Errorf("stop: %v", err)
			}
		}()
		wg.Wait()
		// Whatever won the race, settle must never produce a destructive effect:
		// no decision (evaluate saw stopped) or a stale/blocked one.
		_ = service.SettleForPlacement(ctx, candidate)
		var superseded, cleanups int
		if err := db.QueryRow(`SELECT COUNT(*) FROM placements WHERE managed_anime_id = 'anime-1' AND state = 'superseded'`).Scan(&superseded); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM cleanup_operations WHERE managed_anime_id = 'anime-1'`).Scan(&cleanups); err != nil {
			t.Fatal(err)
		}
		if superseded != 0 || cleanups != 0 {
			t.Fatalf("round %d: stop race must yield zero destructive effects: superseded=%d cleanups=%d evalErr=%v", round, superseded, cleanups, evalErr)
		}
		var bad int
		if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_decisions WHERE managed_anime_id = 'anime-1' AND state IN ('settled')`).Scan(&bad); err != nil {
			t.Fatal(err)
		}
		if bad != 0 {
			t.Fatalf("round %d: no decision may settle across the stop race: %d", round, bad)
		}
		var business *apperrors.Business
		if evalErr != nil && !(errors.As(evalErr, &business) && business.Code == codeSubscriptionStopped) {
			t.Fatalf("round %d: evaluate may only fail with SUBSCRIPTION_STOPPED: %v", round, evalErr)
		}
	}
}
