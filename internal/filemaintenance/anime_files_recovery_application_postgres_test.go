package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"bansoko/internal/library/domain"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/postprocessing/naming"
)

// ---------------------------------------------------------------------------
// Real-transaction tests for RecoverAnimeFileApplication.
//
// Unlike the probe/helper tests these drive the ACTUAL application confirm:
// its own transaction, the latest-generation gate, the frozen candidate read,
// the current-naming re-render, the placement/companion re-stat, the occupancy
// read over the shared SQL constant and the CAS re-attach plus journal write.
// Every refusal asserts the world is unchanged (ownership, versions, journal),
// because a refusal that half-wrote is the failure mode this surface exists to
// prevent.
// ---------------------------------------------------------------------------

// recoveryApplicationScene is one frozen candidate over a real seeded
// generation: an all-locator-missing placement with its rendered target, one
// subtitle companion, and the recovery candidate fact the scan would have
// written into reconciliation_files.facts.
type recoveryApplicationScene struct {
	db         *sql.DB
	fixture    *calibrationFixture
	rules      naming.NamingRules
	animeID    string
	chain      placedRenderChain
	generation int64
	candidate  recoveryCandidateFact
	files      map[string]domain.FileObservation
	// companionDigest is the registration-time hex digest stored in the
	// companion's CHECK-guarded fingerprint column. The confirm must leave it
	// byte-identical; the candidate's companion Fingerprint is a DIFFERENT
	// value (the observed metadata fingerprint).
	companionDigest string
}

// recoveryRules is the naming rule set the scene renders with: the confirm
// re-renders through the same renderer the read model previews with, so the
// frozen target is whatever this rule set produced.
func recoveryRules() naming.NamingRules {
	return rendererRules()
}

// newRecoveryApplicationScene seeds a completed generation whose single
// observed file has the full acquisition chain (so the renderer can produce a
// target), then writes the recovery candidate the scan would freeze and
// rewires the placement as an all-locator-missing source row.
func newRecoveryApplicationScene(t *testing.T) *recoveryApplicationScene {
	t.Helper()
	fixture := newCalibrationFixture(t)
	db := fixture.db
	const animeID = "anime-rec-0"
	rules := recoveryRules()

	observedPath := "/library/Show/raw ep1.mkv"
	seedCompletedGenerationWithRenderChains(t, db, animeID, 1, map[string]string{observedPath: "meta:size=4096"}, rules)

	var chain placedRenderChain
	chain.PlacementID = ""
	if err := db.QueryRow(`SELECT id FROM placements WHERE managed_anime_id = $1 AND state = 'active' ORDER BY id LIMIT 1`, animeID).Scan(&chain.PlacementID); err != nil {
		t.Fatalf("read seeded placement: %v", err)
	}

	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rules})
	targets, err := renderer.RenderPlacementTargets(context.Background(), animeID, []string{chain.PlacementID})
	if err != nil {
		t.Fatalf("render seeded target: %v", err)
	}
	target, ok := targets[chain.PlacementID]
	if !ok {
		t.Fatalf("seeded placement did not render: %v", targets)
	}

	// One active companion bound to the placement, the way a subtitle sidecar
	// is registered. Two DIFFERENT identities live on it and the fixture must
	// not conflate them:
	//   * `fingerprint` is the registration-time content digest, hex-64 by
	//     CHECK; the confirm PRESERVES it (it never rewrites content identity);
	//   * the recovery candidate's companion `Fingerprint` is the METADATA
	//     fingerprint the scan observed at the target (meta:size=..;dev=..;ino=..),
	//     which is what the confirm's MetadataFingerprintsMatch compares.
	// Every NOT NULL / CHECK column of the baseline is supplied.
	const companionDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const companionMetadata = "meta:size=64;dev=2;ino=12"
	var companionID string
	if err := db.QueryRow(`
INSERT INTO placement_companions
(id, placement_id, managed_anime_id, source_ref, registration, media_kind, suffix,
 original_extension, current_path, size_bytes, fingerprint, state, version)
VALUES ('cmp-rec-0', $1, $2, 'src:cmp-rec-0', 'adopted', 'subtitle', 'zh',
        'ass', $3, 64, $4, 'active', 2)
RETURNING id`,
		chain.PlacementID, animeID, "/library/Show/raw ep1.zh.ass", companionDigest).Scan(&companionID); err != nil {
		t.Fatalf("seed companion: %v", err)
	}
	companionTarget := companionRenderPath(t, renderer, animeID, chain.PlacementID, companionID)

	var placementVersion int64
	if err := db.QueryRow(`SELECT version FROM placements WHERE id = $1`, chain.PlacementID).Scan(&placementVersion); err != nil {
		t.Fatal(err)
	}

	candidate := recoveryCandidateFact{
		PlacementID:       chain.PlacementID,
		PlacementVersion:  placementVersion,
		Generation:        1,
		OldPhysicalPath:   observedPath,
		TargetPath:        filepath.Clean(target),
		TargetFingerprint: "meta:size=4096;dev=2;ino=11",
		SizeBytes:         4096,
		Companions: []recoveryCompanionFact{{
			ID:          companionID,
			Version:     2,
			TargetPath:  companionTarget,
			Fingerprint: companionMetadata,
			SizeBytes:   64,
		}},
	}
	seedRecoveryCandidate(t, db, animeID, candidate)

	return &recoveryApplicationScene{
		db: db, fixture: fixture, rules: rules, animeID: animeID, chain: chain,
		generation: 1, candidate: candidate, companionDigest: companionDigest,
		files: map[string]domain.FileObservation{
			candidate.TargetPath:               {Fingerprint: candidate.TargetFingerprint, SizeBytes: 4096},
			candidate.Companions[0].TargetPath: {Fingerprint: companionMetadata, SizeBytes: 64},
		},
	}
}

