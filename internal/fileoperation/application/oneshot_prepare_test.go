package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	artifactapplication "bansoko/internal/artifact/application"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// oneShotRefFromSnapshot decodes the one-shot marker reference from a stored
// WorkItem input snapshot.
func oneShotRefFromSnapshot(t *testing.T, snapshot json.RawMessage) *workitems.OneShotRef {
	t.Helper()
	ref, err := workitems.ParseOneShotRef(snapshot)
	if err != nil {
		t.Fatalf("decode input snapshot %s: %v", snapshot, err)
	}
	return ref
}

// seedOneShotMarkerFixture inserts one running one-shot acquisition marker so
// CreateOrMergeOneShot can lock it during one-shot prepare runs.
func seedOneShotMarkerFixture(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO one_shot_acquisitions(
    id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
    correlation_id, request_idempotency_key, request_hash, result_snapshot, state)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '{}'::jsonb, 'running')`,
		id, "anime-"+id, "sub-"+id, "src-"+id, "rel-"+id, "task-"+id,
		"corr-"+id, "req-key-"+id, "sha256:"+strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
}

func prepareOneShotFixture(t *testing.T, db *sql.DB, service *Service, artifactID string, ref workitems.OneShotRef) error {
	t.Helper()
	tx, err := ownerTestDB(t).BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := workitems.ValidateOneShotMarkerTx(context.Background(), txscope.Wrap(tx), ref); err != nil {
		return err
	}
	if _, err := seedCommittedInspectionPlanTx(context.Background(), txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: artifactID}, &ref); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	claim := claimPlanWorkItem(t, db, "oneshot-fixture-planner")
	_, err = service.PostProcessingPlanHandler().Handle(context.Background(), claim)
	return err
}

func fileExecuteItemSnapshot(t *testing.T, db *sql.DB, artifactID string) (string, json.RawMessage) {
	t.Helper()
	var itemID string
	var snapshot []byte
	if err := db.QueryRow(`
SELECT wi.id, wi.input_snapshot
FROM work_item_history wi
JOIN file_operations fo ON fo.id = wi.owner_id
WHERE wi.owner_type = 'file_operation' AND wi.kind = 'file_execute' AND fo.artifact_id = $1`, artifactID).Scan(&itemID, &snapshot); err != nil {
		t.Fatal(err)
	}
	return itemID, json.RawMessage(snapshot)
}

func prepareOrdinaryFixture(t *testing.T, db *sql.DB, service *Service, artifactID string) error {
	t.Helper()
	prepareFixture(t, db, service, artifactID)
	return nil
}

func setOneShotMarkerStateForPrepareFixture(t *testing.T, db *sql.DB, markerID, state string) {
	t.Helper()
	if _, err := db.Exec(`
UPDATE one_shot_acquisitions
SET state = $2, active_effect_leases = '[]'::jsonb, active_effect_count = 0,
    terminal_outcome = 'failed', terminal_reason_code = 'test_transition',
    terminal_reason = 'test transition', terminal_at = CURRENT_TIMESTAMP,
    retention_until = CURRENT_TIMESTAMP + INTERVAL '1 day'
WHERE id = $1`, markerID, state); err != nil {
		t.Fatalf("set marker %s to %s: %v", markerID, state, err)
	}
}

func setOneShotMarkerEpochForPrepareFixture(t *testing.T, db *sql.DB, markerID string, epoch int64) {
	t.Helper()
	if _, err := db.Exec(`
