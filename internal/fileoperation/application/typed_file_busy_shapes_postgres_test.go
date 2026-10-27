//go:build linux || darwin

package application

import (
	"context"
	"os"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	artifactapplication "bansoko/internal/artifact/application"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"golang.org/x/sys/unix"
)

// observeAnswer makes every observation of the overwrite target answer errno
// while *errno != 0 (an exclusive SMB holder refuses even the open).
func observeAnswer(errno *unix.Errno) filesystem.OSFileExecutor {
	return filesystem.OSFileExecutor{Hooks: filesystem.ExecutorHooks{PublicationObserveTarget: func(string) error {
		if *errno != 0 {
			return *errno
		}
		return nil
	}}}
}

// An overwrite target held open exclusively is refused at the preparation's
// observation, before any permit: the operation is requeued on the busy
// schedule (not TARGET_UNREADABLE, not the generic retry) and publishes once
// the holder lets go.
func TestTypedOverwritePrepareObservationBusyRequeuesThenPublishes(t *testing.T) {
	x := newOverwriteRuntimeFixture(t)
	errno := unix.EBUSY
	x.s.executor = observeAnswer(&errno)
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("busy run: handled=%v err=%v", handled, err)
	}
	row := x.busyRow(t)
	if row.state != ownerStatePending || row.code != "" || row.retries != 0 || row.busyRequeues != 1 || row.permit || row.liveWork != 1 || row.deliveryResult != "" {
		t.Fatalf("busy preparation not requeued: %+v", row)
	}
	if row.busySince == "" || row.dueIn < 4*time.Minute || row.dueIn > 6*time.Minute {
		t.Fatalf("next item busy_since=%q due in %s", row.busySince, row.dueIn)
	}
	if raw, err := os.ReadFile(x.target); err != nil || string(raw) != fixtureOverwriteIncumbentBytes {
		t.Fatalf("refused preparation changed the target: %q %v", raw, err)
	}
	assertNoPermitTemp(t, x.target)

	errno = 0
	x.makeDue(t, "")
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("released run: handled=%v err=%v", handled, err)
	}
	if done := x.busyRow(t); done.state != ownerStateCompleted || done.deliveryResult != "succeeded" || done.liveWork != 0 {
		t.Fatalf("released overwrite not completed: %+v", done)
	}
	if raw, err := os.ReadFile(x.target); err != nil || string(raw) != "source-content" {
		t.Fatalf("target after release %q %v", raw, err)
	}
}

// newOneShotOverwriteRuntimeFixture is newOverwriteRuntimeFixture with the
// plan, and so its file_execute item, owned by a running one-shot marker.
func newOneShotOverwriteRuntimeFixture(t *testing.T, ref workitems.OneShotRef) typedRuntimeFixture {
	t.Helper()
	db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		p["effective_sections"].(map[string]any)[animedomain.SectionFileEffect] = map[string]any{"operation": "copy", "collision_policy": "overwrite"}
	})
	s := newFixtureServiceWithPorts(t, db, f, filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	x := typedRuntimeFixture{db: db, f: f, s: s, store: &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}, source: f.sourcePath}
	seedOneShotMarkerFixture(t, db, ref.MarkerID)
	ctx := context.Background()
	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := workitems.ValidateOneShotMarkerTx(ctx, txscope.Wrap(tx), ref); err != nil {
		t.Fatal(err)
	}
	if x.planID, err = seedCommittedInspectionPlanBudgetTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: f.artifactID}, &ref, nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	runA11PlanWorker(t, db, s)
	if err := db.QueryRow(`SELECT o.id,o.target_path,w.id FROM file_operations o JOIN work_item_history w ON w.owner_type='file_operation' AND w.owner_id=o.id WHERE o.artifact_id=$1 AND o.file_ref='primary'`, f.artifactID).Scan(&x.opID, &x.target, &x.workID); err != nil {
		t.Fatalf("one-shot video operation/work: %v", err)
	}
	if raw, err := os.ReadFile(x.target); err != nil || string(raw) != fixtureOverwriteIncumbentBytes {
		t.Fatalf("overwrite incumbent %q %v", raw, err)
	}
	return x
}

// A one-shot overwrite refused busy is requeued like a subscription's: the
// next item keeps the exact one-shot marker reference, and the released run
// replaces the file.
func TestTypedOneShotOverwriteBusyRequeuesThenPublishes(t *testing.T) {
	ref := workitems.OneShotRef{MarkerID: "busy-marker", CorrelationID: "corr-busy-marker", AdmissionEpoch: 1}
	x := newOneShotOverwriteRuntimeFixture(t, ref)
	errno := unix.EBUSY
	x.s.executor = renameAnswer(&errno)
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("busy run: handled=%v err=%v", handled, err)
	}
	row := x.busyRow(t)
	if row.state != ownerStatePending || row.busyRequeues != 1 || row.retries != 0 || row.liveWork != 1 {
		t.Fatalf("busy one-shot overwrite not requeued: %+v", row)
	}
	var snapshot []byte
	if err := x.db.QueryRow(`SELECT input_snapshot FROM work_items WHERE owner_type='file_operation' AND owner_id=$1`, x.opID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if stored := oneShotRefFromSnapshot(t, snapshot); stored == nil || *stored != ref {
		t.Fatalf("requeued item one_shot = %+v, want %+v (snapshot %s)", stored, ref, snapshot)
	}

	errno = 0
	x.makeDue(t, "")
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("released run: handled=%v err=%v", handled, err)
	}
	if done := x.busyRow(t); done.state != ownerStateCompleted || done.deliveryResult != "succeeded" || done.liveWork != 0 {
		t.Fatalf("released one-shot overwrite not completed: %+v", done)
	}
	if raw, err := os.ReadFile(x.target); err != nil || string(raw) != "source-content" {
		t.Fatalf("target after release %q %v", raw, err)
	}
}
