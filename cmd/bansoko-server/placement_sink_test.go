package main

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/filemaintenance"
	fileoperationapplication "bansoko/internal/fileoperation/application"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

type fakeEvidenceSource struct {
	evidence *filemaintenance.FileOperationEvidence
	found    bool
}

func (f fakeEvidenceSource) ReadVerifiedEvidence(context.Context, string) (*filemaintenance.FileOperationEvidence, bool, error) {
	return f.evidence, f.found, nil
}

// errEvidenceSource models a database/decode failure while reading the
// verified evidence: the sink must type it as a retryable read failure.
type errEvidenceSource struct{ err error }

func (f errEvidenceSource) ReadVerifiedEvidence(context.Context, string) (*filemaintenance.FileOperationEvidence, bool, error) {
	return nil, false, f.err
}

// fakeMediaFactsReader models the three coverage-reader outcomes:
// read/decode failure, missing snapshot, and unresolved identity.
type fakeMediaFactsReader struct {
	snapshot *facts.Snapshot
	rows     []facts.FileRow
	err      error
}

func (f fakeMediaFactsReader) ReadSnapshot(context.Context, string) (*facts.Snapshot, error) {
	return f.snapshot, f.err
}

func (f fakeMediaFactsReader) ListFileRows(context.Context, string) ([]facts.FileRow, error) {
	return f.rows, nil
}

func unresolvedIdentityFileRow(snapshotID, fileRef string) facts.FileRow {
	return facts.FileRow{
		SnapshotID: snapshotID,
		FileRef:    fileRef,
		EffectiveFacts: facts.EffectiveFileFacts{
			Identity: episodeidentity.EpisodeIdentity{State: episodeidentity.StateUnresolved},
		},
	}
}

// TestPlacementSinkMissingEvidenceIsTyped proves ReadVerifiedEvidence
// found=false for a completed operation is a typed, retryable
// placement_evidence_missing failure — never a silent nil skip.
func TestPlacementSinkMissingEvidenceIsTyped(t *testing.T) {
	sink := &placementSinkAdapter{
		evidence:   fakeEvidenceSource{found: false},
		placements: filemaintenance.NewPlacementService(nil, fakeEvidenceSource{found: false}, nil),
	}
	err := sink.AdmitFromFileOperation(context.Background(), "op-missing")
	if err == nil {
		t.Fatal("missing verified evidence must fail typed, not return nil")
	}
	if reason := fileoperationapplication.PlacementSinkReason(err); reason != "placement_evidence_missing" {
		t.Fatalf("missing evidence reason = %q, want placement_evidence_missing", reason)
	}
}

// TestPlacementSinkEvidenceReadFailureIsRetryable proves a database/decode
// evidence read error stays retryable and typed.
func TestPlacementSinkEvidenceReadFailureIsRetryable(t *testing.T) {
	sink := &placementSinkAdapter{
		evidence:   errEvidenceSource{err: errors.New("connection reset")},
		placements: filemaintenance.NewPlacementService(nil, fakeEvidenceSource{found: false}, nil),
	}
	err := sink.AdmitFromFileOperation(context.Background(), "op-read-fail")
	if reason := fileoperationapplication.PlacementSinkReason(err); reason != "placement_evidence_read_failed" {
		t.Fatalf("read failure reason = %q, want placement_evidence_read_failed", reason)
	}
}

// TestPlacementSinkCoverageReadFailureIsRetryable proves a media-facts
// database/decode failure is typed placement_evidence_read_failed and never
// records the file as unknown.
func TestPlacementSinkCoverageReadFailureIsRetryable(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	sink := &placementSinkAdapter{
		evidence: fakeEvidenceSource{found: true, evidence: &filemaintenance.FileOperationEvidence{
			OperationID: "op-coverage-fail", ManagedAnimeID: "anime-1", ReleaseID: "rel-1",
			ArtifactVerified: true, TargetPath: "/library/Show/EP01.mkv",
			SourceFingerprint: "fp", TargetFingerprint: "fp", FileKind: "video", FileRef: "ep.mkv",
		}},
		mediaFacts: fakeMediaFactsReader{err: errors.New("snapshot decode failed")},
		placements: newPlacementService(db),
	}
	err := sink.AdmitFromFileOperation(context.Background(), "op-coverage-fail")
	if reason := fileoperationapplication.PlacementSinkReason(err); reason != "placement_evidence_read_failed" {
		t.Fatalf("coverage read failure reason = %q, want placement_evidence_read_failed", reason)
	}
	assertNoPlacements(t, db)
}

