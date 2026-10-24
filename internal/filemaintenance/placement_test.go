package filemaintenance

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/testutil/disposabledb"
)

type fakeFileOpReader struct {
	evidence *FileOperationEvidence
	found    bool
}

func (f *fakeFileOpReader) ReadVerifiedEvidence(_ context.Context, _ string) (*FileOperationEvidence, bool, error) {
	return f.evidence, f.found, nil
}

type fakeSnapshotReader2 struct {
	usable bool
	obs    *SnapshotObservation
	found  bool
}

func (f *fakeSnapshotReader2) GenerationUsable(_ context.Context, _ string, _ int64) (bool, error) {
	return f.usable, nil
}

func (f *fakeSnapshotReader2) ReadObservation(_ context.Context, _ string, _ int64, _ string) (*SnapshotObservation, bool, error) {
	return f.obs, f.found, nil
}

func openPlacementTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return disposabledb.ResetDisposableSchema(t)
}

func TestAdmitFromFileOperationIdempotent(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-1", ManagedAnimeID: "anime-1", ArtifactVerified: true, TargetPath: "/library/Show/EP01.mkv",
		SourceFingerprint: "fp-src", TargetFingerprint: "fp-src",
	}}
	service := NewPlacementService(db, reader, nil)
	ctx := context.Background()
	first, err := service.AdmitFromFileOperation(ctx, "op-1", []string{"tv:s1:e1"}, "facts-v1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.AdmitFromFileOperation(ctx, "op-1", []string{"tv:s1:e1"}, "facts-v1")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("replay must return the same placement id")
	}
}

func TestAdmitRejectsUnverifiedEvidence(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-2", ArtifactVerified: false, TargetPath: "/library/Show/EP02.mkv",
		SourceFingerprint: "fp", TargetFingerprint: "",
	}}
	service := NewPlacementService(db, reader, nil)
	if _, err := service.AdmitFromFileOperation(context.Background(), "op-2", []string{"tv:s1:e2"}, "facts-v1"); err == nil {
		t.Fatal("unverified file operation must fail closed")
	}
}

func TestAdmitFromSnapshotRejectsUnresolvedCoverage(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	reader := &fakeSnapshotReader2{usable: true, found: true, obs: &SnapshotObservation{
		FilePath: "/library/Show/SPECIAL.mkv", Fingerprint: "fp", Coverage: []string{"unresolved"},
	}}
	service := NewPlacementService(db, nil, reader)
	if _, err := service.AdmitFromSnapshot(context.Background(), "anime-1", 1, "obs-1", "facts-v1"); err == nil {
		t.Fatal("unresolved coverage must fail closed")
	}
}

func TestAdmitFromSnapshotRejectsPartialGeneration(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	reader := &fakeSnapshotReader2{usable: false}
	service := NewPlacementService(db, nil, reader)
	if _, err := service.AdmitFromSnapshot(context.Background(), "anime-1", 1, "obs-1", "facts-v1"); err == nil {
		t.Fatal("partial generation must fail closed")
	}
}