// companionRenderPath renders the companion's own target through the shared
// renderer, so the frozen path is exactly what the confirm will re-render.
func companionRenderPath(t *testing.T, renderer *NamingTargetRenderer, animeID, placementID, companionID string) string {
	t.Helper()
	targets, err := renderer.RenderPlacementTargets(context.Background(), animeID, []string{placementID})
	if err != nil {
		t.Fatalf("render companion target: %v", err)
	}
	path, ok := targets[companionID]
	if !ok {
		t.Fatalf("companion did not render: %v", targets)
	}
	return filepath.Clean(path)
}

// seedRecoveryCandidate writes the frozen candidate into the generation's
// reconciliation_files row exactly as the scan's probe does: the disposition
// stays missing and the candidates ride in facts.
func seedRecoveryCandidate(t *testing.T, db *sql.DB, animeID string, candidate recoveryCandidateFact) {
	t.Helper()
	document, err := json.Marshal(map[string]any{
		"placement_id":        candidate.PlacementID,
		"disposition":         "missing",
		"recovery_candidates": []recoveryCandidateFact{candidate},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
UPDATE reconciliation_files SET facts = facts || $3::jsonb
WHERE managed_anime_id = $1 AND generation = $2 AND facts->>'placement_id' = $4`,
		animeID, candidate.Generation, string(document), candidate.PlacementID); err != nil {
		t.Fatalf("seed recovery candidate: %v", err)
	}
}

// recoveryService wires the application Service exactly as the composition
// root does: the path-diff renderer (shared with the read model), the observer
// and the occupancy reader.
func (s *recoveryApplicationScene) service(observer RecoveryFileObserver) *Service {
	renderer := NewNamingTargetRenderer(s.db, fakeNamingRulesSource{rules: s.rules})
	service := NewService(nil, nil, nil, NewPlacementService(s.db, nil, nil), nil)
	service.SetPathDiff(NewPathDiffService(s.db, renderer, nil))
	service.SetRecoveryObserver(observer)
	return service
}

func (s *recoveryApplicationScene) command() AnimeFileRecoverCommand {
	return AnimeFileRecoverCommand{
		PlacementID:      s.candidate.PlacementID,
		Generation:       s.candidate.Generation,
		TargetPath:       s.candidate.TargetPath,
		Fingerprint:      s.candidate.TargetFingerprint,
		PlacementVersion: s.candidate.PlacementVersion,
	}
}

// staticObserver answers from a fixed table; a path not listed fails like a
// vanished file.
type staticObserver struct {
	files map[string]domain.FileObservation
}

func (o *staticObserver) ObserveMetadataFile(_ context.Context, path string) (domain.FileObservation, error) {
	if observation, ok := o.files[path]; ok {
		return observation, nil
	}
	return domain.FileObservation{}, errors.New("not found")
}

// seedUnfinishedFileOperation inserts a non-terminal file operation holding
// one path, using only the columns the baseline makes NOT NULL (file_operations
// has no managed_anime_id or kind column; its owner wiring hangs off
// acquisition_id/file_ref). The occupancy read treats pending/running/
// waiting_retry operations as unfinished owners, which is what makes the
// target (or a companion path) unavailable to the confirm.
func seedUnfinishedFileOperation(t *testing.T, db *sql.DB, id, state, sourcePath, targetPath string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO file_operations
(id, artifact_id, artifact_version, artifact_file_id, operation, source_path,
 target_path, source_fingerprint, collision_policy, frozen_render_snapshot,
 frozen_config_snapshot, state, idempotency_key, version, acquisition_id,
 file_ref, postprocessing_plan_id, file_ready_at, file_wall_until,
 file_max_attempts, file_attempt_timeout_seconds)
VALUES ($1, 'art-' || $1, 1, 'af-' || $1, 'move', $2, $3, '{"sha256":"fp"}'::jsonb,
        'block', '{}'::jsonb, '{}'::jsonb, $4, $1, 1, 'acq-' || $1, 'file-' || $1,
        'plan-' || $1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '1 day', 3, 60)`,
		id, sourcePath, targetPath, state); err != nil {
		t.Fatalf("seed unfinished file operation %s: %v", id, err)
	}
}

// ownershipSnapshot is the world a refusal must leave untouched.
type ownershipSnapshot struct {
	Path               string
	Version            int64
	EvidenceType       string
	RecoveryGeneration int64
	Fingerprint        string
	CompanionPath      string
	CompanionHex       string
	CompanionVer       int64
	ActivityCount      int
}

func (s *recoveryApplicationScene) snapshot(t *testing.T) ownershipSnapshot {
	t.Helper()
	var snapshot ownershipSnapshot
	if err := s.db.QueryRow(`
SELECT physical_path, version, source_evidence_type,
       COALESCE(recovery_generation, 0), target_fingerprint
FROM placements WHERE id = $1`, s.candidate.PlacementID).
		Scan(&snapshot.Path, &snapshot.Version, &snapshot.EvidenceType,
			&snapshot.RecoveryGeneration, &snapshot.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`
SELECT current_path, fingerprint, version FROM placement_companions WHERE id = $1`,
		s.candidate.Companions[0].ID).
		Scan(&snapshot.CompanionPath, &snapshot.CompanionHex, &snapshot.CompanionVer); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`
SELECT COUNT(*) FROM subscription_log_entries
 WHERE subject_type = 'placement' AND subject_id = $1 AND reason_code = 'file_recovered'`,
		s.candidate.PlacementID).Scan(&snapshot.ActivityCount); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// assertRefusal drives the application and asserts both the typed code and an
// unchanged world.
func (s *recoveryApplicationScene) assertRefusal(t *testing.T, service *Service, code string) {
	t.Helper()
	before := s.snapshot(t)
	_, err := service.RecoverAnimeFileApplication(context.Background(), s.animeID, s.command())
	if err == nil {
		t.Fatalf("confirm succeeded, want %s", code)
	}
	var refusal *AnimeFilesRecoverRefusal
	if !errors.As(err, &refusal) || refusal.Code != code {
		t.Fatalf("refusal = %v, want %s", err, code)
	}
	after := s.snapshot(t)
	if before != after {
		t.Fatalf("refusal %s mutated the world:\nbefore = %+v\nafter  = %+v", code, before, after)
	}
}

// A confirmed recovery re-points the placement to the rendered target, bumps
// its version, refreshes the typed fingerprint envelope, re-attaches the
// companion while PRESERVING the companion's hex digest, and answers the
// frozen target.
func TestRecoverAnimeFileApplicationCommitsUnit(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})

	result, err := service.RecoverAnimeFileApplication(context.Background(), scene.animeID, scene.command())
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if result.Replay || result.Companions != 1 || result.TargetPath != scene.candidate.TargetPath {
		t.Fatalf("result = %+v", result)
	}
	after := scene.snapshot(t)
	if filepath.Clean(after.Path) != filepath.Clean(scene.candidate.TargetPath) {
		t.Fatalf("placement path = %q, want the rendered target", after.Path)
	}
	if after.Version != scene.candidate.PlacementVersion+1 {
		t.Fatalf("placement version = %d, want the CAS bump", after.Version)
	}
	// The zombie-owner guard: recovery changes WHERE the bytes live, not where
	// the placement came from. Six lineage readers join on
	// source_evidence_type = 'file_operation'; rewriting it to 'recovery' would
	// leave an active row that rendering, replacement, subtitle binding, the
	// scan's size join and obligation retry all stop seeing.
	if after.EvidenceType != "file_operation" {
		t.Fatalf("source evidence = %q, want the untouched file_operation lineage", after.EvidenceType)
	}
	if after.RecoveryGeneration != scene.candidate.Generation {
		t.Fatalf("recovery generation = %d, want the confirmed one", after.RecoveryGeneration)
	}
	if after.CompanionPath != scene.candidate.Companions[0].TargetPath {
		t.Fatalf("companion path = %q, want the rendered companion target", after.CompanionPath)
	}
	// The companion's CHECK-guarded fingerprint column keeps the ORIGINAL hex
	// digest: the confirm updates current_path/version only and never rewrites
	// content identity. It must NOT become the candidate's metadata
	// fingerprint (which lives in a different field entirely).
	if after.CompanionHex != scene.companionDigest {
		t.Fatalf("companion digest = %q, want the untouched hex digest %q", after.CompanionHex, scene.companionDigest)
	}
	if after.CompanionHex == scene.candidate.Companions[0].Fingerprint {
		t.Fatalf("companion digest was overwritten with the metadata fingerprint %q", after.CompanionHex)
	}
	if after.CompanionVer != scene.candidate.Companions[0].Version+1 {
		t.Fatalf("companion version = %d, want the CAS bump", after.CompanionVer)
	}
}

