package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bansoko/internal/library/domain"
	"bansoko/internal/testutil/disposabledb"
)

func waitForRecoveryLockWait(t *testing.T, db *sql.DB, queryFragment string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var waiting int
		if err := db.QueryRow(`
SELECT count(*) FROM pg_stat_activity
WHERE pid <> pg_backend_pid() AND wait_event_type = 'Lock' AND query LIKE $1`,
			"%"+queryFragment+"%").Scan(&waiting); err != nil {
			t.Fatalf("inspect PostgreSQL lock waits: %v", err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no PostgreSQL lock wait observed for query containing %q", queryFragment)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type recoveryCallResult struct {
	result AnimeFilesRecoverResult
	err    error
}

func TestRecoverAnimeFileApplicationFencesConcurrentRetirement(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})
	before := scene.snapshot(t)

	retiring, err := scene.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer retiring.Rollback()
	if _, err := retiring.Exec(`
UPDATE managed_anime
SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1,
    lifecycle_version = lifecycle_version + 1, retired_at = CURRENT_TIMESTAMP,
    retired_subscription_version = 1
WHERE id = $1`, scene.animeID); err != nil {
		t.Fatalf("lock and retire owner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan recoveryCallResult, 1)
	go func() {
		result, err := service.RecoverAnimeFileApplication(ctx, scene.animeID, scene.command())
		done <- recoveryCallResult{result: result, err: err}
	}()
	waitForRecoveryLockWait(t, scene.db, "SELECT lifecycle_state FROM managed_anime")
	select {
	case call := <-done:
		t.Fatalf("confirm returned before retirement released its lock: %+v", call)
	default:
	}
	if err := retiring.Commit(); err != nil {
		t.Fatalf("commit retirement: %v", err)
	}
	call := <-done
	var refusal *AnimeFilesRecoverRefusal
	if !errors.As(call.err, &refusal) || refusal.Code != RecoveryReasonOwnerRetired {
		t.Fatalf("confirm after retirement = %+v, %v; want owner_retired", call.result, call.err)
	}
	if after := scene.snapshot(t); after != before {
		t.Fatalf("retired refusal mutated ownership:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestRecoverAnimeFileApplicationUsesSettlementLockOrder(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})

	settlement, err := scene.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer settlement.Rollback()
	if _, err := settlement.Exec(`SELECT state FROM subscriptions WHERE managed_anime_id = $1 FOR UPDATE`, scene.animeID); err != nil {
		t.Fatalf("take settlement subscription lock: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan recoveryCallResult, 1)
	go func() {
		result, err := service.RecoverAnimeFileApplication(ctx, scene.animeID, scene.command())
		done <- recoveryCallResult{result: result, err: err}
	}()
	waitForRecoveryLockWait(t, scene.db, "SELECT state FROM subscriptions")

	// The settlement side must still be able to take its next lock. If recovery
	// had taken the placement first, NOWAIT would expose the ABBA inversion.
	var placementID string
	if err := settlement.QueryRow(`SELECT id FROM placements WHERE id = $1 FOR UPDATE NOWAIT`, scene.candidate.PlacementID).Scan(&placementID); err != nil {
		t.Fatalf("settlement could not take placement after subscription: %v", err)
	}
	if err := settlement.Commit(); err != nil {
		t.Fatalf("release settlement locks: %v", err)
	}
	call := <-done
	if call.err != nil {
		t.Fatalf("confirm after settlement release: %v", call.err)
	}
}

func TestRecoverAnimeFileApplicationRefusesUnfinishedPlacementHandoff(t *testing.T) {
	for _, state := range []string{"pending", "waiting_retry", "failed"} {
		t.Run(state, func(t *testing.T) {
			scene := newRecoveryApplicationScene(t)
			service := scene.service(&staticObserver{files: scene.files})
			if _, err := scene.db.Exec(`
INSERT INTO placement_obligations(file_operation_id, state)
SELECT source_owner_ref, 'pending' FROM placements WHERE id = $1`,
				scene.candidate.PlacementID); err != nil {
				t.Fatalf("seed pending handoff: %v", err)
			}
			if state != "pending" {
				// The same shape the obligation worker leaves: a retrying or
				// exhausted row carries attempts and a reason.
				if _, err := scene.db.Exec(`
UPDATE placement_obligations
SET state = $2, attempts = 1,
    reason_code = 'retry_test', reason = 'retry test'
WHERE file_operation_id = (SELECT source_owner_ref FROM placements WHERE id = $1)`,
					scene.candidate.PlacementID, state); err != nil {
					t.Fatalf("transition handoff to %s: %v", state, err)
				}
			}

			occupied, err := NewRecoveryOccupancy(scene.db).RecoveryTargetsOccupied(
				context.Background(), recoveryCandidatePaths(scene.candidate), scene.candidate.PlacementID)
			if err != nil {
				t.Fatalf("scan occupancy: %v", err)
			}
			if !occupied {
				t.Fatal("scan occupancy offered a candidate whose own handoff is unfinished")
			}
			scene.assertRefusal(t, service, RecoveryReasonPlacementHandoff)
		})
	}
}

func TestRecoveryTargetPathLockSerializesCompleteUnit(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := scene.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback()
	paths := recoveryCandidatePaths(scene.candidate)
	if err := lockRecoveryTargetPathsTx(ctx, first, paths); err != nil {
		t.Fatalf("first unit lock: %v", err)
	}
	second, err := scene.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Rollback()
	done := make(chan error, 1)
	go func() { done <- lockRecoveryTargetPathsTx(ctx, second, paths) }()
	waitForRecoveryLockWait(t, scene.db, "pg_advisory_xact_lock")
	select {
	case err := <-done:
		t.Fatalf("second complete-unit lock returned while first was held: %v", err)
	default:
	}
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("second unit lock after release: %v", err)
	}
	if err := second.Commit(); err != nil {
		t.Fatal(err)
	}
}

type recoveryRaceObserver struct {
	files   map[string]domain.FileObservation
	arrived *atomic.Int32
	release chan struct{}
}

func (o *recoveryRaceObserver) ObserveMetadataFile(ctx context.Context, path string) (domain.FileObservation, error) {
	observation, ok := o.files[path]
	if !ok {
		return domain.FileObservation{}, errors.New("not found")
	}
	if o.arrived.Add(1) == 2 {
		close(o.release)
	}
	select {
	case <-o.release:
		return observation, nil
	case <-ctx.Done():
		return domain.FileObservation{}, ctx.Err()
	}
}

func removeRecoverySceneCompanions(t *testing.T, scene *recoveryApplicationScene) {
	t.Helper()
	if _, err := scene.db.Exec(`DELETE FROM placement_companions WHERE placement_id = $1`, scene.candidate.PlacementID); err != nil {
		t.Fatal(err)
	}
	scene.candidate.Companions = nil
	seedRecoveryCandidate(t, scene.db, scene.animeID, scene.candidate)
}

func TestRecoverAnimeFileApplicationConcurrentSameTargetHasOneOwner(t *testing.T) {
	first := newRecoveryApplicationScene(t)
	second := seedRecoveryApplicationScene(t, first.fixture, "anime-rec-1")
	removeRecoverySceneCompanions(t, first)
	removeRecoverySceneCompanions(t, second)
	if first.candidate.TargetPath != second.candidate.TargetPath {
		t.Fatalf("race fixture targets differ: %q != %q", first.candidate.TargetPath, second.candidate.TargetPath)
	}

	files := make(map[string]domain.FileObservation, len(first.files))
	for path, observation := range first.files {
		files[path] = observation
	}
	var arrived atomic.Int32
	release := make(chan struct{})
	newService := func(scene *recoveryApplicationScene) *Service {
		return scene.service(&recoveryRaceObserver{files: files, arrived: &arrived, release: release})
	}
	services := []*Service{newService(first), newService(second)}
	scenes := []*recoveryApplicationScene{first, second}
	start := make(chan struct{})
	done := make(chan recoveryCallResult, 2)
	var wg sync.WaitGroup
	for i := range services {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := services[i].RecoverAnimeFileApplication(ctx, scenes[i].animeID, scenes[i].command())
			done <- recoveryCallResult{result: result, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(done)
	// Each confirm stats the target twice: once in the lock-free preflight
	// (the barrier both pass before either opens its transaction) and once
	// under its unit advisory lock.
	if arrived.Load() != 4 {
		t.Fatalf("race barrier observed %d target observations, want 4", arrived.Load())
	}
	succeeded, refused := 0, 0
	for call := range done {
		if call.err == nil {
			succeeded++
			continue
		}
		var refusal *AnimeFilesRecoverRefusal
		if !errors.As(call.err, &refusal) || refusal.Code != RecoveryReasonTargetOccupied {
			t.Fatalf("losing confirm = %v, want target_occupied", call.err)
		}
		refused++
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("same-target confirms: succeeded=%d refused=%d, want one each", succeeded, refused)
	}
	var owners int
	if err := first.db.QueryRow(`SELECT count(*) FROM placements WHERE physical_path = $1 AND state = 'active'`, first.candidate.TargetPath).Scan(&owners); err != nil {
		t.Fatal(err)
	}
	if owners != 1 {
		t.Fatalf("active owners at recovered target = %d, want exactly one", owners)
	}
}

func TestRecoverAnimeFileApplicationRechecksUnitOccupancyAfterCAS(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})
	before := scene.snapshot(t)
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	function := fmt.Sprintf(`
CREATE FUNCTION recovery_test_insert_occupant() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.id = %s THEN
    INSERT INTO file_manager_operations
    (id, idempotency_key, request_digest, actor, operation, source_path, target_path,
     expected_size_bytes, collision, state, version)
    VALUES ('op-post-cas', 'op-post-cas', %s, 'web', 'move', '/unused-post-cas',
            %s, 64, 'block', 'ready', 1);
  END IF;
  RETURN NEW;
END $$`, quote(scene.candidate.PlacementID), quote(strings.Repeat("b", 64)), quote(scene.candidate.Companions[0].TargetPath))
	ownerDB := disposabledb.Owner(t)
	if _, err := ownerDB.Exec(function); err != nil {
		t.Fatalf("create CAS race trigger function: %v", err)
	}
	if _, err := ownerDB.Exec(`
CREATE TRIGGER recovery_test_insert_occupant
AFTER UPDATE ON placements FOR EACH ROW
EXECUTE FUNCTION recovery_test_insert_occupant()`); err != nil {
		t.Fatalf("create CAS race trigger: %v", err)
	}

	_, err := service.RecoverAnimeFileApplication(context.Background(), scene.animeID, scene.command())
	var refusal *AnimeFilesRecoverRefusal
	if !errors.As(err, &refusal) || refusal.Code != RecoveryReasonTargetOccupied {
		t.Fatalf("post-CAS occupancy refusal = %v, want target_occupied", err)
	}
	if after := scene.snapshot(t); after != before {
		t.Fatalf("post-CAS refusal did not roll back the unit:\nbefore=%+v\nafter=%+v", before, after)
	}
	var injected int
	if err := scene.db.QueryRow(`SELECT count(*) FROM file_manager_operations WHERE id = 'op-post-cas'`).Scan(&injected); err != nil {
		t.Fatal(err)
	}
	if injected != 0 {
		t.Fatal("the trigger-injected occupancy row survived the recovery rollback")
	}
}
