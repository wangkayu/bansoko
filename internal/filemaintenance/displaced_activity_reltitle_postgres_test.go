package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	observabilityapplication "bansoko/internal/observability/application"
	acquisitionfixture "bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
)

// The file_displaced notice reads frt.* from the victim's own original download
// file parse and taker.frt.* from the displacing file's, each frozen onto its
// placement when the placement was admitted (placements.file_title_tags). The
// source file operation may already be compacted away; the placement copy is
// the read source. Neither side is parsed from the renamed library path and
// neither borrows the other's values.

// fileTitleTagsRender is the render snapshot a file operation prepared from one
// download file name: the complete tag set taken once, at preparation.
func fileTitleTagsRender(tags, valueTags []string) string {
	block := map[string]any{
		"schema":         "reltitle-attrs-v1",
		"implementation": "reltitle@test",
		"config_digest":  "lexicon:sha256:test",
		"tags":           tags,
	}
	if len(valueTags) > 0 {
		block["value_tags"] = valueTags
	}
	encoded, err := json.Marshal(map[string]any{
		"renderer_version": "fileoperation-render-v1",
		"file_title_tags":  block,
	})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// seedDisplacedOperation seeds a verified download through the real planning
// and preparation stages, then freezes render as the operation's render
// snapshot. A placement admitted from it copies the file_title_tags member.
func seedDisplacedOperation(t *testing.T, db *sql.DB, opID, releaseID, sourcePath, render string) {
	t.Helper()
	fingerprint := fmt.Sprintf(`{"sha256":"%s-fp"}`, opID)
	tx, err := disposabledb.Owner(t).Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatalf("seed operation %s: %v", opID, err)
		}
	}
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "acq-"+opID, "dt-"+opID, releaseID, "dt-"+opID, `{}`, `{}`, "completed", `null`,
		`[{"file_ref":"ep.mkv","ordinal":0,"relative_path":"ep.mkv","exact_size":0,"media_kind":"video","identity_evidence":{}}]`)
	exec(`INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ($1, $2, (SELECT acquisition_id FROM download_tasks WHERE id=$2), '{}', '{}', 'verified', CURRENT_TIMESTAMP)`,
		"art-"+opID, "dt-"+opID)
	exec(`INSERT INTO artifact_files
(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, subtitle_binding_state)
VALUES ($1, $2, (SELECT acquisition_id FROM artifacts WHERE id=$2), 'ep.mkv', 'ep.mkv', $3, 'video', 0, $4::jsonb, 'not_applicable')`,
		"af-"+opID, "art-"+opID, sourcePath, fingerprint)
	acquisitionfixture.CommitOperations(t, tx, "art-"+opID, []acquisitionfixture.PlanOperation{{
		ID: opID, FileID: "af-" + opID, FileRef: "ep.mkv", Source: sourcePath,
		Target: "/library/Shared/EP01.mkv", Operation: "copy", Collision: "version", Key: opID, Size: 0,
		Fingerprint: json.RawMessage(fingerprint), Render: json.RawMessage(`{}`), Config: json.RawMessage(`{}`),
	}})
	exec(`UPDATE file_operations SET frozen_render_snapshot = $2::jsonb WHERE id = $1`, opID, render)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// seedFileOperationPlacement records a managed file whose source evidence is the
// operation that placed it. It copies the operation's frozen file-title block
// onto the placement exactly as admission does; an operation that is gone (the
// compact case) leaves the column NULL.
func seedFileOperationPlacement(t *testing.T, db *sql.DB, id, animeID, path, releaseID, opID string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO placements
(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, verification_state, state, version, created_at, updated_at, file_title_tags)
VALUES ($1, $2, $3, 'file_operation', $4, $5, $5, $6, 'facts-v1', 'verified', 'active', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP,
        (SELECT CASE WHEN jsonb_typeof(frozen_render_snapshot->'file_title_tags') = 'object'
                     THEN frozen_render_snapshot->'file_title_tags' END
         FROM public.file_operations WHERE id = $4))`,
		id, path, animeID, opID, "fp-"+id, releaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ($1, 'tv:e1', 0, 'facts-v1', $2)`, id, opID); err != nil {
		t.Fatal(err)
	}
}