func TestAdmitAllowsMultiplePlacementsPerCoverage(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	service := NewPlacementService(db, nil, nil)
	// Two different sources covering the same observation key must both be
	// admitted (no unique constraint on observation_key — `all` needs this).
	first, err := service.admit(ctx, placementInput{
		SourceEvidenceType: "reconciliation", SourceOwnerRef: "obs-a",
		PhysicalPath: "/library/Show/EP01.mkv", SourceFingerprint: "fp-a", TargetFingerprint: "fp-a",
		FactsVersion: "facts-v1", Coverage: []string{"tv:s1:e1"}, ManagedAnimeID: "anime-1", SnapshotGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.admit(ctx, placementInput{
		SourceEvidenceType: "reconciliation", SourceOwnerRef: "obs-b",
		PhysicalPath: "/library/Show/EP01-2.mkv", SourceFingerprint: "fp-b", TargetFingerprint: "fp-b",
		FactsVersion: "facts-v1", Coverage: []string{"tv:s1:e1"}, ManagedAnimeID: "anime-1", SnapshotGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("distinct sources must create distinct placements")
	}
	views, err := service.ListPlacements(ctx, "anime-1", 10, 0)
	if err != nil || len(views) != 2 {
		t.Fatalf("expected 2 placements, got %d err=%v", len(views), err)
	}
}

// An unknown file is admitted and released in one transaction:
// no coverage, never replacement-eligible, one activity entry, and a replay
// changes nothing.
func TestAdmitUnknownFileIsReleasedAtAdmission(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-unknown', 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-unknown', 'anime-unknown', 'custom', 'Show', 'active')`); err != nil {
		t.Fatal(err)
	}
	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-unknown", ManagedAnimeID: "anime-unknown", ArtifactVerified: true,
		TargetPath:        "/library/Show/extras/SPECIAL.mkv",
		SourceFingerprint: "fp-src", TargetFingerprint: "fp-src", FileKind: "video", UnknownFile: true,
	}}
	service := NewPlacementService(db, reader, nil)
	observability, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service.SetActivityWriter(observability)
	first, err := service.AdmitUnknownFromFileOperation(ctx, "op-unknown")
	if err != nil {
		t.Fatalf("unknown admit: %v", err)
	}
	second, err := service.AdmitUnknownFromFileOperation(ctx, "op-unknown")
	if err != nil {
		t.Fatalf("unknown replay: %v", err)
	}
	if first != second {
		t.Fatal("unknown replay must be idempotent")
	}
	var (
		state, factsVersion, releasedBy, requestKey string
		eligible                                    bool
		version                                     int64
		coverageCount, activityCount                int
	)
	if err := db.QueryRowContext(ctx, `
SELECT state, facts_version, replacement_eligible, released_by, release_request_key, version
FROM placements WHERE id = $1`, first).Scan(&state, &factsVersion, &eligible, &releasedBy, &requestKey, &version); err != nil {
		t.Fatal(err)
	}
	if state != "released" || factsVersion != "unknown" || eligible || releasedBy != UnknownFileReleaseActor ||
		requestKey != "unknown-file:"+first || version != 2 {
		t.Fatalf("unknown placement = %s/%s eligible=%v by=%s key=%s v%d, want released at admission",
			state, factsVersion, eligible, releasedBy, requestKey, version)
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM placement_coverage WHERE placement_id = $1`, first).Scan(&coverageCount); err != nil {
		t.Fatal(err)
	}
	if coverageCount != 0 {
		t.Fatalf("unknown placement coverage rows = %d, want 0", coverageCount)
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM subscription_log_entries WHERE subject_id = $1 AND reason_code = 'unknown_file_released'`, first).Scan(&activityCount); err != nil {
		t.Fatal(err)
	}
	if activityCount != 1 {
		t.Fatalf("unknown_file_released activity entries = %d, want exactly 1", activityCount)
	}
	// Released files are not managed: they are never listed as active
	// placements, so scan, alignment and replacement never see them.
	views, err := service.ListPlacementsApplication(ctx, "anime-unknown", 10, 0)
	if err != nil || len(views) != 0 {
		t.Fatalf("active placements = %+v err=%v, want none", views, err)
	}
}

// A subtitle that registers against a released video (in the same admission
// transaction, or arriving later) is recorded released with it.
func TestCompanionOfReleasedPlacementIsRecordedReleased(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-unknown-video", ManagedAnimeID: "anime-1", ArtifactVerified: true,
		TargetPath:        "/library/Show/extras/SPECIAL.mkv",
		SourceFingerprint: "fp-src", TargetFingerprint: "fp-src", FileKind: "video", UnknownFile: true,
	}}
	service := NewPlacementService(db, reader, nil)
	placementID, err := service.AdmitUnknownFromFileOperation(ctx, "op-unknown-video")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	placement, ok, err := placementForOperationTx(ctx, tx, "op-unknown-video")
	if err != nil || !ok || !placement.released || placement.id != placementID {
		t.Fatalf("released placement lookup = %+v ok=%v err=%v", placement, ok, err)
	}
	suffix := "zh"
	done, err := registerCompanionTx(ctx, tx, placement, companionCandidate{
		sourceRef: "op-unknown-subtitle", registration: "intake", mediaKind: "subtitle",
		path: "/library/Show/extras/SPECIAL.zh.ass", sizeBytes: 8,
		fingerprint: strings.Repeat("a", 64), frozenSuffix: &suffix,
	})
	if err != nil || !done {
		t.Fatalf("register companion of a released placement: done=%v err=%v", done, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	companions, err := service.ListCompanions(ctx, placementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(companions) != 1 || companions[0].State != CompanionStateEnded {
		t.Fatalf("companions = %+v, want one ended companion", companions)
	}
	var reason string
	if err := db.QueryRowContext(ctx, `SELECT ended_reason FROM placement_companions WHERE id = $1`, companions[0].ID).Scan(&reason); err != nil || reason != "released" {
		t.Fatalf("ended_reason = %q err=%v, want released", reason, err)
	}
}

func TestAdmitWithoutCoverageFailsClosedForEligible(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-nocov", ManagedAnimeID: "anime-1", ArtifactVerified: true,
		TargetPath:        "/library/Show/EP01.mkv",
		SourceFingerprint: "fp-src", TargetFingerprint: "fp-src",
	}}
	service := NewPlacementService(db, reader, nil)
	if _, err := service.AdmitFromFileOperation(context.Background(), "op-nocov", nil, "facts-v1"); err == nil {
		t.Fatal("eligible placement without coverage must fail closed")
	}
}

func TestReplacementPoolExcludesIneligiblePlacements(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('anime-excl', 'Excl Anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-excl', 'anime-excl', 'custom', 'Excl', 'active')`); err != nil {
		t.Fatal(err)
	}
	// One eligible and one replacement-ineligible placement for the same anime.
	if _, err := db.ExecContext(ctx, `
INSERT INTO placements
(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, replacement_eligible, verification_state, state, version)
VALUES
('plc-file_operation-op-eligible', '/lib/Show/EP01.mkv', 'anime-excl', 'file_operation', 'op-eligible',
 'fp', 'fp', 'facts-v1', true, 'verified', 'active', 1),
('plc-file_operation-op-unclassified', '/lib/Show/Season 00/SPECIAL.mkv', 'anime-excl', 'file_operation', 'op-unclassified',
 'fp', 'fp', 'unclassified', false, 'verified', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ('plc-file_operation-op-eligible', 'tv:s1:e1', 0, 'facts-v1', 'op-eligible')`); err != nil {
		t.Fatal(err)
	}
	decisionService := NewReplacementDecisionService(db)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	placements, err := decisionService.loadPlacementFacts(ctx, tx, "anime-excl")
	if err != nil {
		t.Fatal(err)
	}
	if len(placements) != 1 || placements[0].PlacementID != "plc-file_operation-op-eligible" {
		t.Fatalf("pool placements=%+v, want only the eligible one", placements)
	}
}