UPDATE one_shot_acquisitions
SET admission_epoch = $2
WHERE id = $1`, markerID, epoch); err != nil {
		t.Fatalf("set marker %s epoch to %d: %v", markerID, epoch, err)
	}
}

// Regression: an idempotent one-shot prepare rerun must keep the exact one-shot
// marker reference on the existing live file_execute WorkItem — never strip it
// on an available repair and never falsely report a leased item stale because
// the comparison ignored the reference. A genuinely different snapshot is
// still rejected.
func TestPrepareOneShotRerunPreservesWorkItemMarkerMetadata(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	defer db.Close()
	service := newFixtureService(t, db, fixture)
	const markerID = "m-prepare-rerun"
	seedOneShotMarkerFixture(t, db, markerID)
	ref := workitems.OneShotRef{MarkerID: markerID, CorrelationID: "corr-" + markerID, AdmissionEpoch: 1}

	if err := prepareOneShotFixture(t, db, service, fixture.artifactID, ref); err != nil {
		t.Fatalf("one-shot prepare: %v", err)
	}
	itemID, snapshot := fileExecuteItemSnapshot(t, db, fixture.artifactID)
	stored := oneShotRefFromSnapshot(t, snapshot)
	if stored == nil || *stored != ref {
		t.Fatalf("created item must carry the exact one-shot ref: ref=%v snapshot=%s", stored, snapshot)
	}

	// Idempotent rerun over the available item must not strip the marker.
	assertCommittedReentry(t, service, fixture.artifactID, &ref, "confirmed", "")
	_, snapshot = fileExecuteItemSnapshot(t, db, fixture.artifactID)
	stored = oneShotRefFromSnapshot(t, snapshot)
	if stored == nil || *stored != ref {
		t.Fatalf("available rerun must preserve the one-shot ref: ref=%v snapshot=%s", stored, snapshot)
	}

	// A leased item must not be reported stale when the only difference was
	// the previously ignored one-shot reference.
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	claim, err := store.ClaimDue(context.Background(), "oneshot-prepare-worker", []string{workitems.KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.ID != itemID {
		t.Fatalf("claimed unexpected item %s, want %s", claim.ID, itemID)
	}
	assertCommittedReentry(t, service, fixture.artifactID, &ref, "confirmed", "")
	var leasedState string
	if err := db.QueryRow(`SELECT state FROM work_item_history WHERE id = $1`, itemID).Scan(&leasedState); err != nil {
		t.Fatal(err)
	}
	if leasedState != workitems.StateLeased {
		t.Fatalf("matching leased rerun changed WorkItem state to %q", leasedState)
	}

	// A genuinely different leased snapshot is still rejected.
	if _, err := workItemFixtureDB(t).Exec(`
UPDATE work_items SET input_snapshot = jsonb_set(input_snapshot, '{artifact_file_id}', '"wrong-file"')
WHERE id = $1 AND state = 'leased'`, itemID); err != nil {
		t.Fatal(err)
	}
	assertCommittedReentry(t, service, fixture.artifactID, &ref, "blocked", "CHILD_WORK_SNAPSHOT_INVALID")

	// An available item whose snapshot lost the marker metadata is repaired
	// with the exact ref restored, never left as an ordinary item.
	if _, err := workItemFixtureDB(t).Exec(`
UPDATE work_items SET state = 'available', lease_until = NULL,
    input_snapshot = input_snapshot - 'one_shot'
WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	// Preserve the original compound damage: wrong-file remains alongside missing marker.
	assertCommittedReentry(t, service, fixture.artifactID, &ref, "blocked", "CHILD_WORK_SNAPSHOT_INVALID")
}

func TestPrepareOneShotMarkerGateBlocksExistingItem(t *testing.T) {
	for _, tc := range []struct {
		name       string
		removeMark bool
	}{
		{name: "missing marker", removeMark: true},
		{name: "reclaiming marker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, fixture := openFileOperationDB(t)
			defer db.Close()
			service := newFixtureService(t, db, fixture)
			const markerID = "m-prepare-gate"
			seedOneShotMarkerFixture(t, db, markerID)
			ref := workitems.OneShotRef{MarkerID: markerID, CorrelationID: "corr-" + markerID, AdmissionEpoch: 1}
			if err := prepareOneShotFixture(t, db, service, fixture.artifactID, ref); err != nil {
				t.Fatalf("initial one-shot prepare: %v", err)
			}
			itemID, before := fileExecuteItemSnapshot(t, db, fixture.artifactID)

			if tc.removeMark {
				if _, err := db.Exec(`DELETE FROM one_shot_acquisitions WHERE id = $1`, markerID); err != nil {
					t.Fatal(err)
				}
			} else {
				setOneShotMarkerStateForPrepareFixture(t, db, markerID, "reclaiming")
			}

			err := prepareOneShotFixture(t, db, service, fixture.artifactID, ref)
			wantErr := workitems.ErrOneShotReclaiming
			if tc.removeMark {
				wantErr = workitems.ErrOneShotMarkerMissing
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("prepare error = %v, want %v", err, wantErr)
			}
			afterID, after := fileExecuteItemSnapshot(t, db, fixture.artifactID)
			if afterID != itemID || !jsonEqual(before, after) {
				t.Fatalf("marker gate mutated existing WorkItem: before=(%s,%s), after=(%s,%s)", itemID, before, afterID, after)
			}
		})
	}
}

