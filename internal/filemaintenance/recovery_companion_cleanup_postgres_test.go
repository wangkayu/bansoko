package filemaintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
)

// A recovered companion keeps its registration-time size, and that size at
// its recovered path is the identity the loser deletion handoff records and
// checks before deleting the file. The chain runs on real files: confirm the
// recovery, read the loser companions exactly as the replacement handoff
// does, admit the deletion with what it recorded and end the companion
// relation as the handoff does, and delete. A recovery that
// rewrote the companion identity ends in a skip here instead of deleted.
func TestRecoveredCompanionCleanupMatchesRegistrationIdentity(t *testing.T) {
	ctx := context.Background()
	fixture := newCalibrationFixture(t)
	root := t.TempDir()
	rules := recoveryRules()
	rules.TV.DirectoryTemplate = filepath.Join(root, rules.TV.DirectoryTemplate)
	rules.Movie.DirectoryTemplate = filepath.Join(root, rules.Movie.DirectoryTemplate)
	scene := seedRecoveryApplicationSceneWithRules(t, fixture, "anime-rec-cln", rules)
	db := scene.db
	companion := scene.candidate.Companions[0]
	if !strings.HasPrefix(companion.TargetPath, root) {
		t.Fatalf("companion target %q is not under the test root %q", companion.TargetPath, root)
	}

	// The subtitle the user moved by hand: same size as at registration, so
	// its registered fingerprint is the metadata form of that size.
	content := []byte(strings.Repeat("s", int(companion.SizeBytes)))
	if err := os.MkdirAll(filepath.Dir(companion.TargetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(companion.TargetPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	registered := domain.MetadataFingerprint(int64(len(content)), domain.FileIdentity{})
	if _, err := db.Exec(`UPDATE placement_companions SET fingerprint = $2 WHERE id = $1`, companion.ID, registered); err != nil {
		t.Fatal(err)
	}

	if _, err := scene.service(&staticObserver{files: scene.files}).RecoverAnimeFileApplication(ctx, scene.animeID, scene.command()); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	losers, err := loserCompanionsForCleanup(ctx, db, scene.candidate.PlacementID, "")
	if err != nil {
		t.Fatalf("load loser companions: %v", err)
	}
	if len(losers) != 1 || losers[0].ID != companion.ID {
		t.Fatalf("loser companions = %+v, want only %s", losers, companion.ID)
	}
	if losers[0].Path != companion.TargetPath || losers[0].SizeBytes != companion.SizeBytes {
		t.Fatalf("handoff read path=%q size=%d, want the recovered path %q and registration size %d",
			losers[0].Path, losers[0].SizeBytes, companion.TargetPath, companion.SizeBytes)
	}

	service := NewCleanupService(db)
	store := cleanupWorkItemStore(t, db)
	service.SetWorkItems(store)
	service.SetFileExecutor(filesystem.NewOSCleanupExecutor(root))
	id := companionCleanupID(companion.ID)
	op, err := service.Admit(ctx, CleanupInput{
		ID:                id,
		IdempotencyKey:    "cln:" + companion.ID + ":dec-recovered",
		PlacementID:       scene.candidate.PlacementID,
		ManagedAnimeID:    scene.animeID,
		Coverage:          []string{"tv:e1"},
		ReplacementRef:    "dec-recovered",
		AdmissionMode:     "single_copy",
		SourcePath:        losers[0].Path,
		SourceFingerprint: domain.MetadataFingerprint(losers[0].SizeBytes, domain.FileIdentity{}),
	})
	if err != nil {
		t.Fatalf("admit companion cleanup: %v", err)
	}
	// The handoff closes the companion relation in the admitting transaction
	// (admitCompanionDeletionsTx); an active companion still owns its path and
	// the deletion would skip as loser_path_owned.
	if err := endCompanionForCleanup(ctx, db, op.ID, companion.ID); err != nil {
		t.Fatalf("end companion for cleanup: %v", err)
	}
	runCleanupDelete(t, service, store)
	var state, failureCode, reason string
	if err := db.QueryRow(`SELECT state, COALESCE(failure_code, ''), COALESCE(reason, '') FROM replacement_deletions WHERE id = $1`, op.ID).Scan(&state, &failureCode, &reason); err != nil {
		t.Fatal(err)
	}
	if state != string(CleanupDeleted) {
		t.Fatalf("companion cleanup state = %s (failure_code=%q reason=%q), want deleted", state, failureCode, reason)
	}
	if _, err := os.Lstat(companion.TargetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered companion still at %s after deletion: %v", companion.TargetPath, err)
	}
}