func displacedNotice(t *testing.T, db *sql.DB) noticeRow {
	t.Helper()
	got := noticeRows(t, db, "file_displaced")
	if len(got) != 1 {
		t.Fatalf("file_displaced notices = %+v, want one", got)
	}
	return got[0]
}

// TestDisplacedNoticeReadsBothOriginalFileParses: overwrite across instances.
// Each side's file group is its own; the release group differs from both, and
// the notice's file.name/out name carry no group at all. Neither side stands in
// for the other and neither reads the renamed library path.
func TestDisplacedNoticeReadsBothOriginalFileParses(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedAnimeSubscription(t, db, "anime-a", "sub-a", "active")
	seedAnimeSubscription(t, db, "anime-b", "sub-b", "active")
	seedDisplacedRelease(t, db, "release-old", "sub-b", "ReleaseGroupOld", "[ReleaseGroupOld] Show - 01 [720p]")
	seedDisplacedRelease(t, db, "release-new", "sub-a", "ReleaseGroupNew", "[ReleaseGroupNew] Show - 01 [1080p]")
	seedDisplacedOperation(t, db, "op-old", "release-old", "/downloads/[文件组旧] Show - 01 [720p][CHS].mkv",
		fileTitleTagsRender(
			[]string{"resolution:720p", "video_codec:HEVC"},
			[]string{"title:进击的巨人", "group:文件组旧"}))
	seedDisplacedOperation(t, db, "op-new", "release-new", "/downloads/[文件组新] Show - 01 [1080p][CHT].mkv",
		fileTitleTagsRender(
			[]string{"resolution:1080p", "video_codec:AVC", "content_rating:UNCENSORED"},
			[]string{"title:进击的巨人", "group:文件组新", "version:v2"}))
	seedFileOperationPlacement(t, db, "plc-old", "anime-b", "/library/Shared/EP01.mkv", "release-old", "op-old")

	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-new", ManagedAnimeID: "anime-a", ArtifactVerified: true, TargetPath: "/library/Shared/EP01.mkv",
		ReleaseID: "release-new", SourceFingerprint: "fp-new", TargetFingerprint: "fp-new",
	}}
	service := NewPlacementService(db, reader, nil)
	service.SetActivityWriter(mustActivityWriter(t, db))
	service.SetBusinessDeliveryEnqueuer(noticeService(t, db))
	if _, err := service.AdmitFromFileOperation(ctx, "op-new", []string{"tv:e1"}, "facts-v1"); err != nil {
		t.Fatal(err)
	}

	notice := displacedNotice(t, db)
	if notice.facts["event.cause"] != "overwrite" || notice.facts["file.name"] != "EP01.mkv" {
		t.Fatalf("notice identity facts = %+v", notice.facts)
	}
	for key, want := range map[string]any{
		"frt.group": "文件组旧", "frt.res": "720p", "frt.vc": "HEVC", "frt.title": "进击的巨人",
		"taker.frt.group": "文件组新", "taker.frt.res": "1080p", "taker.frt.vc": "AVC",
		"taker.frt.rev": "v2", "taker.frt.uncensored": true,
		// The release namespace is the source group, not either file's name.
		"taker.group": "ReleaseGroupNew",
	} {
		if notice.facts[key] != want {
			t.Fatalf("notice fact %s = %#v, want %#v (payload %+v)", key, notice.facts[key], want, notice.facts)
		}
	}
	// The victim is a successful parse without the marker; its yes/no answer is
	// false, and none of its values come from the taker side.
	if notice.facts["frt.uncensored"] != false {
		t.Fatalf("frt.uncensored = %#v, want false", notice.facts["frt.uncensored"])
	}
	if notice.facts["frt.lang"] != nil || notice.facts["taker.frt.lang"] != nil {
		t.Fatalf("subtitle language was not in either frozen parse: %+v", notice.facts)
	}

	// The admitted payload stays what the event froze: a replay after the
	// operation rows changed (a deleted source, a new parser) neither admits a
	// second notice nor reprojects either side — the placement copies are the
	// source, not the live operations.
	if _, err := db.Exec(`UPDATE file_operations SET frozen_render_snapshot = '{"renderer_version":"fileoperation-render-v1"}'::jsonb WHERE id IN ('op-old', 'op-new')`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AdmitFromFileOperation(ctx, "op-new", []string{"tv:e1"}, "facts-v1"); err != nil {
		t.Fatal(err)
	}
	replayed := displacedNotice(t, db)
	for key, value := range map[string]any{"frt.group": "文件组旧", "taker.frt.group": "文件组新", "taker.frt.res": "1080p"} {
		if replayed.facts[key] != value {
			t.Fatalf("replay fact %s = %#v, want the frozen %#v", key, replayed.facts[key], value)
		}
	}
}

