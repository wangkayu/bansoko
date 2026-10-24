package application

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	artifactapplication "bansoko/internal/artifact/application"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// Successful planning is not successful acquisition: a local subtitle render
// error must remain a failed owner while an allowed video really completes.
func TestPGPartialNamingFailureKeepsFailedOwnerAndCompletesVideo(t *testing.T) {
	db, f := openFileOperationDBWithPolicy(t, func(policy map[string]any) {
		bindingManifestFixture(t, map[string]any{"file_ref": "subtitle", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"})(policy)
		rules := naming.DefaultNamingRules()
		rules.TV.DirectoryTemplate = "partial"
		rules.TV.VideoFilenameTemplate = "Video.mkv"
		// naming-render-2 renders a whole-interpolation null as "", so the
		// subtitle's render error is a runtime throw: reading a property of the
		// null extract() result.
		rules.SubtitleSuffixTemplate = "${extract(file.name, '(never)').length}"
		policy["effective_sections"].(map[string]any)[animedomain.SectionNamingRules] = rules
	})
	defer db.Close()
	p := filepath.Join(t.TempDir(), "source.chs.ass")
	if err := os.WriteFile(p, []byte("subtitle"), 0600); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, f, "partial-sub", "subtitle", "source.chs.ass", p, "subtitle", 8)
	s := newFixtureServiceWithPorts(t, db, f, filesystem.OSFileExecutor{}, freshTargetObserver{})
	ctx := context.Background()
	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	planID, err := seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: f.artifactID}, nil)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	claim := claimPlanWorkItem(t, db, "partial-plan")
	if claim.Owner.ID != planID {
		t.Fatal("wrong plan")
	}
	out, err := s.PostProcessingPlanHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != worker.OutcomeCommitted || out.OwnerResult.State != "succeeded" {
		t.Fatalf("typed planning outcome=%+v", out)
	}
	rows := loadOperations(t, db, f.artifactID)
	if len(rows) != 1 {
		t.Fatalf("planning created pseudo failed Operation: %+v", rows)
	}
	video := operationForFile(t, rows, f.fileID)
	want := filepath.Join(canonicalFixtureRoot(t, f), "partial", "Video.mkv")
	if video.state != "pending" || video.targetPath != want {
		t.Fatalf("video=%+v want=%s", video, want)
	}
	assertTypedFailedFileResult(t, db, f.artifactID, "subtitle", "template_compile_error")
	var diagnostic string
	if err := db.QueryRow(`SELECT plan_diagnostic::text FROM acquisition_files WHERE file_ref='subtitle' AND acquisition_id=(SELECT acquisition_id FROM artifacts WHERE id=$1)`, f.artifactID).Scan(&diagnostic); err != nil || diagnostic == "{}" {
		t.Fatalf("subtitle diagnostic=%s err=%v", diagnostic, err)
	}
	// B5 step 5 revival: the allowed video really completes through the typed
	// lane while the failed subtitle declaration stays failed and untouched.
	effectClaim, err := store.ClaimDue(ctx, "partial-video", []string{workitems.KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if effectClaim.Owner.ID != video.id {
		t.Fatal("failed subtitle claimed")
	}
	effect, err := s.Handle(ctx, effectClaim)
	if err != nil || effect.Kind != worker.OutcomeCommitted || effect.OwnerResult.State != ownerStateCompleted {
		t.Fatalf("effect=%+v err=%v", effect, err)
	}
	actual, err := os.ReadFile(want)
	if err != nil || string(actual) != "source-content" {
		t.Fatalf("literal target bytes=%q err=%v", actual, err)
	}
	if got, err := os.ReadFile(p); err != nil || string(got) != "subtitle" {
		t.Fatalf("failed subtitle source changed=%q err=%v", got, err)
	}
	rows = loadOperations(t, db, f.artifactID)
	assertTypedFailedFileResult(t, db, f.artifactID, "subtitle", "template_compile_error")
	if operationForFile(t, rows, f.fileID).state != "completed" {
		t.Fatalf("partial result flattened: %+v", rows)
	}
}
