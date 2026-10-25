package filemaintenance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bansoko/internal/library/domain"
)

// recoveryConfirmScene freezes one committed-generation candidate: the video
// at the rendered target plus one subtitle companion, both with metadata
// fingerprints the confirm re-stats through the fake observer.
func recoveryConfirmScene() (recoveryCandidateFact, map[string]domain.FileObservation) {
	candidate := recoveryCandidateFact{
		PlacementID:       "plc-1",
		PlacementVersion:  4,
		Generation:        7,
		OldPhysicalPath:   "/library/Show/raw ep1.mkv",
		TargetPath:        "/library/Show/Season 01/EP 01.mkv",
		TargetFingerprint: "meta:size=4096;dev=2;ino=11",
		SizeBytes:         4096,
		Companions: []recoveryCompanionFact{{
			ID:          "c-1",
			Version:     2,
			TargetPath:  "/library/Show/Season 01/EP 01.zh.ass",
			Fingerprint: "meta:size=64;dev=2;ino=12",
			SizeBytes:   64,
		}},
	}
	files := map[string]domain.FileObservation{
		candidate.TargetPath:               {Fingerprint: candidate.TargetFingerprint, SizeBytes: 4096},
		candidate.Companions[0].TargetPath: {Fingerprint: candidate.Companions[0].Fingerprint, SizeBytes: 64},
	}
	return candidate, files
}

// fakeRecoveryObserver stats from a fixed table; unlisted paths fail like a
// vanished file.
type fakeRecoveryObserver struct {
	files map[string]domain.FileObservation
	asked []string
}

func (f *fakeRecoveryObserver) ObserveFile(_ context.Context, path string) (domain.FileObservation, error) {
	f.asked = append(f.asked, path)
	if observation, ok := f.files[path]; ok {
		return observation, nil
	}
	return domain.FileObservation{}, errors.New("not found")
}

func recoveryCommand(candidate recoveryCandidateFact) AnimeFileRecoverCommand {
	return AnimeFileRecoverCommand{
		PlacementID:      candidate.PlacementID,
		Generation:       candidate.Generation,
		TargetPath:       candidate.TargetPath,
		Fingerprint:      candidate.TargetFingerprint,
		PlacementVersion: candidate.PlacementVersion,
	}
}

// The request token must match the frozen candidate field by field: a stale
// placement version is placement_changed, a different target or fingerprint
// is target_changed. The happy token passes.
func TestMatchRecoveryCommand(t *testing.T) {
	candidate, _ := recoveryConfirmScene()
	command := recoveryCommand(candidate)
	if err := matchRecoveryCommand(candidate, command); err != nil {
		t.Fatalf("matching token refused: %v", err)
	}

	stale := command
	stale.PlacementVersion = candidate.PlacementVersion + 1
	if err := matchRecoveryCommand(candidate, stale); err == nil || err.Error() != "ANIME_FILES_RECOVER_REFUSED: placement_changed" {
		t.Fatalf("stale version = %v, want placement_changed", err)
	}

	moved := command
	moved.TargetPath = "/library/Show/Season 01/OTHER.mkv"
	if err := matchRecoveryCommand(candidate, moved); err == nil || err.Error() != "ANIME_FILES_RECOVER_REFUSED: target_changed" {
		t.Fatalf("moved target = %v, want target_changed", err)
	}

	refingerprinted := command
	refingerprinted.Fingerprint = "meta:size=4096;dev=2;ino=99"
	if err := matchRecoveryCommand(candidate, refingerprinted); err == nil || err.Error() != "ANIME_FILES_RECOVER_REFUSED: target_changed" {
		t.Fatalf("changed fingerprint = %v, want target_changed", err)
	}
}

// Every companion of the unit must still sit at its frozen path with the
// frozen identity: one vanished or changed companion refuses the WHOLE
// confirm and the video is never recovered alone.
func TestVerifyRecoveryCompanionsAtomic(t *testing.T) {
	candidate, files := recoveryConfirmScene()

	if err := verifyRecoveryCompanionStats(context.Background(), &fakeRecoveryObserver{files: files}, candidate); err != nil {
		t.Fatalf("complete unit refused: %v", err)
	}

	vanishedFiles := map[string]domain.FileObservation{}
	err := verifyRecoveryCompanionStats(context.Background(), &fakeRecoveryObserver{files: vanishedFiles}, candidate)
	if err == nil || err.Error() != "ANIME_FILES_RECOVER_REFUSED: companion_missing" {
		t.Fatalf("vanished companion = %v, want companion_missing", err)
	}

	changedFiles := map[string]domain.FileObservation{
		candidate.Companions[0].TargetPath: {Fingerprint: "meta:size=99;dev=2;ino=12", SizeBytes: 99},
	}
	err = verifyRecoveryCompanionStats(context.Background(), &fakeRecoveryObserver{files: changedFiles}, candidate)
	if err == nil || err.Error() != "ANIME_FILES_RECOVER_REFUSED: companion_occupied" {
		t.Fatalf("changed companion = %v, want companion_occupied", err)
	}
}