// A second confirm of the same frozen token is a replay: it answers success
// without bumping any version and without a second journal entry.
func TestRecoverAnimeFileApplicationReplayIsIdempotent(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	// The REAL persistent writer, so "exactly one journal entry" is a SQL fact
	// rather than an in-memory call count.
	writer, err := observabilityapplication.NewService(scene.db)
	if err != nil {
		t.Fatalf("observability service: %v", err)
	}
	service := scene.service(&staticObserver{files: scene.files})
	service.SetRecoveryActivityWriter(writer)

	if _, err := service.RecoverAnimeFileApplication(context.Background(), scene.animeID, scene.command()); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	first := scene.snapshot(t)
	if first.ActivityCount != 1 {
		t.Fatalf("journal entries after the confirm = %d, want exactly one", first.ActivityCount)
	}

	replayed, err := service.RecoverAnimeFileApplication(context.Background(), scene.animeID, scene.command())
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replayed.Replay {
		t.Fatalf("second confirm did not report replay: %+v", replayed)
	}
	second := scene.snapshot(t)
	if first != second {
		t.Fatalf("replay mutated the world:\nfirst  = %+v\nsecond = %+v", first, second)
	}
	if second.ActivityCount != 1 {
		t.Fatalf("replay wrote a second journal entry (%d)", second.ActivityCount)
	}
}