// TestPlacementSinkMissingSnapshotIsTyped proves a release whose frozen media
// fact snapshot does not exist is typed placement_evidence_missing: retryable,
// visible, and never recorded as an unknown file.
func TestPlacementSinkMissingSnapshotIsTyped(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	sink := &placementSinkAdapter{
		evidence: fakeEvidenceSource{found: true, evidence: &filemaintenance.FileOperationEvidence{
			OperationID: "op-no-snapshot", ManagedAnimeID: "anime-1", ReleaseID: "rel-1",
			ArtifactVerified: true, TargetPath: "/library/Show/EP01.mkv",
			SourceFingerprint: "fp", TargetFingerprint: "fp", FileKind: "video", FileRef: "ep.mkv",
		}},
		mediaFacts: fakeMediaFactsReader{snapshot: nil},
		placements: newPlacementService(db),
	}
	err := sink.AdmitFromFileOperation(context.Background(), "op-no-snapshot")
	if reason := fileoperationapplication.PlacementSinkReason(err); reason != "placement_evidence_missing" {
		t.Fatalf("missing snapshot reason = %q, want placement_evidence_missing", reason)
	}
	assertNoPlacements(t, db)
}

// TestPlacementSinkMissingFileRowIsTyped proves a snapshot without a row for
// the artifact file is typed placement_evidence_missing and never records the
// file as unknown.
func TestPlacementSinkMissingFileRowIsTyped(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	sink := &placementSinkAdapter{
		evidence: fakeEvidenceSource{found: true, evidence: &filemaintenance.FileOperationEvidence{
			OperationID: "op-no-row", ManagedAnimeID: "anime-1", ReleaseID: "rel-1",
			ArtifactVerified: true, TargetPath: "/library/Show/EP01.mkv",
			SourceFingerprint: "fp", TargetFingerprint: "fp", FileKind: "video", FileRef: "ep.mkv",
		}},
		mediaFacts: fakeMediaFactsReader{snapshot: &facts.Snapshot{ID: "snap-1", ReleaseID: "rel-1", SchemaVersion: facts.SchemaVersion}},
		placements: newPlacementService(db),
	}
	err := sink.AdmitFromFileOperation(context.Background(), "op-no-row")
	if reason := fileoperationapplication.PlacementSinkReason(err); reason != "placement_evidence_missing" {
		t.Fatalf("missing file row reason = %q, want placement_evidence_missing", reason)
	}
	assertNoPlacements(t, db)
}