// TestRedownloadDisplacedNoticeKeepsTheMissingSideEmpty: the victim's
// placement carries no frozen block (its operation predates this feature), the
// taker has one. frt.* stays null while taker.frt.* keeps its values; the
// library names differ and neither side is reparsed from them.
func TestRedownloadDisplacedNoticeKeepsTheMissingSideEmpty(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedAnimeSubscription(t, db, "anime-a", "sub-a", "active")
	seedDisplacedRelease(t, db, "release-old", "sub-a", "GroupOld", "[GroupOld] Show - 01 [720p]")
	seedDisplacedRelease(t, db, "release-new", "sub-a", "GroupNew", "[GroupNew] Show - 01 [1080p]")
	seedDisplacedOperation(t, db, "op-old", "release-old", "/downloads/[旧文件组] Show - 01 [720p].mkv",
		`{"renderer_version":"fileoperation-render-v1"}`)
	seedDisplacedOperation(t, db, "op-new", "release-new", "/downloads/[新文件组] Show - 01 [1080p].mkv",
		fileTitleTagsRender([]string{"resolution:1080p"}, []string{"title:进击的巨人", "group:新文件组"}))
	seedFileOperationPlacement(t, db, "plc-old", "anime-a", "/library/A/EP01 [old].mkv", "release-old", "op-old")
	if _, err := db.Exec(`UPDATE placements SET redownload_requested_at = CURRENT_TIMESTAMP, redownload_request_key = 'redownload-key-1' WHERE id = 'plc-old'`); err != nil {
		t.Fatal(err)
	}

	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-new", ManagedAnimeID: "anime-a", ArtifactVerified: true, TargetPath: "/library/A/EP01 [new].mkv",
		ReleaseID: "release-new", SourceFingerprint: "fp-new", TargetFingerprint: "fp-new",
	}}
	service := NewPlacementService(db, reader, nil)
	service.SetActivityWriter(mustActivityWriter(t, db))
	service.SetBusinessDeliveryEnqueuer(noticeService(t, db))
	if _, err := service.AdmitFromFileOperation(ctx, "op-new", []string{"tv:e1"}, "facts-v1"); err != nil {
		t.Fatal(err)
	}

	notice := displacedNotice(t, db)
	if notice.facts["event.cause"] != "redownload" || notice.facts["file.name"] != "EP01 [old].mkv" || notice.facts["taker.file"] != "EP01 [new].mkv" {
		t.Fatalf("redownload notice identity facts = %+v", notice.facts)
	}
	for _, field := range []string{"title", "group", "rev", "res", "source", "vc", "ac", "lang", "subflag", "uncensored"} {
		if value := notice.facts["frt."+field]; value != nil {
			t.Fatalf("frt.%s = %#v, want null for an old snapshot (payload %+v)", field, value, notice.facts)
		}
	}
	if notice.facts["taker.frt.group"] != "新文件组" || notice.facts["taker.frt.res"] != "1080p" {
		t.Fatalf("taker.frt = %+v, want the taker's own frozen parse", notice.facts)
	}
}

