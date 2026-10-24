package application

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// TestSubtitlePrepareAdoptedVideoHandleOSEffectFinish: the video already sits
// in the library with the exact content, so the prepare stage adopts it (no video
// operation); the subtitle's independent work then publishes its real file
// next to the adopted video through the typed worker entry and the fixed
// finish, leaving the adopted video untouched. The same-size/different-
// content negative and the strong adoption are checked first.
func TestSubtitlePrepareAdoptedVideoHandleOSEffectFinish(t *testing.T) {
	assertTypedAdoptedPipelinePlanning(t)
	db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		bindingManifestFixture(t, map[string]any{"file_ref": "subtitle", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"})(p)
		rules := naming.DefaultNamingRules()
		rules.TV.DirectoryTemplate, rules.TV.VideoFilenameTemplate = "adopted", "Main.mkv"
		rules.Movie, rules.Unknown = rules.TV, rules.TV
		p["effective_sections"].(map[string]any)[animedomain.SectionNamingRules] = rules
	})
	source := filepath.Join(t.TempDir(), "source.chs.ass")
	if err := os.WriteFile(source, []byte("subtitle"), 0600); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, f, "pipeline-adopt-sub", "subtitle", "source.chs.ass", source, "subtitle", 8)
	videoTarget := filepath.Join(canonicalFixtureRoot(t, f), "adopted", "Main.mkv")
	if err := os.MkdirAll(filepath.Dir(videoTarget), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(videoTarget, []byte("source-content"), 0600); err != nil {
		t.Fatal(err)
	}
	s := newFixtureServiceWithPorts(t, db, f, filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	prepareFixture(t, db, s, f.artifactID)
	rows := loadOperations(t, db, f.artifactID)
	if len(rows) != 1 {
		t.Fatalf("adoption must not fabricate a video operation: %+v", rows)
	}
	sub := operationForFile(t, rows, "pipeline-adopt-sub")
	if sub.boundVideoOperationID.Valid && sub.boundVideoOperationID.String != "" {
		t.Fatal("adoption fabricated a dependency operation")
	}

	// A restarted worker claims the subtitle work and runs the real effect.
	restarted := newFixtureServiceWithPorts(t, db, f, filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	w := worker.NewWorker(store, "subtitle-adopt-pipeline", time.Minute)
	if err := w.Register(workitems.KindFileExecute, restarted); err != nil {
		t.Fatal(err)
	}
	if handled, err := w.RunOnce(context.Background()); !handled || err != nil {
		t.Fatalf("subtitle run: handled=%v err=%v", handled, err)
	}
	var state, workState string
	if err := db.QueryRow(`SELECT o.state, w.state FROM file_operations o JOIN work_item_history w ON w.owner_type='file_operation' AND w.owner_id=o.id WHERE o.id=$1`, sub.id).Scan(&state, &workState); err != nil {
		t.Fatal(err)
	}
	if state != ownerStateCompleted || workState != workitems.StateSucceeded {
		t.Fatalf("subtitle operation=%s work=%s", state, workState)
	}
	if raw, err := os.ReadFile(sub.targetPath); err != nil || string(raw) != "subtitle" || filepath.Dir(sub.targetPath) != filepath.Dir(videoTarget) {
		t.Fatalf("subtitle not published next to the adopted video: %q %v %s", raw, err, sub.targetPath)
	}
	if raw, err := os.ReadFile(videoTarget); err != nil || string(raw) != "source-content" {
		t.Fatalf("adopted video changed: %q %v", raw, err)
	}
}
