package filemaintenance

import (
	"context"
	"fmt"
	"testing"
	"time"

	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
)

// An acquisition accepted under the old offset may finish after the offset
// edit. Whichever transaction takes the source lock first, the first placement
// ends on the latest episode identity while the DownloadTask keeps its frozen
// acceptance input.
func TestPGInflightPlacementAdmissionUsesLatestOffsetForEitherLockOrder(t *testing.T) {
	for _, order := range []string{"offset_first", "admission_first"} {
		t.Run(order, func(t *testing.T) {
			f := newOffsetFixture(t, ModeAll, domain.CollisionBlock)
			const releaseID = "rel-inflight-offset"
			file := f.seedFile(t, "anime-1", "sub-1", releaseID, "Show/inflight.mkv", "tv:e2", "2", Facts{}, "2026-07-01T00:00:00Z")
			if _, err := f.db.Exec(`DELETE FROM placements WHERE id = $1`, file.placementID); err != nil {
				t.Fatal(err)
			}
			var frozenBefore string
			var taskVersionBefore int64
			if err := f.db.QueryRow(`SELECT frozen_input::text, version FROM download_tasks WHERE id = $1`, "dt-"+releaseID).Scan(&frozenBefore, &taskVersionBefore); err != nil {
				t.Fatal(err)
			}
			input := placementInput{
				SourceEvidenceType:  "file_operation",
				SourceOwnerRef:      "op-" + releaseID,
				PhysicalPath:        file.path,
				SourceFingerprint:   file.fingerprint,
				TargetFingerprint:   file.fingerprint,
				QualityFactsRef:     releaseID,
				FactsVersion:        "candidate-facts-v1",
				Coverage:            []string{"tv:e2"},
				ManagedAnimeID:      "anime-1",
				ReplacementEligible: true,
			}
			placementService := NewPlacementService(f.db, nil, nil)
			ctx := context.Background()

			if order == "offset_first" {
				tx, err := f.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				var version int64
				if err := tx.QueryRow(`UPDATE rss_sources SET episode_offset = 25, version = version + 1 WHERE id = $1 RETURNING version`, "src-"+releaseID).Scan(&version); err != nil {
					t.Fatal(err)
				}
				if _, _, err := NewOffsetAlignmentService().AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-1", "src-"+releaseID, version, 0, 25); err != nil {
					t.Fatal(err)
				}
				admitted := make(chan error, 1)
				go func() {
					_, err := placementService.admit(ctx, input)
					admitted <- err
				}()
				requireStillWaiting(t, admitted, "placement admission crossed the uncommitted offset lock")
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				requireTransactionResult(t, admitted, "placement admission")
			} else {
				tx, err := f.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := placementService.admitTx(ctx, tx, input); err != nil {
					t.Fatal(err)
				}
				changed := make(chan error, 1)
				go func() {
					changed <- changeOffsetForInflightTest(ctx, f, releaseID)
				}()
				requireStillWaiting(t, changed, "offset change crossed the uncommitted placement lock")
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				requireTransactionResult(t, changed, "offset change")
			}

			f.expectPlacement(t, PlacementIDFor("file_operation", "op-"+releaseID), "tv:e27", 0, file.path)
			var frozenAfter string
			var taskVersionAfter int64
			if err := f.db.QueryRow(`SELECT frozen_input::text, version FROM download_tasks WHERE id = $1`, "dt-"+releaseID).Scan(&frozenAfter, &taskVersionAfter); err != nil {
				t.Fatal(err)
			}
			if frozenAfter != frozenBefore || taskVersionAfter != taskVersionBefore {
				t.Fatal("offset reconciliation rewrote the historical DownloadTask")
			}
			var changeCount, keyCount int
			if err := f.db.QueryRow(`SELECT count(*), (SELECT count(*) FROM offset_alignment_change_keys) FROM offset_alignment_changes`).Scan(&changeCount, &keyCount); err != nil {
				t.Fatal(err)
			}
			if order == "offset_first" && (changeCount != 1 || keyCount != 0) {
				t.Fatalf("offset-first changes/keys = %d/%d, want 1/0", changeCount, keyCount)
			}
			if order == "admission_first" && (changeCount != 1 || keyCount != 1) {
				t.Fatalf("admission-first changes/keys = %d/%d, want 1/1", changeCount, keyCount)
			}
		})
	}
}

func changeOffsetForInflightTest(ctx context.Context, f *offsetFixture, releaseID string) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int64
	if err := tx.QueryRowContext(ctx, `UPDATE rss_sources SET episode_offset = 25, version = version + 1 WHERE id = $1 RETURNING version`, "src-"+releaseID).Scan(&version); err != nil {
		return err
	}
	if _, _, err := NewOffsetAlignmentService().AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-1", "src-"+releaseID, version, 0, 25); err != nil {
		return err
	}
	return tx.Commit()
}

func requireStillWaiting(t *testing.T, result <-chan error, message string) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("%s: %v", message, err)
	case <-time.After(100 * time.Millisecond):
	}
}

func requireTransactionResult(t *testing.T, result <-chan error, operation string) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal(fmt.Sprintf("%s did not finish after the lock was released", operation))
	}
}