// The typed refusal catalogue stays exactly the confirm's fail-closed
// vocabulary; every code rides as details[0].code on the 409.
func TestRecoveryRefusalCodes(t *testing.T) {
	codes := map[string]string{
		RecoveryReasonOwnerRetired:      "owner_retired",
		RecoveryReasonNotStarted:        "not_started",
		RecoveryReasonCandidateGone:     "candidate_gone",
		RecoveryReasonScanChanged:       "scan_changed",
		RecoveryReasonScanRunning:       "scan_running",
		RecoveryReasonPlacementChanged:  "placement_changed",
		RecoveryReasonTargetMissing:     "target_missing",
		RecoveryReasonTargetChanged:     "target_changed",
		RecoveryReasonTargetOccupied:    "target_occupied",
		RecoveryReasonCompanionMissing:  "companion_missing",
		RecoveryReasonCompanionOccupied: "companion_occupied",
	}
	for constant, literal := range codes {
		if constant != literal {
			t.Fatalf("constant %q drifted from %q", constant, literal)
		}
		var refusal *AnimeFilesRecoverRefusal
		err := &AnimeFilesRecoverRefusal{Code: constant}
		if !errors.As(err, &refusal) || refusal.Code != constant {
			t.Fatalf("refusal %q does not unwrap", constant)
		}
		if !strings.HasPrefix(err.Error(), "ANIME_FILES_RECOVER_REFUSED: ") {
			t.Fatalf("refusal error = %q", err.Error())
		}
	}
}

// A companion is identified by size plus metadata fingerprint exactly like the
// video: a device-only change (same size, same inode, new dev) is accepted,
// while an inode or size change refuses the whole unit. Companions keep their
// hex digest in the CHECK-guarded column, so no meta fingerprint is written.
func TestRecoveryCompanionIdentityIgnoresDevice(t *testing.T) {
	unit := func() (recoveryCandidateFact, map[string]domain.FileObservation) {
		candidate, files := recoveryConfirmScene()
		return candidate, files
	}

	t.Run("device-only change is accepted", func(t *testing.T) {
		candidate, files := unit()
		files[candidate.Companions[0].TargetPath] = domain.FileObservation{
			Fingerprint: "meta:size=64;dev=9;ino=12", SizeBytes: 64,
		}
		if err := verifyRecoveryCompanionStats(context.Background(), &fakeRecoveryObserver{files: files}, candidate); err != nil {
			t.Fatalf("remounted companion refused: %v", err)
		}
	})
	t.Run("inode change is rejected", func(t *testing.T) {
		candidate, files := unit()
		files[candidate.Companions[0].TargetPath] = domain.FileObservation{
			Fingerprint: "meta:size=64;dev=2;ino=99", SizeBytes: 64,
		}
		err := verifyRecoveryCompanionStats(context.Background(), &fakeRecoveryObserver{files: files}, candidate)
		if err == nil || err.Error() != "ANIME_FILES_RECOVER_REFUSED: companion_occupied" {
			t.Fatalf("inode change = %v, want companion_occupied", err)
		}
	})
	t.Run("size change is rejected", func(t *testing.T) {
		candidate, files := unit()
		files[candidate.Companions[0].TargetPath] = domain.FileObservation{
			Fingerprint: "meta:size=65;dev=2;ino=12", SizeBytes: 65,
		}
		err := verifyRecoveryCompanionStats(context.Background(), &fakeRecoveryObserver{files: files}, candidate)
		if err == nil || err.Error() != "ANIME_FILES_RECOVER_REFUSED: companion_occupied" {
			t.Fatalf("size change = %v, want companion_occupied", err)
		}
	})
}

// The metadata fingerprint comparison the confirm uses excludes the device
// number: an NFS remount (same size, same inode, new dev) must not read as a
// content change, while any size or inode change fails closed.
func TestRecoveryIdentityIgnoresDevice(t *testing.T) {
	candidate, _ := recoveryConfirmScene()
	if !domain.MetadataFingerprintsMatch("meta:size=4096;dev=9;ino=11", candidate.TargetFingerprint) {
		t.Fatal("remounted device read as a change")
	}
	if domain.MetadataFingerprintsMatch("meta:size=4096;dev=2;ino=99", candidate.TargetFingerprint) {
		t.Fatal("inode change accepted")
	}
	if domain.MetadataFingerprintsMatch("meta:size=1;dev=2;ino=11", candidate.TargetFingerprint) {
		t.Fatal("size change accepted")
	}
}
