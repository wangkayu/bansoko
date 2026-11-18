package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	observabilityapplication "bansoko/internal/observability/application"
	plugintargetapplication "bansoko/internal/plugintarget/application"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// The replacement and displacement notices: a settled replace decision admits
// one replacement_applied naming the winning release and the versions it took
// over, and a file another file displaced admits one file_displaced naming the
// taker. Both are written in the same transaction as the run-log line, and a
// replay admits nothing new.

type noticeRow struct {
	ownerType, ownerID, resultKey, animeID string
	facts                                  map[string]any
}

func noticeRows(t *testing.T, db *sql.DB, trigger string) []noticeRow {
	t.Helper()
	rows, err := db.Query(`
SELECT source_owner_type, source_owner_id, result_key, managed_anime_id, payload::text
FROM notification_admissions WHERE trigger_kind = $1 ORDER BY source_owner_id`, trigger)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []noticeRow
	for rows.Next() {
		var row noticeRow
		var payload string
		if err := rows.Scan(&row.ownerType, &row.ownerID, &row.resultKey, &row.animeID, &payload); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(payload), &row.facts); err != nil {
			t.Fatalf("decode notice payload %s: %v", payload, err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func noticeService(t *testing.T, db *sql.DB) *plugintargetapplication.Service {
	t.Helper()
	service, err := plugintargetapplication.NewService(db, nil, &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// TestSettledReplacementAdmitsOneNotice: one replace decision that takes over
// an old version admits exactly one replacement_applied naming both sides, and
// settling again admits nothing.
func TestSettledReplacementAdmitsOneNotice(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")
	service, _ := lifecycleService(t, db)
	service.SetBusinessDeliveryEnqueuer(noticeService(t, db))

	views, err := service.EvaluateApplication(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"})
	if err != nil || len(views) != 1 || views[0].Verdict != "replace" {
		t.Fatalf("evaluate = %+v, %v; want one replace decision", views, err)
	}
	if got := noticeRows(t, db, "replacement_applied"); len(got) != 0 {
		t.Fatalf("a frozen decision admitted %+v; it admits nothing until it settles", got)
	}
	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	got := noticeRows(t, db, "replacement_applied")
	if len(got) != 1 {
		t.Fatalf("replacement_applied notices = %+v, want one", got)
	}
	notice := got[0]
	if notice.ownerType != "replacement_decision" || notice.ownerID != views[0].ID || notice.resultKey != "replaced" || notice.animeID != "anime-1" {
		t.Fatalf("notice identity = %+v", notice)
	}
	if notice.facts["anime.title"] != "Show" || notice.facts["anime.group"] != "GroupA" {
		t.Fatalf("notice anime facts = %+v", notice.facts)
	}
	if releaseTitle, _ := notice.facts["release.title"].(string); releaseTitle != "[GroupA] Show - 01 [1080p]" {
		t.Fatalf("release.title = %q, want the winning release", releaseTitle)
	}
	if newFile, _ := notice.facts["replaced.new.file"].(string); newFile != "ep.mkv" {
		t.Fatalf("replaced.new.file = %q, want the winner's original file name", newFile)
	}
	if notice.facts["replaced.new.title"] != "[GroupA] Show - 01 [1080p]" || notice.facts["replaced.new.group"] != "GroupA" {
		t.Fatalf("winner original source = %+v", notice.facts)
	}
	if count, _ := notice.facts["replaced.count"].(float64); count != 1 {
		t.Fatalf("replaced.count = %v, want 1", notice.facts["replaced.count"])
	}
	targets, _ := notice.facts["replaced.targets"].([]any)
	if len(targets) != 1 {
		t.Fatalf("replaced.targets = %v, want one version", notice.facts["replaced.targets"])
	}
	target, _ := targets[0].(map[string]any)
	if target["path"] != "ep.mkv" || target["group"] != "GroupA" || target["title"] != "[GroupA] Show - 01 [720p]" {
		t.Fatalf("replaced target = %+v", target)
	}
	originals, _ := notice.facts["replaced.old_versions"].([]any)
	if len(originals) != 1 {
		t.Fatalf("replaced.old_versions = %v, want one version", notice.facts["replaced.old_versions"])
	}
	original, _ := originals[0].(map[string]any)
	if original["path"] != "ep.mkv" || original["group"] != "GroupA" || original["title"] != "[GroupA] Show - 01 [720p]" {
		t.Fatalf("replaced original = %+v", original)
	}

	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if again := noticeRows(t, db, "replacement_applied"); len(again) != 1 {
		t.Fatalf("a repeated settle admitted more notices: %+v", again)
	}
}

// TestReplacementNoticeReadsTheFrozenSourceOfEachVersion: the default names
// each version by its accepted acquisition source and artifact manifest, not
// by the renamed library path and not by the current Release/RSS source row.
// The same admission keeps the legacy custom-template variables on the current
// names, including one old version whose frozen source chain is already gone.
func TestReplacementNoticeReadsTheFrozenSourceOfEachVersion(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old2", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-15T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")
	service, _ := lifecycleService(t, db)
	service.SetBusinessDeliveryEnqueuer(noticeService(t, db))
	// The renamed loser path is a real other file, so settling hands it to the
	// cleanup service and needs the work-item store the production wiring has.
	cleanup := NewCleanupService(db)
	cleanup.SetWorkItems(&workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays})
	service.SetCleanupHandoff(cleanup)
	views, err := service.EvaluateApplication(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"})
	if err != nil || len(views) != 1 || views[0].Verdict != "replace" {
		t.Fatalf("evaluate = %+v, %v; want one replace decision", views, err)
	}

	// The library names and the current Release/source rows disagree with the
	// files' frozen download sources; the notice must read the frozen facts.
	owner := disposabledb.Owner(t)
	for _, update := range []struct {
		query string
		args  []any
	}{
		{`UPDATE placements SET physical_path = $1 WHERE id = $2`, []any{"/library/Show/RENAMED-OLD.mkv", "plc-file_operation-op-release-old"}},
		{`UPDATE placements SET physical_path = $1 WHERE id = $2`, []any{"/library/Show/RENAMED-OLD2.mkv", "plc-file_operation-op-release-old2"}},
		{`UPDATE placements SET physical_path = $1 WHERE id = $2`, []any{"/library/Show/RENAMED-NEW.mkv", candidate}},
		{`UPDATE artifact_files SET relative_path = $1 WHERE id = $2`, []any{"[GroupA] Original Old.mkv", "af-release-old"}},
		{`UPDATE artifact_files SET relative_path = $1 WHERE id = $2`, []any{"[GroupA] Original New.mkv", "af-release-new"}},
		{`UPDATE releases SET raw_facts = jsonb_set(raw_facts, '{title}', to_jsonb('CURRENT-TITLE'::text)) WHERE id IN ('release-old', 'release-old2', 'release-new')`, nil},
		{`UPDATE rss_sources SET group_name = 'CurrentGroup' WHERE id IN ('src-release-old', 'src-release-old2', 'src-release-new')`, nil},
	} {
		if _, err := owner.Exec(update.query, update.args...); err != nil {
			t.Fatalf("mutate current rows: %v", err)
		}
	}
	// The second old version's frozen source chain is gone (detail retention),
	// so the default can name only the first old version while the legacy
	// variables still name both by where they currently live.
	for _, query := range []string{
		`UPDATE acquisition_files SET plan_kind=NULL, plan_target_file_ref=NULL, plan_operation_ref=NULL,
    plan_evidence=NULL, plan_reason_code=NULL, plan_diagnostic=NULL WHERE acquisition_id = 'acq-release-old2'`,
		`DELETE FROM placement_obligations WHERE file_operation_id = 'op-release-old2'`,
		`DELETE FROM file_operations WHERE id = 'op-release-old2'`,
	} {
		if _, err := owner.Exec(query); err != nil {
			t.Fatal(err)
		}
	}

	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	got := noticeRows(t, db, "replacement_applied")
	if len(got) != 1 {
		t.Fatalf("replacement_applied notices = %+v, want one", got)
	}
	notice := got[0]
	// The variables existing custom templates read keep their old sourcing:
	// the current library file name and the current Release/RSS source row.
	if notice.facts["anime.group"] != "CurrentGroup" || notice.facts["release.title"] != "CURRENT-TITLE" {
		t.Fatalf("legacy winner facts = %+v", notice.facts)
	}
	targets, _ := notice.facts["replaced.targets"].([]any)
	if len(targets) != 2 {
		t.Fatalf("replaced.targets = %v, want both versions", notice.facts["replaced.targets"])
	}
	legacyTargets := map[string]map[string]any{}
	for _, entry := range targets {
		target, _ := entry.(map[string]any)
		name, _ := target["path"].(string)
		legacyTargets[name] = target
	}
	for _, name := range []string{"RENAMED-OLD.mkv", "RENAMED-OLD2.mkv"} {
		target, ok := legacyTargets[name]
		if !ok || target["group"] != "CurrentGroup" || target["title"] != "CURRENT-TITLE" {
			t.Fatalf("legacy replaced target %s = %+v (all %v)", name, target, notice.facts["replaced.targets"])
		}
	}
	// The default presentation reads the separate frozen original source of
	// both sides, never the legacy current-name facts above. Only the first old
	// version still has a source chain, so only it has an original entry.
	if notice.facts["replaced.new.title"] != "[GroupA] Show - 01 [1080p]" || notice.facts["replaced.new.group"] != "GroupA" ||
		notice.facts["replaced.new.file"] != "[GroupA] Original New.mkv" {
		t.Fatalf("winner original source = %+v", notice.facts)
	}
	originals, _ := notice.facts["replaced.old_versions"].([]any)
	if len(originals) != 1 {
		t.Fatalf("replaced.old_versions = %v, want only the version with a source", notice.facts["replaced.old_versions"])
	}
	original, _ := originals[0].(map[string]any)
	if original["path"] != "[GroupA] Original Old.mkv" || original["group"] != "GroupA" || original["title"] != "[GroupA] Show - 01 [720p]" {
		t.Fatalf("original replaced version = %+v", original)
	}
	for key, value := range map[string]any{
		"replaced.new.title":    notice.facts["replaced.new.title"],
		"replaced.new.group":    notice.facts["replaced.new.group"],
		"replaced.new.file":     notice.facts["replaced.new.file"],
		"replaced.old_versions": originals,
	} {
		if text, _ := value.(string); text == "RENAMED-OLD.mkv" || text == "RENAMED-OLD2.mkv" || text == "RENAMED-NEW.mkv" || text == "CURRENT-TITLE" || text == "CurrentGroup" {
			t.Fatalf("original fact %s borrowed %q", key, text)
		}
	}
	for _, entry := range originals {
		version, _ := entry.(map[string]any)
		for key, value := range version {
			if text, _ := value.(string); text == "RENAMED-OLD.mkv" || text == "RENAMED-OLD2.mkv" || text == "CURRENT-TITLE" || text == "CurrentGroup" {
				t.Fatalf("original version field %s borrowed %q", key, text)
			}
		}
	}
}

// TestReplacementNoticeKeepsTheCountWhenAnOldSourceChainIsGone: detail
// retention may delete an old version's operation and artifact file while its
// placement stays. The version still counts as replaced; its frozen original
// source is absent, so the default hides that block instead of filling the
// original name from the library path, while the legacy custom-template facts
// keep the current library name exactly as before.
func TestReplacementNoticeKeepsTheCountWhenAnOldSourceChainIsGone(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")
	service, _ := lifecycleService(t, db)
	service.SetBusinessDeliveryEnqueuer(noticeService(t, db))
	views, err := service.EvaluateApplication(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"})
	if err != nil || len(views) != 1 || views[0].Verdict != "replace" {
		t.Fatalf("evaluate = %+v, %v; want one replace decision", views, err)
	}

	// Apply the detail-expiry compaction's own order: the acquisition file's
	// plan pointers go first, then the operation (which still owns the
	// placement) disappears while the placement row stays.
	owner := disposabledb.Owner(t)
	for _, query := range []string{
		`UPDATE acquisition_files SET plan_kind=NULL, plan_target_file_ref=NULL, plan_operation_ref=NULL,
    plan_evidence=NULL, plan_reason_code=NULL, plan_diagnostic=NULL WHERE acquisition_id = 'acq-release-old'`,
		`DELETE FROM placement_obligations WHERE file_operation_id = 'op-release-old'`,
		`DELETE FROM file_operations WHERE id = 'op-release-old'`,
	} {
		if _, err := owner.Exec(query); err != nil {
			t.Fatal(err)
		}
	}

	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	got := noticeRows(t, db, "replacement_applied")
	if len(got) != 1 {
		t.Fatalf("replacement_applied notices = %+v, want one", got)
	}
	notice := got[0]
	if count, _ := notice.facts["replaced.count"].(float64); count != 1 {
		t.Fatalf("replaced.count = %v, want the one replaced version", notice.facts["replaced.count"])
	}
	targets, _ := notice.facts["replaced.targets"].([]any)
	if len(targets) != 1 {
		t.Fatalf("replaced.targets = %v, want the version's current name", notice.facts["replaced.targets"])
	}
	target, _ := targets[0].(map[string]any)
	if target["path"] != "ep.mkv" {
		t.Fatalf("legacy target = %+v, want the library file name", target)
	}
	if originals, _ := notice.facts["replaced.old_versions"].([]any); len(originals) != 0 {
		t.Fatalf("replaced.old_versions = %v, want no invented original source", notice.facts["replaced.old_versions"])
	}
	if notice.facts["replaced.new.title"] != "[GroupA] Show - 01 [1080p]" || notice.facts["replaced.new.file"] != "ep.mkv" {
		t.Fatalf("the winner's own source disappeared: %+v", notice.facts)
	}
}

// TestDisplacedFileAdmitsOneNotice: a file another instance's file takes over
// admits one file_displaced on the victim's anime, naming the taker; a replay
// (and the successor's own admission) admits nothing more.
func TestDisplacedFileAdmitsOneNotice(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedAnimeSubscription(t, db, "anime-a", "sub-a", "active")
	seedAnimeSubscription(t, db, "anime-b", "sub-b", "active")
	seedDisplacedRelease(t, db, "release-old", "sub-b", "GroupOld", "[GroupOld] Show - 01 [720p]")
	seedDisplacedRelease(t, db, "release-new", "sub-a", "GroupNew", "[GroupNew] Show - 01 [1080p]")
	seedDisplacedPlacement(t, db, "plc-old", "anime-b", "/library/Shared/EP01.mkv", "release-old")

	writer, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-new", ManagedAnimeID: "anime-a", ArtifactVerified: true, TargetPath: "/library/Shared/EP01.mkv",
		ReleaseID: "release-new", SourceFingerprint: "fp-new", TargetFingerprint: "fp-new",
	}}
	service := NewPlacementService(db, reader, nil)
	service.SetActivityWriter(writer)
	service.SetBusinessDeliveryEnqueuer(noticeService(t, db))
	if _, err := service.AdmitFromFileOperation(ctx, "op-new", []string{"tv:e1"}, "facts-v1"); err != nil {
		t.Fatal(err)
	}

	got := noticeRows(t, db, "file_displaced")
	if len(got) != 1 {
		t.Fatalf("file_displaced notices = %+v, want one", got)
	}
	notice := got[0]
	if notice.ownerType != "placement" || notice.ownerID != "plc-old" || notice.animeID != "anime-b" {
		t.Fatalf("notice identity = %+v", notice)
	}
	want := map[string]any{
		"anime.title": "Show", "file.name": "EP01.mkv", "event.cause": "overwrite",
		"taker.file": "EP01.mkv", "taker.group": "GroupNew", "taker.title": "[GroupNew] Show - 01 [1080p]",
	}
	for key, value := range want {
		if notice.facts[key] != value {
			t.Fatalf("notice fact %s = %v, want %v (payload %+v)", key, notice.facts[key], value, notice.facts)
		}
	}
}