// commitPointObserver registers a second companion as a SIDE EFFECT of the
// confirm's own observation, i.e. exactly between the pre-check that narrowed
// the window and the commit. The name set is read once (the pre-check); after
// that every observation call performs the registration and merely answers the
// frozen identity, so the pre-check passes and only the commit-point re-check
// can catch the newcomer.
type commitPointObserver struct {
	files      map[string]domain.FileObservation
	armed      bool
	register   func()
	registered bool
}

func (o *commitPointObserver) ObserveMetadataFile(_ context.Context, path string) (domain.FileObservation, error) {
	observation, ok := o.files[path]
	if !ok {
		return domain.FileObservation{}, errors.New("not found")
	}
	if o.armed && !o.registered {
		o.registered = true
		o.register()
	}
	return observation, nil
}

// A companion registered while the confirm is in flight splits the unit: the
// video and the frozen companions would move while the new companion stays at
// the old directory. The commit-point re-check (inside the transaction, after
// the CAS updates, before the journal) must refuse and roll the whole confirm
// back — nothing persists.
func TestRecoverAnimeFileApplicationRefusesConcurrentlyRegisteredCompanion(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	observer := &commitPointObserver{files: scene.files}
	observer.register = func() {
		if _, err := scene.db.Exec(`
INSERT INTO placement_companions
(id, placement_id, managed_anime_id, source_ref, registration, media_kind, suffix,
 original_extension, current_path, size_bytes, fingerprint, state, version)
VALUES ('cmp-late', $1, $2, 'src:cmp-late', 'adopted', 'subtitle', 'en',
        'ass', '/library/Show/raw ep1.en.ass', 48, $3, 'active', 1)`,
			scene.candidate.PlacementID, scene.animeID,
			"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"); err != nil {
			t.Errorf("register concurrent companion: %v", err)
		}
	}

	before := scene.snapshot(t)
	service := scene.service(observer)
	_, err := service.RecoverAnimeFileApplication(context.Background(), scene.animeID, scene.command())
	if err == nil {
		t.Fatal("confirm with a concurrently registered companion succeeded")
	}
	if !observer.registered {
		t.Fatal("the observer never registered the concurrent companion: the test verified nothing")
	}
	var refusal *AnimeFilesRecoverRefusal
	if !errors.As(err, &refusal) || refusal.Code != RecoveryReasonCompanionMissing {
		t.Fatalf("refusal = %v, want %s", err, RecoveryReasonCompanionMissing)
	}
	// The rollback must leave the recovered row exactly as it was: the video
	// was never re-pointed, no version bumped, no journal entry written.
	after := scene.snapshot(t)
	if after != before {
		t.Fatalf("concurrent registration mutated the recovered unit:\nbefore = %+v\nafter  = %+v", before, after)
	}
}