func TestPrepareOneShotMarkerGateRejectsStaleEpochWithoutMutation(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	defer db.Close()
	service := newFixtureService(t, db, fixture)
	const markerID = "m-prepare-stale-epoch"
	seedOneShotMarkerFixture(t, db, markerID)
	ref := workitems.OneShotRef{MarkerID: markerID, CorrelationID: "corr-" + markerID, AdmissionEpoch: 1}
	if err := prepareOneShotFixture(t, db, service, fixture.artifactID, ref); err != nil {
		t.Fatalf("initial one-shot prepare: %v", err)
	}
	itemID, before := fileExecuteItemSnapshot(t, db, fixture.artifactID)

	// A reclaimer advancing the marker epoch fences the old ref even while the
	// marker remains running. The marker gate runs before the artifact graph is
	// read, so the existing WorkItem must remain byte-for-byte unchanged.
	setOneShotMarkerEpochForPrepareFixture(t, db, markerID, 2)
	if err := prepareOneShotFixture(t, db, service, fixture.artifactID, ref); !errors.Is(err, workitems.ErrOneShotReclaiming) {
		t.Fatalf("stale epoch prepare error = %v, want ErrOneShotReclaiming", err)
	}
	afterID, after := fileExecuteItemSnapshot(t, db, fixture.artifactID)
	if afterID != itemID || !jsonEqual(before, after) {
		t.Fatalf("stale epoch changed WorkItem: before=(%s,%s), after=(%s,%s)", itemID, before, afterID, after)
	}
}