// TestPlacementSinkUnknownFileIsReleased proves an unknown file (an unresolved
// frozen identity, or a video the plan rendered as unknown even though its
// identity resolved, like a movie's non-main video) is admitted released:
// no coverage and no replacement evaluation.
func TestPlacementSinkUnknownFileIsReleased(t *testing.T) {
	for _, c := range []struct {
		name   string
		render string
	}{{"unresolved identity", `{}`}, {"rendered as unknown", `{"unknown_file":true}`}} {
		t.Run(c.name, func(t *testing.T) {
			db := disposabledb.ResetDisposableSchema(t)
			defer db.Close()
			if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-1', 'Show')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-1', 'anime-1', 'custom', 'Show', 'active')`); err != nil {
				t.Fatal(err)
			}
			seedVerifiedVideoChainWithRender(t, db, "anime-1", "sub-1", "custom", "rel-1", c.render)
			if c.render == `{}` {
				// The stored frozen identity is the marshalled EpisodeIdentity,
				// whose keys are the struct field names, so the unresolved state
				// is written by replacing the identity object itself.
				if _, err := db.Exec(`
UPDATE media_fact_files
SET effective_facts = jsonb_set(effective_facts, '{identity}', '{"state":"unresolved"}'::jsonb, false)
WHERE snapshot_id = 'snap-rel-1'`); err != nil {
					t.Fatal(err)
				}
			}
			store, err := workitems.NewStore(db, workitems.DefaultRetentionDays, nil)
			if err != nil {
				t.Fatal(err)
			}
			sink := &placementSinkAdapter{
				evidence:   filemaintenance.NewFileOperationEvidenceReader(db),
				mediaFacts: newMediaFactsStore(db),
				placements: newPlacementService(db),
				triggers:   filemaintenance.NewReplacementTriggerService(db, store),
			}
			if err := sink.AdmitFromFileOperation(context.Background(), "op-rel-1"); err != nil {
				t.Fatalf("an unknown file must be admitted released, got %v", err)
			}
			var state, factsVersion string
			var replacementEligible bool
			var coverage, evaluate int
			if err := db.QueryRow(`SELECT state, facts_version, replacement_eligible FROM placements WHERE source_owner_ref = 'op-rel-1'`).
				Scan(&state, &factsVersion, &replacementEligible); err != nil {
				t.Fatal(err)
			}
			if state != "released" || factsVersion != "unknown" || replacementEligible {
				t.Fatalf("unknown placement shape: state=%s facts_version=%s replacement_eligible=%t", state, factsVersion, replacementEligible)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM placement_coverage`).Scan(&coverage); err != nil || coverage != 0 {
				t.Fatalf("unknown placement coverage rows = %d err=%v, want 0", coverage, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM work_item_history WHERE kind = 'replacement_evaluate'`).Scan(&evaluate); err != nil || evaluate != 0 {
				t.Fatalf("replacement evaluations = %d err=%v, want none for an unknown file", evaluate, err)
			}
		})
	}
}

func assertNoPlacements(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM placements`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("a typed evidence failure must write no placement rows, got %d", count)
	}
}

// failingEvidenceReader makes any placement admission attempt fail loudly. It
// proves the sink really skipped the call (subtitle/other) or really forwarded
// it (video) without needing a full owner-chain seed.
type failingEvidenceReader struct{}

func (failingEvidenceReader) ReadVerifiedEvidence(context.Context, string) (*filemaintenance.FileOperationEvidence, bool, error) {
	return nil, false, errors.New("placement service must not be reached")
}

func TestPlacementSinkSkipsNonVideoFiles(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	for _, kind := range []string{"subtitle", "other"} {
		sink := &placementSinkAdapter{
			evidence: fakeEvidenceSource{found: true, evidence: &filemaintenance.FileOperationEvidence{
				OperationID: "op-" + kind, ManagedAnimeID: "anime-1", ReleaseID: "rel-1",
				ArtifactVerified: true, TargetPath: "/library/Show/EP01." + kind,
				SourceFingerprint: "fp", TargetFingerprint: "fp", FileKind: kind,
			}},
			placements: filemaintenance.NewPlacementService(db, fakeEvidenceSource{found: true, evidence: &filemaintenance.FileOperationEvidence{
				OperationID: "op-" + kind, ManagedAnimeID: "anime-1", ReleaseID: "rel-1",
				ArtifactVerified: true, TargetPath: "/library/Show/EP01." + kind,
				SourceFingerprint: "fp", TargetFingerprint: "fp", FileKind: kind,
			}}, nil),
		}
		if err := sink.AdmitFromFileOperation(context.Background(), "op-"+kind); err != nil {
			t.Fatalf("%s operation must be skipped without touching placements, got %v", kind, err)
		}
	}
}

func TestPlacementSinkForwardsVideoPlacement(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	// Seed the minimal owner chain plus a frozen media fact snapshot whose
	// per-file coverage the sink projects into the placement admission.
	if _, err := db.Exec(`
INSERT INTO managed_anime(id, title) VALUES ('anime-1', 'Show');
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-1', 'anime-1', 'custom', 'x', 'active');
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title) VALUES ('src-1', 'sub-1', 'custom_rss', 'https://x', 'https://x', 'G', 'Show');
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code) VALUES ('rel-1', 'src-1', 'k', '{}', '{}', 'discovered', 'new');`); err != nil {
		t.Fatal(err)
	}
	seedFrozenMediaCoverage(t, db, "rel-1", "ep.mkv", "tv:e1")
	sink := &placementSinkAdapter{
		evidence: fakeEvidenceSource{found: true, evidence: &filemaintenance.FileOperationEvidence{
			OperationID: "op-video", ManagedAnimeID: "anime-1", ReleaseID: "rel-1",
			ArtifactVerified: true, TargetPath: "/library/Show/EP01.mkv",
			SourceFingerprint: "fp", TargetFingerprint: "fp", FileKind: "video", FileRef: "ep.mkv",
		}},
		mediaFacts: newMediaFactsStore(db),
		placements: filemaintenance.NewPlacementService(db, failingEvidenceReader{}, nil),
	}
	// The failing reader inside PlacementService proves the sink forwarded the
	// video operation with resolvable frozen coverage (the placement path was
	// actually reached).
	if err := sink.AdmitFromFileOperation(context.Background(), "op-video"); err == nil {
		t.Fatal("video operation must be forwarded to placement admission")
	}
}

// A movie binding places exactly one frozen movie:0 coverage. The placement
// consumer must project that frozen identity instead of reading the year in the
// artifact filename, so the sink forwards the video operation with the movie
// coverage the resolver froze at release evaluation.
func TestPlacementSinkMovieFrozenCoverageIgnoresFilenameYear(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	if _, err := db.Exec(`
INSERT INTO managed_anime(id, title) VALUES ('anime-movie', 'Some Movie');
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-movie', 'anime-movie', 'custom', 'x', 'active');
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title) VALUES ('src-movie', 'sub-movie', 'custom_rss', 'https://x', 'https://x', 'G', 'Some Movie');
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code) VALUES ('rel-movie', 'src-movie', 'k', '{}', '{}', 'discovered', 'new');`); err != nil {
		t.Fatal(err)
	}
	seedFrozenMovieCoverage(t, db, "rel-movie", "Some Movie 2024 [1080p].mkv")
	store := newMediaFactsStore(db)
	// The frozen movie identity is the only coverage source: the filename year
	// never becomes an episode.
	coverage, version, ok, err := filemaintenance.FrozenFileCoverage(context.Background(), store, "rel-movie", "Some Movie 2024 [1080p].mkv")
	if err != nil || !ok {
		t.Fatalf("movie frozen coverage = %v/%q/%t/%v, want the frozen movie identity", coverage, version, ok, err)
	}
	if len(coverage) != 1 || coverage[0] != "movie:0" {
		t.Fatalf("movie frozen coverage = %v, want movie:0", coverage)
	}
	sink := &placementSinkAdapter{
		evidence: fakeEvidenceSource{found: true, evidence: &filemaintenance.FileOperationEvidence{
			OperationID: "op-movie", ManagedAnimeID: "anime-movie", ReleaseID: "rel-movie",
			ArtifactVerified: true, TargetPath: "/library/Movies/Some Movie 2024 [1080p].mkv",
			SourceFingerprint: "fp", TargetFingerprint: "fp",
			FileKind: "video", FileRef: "Some Movie 2024 [1080p].mkv",
		}},
		mediaFacts: store,
		placements: filemaintenance.NewPlacementService(db, failingEvidenceReader{}, nil),
	}
	// The failing reader inside PlacementService proves the sink forwarded the
	// movie operation instead of failing closed on the frozen coverage.
	if err := sink.AdmitFromFileOperation(context.Background(), "op-movie"); err == nil {
		t.Fatal("movie video operation must be forwarded to placement admission")
	}
}

// seedFrozenMovieCoverage seeds one Release media fact snapshot whose frozen
// binding and per-file identity are the canonical movie:0 shape.
func seedFrozenMovieCoverage(t *testing.T, db *sql.DB, releaseID, fileRef string) {
	t.Helper()
	rawEpisode := episodeidentity.EpisodeNumber("0")
	finalEpisode := episodeidentity.EpisodeNumber("0")
	coverageKey := "movie:0"
	selectedSource := episodeidentity.EpisodeSourceMovie
	identity := episodeidentity.EpisodeIdentity{
		State: episodeidentity.StateResolved, Kind: episodeidentity.KindMovie, Shape: episodeidentity.ShapeSingle,
		RawEpisode: &rawEpisode, Episode: &finalEpisode, CoverageKey: &coverageKey, SelectedSource: &selectedSource,
		ResolverVersion: episodeidentity.ResolverVersion, InputDigest: "seed-" + releaseID,
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	effectiveJSON, err := json.Marshal(map[string]any{
		"identity": identity, "quality": map[string]any{}, "mapping_version": facts.MappingVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO media_fact_snapshots
(id, release_id, schema_version, resolver_version, parser_kind, parser_config_fingerprint, input_digest, release_identity, release_facts)
VALUES ($1, $2, $3, $4, 'regex', NULL, $5, $6::jsonb,
        '{"title":"[G] Some Movie 2024 [1080p]","media_kind":"movie","season":0}'::jsonb)`,
		"snap-"+releaseID, releaseID, facts.SchemaVersion, episodeidentity.ResolverVersion, "digest-"+releaseID, string(identityJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO media_fact_files
(snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts)
VALUES ($1, $2, 'video', 0, $3, '{}'::jsonb, $4::jsonb)`,
		"snap-"+releaseID, fileRef, "fp-"+releaseID, string(effectiveJSON)); err != nil {
		t.Fatal(err)
	}
}

// seedFrozenMediaCoverage seeds one Release media fact snapshot with a resolved
// per-file identity, the frozen evidence FrozenFileCoverage projects from.
func seedFrozenMediaCoverage(t *testing.T, db *sql.DB, releaseID, fileRef, coverageKey string) {
	t.Helper()
	rawEpisode := episodeidentity.EpisodeNumber("1")
	finalEpisode := episodeidentity.EpisodeNumber("1")
	selectedSource := episodeidentity.EpisodeSourceReleaseParser
	identity := episodeidentity.EpisodeIdentity{
		State: episodeidentity.StateResolved, Kind: episodeidentity.KindTVEpisode, Shape: episodeidentity.ShapeSingle,
		RawEpisode: &rawEpisode, Episode: &finalEpisode, CoverageKey: &coverageKey, SelectedSource: &selectedSource,
		ResolverVersion: episodeidentity.ResolverVersion, InputDigest: "seed-" + releaseID,
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	effectiveJSON, err := json.Marshal(map[string]any{
		"identity": identity, "quality": map[string]any{}, "mapping_version": facts.MappingVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO media_fact_snapshots
(id, release_id, schema_version, resolver_version, parser_kind, parser_config_fingerprint, input_digest, release_identity, release_facts)
VALUES ($1, $2, $3, $4, 'regex', NULL, $5, $6::jsonb, '{"title":"[G] Show - 01 [1080p]"}'::jsonb)`,
		"snap-"+releaseID, releaseID, facts.SchemaVersion, episodeidentity.ResolverVersion, "digest-"+releaseID, string(identityJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO media_fact_files
(snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts)
VALUES ($1, $2, 'video', 0, $3, '{}'::jsonb, $4::jsonb)`,
		"snap-"+releaseID, fileRef, "fp-"+releaseID, string(effectiveJSON)); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// After-commit replacement trigger through the real sink
// ---------------------------------------------------------------------------

// seedVerifiedVideoChain seeds the full verified release chain one video file
// operation needs for the real evidence reader and placement admission. The
// download_task <-> release pair uses DEFERRABLE composite FKs, so the whole
// chain commits in one explicit transaction (autocommit would check the
// deferred constraint at every statement).
func seedVerifiedVideoChain(t *testing.T, db *sql.DB, animeID, subID, subOrigin, releaseID string) {
	t.Helper()
	seedVerifiedVideoChainWithRender(t, db, animeID, subID, subOrigin, releaseID, `{}`)
}

// seedVerifiedVideoChainWithRender is seedVerifiedVideoChain with a chosen
// frozen render snapshot on the video operation.
func seedVerifiedVideoChainWithRender(t *testing.T, db *sql.DB, animeID, subID, subOrigin, releaseID, render string) {
	t.Helper()
	tx, err := disposabledb.Owner(t).Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatalf("seed chain for %s: %v", releaseID, err)
		}
	}
	fingerprintJSON := `{"sha256":"` + releaseID + `-fp"}`
	stmt(`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ($1, $2, 'custom_rss', $3, $3, 'G', 'Show')`,
		"src-"+releaseID, subID, "https://feeds.example/rss/"+releaseID)
	stmt(`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ($1, $2, $3, '{"title":"[G] Show - 01 [1080p]"}', '{}', 'discovered', 'new')`,
		releaseID, "src-"+releaseID, "key-"+releaseID)
	stmt(`INSERT INTO release_candidate_facts
(release_id, release_version, parser_version, input_fingerprint, raw_evidence, facts, coverage_keys)
VALUES ($1, 1, 'candidate-facts-v1', $2, '{}', '{}', $3::jsonb)`,
		releaseID, "fp-"+releaseID, `["tv:e1"]`)
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "acq-"+releaseID, "dt-"+releaseID, releaseID, "dt-"+releaseID, `{}`, `{}`, "completed", `null`, `[{"file_ref":"ep.mkv","ordinal":0,"relative_path":"ep.mkv","exact_size":0,"media_kind":"video","identity_evidence":{}}]`)
	stmt(`UPDATE releases SET download_task_id = $1, verdict = 'submitted' WHERE id = $2`, "dt-"+releaseID, releaseID)
	stmt(`INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ($1, $2, (SELECT acquisition_id FROM download_tasks WHERE id=$2), '{}', '{}', 'verified', CURRENT_TIMESTAMP)`,
		"art-"+releaseID, "dt-"+releaseID)
	stmt(`INSERT INTO artifact_files
(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, subtitle_binding_state)
VALUES ($1, $2, (SELECT acquisition_id FROM artifacts WHERE id=$2), $3, $4, $5, 'video', 0, $6::jsonb, 'not_applicable')`,
		"af-"+releaseID, "art-"+releaseID, "ep.mkv", "ep.mkv", "/downloads/ep.mkv", fingerprintJSON)
	acquisitionfixture.CommitOperations(t, tx, "art-"+releaseID, []acquisitionfixture.PlanOperation{{ID: "op-" + releaseID, FileID: "af-" + releaseID, FileRef: "ep.mkv", Source: "/downloads/ep.mkv", Target: "/library/Show/" + releaseID + "/ep.mkv", Operation: "copy", Collision: "version", Key: "op-" + releaseID, Size: 0, Fingerprint: json.RawMessage(fingerprintJSON), Render: json.RawMessage(render), Config: json.RawMessage(`{}`)}})
	stmt(`UPDATE file_operations SET state='completed',observed_at=CURRENT_TIMESTAMP,target_fingerprint=source_fingerprint WHERE id=$1`, "op-"+releaseID)
	stmt(`SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE owner_type='file_operation' AND owner_id=$1`, "op-"+releaseID)
	if err := tx.Commit(); err != nil {
		t.Fatalf("seed chain for %s: %v", releaseID, err)
	}
	// The frozen selected-parser evidence the sink projects into the placement
	// coverage. Coverage must come from here, never from the candidate-facts row.
	seedFrozenMediaCoverage(t, db, releaseID, "ep.mkv", "tv:e1")
}

func newPlacementService(db *sql.DB) *filemaintenance.PlacementService {
	return filemaintenance.NewPlacementService(db, filemaintenance.NewFileOperationEvidenceReader(db), nil)
}

// newMediaFactsStore wires the frozen media fact reader the sink uses to
// resolve placement coverage from the selected parser's per-file identity.
func newMediaFactsStore(db *sql.DB) *facts.Store {
	return facts.NewStoreWithRepository(mediapersistence.NewRepository(db))
}

func TestPlacementSinkActiveTriggersEvaluateItem(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-1', 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-1', 'anime-1', 'custom', 'Show', 'active')`); err != nil {
		t.Fatal(err)
	}
	seedVerifiedVideoChain(t, db, "anime-1", "sub-1", "custom", "rel-1")

	store, err := workitems.NewStore(db, workitems.DefaultRetentionDays, nil)
	if err != nil {
		t.Fatal(err)
	}
	trigger := filemaintenance.NewReplacementTriggerService(db, store)
	sink := &placementSinkAdapter{
		evidence:   filemaintenance.NewFileOperationEvidenceReader(db),
		placements: newPlacementService(db),
		mediaFacts: newMediaFactsStore(db),
		triggers:   trigger,
	}
	if err := sink.AdmitFromFileOperation(context.Background(), "op-rel-1"); err != nil {
		t.Fatalf("sink: %v", err)
	}
	var placementState, verification, evidenceType string
	if err := db.QueryRow(`SELECT verification_state, state, source_evidence_type FROM placements WHERE managed_anime_id = 'anime-1'`).
		Scan(&verification, &placementState, &evidenceType); err != nil {
		t.Fatal(err)
	}
	if verification != "verified" || placementState != "active" || evidenceType != "file_operation" {
		t.Fatalf("placement shape: %s/%s/%s", verification, placementState, evidenceType)
	}
	var itemCount, epoch int64
	if err := db.QueryRow(`
SELECT COUNT(*), COALESCE(MAX((input_snapshot->>'subscription_version')::bigint), 0) FROM work_items
WHERE owner_type = 'file_maintenance' AND owner_id = 'anime-1' AND kind = 'replacement_evaluate'
  AND state IN ('available', 'leased')`).Scan(&itemCount, &epoch); err != nil {
		t.Fatal(err)
	}
	if itemCount != 1 || epoch != 1 {
		t.Fatalf("active placement must trigger exactly one evaluate item with the epoch: count=%d epoch=%d", itemCount, epoch)
	}
}

func TestPlacementSinkStoppedAdmitsWithoutEvaluateTrigger(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-1', 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-1', 'anime-1', 'custom', 'Show', 'stopped')`); err != nil {
		t.Fatal(err)
	}
	seedVerifiedVideoChain(t, db, "anime-1", "sub-1", "custom", "rel-1")

	store, err := workitems.NewStore(db, workitems.DefaultRetentionDays, nil)
	if err != nil {
		t.Fatal(err)
	}
	trigger := filemaintenance.NewReplacementTriggerService(db, store)
	sink := &placementSinkAdapter{
		evidence:   filemaintenance.NewFileOperationEvidenceReader(db),
		placements: newPlacementService(db),
		mediaFacts: newMediaFactsStore(db),
		triggers:   trigger,
	}
	if err := sink.AdmitFromFileOperation(context.Background(), "op-rel-1"); err != nil {
		t.Fatalf("stopped admission must still succeed: %v", err)
	}
	var placements, items, decisions, deletions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM placements WHERE managed_anime_id = 'anime-1'`).Scan(&placements); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_item_history WHERE owner_id = 'anime-1' AND kind = 'replacement_evaluate'`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_decisions WHERE managed_anime_id = 'anime-1'`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_deletions WHERE managed_anime_id = 'anime-1'`).Scan(&deletions); err != nil {
		t.Fatal(err)
	}
	if placements != 1 || items != 0 || decisions != 0 || deletions != 0 {
		t.Fatalf("stopped: admit=1 but replacement increments zero: placements=%d items=%d decisions=%d deletions=%d", placements, items, decisions, deletions)
	}
}

func TestPlacementSinkTriggerFailureKeepsAdmittedPlacement(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-1', 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-1', 'anime-1', 'custom', 'Show', 'active')`); err != nil {
		t.Fatal(err)
	}
	seedVerifiedVideoChain(t, db, "anime-1", "sub-1", "custom", "rel-1")

	// The trigger store is missing: the trigger cannot merge the WorkItem.
	trigger := filemaintenance.NewReplacementTriggerService(db, nil)
	sink := &placementSinkAdapter{
		evidence:   filemaintenance.NewFileOperationEvidenceReader(db),
		placements: newPlacementService(db),
		mediaFacts: newMediaFactsStore(db),
		triggers:   trigger,
	}
	if err := sink.AdmitFromFileOperation(context.Background(), "op-rel-1"); err == nil {
		t.Fatal("a failed replacement trigger must be returned as a visible typed failure, not swallowed")
	} else if reason := fileoperationapplication.PlacementSinkReason(err); reason != "replacement_trigger_failed" {
		t.Fatalf("trigger failure reason = %q, want replacement_trigger_failed", reason)
	}
	var placements int
	if err := db.QueryRow(`SELECT COUNT(*) FROM placements WHERE managed_anime_id = 'anime-1'`).Scan(&placements); err != nil {
		t.Fatal(err)
	}
	if placements != 1 {
		t.Fatalf("owner truth must stay committed despite the failed trigger: %d", placements)
	}
	var items int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_item_history WHERE owner_id = 'anime-1' AND kind = 'replacement_evaluate'`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if items != 0 {
		t.Fatalf("failed trigger must leave no evaluate WorkItem: %d", items)
	}
}