// The zombie-owner regression: a confirmed recovery must NOT rewrite the
// placement's source evidence type. Recovery moves bytes; it does not change
// where the placement came from. Six readers join on
// source_evidence_type = 'file_operation' (render facts, the scan's size join,
// replacement triggers, decision pooling, companion binding, obligation
// retry) and every one of them silently stops seeing an active row once that
// value changes — an owner that is state='active' yet invisible.
func TestRecoverAnimeFileApplicationKeepsFileOperationLineage(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})

	var before string
	if err := scene.db.QueryRow(`SELECT source_evidence_type FROM placements WHERE id = $1`,
		scene.candidate.PlacementID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != "file_operation" {
		t.Fatalf("seeded lineage = %q, want file_operation", before)
	}

	if _, err := service.RecoverAnimeFileApplication(context.Background(), scene.animeID, scene.command()); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	after := scene.snapshot(t)
	if after.EvidenceType != before {
		t.Fatalf("recovery rewrote the lineage: %q -> %q", before, after.EvidenceType)
	}
	// The lineage JOIN the readers use must still resolve the producing
	// operation for the recovered row.
	var joined int
	if err := scene.db.QueryRow(`
SELECT COUNT(*) FROM placements p
  JOIN file_operations fo ON p.source_evidence_type = 'file_operation'
                         AND fo.id = p.source_owner_ref
 WHERE p.id = $1 AND p.state = 'active'`, scene.candidate.PlacementID).Scan(&joined); err != nil {
		t.Fatal(err)
	}
	if joined != 1 {
		t.Fatalf("the recovered placement no longer joins its producing operation (rows=%d): zombie owner", joined)
	}
}

// After a confirmed recovery the renderer must still produce the placement's
// target: the row moved to the rendered path, so its render is unchanged and
// the row is not stranded off-rule.
func TestRecoverAnimeFileApplicationKeepsRendering(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})
	if _, err := service.RecoverAnimeFileApplication(context.Background(), scene.animeID, scene.command()); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	renderer := NewNamingTargetRenderer(scene.db, fakeNamingRulesSource{rules: scene.rules})
	targets, err := renderer.RenderPlacementTargets(context.Background(), scene.animeID, []string{scene.candidate.PlacementID})
	if err != nil {
		t.Fatalf("render after recovery: %v", err)
	}
	target, ok := targets[scene.candidate.PlacementID]
	if !ok {
		t.Fatalf("the recovered placement no longer renders: %v", targets)
	}
	if filepath.Clean(target) != filepath.Clean(scene.candidate.TargetPath) {
		t.Fatalf("render after recovery = %q, want the recovered path %q",
			target, scene.candidate.TargetPath)
	}
}

