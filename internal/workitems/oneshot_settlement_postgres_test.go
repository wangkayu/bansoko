package workitems

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"bansoko/internal/persistence/txscope"
)

type oneShotSettlementFixture struct {
	db       *sql.DB
	store    *Store
	markerID string
	foID     string
	itemID   string
	claim    Claim
	lease    AdmissionLease
}

// newOneShotSettlementFixture creates one admitted live file-operation item.
// The fixture uses the shared owner-chain helpers so every settlement test
// exercises the same guarded disposable schema and marker-first admission path.
func newOneShotSettlementFixture(t *testing.T, markerID string) oneShotSettlementFixture {
	t.Helper()
	db := openTestDB(t)
	store := newTestStore(t, db)
	ctx := context.Background()
	files := createOneShotChain(t, db, markerID, []string{"settlement.mkv"})
	insertOneShotMarker(t, db, markerID, OneShotStateRunning, nil)
	foID := "fo-" + markerID
	insertOneShotFileOperation(t, db, foID, "art-"+markerID, files[0], "corr-"+markerID)

	ref := OneShotRef{MarkerID: markerID, CorrelationID: "corr-" + markerID, AdmissionEpoch: 1}
	snapshot, err := json.Marshal(map[string]string{
		"operation_id": foID,
		"artifact_id":  "art-" + markerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	itemID, created, err := store.CreateOrMergeOneShot(ctx, txscope.Wrap(tx), ref, WorkItemSpec{
		Owner:                OwnerRef{Type: OwnerFileOperation, ID: foID},
		Kind:                 KindFileExecute,
		IdempotencyKey:       "idem-fo-" + foID,
		InputSnapshot:        snapshot,
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
	})
	if err != nil {
		t.Fatalf("create one-shot settlement item: %v", err)
	}
	if !created || itemID == "" {
		t.Fatalf("settlement fixture item = %q created=%t, want a new item", itemID, created)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	claim, err := store.ClaimDue(ctx, "oneshot-settlement-worker", []string{KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatalf("claim one-shot settlement item: %v", err)
	}
	if claim.ID != itemID {
		t.Fatalf("settlement claim = %q, want %q", claim.ID, itemID)
	}
	lease, err := store.AdmitOneShotEffect(ctx, claim)
	if err != nil {
		t.Fatalf("admit one-shot settlement effect: %v", err)
	}
	if lease.AdmissionID == "" {
		t.Fatal("admitted settlement lease has no admission id")
	}
	return oneShotSettlementFixture{
		db: db, store: store, markerID: markerID, foID: foID,
		itemID: itemID, claim: claim, lease: lease,
	}
}

type oneShotSettlementMarkerSnapshot struct {
	state   string
	epoch   int64
	version int64
	count   int
	leases  []oneShotLedgerEntry
}

func readOneShotSettlementMarker(t *testing.T, db *sql.DB, markerID string) oneShotSettlementMarkerSnapshot {
	t.Helper()
	var snapshot oneShotSettlementMarkerSnapshot
	var raw []byte
	if err := db.QueryRowContext(context.Background(), `
SELECT state, admission_epoch, version, active_effect_count, active_effect_leases
FROM one_shot_acquisitions WHERE id = $1`, markerID).Scan(
		&snapshot.state, &snapshot.epoch, &snapshot.version, &snapshot.count, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &snapshot.leases); err != nil {
			t.Fatalf("decode settlement marker ledger: %v", err)
		}
	}
	return snapshot
}

func readOneShotSettlementOwnerVersion(t *testing.T, db *sql.DB, foID string) int64 {
	t.Helper()
	var version int64
	if err := db.QueryRowContext(context.Background(), `SELECT version FROM file_operations WHERE id = $1`, foID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

// reclaimSettlementClaim advances the WorkItem fencing token while preserving
// the original admission lease. The old claim is then stale without touching
// the marker, owner, or external-effect evidence.
func reclaimSettlementClaim(t *testing.T, fixture oneShotSettlementFixture) Claim {
	t.Helper()
	ctx := context.Background()
	if _, err := workItemFixtureDB(t).ExecContext(ctx, `
UPDATE work_items
SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second'
WHERE id = $1`, fixture.itemID); err != nil {
		t.Fatal(err)
	}
	current, err := fixture.store.ClaimDue(ctx, "oneshot-settlement-reclaimer", []string{KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatalf("reclaim one-shot settlement item: %v", err)
	}
	if current.ID != fixture.itemID || current.FencingToken != fixture.claim.FencingToken+1 {
		t.Fatalf("reclaimed settlement claim = %+v, want item %s fencing token %d", current, fixture.itemID, fixture.claim.FencingToken+1)
	}
	return current
}

func assertOneShotSettlementLeaseUnchanged(t *testing.T, got, want oneShotSettlementMarkerSnapshot) {
	t.Helper()
	if got.count != want.count || len(got.leases) != len(want.leases) {
		t.Fatalf("settlement ledger = count %d leases %v, want count %d leases %v", got.count, got.leases, want.count, want.leases)
	}
	for index := range want.leases {
		if got.leases[index] != want.leases[index] {
			t.Fatalf("settlement lease %d = %+v, want %+v", index, got.leases[index], want.leases[index])
		}
	}
}

// An ambiguous disposition must not consume its lease. If the worker loses its
// WorkItem fence, the settlement transaction must roll back rather than leave
// a partial marker/owner/item transition behind.
func TestRetryOneShotAmbiguousStaleRollsBack(t *testing.T) {
	fixture := newOneShotSettlementFixture(t, "m-settlement-ambiguous-stale")
	beforeMarker := readOneShotSettlementMarker(t, fixture.db, fixture.markerID)
	beforeOwnerVersion := readOneShotSettlementOwnerVersion(t, fixture.db, fixture.foID)
	current := reclaimSettlementClaim(t, fixture)

	ownerCalls := 0
	writeOwner := func(context.Context, txscope.Tx, OwnerResult) (int64, error) {
		ownerCalls++
		return 0, errors.New("stale ambiguous settlement must not write the owner")
	}
	if _, err := fixture.store.RetryOneShot(
		context.Background(), fixture.claim, fixture.lease,
		DispositionAmbiguous, "effect_uncertain", "external disposition is unresolved", time.Minute, writeOwner,
	); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("ambiguous stale retry = %v, want ErrStaleClaim", err)
	}
	if ownerCalls != 0 {
		t.Fatalf("ambiguous stale retry invoked owner writer %d times", ownerCalls)
	}

	afterMarker := readOneShotSettlementMarker(t, fixture.db, fixture.markerID)
	if afterMarker.state != OneShotStateRunning || afterMarker.epoch != beforeMarker.epoch || afterMarker.version != beforeMarker.version {
		t.Fatalf("ambiguous stale retry changed marker: before=%+v after=%+v", beforeMarker, afterMarker)
	}
	assertOneShotSettlementLeaseUnchanged(t, afterMarker, beforeMarker)
	row := readWorkItem(t, fixture.db, fixture.itemID)
	if row.state != StateLeased || row.fencingToken != current.FencingToken || row.attempt != current.Attempt || row.terminalAt != nil || row.expectedOwnerVersion != beforeOwnerVersion {
		t.Fatalf("ambiguous stale retry changed the current WorkItem: %+v, current claim=%+v", row, current)
	}
	if got := readOneShotSettlementOwnerVersion(t, fixture.db, fixture.foID); got != beforeOwnerVersion {
		t.Fatalf("ambiguous stale retry changed owner version from %d to %d", beforeOwnerVersion, got)
	}
}

// A known disposition consumes its lease even when the WorkItem fence is
// stale. Only the marker-ledger half commits; the stale owner and WorkItem
// remain untouched.
func TestFinishOneShotConsumingStaleCommitsMarkerOnly(t *testing.T) {
	fixture := newOneShotSettlementFixture(t, "m-settlement-consuming-stale")
	beforeMarker := readOneShotSettlementMarker(t, fixture.db, fixture.markerID)
	beforeOwnerVersion := readOneShotSettlementOwnerVersion(t, fixture.db, fixture.foID)
	current := reclaimSettlementClaim(t, fixture)

	ownerCalls := 0
	writeOwner := func(context.Context, txscope.Tx, OwnerResult) (int64, error) {
		ownerCalls++
		return 0, errors.New("marker-only stale settlement must not write the owner")
	}
	if err := fixture.store.FinishOneShotWithResult(
		context.Background(), fixture.claim, fixture.lease,
		DispositionConfirmed, StateSucceeded, OwnerResult{State: StateSucceeded}, writeOwner,
	); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("consuming stale finish = %v, want ErrStaleClaim", err)
	}
	if ownerCalls != 0 {
		t.Fatalf("consuming stale finish invoked owner writer %d times", ownerCalls)
	}

	afterMarker := readOneShotSettlementMarker(t, fixture.db, fixture.markerID)
	if afterMarker.state != OneShotStateRunning || afterMarker.epoch != beforeMarker.epoch || afterMarker.version != beforeMarker.version+1 {
		t.Fatalf("consuming stale finish marker = %+v, want running epoch=%d version=%d", afterMarker, beforeMarker.epoch, beforeMarker.version+1)
	}
	if afterMarker.count != 0 || len(afterMarker.leases) != 0 {
		t.Fatalf("consuming stale finish must drain only the marker lease: %+v", afterMarker)
	}
	row := readWorkItem(t, fixture.db, fixture.itemID)
	if row.state != StateLeased || row.fencingToken != current.FencingToken || row.attempt != current.Attempt || row.terminalAt != nil || row.expectedOwnerVersion != beforeOwnerVersion {
		t.Fatalf("consuming stale finish changed the current WorkItem: %+v, current claim=%+v", row, current)
	}
	if got := readOneShotSettlementOwnerVersion(t, fixture.db, fixture.foID); got != beforeOwnerVersion {
		t.Fatalf("consuming stale finish changed owner version from %d to %d", beforeOwnerVersion, got)
	}
}

// Once another settlement has consumed an admission id, a replay is a typed
// no-op: it must not inspect or mutate the already-terminal WorkItem/owner.
func TestFinishOneShotAlreadyConsumedIsNoOp(t *testing.T) {
	fixture := newOneShotSettlementFixture(t, "m-settlement-already-consumed")
	if err := fixture.store.FinishOneShotWithResult(
		context.Background(), fixture.claim, fixture.lease,
		DispositionConfirmed, StateSucceeded, OwnerResult{State: StateSucceeded}, nil,
	); err != nil {
		t.Fatalf("initial one-shot finish: %v", err)
	}
	beforeMarker := readOneShotSettlementMarker(t, fixture.db, fixture.markerID)
	beforeRow := readWorkItem(t, fixture.db, fixture.itemID)
	beforeOwnerVersion := readOneShotSettlementOwnerVersion(t, fixture.db, fixture.foID)

	ownerCalls := 0
	writeOwner := func(context.Context, txscope.Tx, OwnerResult) (int64, error) {
		ownerCalls++
		return 0, errors.New("already-consumed settlement must not write the owner")
	}
	if err := fixture.store.FinishOneShotWithResult(
		context.Background(), fixture.claim, fixture.lease,
		DispositionConfirmed, StateFailed, OwnerResult{State: StateFailed}, writeOwner,
	); err != nil {
		t.Fatalf("already-consumed one-shot finish = %v, want nil no-op", err)
	}
	if ownerCalls != 0 {
		t.Fatalf("already-consumed finish invoked owner writer %d times", ownerCalls)
	}

	afterMarker := readOneShotSettlementMarker(t, fixture.db, fixture.markerID)
	if afterMarker.state != beforeMarker.state || afterMarker.epoch != beforeMarker.epoch || afterMarker.version != beforeMarker.version {
		t.Fatalf("already-consumed finish changed marker: before=%+v after=%+v", beforeMarker, afterMarker)
	}
	assertOneShotSettlementLeaseUnchanged(t, afterMarker, beforeMarker)
	afterRow := readWorkItem(t, fixture.db, fixture.itemID)
	if afterRow.state != beforeRow.state || afterRow.fencingToken != beforeRow.fencingToken || afterRow.attempt != beforeRow.attempt || afterRow.expectedOwnerVersion != beforeRow.expectedOwnerVersion || afterRow.terminalAt == nil {
		t.Fatalf("already-consumed finish changed WorkItem: before=%+v after=%+v", beforeRow, afterRow)
	}
	if got := readOneShotSettlementOwnerVersion(t, fixture.db, fixture.foID); got != beforeOwnerVersion {
		t.Fatalf("already-consumed finish changed owner version from %d to %d", beforeOwnerVersion, got)
	}
}

func blockOneShotMarkerRetainingLease(t *testing.T, db *sql.DB, markerID string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
UPDATE one_shot_acquisitions
SET state = 'cleanup_blocked',
    terminal_outcome = 'failed',
    terminal_reason_code = 'effect_uncertain',
    terminal_reason = 'external effect remains unresolved',
    terminal_at = CURRENT_TIMESTAMP,
    retention_until = CURRENT_TIMESTAMP + INTERVAL '1 day',
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1`, markerID); err != nil {
		t.Fatalf("block one-shot marker while retaining lease: %v", err)
	}
}

func oneShotSettlementOwnerWriter(foID string) OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ OwnerResult) (int64, error) {
		var version int64
		err := scope.SQLTx().QueryRowContext(ctx, `
UPDATE file_operations
SET version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = 1
RETURNING version`, foID).Scan(&version)
		return version, err
	}
}

// A cleanup_blocked marker is terminal for retention but may still carry an
// unresolved admission lease. Settlement remains marker-first and is allowed to
// update the current retry state while preserving that lease for recovery.
func TestRetryOneShotCleanupBlockedSettlementRetainsLease(t *testing.T) {
	fixture := newOneShotSettlementFixture(t, "m-settlement-cleanup-blocked")
	blockOneShotMarkerRetainingLease(t, fixture.db, fixture.markerID)
	beforeMarker := readOneShotSettlementMarker(t, fixture.db, fixture.markerID)
	if beforeMarker.state != OneShotStateCleanupBlocked || beforeMarker.count != 1 || len(beforeMarker.leases) != 1 {
		t.Fatalf("cleanup_blocked fixture must retain one lease: %+v", beforeMarker)
	}

	outcome, err := fixture.store.RetryOneShot(
		context.Background(), fixture.claim, fixture.lease,
		DispositionAmbiguous, "effect_uncertain", "external effect remains unresolved", time.Minute,
		oneShotSettlementOwnerWriter(fixture.foID),
	)
	if err != nil {
		t.Fatalf("cleanup_blocked ambiguous settlement: %v", err)
	}
	if outcome.Terminal {
		t.Fatal("cleanup_blocked ambiguous settlement must return a retryable WorkItem")
	}

	afterMarker := readOneShotSettlementMarker(t, fixture.db, fixture.markerID)
	if afterMarker.state != OneShotStateCleanupBlocked || afterMarker.epoch != beforeMarker.epoch || afterMarker.version != beforeMarker.version {
		t.Fatalf("cleanup_blocked settlement changed marker state/evidence: before=%+v after=%+v", beforeMarker, afterMarker)
	}
	assertOneShotSettlementLeaseUnchanged(t, afterMarker, beforeMarker)
	row := readWorkItem(t, fixture.db, fixture.itemID)
	if row.state != StateAvailable || row.leaseUntil != nil || row.expectedOwnerVersion != 2 {
		t.Fatalf("cleanup_blocked settlement must retry the item while retaining the lease: %+v", row)
	}
	if got := readOneShotSettlementOwnerVersion(t, fixture.db, fixture.foID); got != 2 {
		t.Fatalf("cleanup_blocked settlement owner version = %d, want 2", got)
	}
}
