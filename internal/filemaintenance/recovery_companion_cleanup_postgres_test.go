package filemaintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bansoko/internal/library/adapter/filesystem"
)

// A recovered companion keeps its registration-time content digest, and that
// digest is the identity the loser cleanup handoff freezes and checks before
// moving the file. The chain runs on real files: confirm the recovery, read
// the loser companions exactly as the replacement handoff does, admit the
// cleanup with what it froze, and quarantine. A recovery that rewrote the
// companion identity, or a handoff that froze a different field, ends in
// cleanup_source_changed here instead of quarantined.
func TestRecoveredCompanionCleanupMatchesRegistrationDigest(t *testing.T) {
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

	// The subtitle the user moved by hand: same bytes as at registration, so
	// its registered digest is the sha256 of these bytes.
	content := []byte(strings.Repeat("s", int(companion.SizeBytes)))
	if err := os.MkdirAll(filepath.Dir(companion.TargetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(companion.TargetPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	if _, err := db.Exec(`UPDATE placement_companions SET fingerprint = $2 WHERE id = $1`, companion.ID, digest); err != nil {
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
	if losers[0].Path != companion.TargetPath || losers[0].Fingerprint != digest {
		t.Fatalf("handoff froze path=%q fingerprint=%q, want the recovered path %q and registration digest %q",
			losers[0].Path, losers[0].Fingerprint, companion.TargetPath, digest)
	}

	service := NewCleanupService(db)
	store := cleanupWorkItemStore(t, db)
	service.SetWorkItems(store)
	service.SetFileExecutor(filesystem.NewOSCleanupExecutor(root))
	id := companionCleanupID(companion.ID)
	input := cleanupInput()
	input.ID = id
	input.IdempotencyKey = "cln:" + companion.ID + ":dec-recovered"
	input.PlacementID = scene.candidate.PlacementID
	input.ManagedAnimeID = scene.animeID
	input.SourcePath = losers[0].Path
	input.SourceFingerprint = losers[0].Fingerprint
	input.QuarantinePath = filepath.Join(filepath.Dir(losers[0].Path), ".bansoko-cleanup-"+id, "object")
	op, err := service.Admit(ctx, input)
	if err != nil {
		t.Fatalf("admit companion cleanup: %v", err)
	}
	runMetadataQuarantine(t, service, store, op)
	if _, err := os.Lstat(companion.TargetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered companion still at %s after quarantine: %v", companion.TargetPath, err)
	}
}