// A naming-rule change after the scan re-renders the placement to a different
// target: the confirm refuses target_changed instead of re-attaching the old
// path.
func TestRecoverAnimeFileApplicationRefusesChangedRender(t *testing.T) {
	scene := newRecoveryApplicationScene(t)

	// The operator retunes the naming rules: the same placement now renders to
	// a different target, so the frozen target is stale and re-attaching it
	// would put the file where no current rule set points. The rules are
	// changed through the renderer's own source — the same port the read model
	// and the confirm share — so the test cannot pass by guessing a table
	// shape.
	changedRules := scene.rules
	changedRules.TV.VideoFilenameTemplate = "${sanitize(anime.title)} - S${pad(tmdb.season, 2)}E${pad(ep.display, 2)}.${file.ext}"
	if changedRules == scene.rules {
		t.Fatal("the changed rule set is identical: the render check would be vacuous")
	}
	service := NewService(nil, nil, nil, NewPlacementService(scene.db, nil, nil), nil)
	service.SetPathDiff(NewPathDiffService(scene.db,
		NewNamingTargetRenderer(scene.db, fakeNamingRulesSource{rules: changedRules}), nil))
	service.SetRecoveryObserver(&staticObserver{files: scene.files})

	before := scene.snapshot(t)
	_, err := service.RecoverAnimeFileApplication(context.Background(), scene.animeID, scene.command())
	if err == nil {
		t.Fatal("confirm under changed rules succeeded")
	}
	var refusal *AnimeFilesRecoverRefusal
	if !errors.As(err, &refusal) || refusal.Code != RecoveryReasonTargetChanged {
		t.Fatalf("refusal = %v, want target_changed", err)
	}
	if after := scene.snapshot(t); after != before {
		t.Fatalf("changed render mutated the world:\nbefore = %+v\nafter  = %+v", before, after)
	}
}

// A newer decided generation makes the read the user confirmed stale: the
// confirm refuses scan_changed.
func TestRecoverAnimeFileApplicationRefusesNewerGeneration(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})

	// A newer generation lands (a fresh scan decided rows after the read).
	seedCompletedGeneration(t, scene.db, scene.animeID, scene.generation+1, map[string]string{"/library/Show/other.mkv": "meta:size=1"})
	scene.assertRefusal(t, service, RecoveryReasonScanChanged)
}

// A live scan makes the confirm refuse scan_running: the generation could be
// re-decided under the operator.
func TestRecoverAnimeFileApplicationRefusesLiveScan(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})

	if _, err := scene.db.Exec(`
UPDATE reconciliation_scans SET state = 'running' WHERE managed_anime_id = $1`, scene.animeID); err != nil {
		t.Fatal(err)
	}
	scene.assertRefusal(t, service, RecoveryReasonScanRunning)
}

// A placement whose version moved since the scan refuses placement_changed:
// the token the user holds is stale.
func TestRecoverAnimeFileApplicationRefusesStalePlacementVersion(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})

	command := scene.command()
	command.PlacementVersion = scene.candidate.PlacementVersion + 5
	before := scene.snapshot(t)
	if _, err := service.RecoverAnimeFileApplication(context.Background(), scene.animeID, command); err == nil {
		t.Fatal("stale version confirm succeeded")
	} else {
		var refusal *AnimeFilesRecoverRefusal
		if !errors.As(err, &refusal) || refusal.Code != RecoveryReasonPlacementChanged {
			t.Fatalf("refusal = %v, want placement_changed", err)
		}
	}
	if after := scene.snapshot(t); after != before {
		t.Fatalf("stale version mutated the world")
	}
}

