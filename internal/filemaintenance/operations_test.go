package filemaintenance

import (
	"bansoko/internal/deployment"
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openOpsTestDB(t *testing.T) *opsFixture {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	return &opsFixture{svc: NewAlignmentService(db), db: db}
}

func alignmentWorkItemClaim(t *testing.T, ctx context.Context, db *sql.DB, operationID string, ownerVersion int64) (*workitems.Store, workitems.Claim) {
	t.Helper()
	store := newOffsetMigrationWorkStore(t, db)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner: workitems.OwnerRef{Type: workitems.OwnerAlignment, ID: operationID},
		Kind:  workitems.KindAlignmentExecute, IdempotencyKey: "alignment-test:" + operationID,
		MaxAttempts: 3, ExpectedOwnerVersion: ownerVersion,
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "alignment-test-worker", []string{workitems.KindAlignmentExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return store, claim
}

type opsFixture struct {
	svc *AlignmentService
	db  *sql.DB
}

func testOperation() AlignmentOperation {
	contract, _ := json.Marshal(map[string]any{"expected": "absent"})
	dryRun, _ := json.Marshal(map[string]any{"candidate": true})
	return AlignmentOperation{
		ID:                "op-align-1",
		IdempotencyKey:    "align:op-align-1",
		ManagedAnimeID:    "anime-align",
		ScanGeneration:    1,
		ObservationRef:    "anime-align:1:/library/EP01.mkv",
		CandidateRef:      "facts:anime-align:1",
		PolicyVersion:     1,
		MetadataVersion:   1,
		RulesVersion:      1,
		NamingVersion:     1,
		SourcePath:        "/library/EP01.mkv",
		SourceFingerprint: "fp-source",
		TargetPath:        "/library/Show/Season 01/EP01.mkv",
		TargetContract:    contract,
		DryRunResult:      dryRun,
	}
}

type fakeOpsExecutor struct {
	calls                int
	err                  error
	panicAfterCheckpoint bool
	panicAfterEffect     bool
	operation            domain.FileOperation
	observer             *fakeOpsObserver // registers the executed target when set
	landedFingerprint    string           // registered instead of the source fingerprint when set
}

func (f *fakeOpsExecutor) Execute(_ context.Context, operation domain.FileOperation) (domain.FileOperation, error) {
	if f.panicAfterCheckpoint {
		panic("injected crash after attempt checkpoint")
	}
	f.calls++
	f.operation = operation
	if f.err != nil {
		return operation, f.err
	}
	operation.TargetFingerprint = operation.SourceFingerprint
	if f.observer != nil {
		landed := operation.SourceFingerprint
		if f.landedFingerprint != "" {
			landed = f.landedFingerprint
		}
		f.observer.registered[operation.TargetPath] = landed
	}
	if f.panicAfterEffect {
		panic("injected crash after file effect")
	}
	return operation, nil
}

type panicAfterAlignmentExecutor struct {
	inner  filesystem.OSFileExecutor
	before bool
}

func (e panicAfterAlignmentExecutor) Execute(ctx context.Context, operation domain.FileOperation) (domain.FileOperation, error) {
	if e.before {
		panic("injected alignment crash before file effect")
	}
	executed, err := e.inner.Execute(ctx, operation)
	if err != nil {
		return executed, err
	}
	panic("injected alignment crash after file effect")
}

func (e panicAfterAlignmentExecutor) ExecuteMaintenance(ctx context.Context, op domain.FileOperation, i executorpkg.MaintenanceInvocation) (domain.FileOperation, error) {
	if e.before {
		panic("injected alignment crash before maintenance effect")
	}
	executed, err := e.inner.ExecuteMaintenance(ctx, op, i)
	if err != nil {
		return executed, err
	}
	panic("injected alignment crash after actual final receipt")
}
func (e panicAfterAlignmentExecutor) ReadMaintenanceReceipt(ctx context.Context, i executorpkg.MaintenanceInvocation) (executorpkg.MaintenanceReceipt, error) {
	return e.inner.ReadMaintenanceReceipt(ctx, i)
}

type fakeOpsObserver struct {
	fingerprint  string
	fingerprints map[string]string // pre-existing files: path -> fingerprint
	registered   map[string]string // files created by the executor effect
	err          error
}

func (f *fakeOpsObserver) Observe(_ context.Context, path string) (string, error) {
	if f.registered != nil {
		if fp, ok := f.registered[path]; ok {
			return fp, nil
		}
	}
	if f.fingerprints != nil {
		return f.fingerprints[path], f.err
	}
	return f.fingerprint, f.err
}

// seedAlignmentPlacement creates the managed anime plus one verified active
// Placement whose current path the alignment operation will commit.
func seedAlignmentPlacement(t *testing.T, db *sql.DB, animeID, sourcePath, fingerprint string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ($1, 'Align Show')`, animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, version, created_at, updated_at)
VALUES ('plc-align-1', $1, $2, 'reconciliation', 'obs-align-1', $3, $3, 'parser-v1', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		sourcePath, animeID, fingerprint); err != nil {
		t.Fatal(err)
	}
}

func TestAlignmentOperationLifecycle(t *testing.T) {
	fixture := openOpsTestDB(t)
	defer fixture.svc.db.Close()
	ctx := context.Background()

	created, err := fixture.svc.CreateOperation(ctx, testOperation())
	if err != nil {
		t.Fatal(err)
	}
	if created.State != AlignmentPlanned || created.Version != 1 {
		t.Fatalf("unexpected created op: %+v", created)
	}
	// Idempotent by key.
	replay, err := fixture.svc.CreateOperation(ctx, testOperation())
	if err != nil || replay.ID != created.ID {
		t.Fatalf("idempotent create failed: %+v err=%v", replay, err)
	}
	version, err := fixture.svc.MarkReady(ctx, created.ID, created.Version)
	if err != nil || version != 2 {
		t.Fatalf("mark ready failed: %v", err)
	}
	attempt, _ := json.Marshal(map[string]any{"started": true})
	version, err = fixture.svc.PersistAttempt(ctx, created.ID, version, attempt)
	if err != nil || version != 3 {
		t.Fatalf("persist attempt failed: %v", err)
	}
	if err := fixture.svc.Settle(ctx, created.ID, version, AlignmentCompleted, "", ""); err != nil {
		t.Fatal(err)
	}
}
func TestAlignmentMovesRealFileAndCommitsExecutedTarget(t *testing.T) {
	fixture := openOpsTestDB(t)
	defer fixture.db.Close()
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "EP01.mkv")
	target := filepath.Join(root, "Show", "EP01.mkv")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("alignment-move-proof"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("alignment-move-proof"))
	fingerprint := hex.EncodeToString(sum[:])
	if _, err := fixture.db.Exec(`INSERT INTO managed_anime(id,title) VALUES ('anime-align','Align')`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO placements(id,physical_path,managed_anime_id,source_evidence_type,source_owner_ref,source_fingerprint,target_fingerprint,facts_version,version,created_at,updated_at) VALUES ('plc-align-1',$1,'anime-align','reconciliation','obs-align-1',$2,$2,'parser-v1',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, source, fingerprint); err != nil {
		t.Fatal(err)
	}
	fixture.svc.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	op := testOperation()
	op.SourcePath, op.TargetPath, op.SourceFingerprint = source, target, `{"algorithm":"sha256","value":"`+fingerprint+`"}`
	created, err := fixture.svc.CreateOperation(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	version, err := fixture.svc.MarkReady(ctx, created.ID, created.Version)
	if err != nil {
		t.Fatal(err)
	}
	store, claim := alignmentWorkItemClaim(t, ctx, fixture.db, created.ID, version)
	fixture.svc.SetWorkItems(store)
	outcome, err := fixture.svc.ExecuteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	if outcome.OwnerResult.State != "completed" {
		t.Fatalf("outcome=%+v", outcome)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists or stat failed: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "alignment-move-proof" {
		t.Fatalf("target content=%q err=%v", got, err)
	}
	var path string
	if err := fixture.db.QueryRow(`SELECT physical_path FROM placements WHERE id='plc-align-1'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != target {
		t.Fatalf("placement path=%q, want executed target %q", path, target)
	}
	var ownerVersion, itemVersion int64
	if err := fixture.db.QueryRow(`SELECT operation.version, item.expected_owner_version FROM alignment_operations operation JOIN work_item_history item ON item.owner_id=operation.id WHERE operation.id=$1`, created.ID).Scan(&ownerVersion, &itemVersion); err != nil {
		t.Fatal(err)
	}
	if ownerVersion != itemVersion {
		t.Fatalf("owner version=%d work item expected version=%d", ownerVersion, itemVersion)
	}

}

func TestAlignmentExecuteHandlerSettlesCompletedWithVerifiedTarget(t *testing.T) {
	fixture := openOpsTestDB(t)
	defer fixture.svc.db.Close()
	ctx := context.Background()
	// Source exists with the frozen identity; the target does not exist until
	// the executor effect registers it.
	observer := &fakeOpsObserver{fingerprints: map[string]string{"/library/EP01.mkv": "fp-source"}, registered: map[string]string{}}
	executor := &fakeOpsExecutor{observer: observer}
	fixture.svc.SetFilePorts(executor, observer)
	seedAlignmentPlacement(t, fixture.db, "anime-align", "/library/EP01.mkv", "fp-source")

	created, err := fixture.svc.CreateOperation(ctx, testOperation())
	if err != nil {
		t.Fatal(err)
	}
	version, err := fixture.svc.MarkReady(ctx, created.ID, created.Version)
	if err != nil {
		t.Fatal(err)
	}
	store, claim := alignmentWorkItemClaim(t, ctx, fixture.db, created.ID, version)
	fixture.svc.SetWorkItems(store)
	outcome, err := fixture.svc.ExecuteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	if outcome.TerminalState != "succeeded" || outcome.OwnerResult.State != "completed" {
		t.Fatalf("execution must complete: %+v", outcome)
	}
	if executor.calls != 1 {
		t.Fatalf("executor must run once, got %d", executor.calls)
	}
	if executor.operation.Operation != domain.OperationMove || executor.operation.CollisionPolicy != domain.CollisionBlock {
		t.Fatalf("alignment must execute move with collision block, got operation=%q collision=%q", executor.operation.Operation, executor.operation.CollisionPolicy)
	}
	// The verified target becomes the authoritative Placement current path.
	var currentPath string
	if err := fixture.db.QueryRow(`SELECT physical_path FROM placements WHERE id = 'plc-align-1'`).Scan(&currentPath); err != nil {
		t.Fatal(err)
	}
	if currentPath != "/library/Show/Season 01/EP01.mkv" {
		t.Fatalf("placement current path must commit to the verified target, got %q", currentPath)
	}
	if len(observer.registered) != 1 || observer.registered[currentPath] != "fp-source" {
		t.Fatalf("expected one executed file at placement target %q, observed=%v", currentPath, observer.registered)
	}
}

// TestAlignmentExecuteSettlesWithDuplicateIndexRowsForOnePath pins the
// alignment settle against the shape the reconciliation index produces: the
// owner placement and its index projections are separate verified active rows
// for the same physical path. The settle commits the target for all of them;
// before the fix it required exactly one affected row, so it failed with
// "changed concurrently" on every retry and the operation never settled.
func TestAlignmentExecuteSettlesWithDuplicateIndexRowsForOnePath(t *testing.T) {
	fixture := openOpsTestDB(t)
	defer fixture.db.Close()
	ctx := context.Background()
	observer := &fakeOpsObserver{fingerprints: map[string]string{"/library/EP01.mkv": "fp-source"}, registered: map[string]string{}}
	executor := &fakeOpsExecutor{observer: observer}
	fixture.svc.SetFilePorts(executor, observer)
	seedAlignmentPlacement(t, fixture.db, "anime-align", "/library/EP01.mkv", "fp-source")
	if _, err := fixture.db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, version, created_at, updated_at)
VALUES ('plc-align-index', '/library/EP01.mkv', 'anime-align', 'reconciliation', 'anime-align:1:/library/EP01.mkv',
 'fp-source', 'fp-source', 'parser-v1', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	created, err := fixture.svc.CreateOperation(ctx, testOperation())
	if err != nil {
		t.Fatal(err)
	}
	version, err := fixture.svc.MarkReady(ctx, created.ID, created.Version)
	if err != nil {
		t.Fatal(err)
	}
	store, claim := alignmentWorkItemClaim(t, ctx, fixture.db, created.ID, version)
	fixture.svc.SetWorkItems(store)
	outcome, err := fixture.svc.ExecuteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	if outcome.TerminalState != "succeeded" || outcome.OwnerResult.State != "completed" {
		t.Fatalf("execution must complete with duplicate index rows: %+v", outcome)
	}
	for _, placementID := range []string{"plc-align-1", "plc-align-index"} {
		var currentPath string
		if err := fixture.db.QueryRow(`SELECT physical_path FROM placements WHERE id = $1`, placementID).Scan(&currentPath); err != nil {
			t.Fatal(err)
		}
		if currentPath != "/library/Show/Season 01/EP01.mkv" {
			t.Fatalf("placement %s current path must commit to the verified target, got %q", placementID, currentPath)
		}
	}
}

// TestAlignmentExecuteSettleFailsClosedWithoutAnOwningPath keeps the other
// half of the settle guard honest: when no verified active placement owns the
// operation's source path any more, the settle must fail instead of committing
// the target for whatever else happens to be at that path.
func TestAlignmentExecuteSettleFailsClosedWithoutAnOwningPath(t *testing.T) {
	fixture := openOpsTestDB(t)
	defer fixture.db.Close()
	ctx := context.Background()
	observer := &fakeOpsObserver{fingerprints: map[string]string{"/library/EP01.mkv": "fp-source"}, registered: map[string]string{}}
	executor := &fakeOpsExecutor{observer: observer}
	fixture.svc.SetFilePorts(executor, observer)
	seedAlignmentPlacement(t, fixture.db, "anime-align", "/library/MOVED-AWAY.mkv", "fp-source")

	created, err := fixture.svc.CreateOperation(ctx, testOperation())
	if err != nil {
		t.Fatal(err)
	}
	version, err := fixture.svc.MarkReady(ctx, created.ID, created.Version)
	if err != nil {
		t.Fatal(err)
	}
	store, claim := alignmentWorkItemClaim(t, ctx, fixture.db, created.ID, version)
	fixture.svc.SetWorkItems(store)
	outcome, err := fixture.svc.ExecuteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err == nil {
		t.Fatal("settle must fail closed when no active verified placement owns the source path")
	}
	var state string
	if err := fixture.db.QueryRow(`SELECT state FROM alignment_operations WHERE id = $1`, created.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "completed" {
		t.Fatalf("operation state=%q, want an unsettled state after the failed commit", state)
	}
}

func TestAlignmentExecuteRecoveredAlignedWithoutSecondEffect(t *testing.T) {
	fixture := openOpsTestDB(t)
	defer fixture.db.Close()
	ctx := context.Background()
	executor := &fakeOpsExecutor{}
	observer := &fakeOpsObserver{fingerprints: map[string]string{"/library/Show/Season 01/EP01.mkv": "fp-source"}}
	fixture.svc.SetFilePorts(executor, observer)
	seedAlignmentPlacement(t, fixture.db, "anime-align", "/library/EP01.mkv", "fp-source")
	created, err := fixture.svc.CreateOperation(ctx, testOperation())
	if err != nil {
		t.Fatal(err)
	}
	version, err := fixture.svc.MarkReady(ctx, created.ID, created.Version)
	if err != nil {
		t.Fatal(err)
	}
	store, claim := alignmentWorkItemClaim(t, ctx, fixture.db, created.ID, version)
	fixture.svc.SetWorkItems(store)
	outcome, err := fixture.svc.ExecuteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	if outcome.OwnerResult.ReasonCode != "recovered_aligned" || executor.calls != 0 {
		t.Fatalf("outcome=%+v calls=%d", outcome, executor.calls)
	}
}

func TestAlignmentBlockedTargetDoesNotMovePlacement(t *testing.T) {
	fixture := openOpsTestDB(t)
	defer fixture.db.Close()
	ctx := context.Background()
	executor := &fakeOpsExecutor{err: &filesystem.OperationError{Code: filesystem.CodeTargetOccupied, Err: errors.New("occupied")}}
	fixture.svc.SetFilePorts(executor, &fakeOpsObserver{fingerprints: map[string]string{"/library/EP01.mkv": "fp-source"}, registered: map[string]string{}})
	seedAlignmentPlacement(t, fixture.db, "anime-align", "/library/EP01.mkv", "fp-source")
	created, err := fixture.svc.CreateOperation(ctx, testOperation())
	if err != nil {
		t.Fatal(err)
	}
	version, err := fixture.svc.MarkReady(ctx, created.ID, created.Version)
	if err != nil {
		t.Fatal(err)
	}
	store, claim := alignmentWorkItemClaim(t, ctx, fixture.db, created.ID, version)
	fixture.svc.SetWorkItems(store)
	outcome, err := fixture.svc.ExecuteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	var state, path string
	if err := fixture.db.QueryRow(`SELECT state FROM alignment_operations WHERE id=$1`, created.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT physical_path FROM placements WHERE id='plc-align-1'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if state != "blocked" || path != "/library/EP01.mkv" || outcome.OwnerResult.ReasonCode != "target_occupied" {
		t.Fatalf("state=%s path=%s outcome=%+v", state, path, outcome)
	}
	var ownerVersion, itemVersion int64
	if err := fixture.db.QueryRow(`SELECT operation.version, item.expected_owner_version FROM alignment_operations operation JOIN work_item_history item ON item.owner_type='alignment_operation' AND item.owner_id=operation.id WHERE operation.id=$1`, created.ID).Scan(&ownerVersion, &itemVersion); err != nil {
		t.Fatal(err)
	}
	if ownerVersion != itemVersion {
		t.Fatalf("owner version=%d work item expected version=%d", ownerVersion, itemVersion)
	}
}

func TestAlignmentReclaimedAttemptConverges(t *testing.T) {
	type reclaimCase struct {
		name      string
		moved     bool
		perturb   func(t *testing.T, source, target string)
		converges bool
	}
	// A pre-effect crash leaves no start marker. With the source intact at its
	// frozen fingerprint and no target, the attempt provably never ran and must
	// converge; any other disk state must stay unresolved and untouched.
	cases := []reclaimCase{
		{name: "not_started", converges: true},
		{name: "moved", moved: true, converges: true},
		{name: "not_started_source_drifted", perturb: func(t *testing.T, source, _ string) {
			if err := os.WriteFile(source, []byte("drifted after checkpoint"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "not_started_target_occupied", perturb: func(t *testing.T, _, target string) {
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("occupied after checkpoint"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		moved := tc.moved
		t.Run(tc.name, func(t *testing.T) {
			fixture := openOpsTestDB(t)
			defer fixture.db.Close()
			ctx := context.Background()
			root := t.TempDir()
			source := filepath.Join(root, "EP01.mkv")
			target := filepath.Join(root, "Show", "EP01.mkv")
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			content := []byte("alignment crash recovery")
			if err := os.WriteFile(source, content, 0o600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(content)
			fingerprint := hex.EncodeToString(sum[:])
			store := newOffsetMigrationWorkStore(t, fixture.db)
			fixture.svc.SetWorkItems(store)
			if _, err := fixture.db.ExecContext(ctx, `INSERT INTO managed_anime(id,title) VALUES ('anime-align','Align')`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.ExecContext(ctx, `INSERT INTO placements(id,physical_path,managed_anime_id,source_evidence_type,source_owner_ref,source_fingerprint,target_fingerprint,facts_version,version,created_at,updated_at) VALUES ('plc-align-1',$1,'anime-align','reconciliation','obs-align-1',$2,$2,'parser-v1',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, source, fingerprint); err != nil {
				t.Fatal(err)
			}
			op := testOperation()
			op.SourcePath, op.TargetPath, op.SourceFingerprint = source, target, fingerprint
			created, err := fixture.svc.CreateOperation(ctx, op)
			if err != nil {
				t.Fatal(err)
			}
			version, err := fixture.svc.MarkReady(ctx, created.ID, created.Version)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := fixture.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			itemID, err := store.Create(ctx, tx, workitems.WorkItemSpec{Owner: workitems.OwnerRef{Type: workitems.OwnerAlignment, ID: created.ID}, Kind: workitems.KindAlignmentExecute, IdempotencyKey: "crash:" + created.ID, MaxAttempts: 3, ExpectedOwnerVersion: version})
			if err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			executor := panicAfterAlignmentExecutor{before: !moved}
			fixture.svc.SetFilePorts(executor, filesystem.OSFileObserver{})
			claim, err := store.ClaimDue(ctx, "crash-worker", []string{workitems.KindAlignmentExecute}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("expected injected crash after real handler checkpoint")
					}
				}()
				_, _ = fixture.svc.ExecuteHandler().Handle(ctx, claim)
			}()
			if moved {
				if _, err := os.Stat(source); !os.IsNotExist(err) {
					t.Fatalf("source remains: %v", err)
				}
				if _, err := os.Stat(target); err != nil {
					t.Fatalf("target absent: %v", err)
				}
			} else {
				if _, err := os.Stat(source); err != nil {
					t.Fatalf("source absent before effect: %v", err)
				}
			}
			var checkpointState string
			var checkpointVersion, checkpointExpected int64
			if err := fixture.db.QueryRowContext(ctx, `SELECT operation.state, operation.version, item.expected_owner_version FROM alignment_operations operation JOIN work_item_history item ON item.owner_type='alignment_operation' AND item.owner_id=operation.id WHERE item.id=$1`, itemID).Scan(&checkpointState, &checkpointVersion, &checkpointExpected); err != nil {
				t.Fatal(err)
			}
			if checkpointState != string(AlignmentRunning) || checkpointVersion != 3 || checkpointExpected != 3 {
				t.Fatalf("crash checkpoint owner state=%s version=%d workitem expected=%d", checkpointState, checkpointVersion, checkpointExpected)
			}

			if tc.perturb != nil {
				tc.perturb(t, source, target)
			}
			sourceBefore, targetBefore := readFileOrNil(t, source), readFileOrNil(t, target)
			if _, err := workItemFixtureDB(t).ExecContext(ctx, `UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, itemID); err != nil {
				t.Fatal(err)
			}
			reclaimed, err := store.ClaimDue(ctx, "reclaim-worker", []string{workitems.KindAlignmentExecute}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			fixture.svc.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
			outcome, err := fixture.svc.ExecuteHandler().Handle(ctx, reclaimed)
			if !tc.converges {
				if !errors.Is(err, workitems.ErrPublicationUnresolved) {
					t.Fatalf("unproven attempt must remain unresolved: %v", err)
				}
				if !bytes.Equal(readFileOrNil(t, source), sourceBefore) || !bytes.Equal(readFileOrNil(t, target), targetBefore) {
					t.Fatal("unresolved attempt mutated the disk")
				}
				var state, path string
				var ownerVersion, itemVersion int64
				if err := fixture.db.QueryRow(`SELECT operation.state,operation.version,item.expected_owner_version FROM alignment_operations operation JOIN work_item_history item ON item.owner_type='alignment_operation' AND item.owner_id=operation.id WHERE item.id=$1`, itemID).Scan(&state, &ownerVersion, &itemVersion); err != nil {
					t.Fatal(err)
				}
				if state != "running" || ownerVersion != checkpointVersion || ownerVersion != itemVersion {
					t.Fatalf("unresolved state=%s owner=%d item=%d", state, ownerVersion, itemVersion)
				}
				if err := fixture.db.QueryRow(`SELECT physical_path FROM placements WHERE id='plc-align-1'`).Scan(&path); err != nil {
					t.Fatal(err)
				}
				if path != source {
					t.Fatalf("unproven attempt changed placement %s", path)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := store.FinishWithResult(ctx, reclaimed, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
				t.Fatal(err)
			}
			var state, path, itemState string
			if err := fixture.db.QueryRowContext(ctx, `SELECT state FROM alignment_operations WHERE id=$1`, created.ID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id='plc-align-1'`).Scan(&path); err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id=$1`, itemID).Scan(&itemState); err != nil {
				t.Fatal(err)
			}
			if state != "completed" || path != target || itemState != workitems.StateSucceeded {
				t.Fatalf("state=%s path=%s item=%s outcome=%+v", state, path, itemState, outcome)
			}
			var ownerVersion, itemVersion int64
			if err := fixture.db.QueryRowContext(ctx, `SELECT operation.version, item.expected_owner_version FROM alignment_operations operation JOIN work_item_history item ON item.owner_type='alignment_operation' AND item.owner_id=operation.id WHERE operation.id=$1`, created.ID).Scan(&ownerVersion, &itemVersion); err != nil {
				t.Fatal(err)
			}
			if ownerVersion != itemVersion {
				t.Fatalf("owner version=%d work item expected version=%d", ownerVersion, itemVersion)
			}
			if _, err := os.Stat(source); !os.IsNotExist(err) {
				t.Fatalf("source remains after recovery: %v", err)
			}
			if _, err := os.Stat(target); err != nil {
				t.Fatalf("target missing after recovery: %v", err)
			}
		})
	}
}
func TestAlignmentExecuteHandlerFailsClosedOnTargetMismatch(t *testing.T) {
	fixture := openOpsTestDB(t)
	defer fixture.svc.db.Close()
	ctx := context.Background()
	// The target is free before the effect (the unit precheck passes) and
	// holds different bytes after it.
	observer := &fakeOpsObserver{fingerprints: map[string]string{}, registered: map[string]string{}}
	fixture.svc.SetFilePorts(&fakeOpsExecutor{observer: observer, landedFingerprint: "DIFFERENT"}, observer)

	created, err := fixture.svc.CreateOperation(ctx, testOperation())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.svc.MarkReady(ctx, created.ID, created.Version); err != nil {
		t.Fatal(err)
	}
	outcome, err := fixture.svc.ExecuteHandler().Handle(ctx, alignClaim(created.ID, 2))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.OwnerResult.ReasonCode != "alignment_target_mismatch" {
		t.Fatalf("target mismatch must fail closed: %+v", outcome)
	}
}

func TestAlignmentReconcileSettlesCommittedEffect(t *testing.T) {
	fixture := openOpsTestDB(t)
	defer fixture.svc.db.Close()
	ctx := context.Background()
	fixture.svc.SetFilePorts(&fakeOpsExecutor{}, &fakeOpsObserver{fingerprint: "fp-source"})

	created, err := fixture.svc.CreateOperation(ctx, testOperation())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.svc.MarkReady(ctx, created.ID, created.Version); err != nil {
		t.Fatal(err)
	}
	attempt, _ := json.Marshal(map[string]any{"started": true})
	if _, err := fixture.svc.PersistAttempt(ctx, created.ID, 2, attempt); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.svc.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{OperationID: "recovery-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != deployment.StateCompleted || result.Effect != deployment.EffectPresent {
		t.Fatalf("committed alignment effect must settle present: %+v", result)
	}
}

func TestAlignmentReconcileConflictsOnFingerprintMismatch(t *testing.T) {
	fixture := openOpsTestDB(t)
	defer fixture.svc.db.Close()
	ctx := context.Background()
	fixture.svc.SetFilePorts(&fakeOpsExecutor{}, &fakeOpsObserver{fingerprint: "WRONG"})

	created, err := fixture.svc.CreateOperation(ctx, testOperation())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.svc.MarkReady(ctx, created.ID, created.Version); err != nil {
		t.Fatal(err)
	}
	attempt, _ := json.Marshal(map[string]any{"started": true})
	if _, err := fixture.svc.PersistAttempt(ctx, created.ID, 2, attempt); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.svc.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{OperationID: "recovery-2"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != deployment.StateConflict {
		t.Fatalf("fingerprint mismatch must be conflict: %+v", result)
	}
}

func TestAlignmentReconcileMissingObserverBlocksClosed(t *testing.T) {
	fixture := openOpsTestDB(t)
	defer fixture.svc.db.Close()
	ctx := context.Background()
	// No observer wired.
	result, err := fixture.svc.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{OperationID: "recovery-3"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != deployment.StateBlocked {
		t.Fatalf("missing observer must fail closed: %+v", result)
	}
}

func alignClaim(operationID string, version int64) workitems.Claim {
	return workitems.Claim{
		Kind:                 workitems.KindAlignmentExecute,
		Owner:                workitems.OwnerRef{Type: workitems.OwnerAlignment, ID: operationID},
		ExpectedOwnerVersion: version,
		FencingToken:         1,
	}
}

func readFileOrNil(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