// TestDisplacedNoticeOutlivesTheCompactedVictimOperation: the victim placement
// copied its block at admission, then the operation is removed exactly as
// acquisition_compact(detail_expired) removes it (plan columns nulled, row
// deleted) while the placement and its Release survive. The displaced notice
// still reads the victim's own parse from the placement.
func TestDisplacedNoticeOutlivesTheCompactedVictimOperation(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedAnimeSubscription(t, db, "anime-a", "sub-a", "active")
	seedAnimeSubscription(t, db, "anime-b", "sub-b", "active")
	seedDisplacedRelease(t, db, "release-old", "sub-b", "GroupOld", "[GroupOld] Show - 01 [720p]")
	seedDisplacedRelease(t, db, "release-new", "sub-a", "GroupNew", "[GroupNew] Show - 01 [1080p]")
	seedDisplacedOperation(t, db, "op-old", "release-old", "/downloads/[文件组旧] Show - 01 [720p].mkv",
		fileTitleTagsRender([]string{"resolution:720p", "video_codec:HEVC"}, []string{"title:进击的巨人", "group:文件组旧"}))
	seedDisplacedOperation(t, db, "op-new", "release-new", "/downloads/[文件组新] Show - 01 [1080p].mkv",
		fileTitleTagsRender([]string{"resolution:1080p"}, []string{"title:进击的巨人", "group:文件组新"}))
	// The victim placement is admitted through the production path, which
	// copies the operation's frozen block onto the placement.
	oldService := NewPlacementService(db, &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-old", ManagedAnimeID: "anime-b", ArtifactVerified: true, TargetPath: "/library/Shared/EP01.mkv",
		ReleaseID: "release-old", SourceFingerprint: "fp-old", TargetFingerprint: "fp-old",
	}}, nil)
	oldService.SetActivityWriter(mustActivityWriter(t, db))
	if _, err := oldService.AdmitFromFileOperation(ctx, "op-old", []string{"tv:e1"}, "facts-v1"); err != nil {
		t.Fatal(err)
	}
	var copied []byte
	if err := db.QueryRow(`SELECT file_title_tags FROM placements WHERE source_owner_ref='op-old'`).Scan(&copied); err != nil {
		t.Fatal(err)
	}
	if len(copied) == 0 {
		t.Fatal("admission did not copy the operation's file_title_tags onto the placement")
	}
	// Fixture-only removal models expired detail; production admission and
	// notification still run through the restricted runtime handle.
	owner := disposabledb.Owner(t)
	if _, err := owner.Exec(`
UPDATE acquisition_files SET plan_kind=NULL, plan_target_file_ref=NULL, plan_operation_ref=NULL,
       plan_evidence=NULL, plan_reason_code=NULL, plan_diagnostic=NULL
WHERE acquisition_id=(SELECT acquisition_id FROM file_operations WHERE id='op-old')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(`DELETE FROM file_operations WHERE id='op-old'`); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE id='op-old'`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("victim operation still present=%d err=%v", remaining, err)
	}

	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-new", ManagedAnimeID: "anime-a", ArtifactVerified: true, TargetPath: "/library/Shared/EP01.mkv",
		ReleaseID: "release-new", SourceFingerprint: "fp-new", TargetFingerprint: "fp-new",
	}}
	service := NewPlacementService(db, reader, nil)
	service.SetActivityWriter(mustActivityWriter(t, db))
	service.SetBusinessDeliveryEnqueuer(noticeService(t, db))
	if _, err := service.AdmitFromFileOperation(ctx, "op-new", []string{"tv:e1"}, "facts-v1"); err != nil {
		t.Fatal(err)
	}

	notice := displacedNotice(t, db)
	if notice.facts["event.cause"] != "overwrite" {
		t.Fatalf("notice cause = %#v", notice.facts["event.cause"])
	}
	for key, want := range map[string]any{
		"frt.title": "进击的巨人", "frt.group": "文件组旧", "frt.res": "720p", "frt.vc": "HEVC",
		"taker.frt.title": "进击的巨人", "taker.frt.group": "文件组新", "taker.frt.res": "1080p",
	} {
		if notice.facts[key] != want {
			t.Fatalf("compacted-operation fact %s = %#v, want %#v (payload %+v)", key, notice.facts[key], want, notice.facts)
		}
	}
}

