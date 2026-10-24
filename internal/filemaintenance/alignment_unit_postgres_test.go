package filemaintenance

import (
	"bansoko/internal/deployment"
	executorpkg "bansoko/internal/filemaintenance/executor"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// alignmentUnitFixture: one video placement at source with a frozen unit
// contract (collision + companion targets) ready to execute.
type alignmentUnitFixture struct {
	f                     *opsFixture
	op                    *AlignmentOperation
	claim                 workitems.Claim
	store                 *workitems.Store
	source, target, root  string
	companionTarget       string
	sourceBytes, occupant []byte
}

func newAlignmentUnitFixture(t *testing.T, collision domain.CollisionPolicy, occupyTarget, occupyCompanion bool) *alignmentUnitFixture {
	t.Helper()
	return newAlignmentUnitFixtureIn(t, collision, occupyTarget, occupyCompanion, "")
}

// newAlignmentUnitFixtureIn places the target under targetDir (relative to the
// fixture root), which is left uncreated.
func newAlignmentUnitFixtureIn(t *testing.T, collision domain.CollisionPolicy, occupyTarget, occupyCompanion bool, targetDir string) *alignmentUnitFixture {
	t.Helper()
	ctx := context.Background()
	f := openOpsTestDB(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u := &alignmentUnitFixture{f: f, root: root, sourceBytes: []byte("video bytes"), occupant: []byte("a file the user put here")}
	u.source, u.target = filepath.Join(root, "source.mkv"), filepath.Join(root, targetDir, "Show - 01.mkv")
	u.companionTarget = CompanionPath(u.target, "chs", "ass")
	if err := os.WriteFile(u.source, u.sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if occupyTarget {
		if err := os.WriteFile(u.target, u.occupant, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if occupyCompanion {
		if err := os.WriteFile(u.companionTarget, u.occupant, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint, err := (filesystem.OSFileObserver{}).ObserveMetadata(ctx, u.source) // a scan-derived source is path-level
	if err != nil {
		t.Fatal(err)
	}
	input := testOperation()
	input.SourcePath, input.TargetPath, input.SourceFingerprint = u.source, u.target, fingerprint
	input.TargetContract, _ = json.Marshal(videoAlignmentContract{
		TargetPath: u.target, Fingerprint: fingerprint, Collision: string(collision),
		Companions: []unitCompanionTarget{{TargetPath: u.companionTarget, Fingerprint: strings.Repeat("a", 64)}},
	})
	seedAlignmentPlacement(t, f.db, input.ManagedAnimeID, u.source, fingerprint)
	op, err := f.svc.CreateOperation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	version, err := f.svc.MarkReady(ctx, op.ID, op.Version)
	if err != nil {
		t.Fatal(err)
	}
	u.op = op
	u.store, u.claim = alignmentWorkItemClaim(t, ctx, f.db, op.ID, version)
	f.svc.SetWorkItems(u.store)
	f.svc.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	return u
}

func (u *alignmentUnitFixture) commit(t *testing.T, outcome worker.Outcome) {
	t.Helper()
	tx, err := u.f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outcome.WriteOwner(context.Background(), txscope.Wrap(tx), outcome.OwnerResult); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func (u *alignmentUnitFixture) owner(t *testing.T) (state, code, reason, placementPath string) {
	t.Helper()
	if err := u.f.db.QueryRow(`SELECT state, COALESCE(failure_code,''), COALESCE(reason,'') FROM alignment_operations WHERE id=$1`, u.op.ID).Scan(&state, &code, &reason); err != nil {
		t.Fatal(err)
	}
	if err := u.f.db.QueryRow(`SELECT physical_path FROM placements WHERE id='plc-align-1'`).Scan(&placementPath); err != nil {
		t.Fatal(err)
	}
	return
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	if got, err := os.ReadFile(path); err != nil || string(got) != string(want) {
		t.Fatalf("%s = %q err=%v, want %q", filepath.Base(path), got, err, want)
	}
}

// A naming change can rename the anime and season folders, so the align
// creates the missing target folders before the move.
func TestPGAlignmentCreatesMissingTargetFolders(t *testing.T) {
	u := newAlignmentUnitFixtureIn(t, domain.CollisionBlock, false, false, filepath.Join("Show (2021)", "Season 02"))
	outcome, err := u.f.svc.ExecuteHandler().Handle(context.Background(), u.claim)
	if err != nil {
		t.Fatal(err)
	}
	u.commit(t, outcome)
	state, code, reason, path := u.owner(t)
	if state != "completed" || path != u.target {
		t.Fatalf("state=%s code=%s reason=%q path=%s, want completed at %s", state, code, reason, path, u.target)
	}
	assertFileBytes(t, u.target, u.sourceBytes)
}

// TestPGAlignmentUnitCollision proves the unit rules at execution: under fail
// an occupied subtitle target keeps the video in place; under overwrite an
// occupant of the video target is replaced; a managed one is displaced with
// the moved placement as its successor and its companions end.
func TestPGAlignmentUnitCollision(t *testing.T) {
	ctx := context.Background()
	t.Run("fail_companion_occupied", func(t *testing.T) {
		u := newAlignmentUnitFixture(t, domain.CollisionBlock, false, true)
		outcome, err := u.f.svc.ExecuteHandler().Handle(ctx, u.claim)
		if err != nil {
			t.Fatal(err)
		}
		u.commit(t, outcome)
		state, code, reason, path := u.owner(t)
		if state != "blocked" || code != "companion_target_occupied" || !strings.Contains(reason, filepath.Base(u.companionTarget)) || path != u.source {
			t.Fatalf("state=%s code=%s reason=%q path=%s", state, code, reason, path)
		}
		assertFileBytes(t, u.source, u.sourceBytes)
		assertFileBytes(t, u.companionTarget, u.occupant)
	})
	t.Run("overwrite_unmanaged_video_occupant", func(t *testing.T) {
		u := newAlignmentUnitFixture(t, domain.CollisionOverwrite, true, true)
		outcome, err := u.f.svc.ExecuteHandler().Handle(ctx, u.claim)
		if err != nil {
			t.Fatal(err)
		}
		u.commit(t, outcome)
		state, code, _, path := u.owner(t)
		if state != "completed" || code != "" || path != u.target {
			t.Fatalf("state=%s code=%s path=%s", state, code, path)
		}
		assertFileBytes(t, u.target, u.sourceBytes)
		if leftovers, _ := filepath.Glob(filepath.Join(u.root, ".bansoko-overwrite-*")); len(leftovers) != 0 {
			t.Fatalf("staging left behind: %v", leftovers)
		}
	})
	t.Run("overwrite_managed_video_target", func(t *testing.T) {
		u := newAlignmentUnitFixture(t, domain.CollisionOverwrite, true, false)
		if _, err := u.f.db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, version, created_at, updated_at)
VALUES ('plc-align-managed', $1, $2, 'reconciliation', 'obs-align-managed', $3, $3, 'parser-v1', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			u.target, u.op.ManagedAnimeID, strings.Repeat("b", 64)); err != nil {
			t.Fatal(err)
		}
		if _, err := u.f.db.Exec(`
INSERT INTO placement_companions(id, placement_id, managed_anime_id, source_ref, registration, media_kind, suffix, original_extension, current_path, size_bytes, fingerprint)
VALUES ('cmp-align-managed', 'plc-align-managed', $2, 'src-align-managed', 'adopted', 'subtitle', 'jpn', 'ass', $1, 1, $3)`, CompanionPath(u.target, "jpn", "ass"), u.op.ManagedAnimeID, strings.Repeat("c", 64)); err != nil {
			t.Fatal(err)
		}
		outcome, err := u.f.svc.ExecuteHandler().Handle(ctx, u.claim)
		if err != nil {
			t.Fatal(err)
		}
		u.commit(t, outcome)
		state, code, _, path := u.owner(t)
		if state != "completed" || code != "" || path != u.target {
			t.Fatalf("state=%s code=%s path=%s, want completed at target", state, code, path)
		}
		assertFileBytes(t, u.target, u.sourceBytes)
		var occupantState, cause, successor, companionState, endedReason string
		if err := u.f.db.QueryRow(`SELECT state, COALESCE(displaced_cause,''), COALESCE(displaced_by_placement_id,'') FROM placements WHERE id='plc-align-managed'`).Scan(&occupantState, &cause, &successor); err != nil {
			t.Fatal(err)
		}
		if occupantState != "displaced" || cause != "overwrite" || successor != "plc-align-1" {
			t.Fatalf("managed occupant state=%s cause=%s successor=%s, want displaced by plc-align-1", occupantState, cause, successor)
		}
		if err := u.f.db.QueryRow(`SELECT state, COALESCE(ended_reason,'') FROM placement_companions WHERE id='cmp-align-managed'`).Scan(&companionState, &endedReason); err != nil {
			t.Fatal(err)
		}
		if companionState != "ended" || endedReason != "displaced" {
			t.Fatalf("displaced video's companion state=%s reason=%s", companionState, endedReason)
		}
	})
	t.Run("overwrite_managed_companion_target", func(t *testing.T) {
		u := newAlignmentUnitFixture(t, domain.CollisionOverwrite, false, true)
		if _, err := u.f.db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, version, created_at, updated_at)
VALUES ('plc-align-other', $1, $2, 'reconciliation', 'obs-align-other', $3, $3, 'parser-v1', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			filepath.Join(u.root, "Other - 01.mkv"), u.op.ManagedAnimeID, strings.Repeat("b", 64)); err != nil {
			t.Fatal(err)
		}
		if _, err := u.f.db.Exec(`
INSERT INTO placement_companions(id, placement_id, managed_anime_id, source_ref, registration, media_kind, suffix, original_extension, current_path, size_bytes, fingerprint)
VALUES ('cmp-align-other', 'plc-align-other', $2, 'src-align-other', 'adopted', 'subtitle', 'chs', 'ass', $1, 1, $3)`, u.companionTarget, u.op.ManagedAnimeID, strings.Repeat("c", 64)); err != nil {
			t.Fatal(err)
		}
		outcome, err := u.f.svc.ExecuteHandler().Handle(ctx, u.claim)
		if err != nil {
			t.Fatal(err)
		}
		u.commit(t, outcome)
		state, code, _, path := u.owner(t)
		if state != "completed" || code != "" || path != u.target {
			t.Fatalf("state=%s code=%s path=%s, a managed companion target no longer blocks overwrite", state, code, path)
		}
		assertFileBytes(t, u.target, u.sourceBytes)
	})
}

// crashingOverwriteExecutor stops the process right after the exchange
// stage was persisted, once.
type crashingOverwriteExecutor struct {
	filesystem.OSFileExecutor
	at      domain.OverwriteStage
	crashed *bool
}

func (e crashingOverwriteExecutor) ExecuteMaintenanceOverwrite(ctx context.Context, op domain.FileOperation, i executorpkg.MaintenanceInvocation, checkpoint domain.FileOperationCheckpoint) (domain.FileOperation, error) {
	return e.OSFileExecutor.ExecuteMaintenanceOverwrite(ctx, op, i, func(ctx context.Context, current domain.FileOperation, recovery domain.OverwriteRecovery) (domain.FileOperation, error) {
		updated, err := checkpoint(ctx, current, recovery)
		if err == nil && !*e.crashed && recovery.Stage == e.at {
			*e.crashed = true
			return updated, errors.New("process stopped")
		}
		return updated, err
	})
}

// TestPGAlignmentOverwriteRecoversFromPersistedRecord: a process stopped
// right after a persisted exchange stage leaves the work item unresolved; a
// restart (deployment reconcile and/or work-item recovery) finishes the
// exchange from the operation's record and settles the placement.
func TestPGAlignmentOverwriteRecoversFromPersistedRecord(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		stage     domain.OverwriteStage
		reconcile bool
	}{{domain.OverwriteStageStaged, false}, {domain.OverwriteStageExchanged, false}, {domain.OverwriteStageExchanged, true}} {
		t.Run(string(tc.stage)+map[bool]string{true: "_reconcile", false: ""}[tc.reconcile], func(t *testing.T) {
			u := newAlignmentUnitFixture(t, domain.CollisionOverwrite, true, false)
			crashed := false
			u.f.svc.SetFilePorts(crashingOverwriteExecutor{at: tc.stage, crashed: &crashed}, filesystem.OSFileObserver{})
			if _, err := u.f.svc.ExecuteHandler().Handle(ctx, u.claim); !errors.Is(err, workitems.ErrPublicationUnresolved) || !crashed {
				t.Fatalf("the interrupted exchange must stay unresolved: err=%v crashed=%v", err, crashed)
			}
			var record sql.NullString
			if err := u.f.db.QueryRow(`SELECT overwrite_recovery::text FROM alignment_operations WHERE id=$1`, u.op.ID).Scan(&record); err != nil || !record.Valid {
				t.Fatalf("no persisted exchange record: %v", err)
			}
			// Restart: a fresh executor with no in-process state.
			u.f.svc.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
			if tc.reconcile {
				if _, err := u.f.svc.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{OperationID: "align-overwrite"}); err != nil {
					t.Fatal(err)
				}
				assertFileBytes(t, u.target, u.sourceBytes)
			}
			claim := u.claim
			if err := u.f.db.QueryRow(`SELECT version FROM alignment_operations WHERE id=$1`, u.op.ID).Scan(&claim.ExpectedOwnerVersion); err != nil {
				t.Fatal(err)
			}
			recovered, err := u.f.svc.ExecuteHandler().(worker.RecoveryObserver).ObserveRecovery(ctx, claim)
			if err != nil || recovered.Disposition != workitems.DispositionConfirmed {
				t.Fatalf("recovery=%+v %v", recovered, err)
			}
			u.commit(t, recovered)
			state, _, _, path := u.owner(t)
			if state != "completed" || path != u.target {
				t.Fatalf("state=%s path=%s", state, path)
			}
			assertFileBytes(t, u.target, u.sourceBytes)
			if _, err := os.Stat(u.source); !os.IsNotExist(err) {
				t.Fatalf("source must be moved: %v", err)
			}
			if leftovers, _ := filepath.Glob(filepath.Join(u.root, ".bansoko-overwrite-*")); len(leftovers) != 0 {
				t.Fatalf("staging left behind: %v", leftovers)
			}
		})
	}
}