func TestPrepareOneShotRejectsConflictingExistingMarkerForAvailableAndLeased(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	defer db.Close()
	service := newFixtureService(t, db, fixture)
	const markerID = "m-prepare-conflict-a"
	const otherMarkerID = "m-prepare-conflict-b"
	seedOneShotMarkerFixture(t, db, markerID)
	seedOneShotMarkerFixture(t, db, otherMarkerID)
	ref := workitems.OneShotRef{MarkerID: markerID, CorrelationID: "corr-" + markerID, AdmissionEpoch: 1}
	otherRef := workitems.OneShotRef{MarkerID: otherMarkerID, CorrelationID: "corr-" + otherMarkerID, AdmissionEpoch: 1}
	if err := prepareOneShotFixture(t, db, service, fixture.artifactID, ref); err != nil {
		t.Fatalf("initial one-shot prepare: %v", err)
	}
	itemID, before := fileExecuteItemSnapshot(t, db, fixture.artifactID)

	if out, err := observeFixturePlan(service, fixture.artifactID, &otherRef); out.Status != "blocked" || err == nil || !errors.Is(err, workitems.ErrInvalidSpec) {
		t.Fatalf("available conflicting marker error = %v, want ErrInvalidSpec", err)
	}
	afterID, after := fileExecuteItemSnapshot(t, db, fixture.artifactID)
	if afterID != itemID || !jsonEqual(before, after) {
		t.Fatalf("available conflict changed WorkItem: before=(%s,%s), after=(%s,%s)", itemID, before, afterID, after)
	}

	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	claim, err := store.ClaimDue(context.Background(), "prepare-conflict-worker", []string{workitems.KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.ID != itemID {
		t.Fatalf("claimed unexpected item %s, want %s", claim.ID, itemID)
	}
	if out, err := observeFixturePlan(service, fixture.artifactID, &otherRef); out.Status != "blocked" || err == nil || !errors.Is(err, workitems.ErrInvalidSpec) {
		t.Fatalf("leased conflicting marker error = %v, want ErrInvalidSpec", err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM work_item_history WHERE id = $1`, itemID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != workitems.StateLeased {
		t.Fatalf("leased conflict changed WorkItem state to %q", state)
	}
	afterID, after = fileExecuteItemSnapshot(t, db, fixture.artifactID)
	if afterID != itemID || !jsonEqual(before, after) {
		t.Fatalf("leased conflict changed WorkItem snapshot: before=(%s,%s), after=(%s,%s)", itemID, before, afterID, after)
	}
}

func TestPrepareOneShotRejectsMalformedExistingMarkerForAvailableAndLeased(t *testing.T) {
	for _, state := range []string{workitems.StateAvailable, workitems.StateLeased} {
		t.Run(state, func(t *testing.T) {
			db, fixture := openFileOperationDB(t)
			defer db.Close()
			service := newFixtureService(t, db, fixture)
			markerID := "m-prepare-malformed-existing-" + state
			seedOneShotMarkerFixture(t, db, markerID)
			ref := workitems.OneShotRef{MarkerID: markerID, CorrelationID: "corr-" + markerID, AdmissionEpoch: 1}
			if err := prepareOneShotFixture(t, db, service, fixture.artifactID, ref); err != nil {
				t.Fatalf("initial one-shot prepare: %v", err)
			}
			itemID, _ := fileExecuteItemSnapshot(t, db, fixture.artifactID)
			if state == workitems.StateLeased {
				store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
				claim, err := store.ClaimDue(context.Background(), "prepare-malformed-worker", []string{workitems.KindFileExecute}, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				if claim.ID != itemID {
					t.Fatalf("claimed unexpected item %s, want %s", claim.ID, itemID)
				}
			} else if _, err := workItemFixtureDB(t).Exec(`
UPDATE work_items
SET state = 'available', lease_until = NULL
WHERE id = $1`, itemID); err != nil {
				t.Fatal(err)
			}
			if _, err := workItemFixtureDB(t).Exec(`
UPDATE work_items
SET input_snapshot = jsonb_set(input_snapshot, '{one_shot}', jsonb_build_object('marker_id', $2::text))
WHERE id = $1`, itemID, markerID); err != nil {
				t.Fatal(err)
			}
			_, malformedBefore := fileExecuteItemSnapshot(t, db, fixture.artifactID)

			assertCommittedReentry(t, service, fixture.artifactID, &ref, "blocked", "CHILD_ONE_SHOT_INVALID")
			afterID, after := fileExecuteItemSnapshot(t, db, fixture.artifactID)
			if afterID != itemID || !jsonEqual(malformedBefore, after) {
				t.Fatalf("malformed existing marker was mutated: before=(%s,%s), after=(%s,%s)", itemID, malformedBefore, afterID, after)
			}
		})
	}
}

func TestOrdinaryPrepareRejectsDeclaredOneShotMarker(t *testing.T) {
	for _, tc := range []struct {
		name            string
		malformedMarker bool
	}{
		{name: "valid marker"},
		{name: "malformed marker", malformedMarker: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, fixture := openFileOperationDB(t)
			defer db.Close()
			service := newFixtureService(t, db, fixture)
			const markerID = "m-prepare-ordinary"
			seedOneShotMarkerFixture(t, db, markerID)
			ref := workitems.OneShotRef{MarkerID: markerID, CorrelationID: "corr-" + markerID, AdmissionEpoch: 1}
			if err := prepareOneShotFixture(t, db, service, fixture.artifactID, ref); err != nil {
				t.Fatalf("initial one-shot prepare: %v", err)
			}
			itemID, before := fileExecuteItemSnapshot(t, db, fixture.artifactID)
			if tc.malformedMarker {
				if _, err := workItemFixtureDB(t).Exec(`
UPDATE work_items
SET input_snapshot = jsonb_build_object(
    'operation_id', $2::text,
    'artifact_id', $3::text,
    'artifact_file_id', $4::text,
    'one_shot', jsonb_build_object('marker_id', $5::text)
)
WHERE id = $1`, itemID, fixture.fileID, fixture.artifactID, fixture.fileID, markerID); err != nil {
					t.Fatal(err)
				}
				_, before = fileExecuteItemSnapshot(t, db, fixture.artifactID)
			}

			out, err := observeFixturePlan(service, fixture.artifactID, nil)
			if out.Status != "blocked" || err == nil || !errors.Is(err, workitems.ErrInvalidOneShotClaim) {
				t.Fatalf("ordinary prepare error = %v, want ErrInvalidOneShotClaim", err)
			}
			afterID, after := fileExecuteItemSnapshot(t, db, fixture.artifactID)
			if afterID != itemID || !jsonEqual(before, after) {
				t.Fatalf("ordinary prepare changed declared marker: before=(%s,%s), after=(%s,%s)", itemID, before, afterID, after)
			}
		})
	}
}