// TestDisplacedNoticeKeepsTheTakerEmptyWhenItHasNoBlock is the reverse of the
// missing-victim case: the victim has its frozen copy, while the displacing
// operation predates the feature and its placement carries no block. taker.frt
// stays null and frt keeps the victim's values; nothing is borrowed.
func TestDisplacedNoticeKeepsTheTakerEmptyWhenItHasNoBlock(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedAnimeSubscription(t, db, "anime-a", "sub-a", "active")
	seedAnimeSubscription(t, db, "anime-b", "sub-b", "active")
	seedDisplacedRelease(t, db, "release-old", "sub-b", "GroupOld", "[GroupOld] Show - 01 [720p]")
	seedDisplacedRelease(t, db, "release-new", "sub-a", "GroupNew", "[GroupNew] Show - 01 [1080p]")
	seedDisplacedOperation(t, db, "op-old", "release-old", "/downloads/[文件组旧] Show - 01 [720p].mkv",
		fileTitleTagsRender([]string{"resolution:720p"}, []string{"title:进击的巨人", "group:文件组旧"}))
	seedDisplacedOperation(t, db, "op-new", "release-new", "/downloads/[新文件组] Show - 01 [1080p].mkv",
		`{"renderer_version":"fileoperation-render-v1"}`)
	seedFileOperationPlacement(t, db, "plc-old", "anime-b", "/library/Shared/EP01.mkv", "release-old", "op-old")

	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-new", ManagedAnimeID: "anime-a", ArtifactVerified: true, TargetPath: "/library/Shared/EP01.mkv",
		ReleaseID: "release-new", SourceFingerprint: "fp-new", TargetFingerprint: "fp-new",
	}}
	service := NewPlacementService(db, reader, nil)
	service.SetActivityWriter(mustActivityWriter(t, db))
	service.SetBusinessDeliveryEnqueuer(noticeService(t, db))
	if _, err := service.AdmitFromFileOperation(ctx, "op-new", []string{"tv:e1"}, "facts-v1"); err != nil {
		t.Fatal(err)
	}

	notice := displacedNotice(t, db)
	if notice.facts["frt.group"] != "文件组旧" || notice.facts["frt.res"] != "720p" {
		t.Fatalf("victim frt = %+v, want its own frozen parse", notice.facts)
	}
	for _, field := range []string{"title", "group", "rev", "res", "source", "vc", "ac", "lang", "subflag", "uncensored"} {
		if value := notice.facts["taker.frt."+field]; value != nil {
			t.Fatalf("taker.frt.%s = %#v, want null without a frozen taker block (payload %+v)", field, value, notice.facts)
		}
	}
}

// TestDisplacedNoticeWithoutTheVictimOperationStaysEmpty: the victim placement
// has no block (its source operation was already gone when it was admitted, or
// it predates the feature); its frt values stay null and nothing falls back to
// the release group or the taker.
func TestDisplacedNoticeWithoutTheVictimOperationStaysEmpty(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedAnimeSubscription(t, db, "anime-a", "sub-a", "active")
	seedAnimeSubscription(t, db, "anime-b", "sub-b", "active")
	seedDisplacedRelease(t, db, "release-old", "sub-b", "GroupOld", "[GroupOld] Show - 01 [720p]")
	seedDisplacedRelease(t, db, "release-new", "sub-a", "GroupNew", "[GroupNew] Show - 01 [1080p]")
	seedDisplacedOperation(t, db, "op-new", "release-new", "/downloads/[新文件组] Show - 01 [1080p].mkv",
		fileTitleTagsRender([]string{"resolution:1080p"}, []string{"title:进击的巨人", "group:新文件组"}))
	seedFileOperationPlacement(t, db, "plc-old", "anime-b", "/library/Shared/EP01.mkv", "release-old", "op-gone")

	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-new", ManagedAnimeID: "anime-a", ArtifactVerified: true, TargetPath: "/library/Shared/EP01.mkv",
		ReleaseID: "release-new", SourceFingerprint: "fp-new", TargetFingerprint: "fp-new",
	}}
	service := NewPlacementService(db, reader, nil)
	service.SetActivityWriter(mustActivityWriter(t, db))
	service.SetBusinessDeliveryEnqueuer(noticeService(t, db))
	if _, err := service.AdmitFromFileOperation(ctx, "op-new", []string{"tv:e1"}, "facts-v1"); err != nil {
		t.Fatal(err)
	}

	notice := displacedNotice(t, db)
	for _, field := range []string{"title", "group", "rev", "res", "source", "vc", "ac", "lang", "subflag", "uncensored"} {
		if value := notice.facts["frt."+field]; value != nil {
			t.Fatalf("frt.%s = %#v, want null without the victim's operation (payload %+v)", field, value, notice.facts)
		}
	}
	if notice.facts["taker.frt.group"] != "新文件组" || notice.facts["taker.group"] != "GroupNew" {
		t.Fatalf("taker facts = %+v, want the taker's frozen parse and its release group", notice.facts)
	}
}

func mustActivityWriter(t *testing.T, db *sql.DB) *observabilityapplication.Service {
	t.Helper()
	writer, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	return writer
}
