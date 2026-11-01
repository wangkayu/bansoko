package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	observabilityapplication "bansoko/internal/observability/application"
	plugintargetapplication "bansoko/internal/plugintarget/application"
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

	if err := service.SettleForPlacement(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if again := noticeRows(t, db, "replacement_applied"); len(again) != 1 {
		t.Fatalf("a repeated settle admitted more notices: %+v", again)
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
