package application

import (
	animedomain "bansoko/internal/anime/domain"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// File rules select videos by filename only; a subtitle is never subject to
// them and follows the video it binds to. A subtitle therefore has an effect
// exactly when its bound video is selected, and zero effects otherwise.
func TestSubtitlePrepareExcludedSetsHaveZeroSubtitleEffects(t *testing.T) {
	for _, tc := range []struct {
		filter      string
		wantEffects int
	}{
		{`\.mkv$`, 1},      // the video is selected, so its subtitle follows it
		{`\.ass$`, 0},      // the rule rejects the video; the subtitle is not selected by its own name
		{`NEVER_MATCH`, 0}, // no video is selected
	} {
		filter := tc.filter
		t.Run(filter, func(t *testing.T) {
			db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
				bindingManifestFixture(t, map[string]any{"file_ref": "subtitle", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"})(p)
				p["effective_sections"].(map[string]any)[animedomain.SectionFileRules] = map[string]any{"must_contain": filter, "not_in": ""}
			})
			defer db.Close()
			p := filepath.Join(t.TempDir(), "source.chs.ass")
			if err := os.WriteFile(p, []byte("subtitle"), 0600); err != nil {
				t.Fatal(err)
			}
			addArtifactFile(t, db, fixture, "excluded-sub", "subtitle", "source.chs.ass", p, "subtitle", 8)
			executor := &testExecutor{}
			service := newFixtureServiceWithPorts(t, db, fixture, executor, freshTargetObserver{})
			prepareFixture(t, db, service, fixture.artifactID)
			var executable int
			if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_file_id='excluded-sub' AND state='pending'`).Scan(&executable); err != nil {
				t.Fatal(err)
			}
			if executable != tc.wantEffects {
				t.Fatalf("subtitle has %d executable operations, want %d", executable, tc.wantEffects)
			}
			var work int
			if err := db.QueryRow(`SELECT count(*) FROM work_items w JOIN file_operations fo ON fo.id=w.owner_id WHERE fo.artifact_file_id='excluded-sub' AND w.state IN ('available','leased')`).Scan(&work); err != nil {
				t.Fatal(err)
			}
			if tc.wantEffects == 0 && work != 0 {
				t.Fatalf("excluded subtitle queued %d effects", work)
			}
			if executor.calls != 0 {
				t.Fatalf("prepare performed effects: %d", executor.calls)
			}
		})
	}
}

// assertRequirement reads the declared requirement of one manifest file.
func assertRequirement(t *testing.T, db *sql.DB, artifactID, fileRef, wantRequirement, wantReason string) {
	t.Helper()
	var requirement, reason string
	if err := db.QueryRow(`SELECT rf.requirement, rf.requirement_reason_code FROM acquisition_files rf JOIN artifacts a ON a.acquisition_id=rf.acquisition_id WHERE a.id=$1 AND rf.file_ref=$2`, artifactID, fileRef).Scan(&requirement, &reason); err != nil {
		t.Fatal(err)
	}
	if requirement != wantRequirement || reason != wantReason {
		t.Fatalf("requirement of %s = %s/%s, want %s/%s", fileRef, requirement, reason, wantRequirement, wantReason)
	}
}

func failedDeliveryResults(t *testing.T, db *sql.DB, artifactID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM acquisition_files r JOIN artifacts a ON a.acquisition_id=r.acquisition_id WHERE a.id=$1 AND r.outcome='failed'`, artifactID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// A subtitle follows its video: when the file rules reject the video whose
// name is the subtitle's prefix, the subtitle is excluded with it (no binding,
// no operation, no failure), while the other video and its plan are untouched.
func TestSubtitleOfRuleExcludedVideoIsExcludedNotFailed(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		p["fixture_manifest"] = []map[string]any{
			{"file_ref": "primary", "ordinal": 0, "relative_path": "source.mkv", "exact_size": 14, "media_kind": "video", "identity_evidence": map[string]any{}},
			{"file_ref": "video-ep2", "ordinal": 1, "relative_path": "Show - 02.mkv", "exact_size": 5, "media_kind": "video", "identity_evidence": map[string]any{}},
			{"file_ref": "sub-ep2", "ordinal": 2, "relative_path": "Show - 02.chs.ass", "exact_size": 8, "media_kind": "subtitle", "identity_evidence": map[string]any{}},
		}
		p["effective_sections"].(map[string]any)[animedomain.SectionFileRules] = map[string]any{"must_contain": "", "not_in": "- 02"}
	})
	defer db.Close()
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "Show - 02.mkv")
	subtitlePath := filepath.Join(dir, "Show - 02.chs.ass")
	for path, content := range map[string]string{videoPath: "video", subtitlePath: "subtitle"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	addArtifactFile(t, db, fixture, "artifact-file-video-ep2", "video-ep2", "Show - 02.mkv", videoPath, "video", 5)
	addArtifactFile(t, db, fixture, "artifact-file-sub-ep2", "sub-ep2", "Show - 02.chs.ass", subtitlePath, "subtitle", 8)

	service := newFixtureService(t, db, fixture)
	prepareFixture(t, db, service, fixture.artifactID)

	operations := loadOperations(t, db, fixture.artifactID)
	if len(operations) != 1 || operations[0].artifactFileID != fixture.fileID {
		t.Fatalf("only the selected video may be planned, got %+v", operations)
	}
	assertRequirement(t, db, fixture.artifactID, "video-ep2", "excluded", "file_rules_excluded")
	assertRequirement(t, db, fixture.artifactID, "sub-ep2", "excluded", "subtitle_video_excluded")
	if failed := failedDeliveryResults(t, db, fixture.artifactID); failed != 0 {
		t.Fatalf("a subtitle that follows an excluded video must not fail: %d failed results", failed)
	}
	var state sql.NullString
	if err := db.QueryRow(`SELECT subtitle_binding_state FROM artifact_files WHERE id = 'artifact-file-sub-ep2'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state.String == bindingStateBound {
		t.Fatalf("an excluded subtitle must not carry a bound decision")
	}
}