// The video must still sit at the frozen target with the frozen identity: a
// vanished target is target_missing, a changed one target_changed.
func TestRecoverAnimeFileApplicationRefusesChangedVideoMetadata(t *testing.T) {
	t.Run("missing target", func(t *testing.T) {
		scene := newRecoveryApplicationScene(t)
		files := map[string]domain.FileObservation{}
		for path, observation := range scene.files {
			if path != scene.candidate.TargetPath {
				files[path] = observation
			}
		}
		scene.assertRefusal(t, scene.service(&staticObserver{files: files}), RecoveryReasonTargetMissing)
	})
	t.Run("changed inode", func(t *testing.T) {
		scene := newRecoveryApplicationScene(t)
		files := map[string]domain.FileObservation{}
		for path, observation := range scene.files {
			files[path] = observation
		}
		files[scene.candidate.TargetPath] = domain.FileObservation{
			Fingerprint: "meta:size=4096;dev=2;ino=99", SizeBytes: 4096,
		}
		scene.assertRefusal(t, scene.service(&staticObserver{files: files}), RecoveryReasonTargetChanged)
	})
	t.Run("device-only change is accepted", func(t *testing.T) {
		scene := newRecoveryApplicationScene(t)
		files := map[string]domain.FileObservation{}
		for path, observation := range scene.files {
			files[path] = observation
		}
		files[scene.candidate.TargetPath] = domain.FileObservation{
			Fingerprint: "meta:size=4096;dev=9;ino=11", SizeBytes: 4096,
		}
		if _, err := scene.service(&staticObserver{files: files}).RecoverAnimeFileApplication(
			context.Background(), scene.animeID, scene.command()); err != nil {
			t.Fatalf("remounted device refused: %v", err)
		}
	})
}

// An unfinished owner holding the frozen target refuses target_occupied: the
// occupancy read covers the same surface the file manager refuses new
// operations on, not just active placements.
func TestRecoverAnimeFileApplicationRefusesUnfinishedOwner(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})

	// An unfinished file operation still holds the target path.
	seedUnfinishedFileOperation(t, scene.db, "op-unfinished", "running", "/library/Show/raw ep1.mkv", scene.candidate.TargetPath)
	scene.assertRefusal(t, service, RecoveryReasonTargetOccupied)
}

// One companion held by an unfinished owner refuses the whole unit: the video
// is never recovered without its complete unit.
func TestRecoverAnimeFileApplicationRefusesOccupiedCompanion(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})

	seedUnfinishedFileOperation(t, scene.db, "op-companion", "pending",
		"/library/Show/raw ep1.zh.ass", scene.candidate.Companions[0].TargetPath)
	before := scene.snapshot(t)
	_, err := service.RecoverAnimeFileApplication(context.Background(), scene.animeID, scene.command())
	if err == nil {
		t.Fatal("confirm with an occupied companion succeeded")
	}
	var refusal *AnimeFilesRecoverRefusal
	if !errors.As(err, &refusal) || (refusal.Code != RecoveryReasonCompanionOccupied && refusal.Code != RecoveryReasonTargetOccupied) {
		t.Fatalf("refusal = %v, want a companion/unit occupancy refusal", err)
	}
	if after := scene.snapshot(t); after != before {
		t.Fatalf("occupied companion mutated the world")
	}
}

// A companion that changed identity refuses the whole unit and rolls the video
// back: no partial recovery, no version bump, no journal entry.
func TestRecoverAnimeFileApplicationRefusesChangedCompanion(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	files := map[string]domain.FileObservation{}
	for path, observation := range scene.files {
		files[path] = observation
	}
	files[scene.candidate.Companions[0].TargetPath] = domain.FileObservation{
		Fingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdee",
		SizeBytes:   64,
	}
	scene.assertRefusal(t, scene.service(&staticObserver{files: files}), RecoveryReasonCompanionOccupied)
}

// A companion that vanished refuses the unit: companion_missing, nothing
// written.
func TestRecoverAnimeFileApplicationRefusesMissingCompanion(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	files := map[string]domain.FileObservation{}
	for path, observation := range scene.files {
		if path != scene.candidate.Companions[0].TargetPath {
			files[path] = observation
		}
	}
	scene.assertRefusal(t, scene.service(&staticObserver{files: files}), RecoveryReasonCompanionMissing)
}

// The renderer is required: an unwired renderer fails closed as target_changed
// instead of re-attaching a path no rule set produced.
func TestRecoverAnimeFileApplicationRequiresRenderer(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := NewService(nil, nil, nil, NewPlacementService(scene.db, nil, nil), nil)
	service.SetRecoveryObserver(&staticObserver{files: scene.files})
	scene.assertRefusal(t, service, RecoveryReasonTargetChanged)
}
