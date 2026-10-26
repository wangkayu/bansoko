package filemaintenance

import (
	"context"
	"testing"
)

// A placement re-attached by a REAL confirm carries the typed metadata
// envelope; calibration's identity read (placementObservationConflictTx) must
// see the inode inside it. A same-size different-inode file at the recovered
// target is fingerprint_conflict; the recovered file itself is not.
func TestRecoveredPlacementCalibratesSameSizeOtherInodeAsConflict(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})
	if _, err := service.RecoverAnimeFileApplication(context.Background(), scene.animeID, scene.command()); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	ctx := context.Background()
	tx, err := scene.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for observed, want := range map[string]bool{
		"meta:size=4096;dev=2;ino=11": false, // the recovered file
		"meta:size=4096;dev=9;ino=11": false, // a device-only remount
		"meta:size=4096;dev=2;ino=99": true,  // same size, replaced inode
		"meta:size=4097;dev=2;ino=11": true,  // size changed
	} {
		conflict, err := placementObservationConflictTx(ctx, tx, scene.candidate.PlacementID, observed)
		if err != nil {
			t.Fatalf("%s: %v", observed, err)
		}
		if conflict != want {
			t.Fatalf("%s conflict = %v, want %v", observed, conflict, want)
		}
	}
}