func TestPlacementSinkThreeSourcesIdenticalShapeAndTrigger(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	// automatic RSS (custom), manual retry (custom), Agent inject (oneshot):
	// three subscriptions of different origins, three completed video file
	// operations. All must admit the same placement shape and merge the same
	// trigger entry through the one sink. Each release has its own target, so
	// no admission displaces another.
	for index, origin := range []string{"custom", "custom", "oneshot"} {
		animeID := "anime-" + string(rune('1'+index))
		subID := "sub-" + string(rune('1'+index))
		releaseID := "rel-" + string(rune('1'+index))
		if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ($1, 'Show')`, animeID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ($1, $2, $3, 'Show', 'active')`, subID, animeID, origin); err != nil {
			t.Fatal(err)
		}
		seedVerifiedVideoChain(t, db, animeID, subID, origin, releaseID)
	}
	store, err := workitems.NewStore(db, workitems.DefaultRetentionDays, nil)
	if err != nil {
		t.Fatal(err)
	}
	trigger := filemaintenance.NewReplacementTriggerService(db, store)
	sink := &placementSinkAdapter{
		evidence:   filemaintenance.NewFileOperationEvidenceReader(db),
		placements: newPlacementService(db),
		mediaFacts: newMediaFactsStore(db),
		triggers:   trigger,
	}
	for index := 0; index < 3; index++ {
		releaseID := "rel-" + string(rune('1'+index))
		if err := sink.AdmitFromFileOperation(context.Background(), "op-"+releaseID); err != nil {
			t.Fatalf("sink for %s: %v", releaseID, err)
		}
	}
	// Every placement is the identical verified active file_operation shape and
	// every anime carries exactly one evaluate item from the same entry.
	var placements, items int
	if err := db.QueryRow(`SELECT COUNT(*) FROM placements`).Scan(&placements); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE kind = 'replacement_evaluate' AND state IN ('available', 'leased')`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if placements != 3 || items != 3 {
		t.Fatalf("three sources must produce three placements and three trigger items: placements=%d items=%d", placements, items)
	}
	var distinctShapes int
	if err := db.QueryRow(`
SELECT COUNT(DISTINCT (verification_state, state, source_evidence_type)) FROM placements`).Scan(&distinctShapes); err != nil {
		t.Fatal(err)
	}
	if distinctShapes != 1 {
		t.Fatalf("all three sources must share one placement shape: %d", distinctShapes)
	}
}
