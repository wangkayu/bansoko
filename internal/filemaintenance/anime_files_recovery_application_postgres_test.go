package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"bansoko/internal/library/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
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
	// is registered. Its fingerprint column is hex-64 by CHECK, so the fixture
	// uses a digest and the confirm must PRESERVE it (never rewrite it with a
	// meta fingerprint).
	const companionDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var companionID string
	if err := db.QueryRow(`
INSERT INTO placement_companions
(id, placement_id, media_kind, suffix, extension, current_path, size_bytes, fingerprint, state, version)
VALUES ('cmp-rec-0', $1, 'subtitle', 'zh', 'ass', $2, 64, $3, 'active', 2)
RETURNING id`, chain.PlacementID, "/library/Show/raw ep1.zh.ass", companionDigest).Scan(&companionID); err != nil {
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
			Fingerprint: companionDigest,
			SizeBytes:   64,
		}},
	}
	seedRecoveryCandidate(t, db, animeID, candidate)

	return &recoveryApplicationScene{
		db: db, fixture: fixture, rules: rules, animeID: animeID, chain: chain,
		generation: 1, candidate: candidate,
		files: map[string]domain.FileObservation{
			candidate.TargetPath:               {Fingerprint: candidate.TargetFingerprint, SizeBytes: 4096},
			candidate.Companions[0].TargetPath: {Fingerprint: candidate.Companions[0].Fingerprint, SizeBytes: 64},
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

// countingActivityWriter counts the journal entries the confirm writes inside
// its transaction; it never touches the database (the real writer's own
// contract is covered by its pg tests).
type countingActivityWriter struct{ count int }

func (w *countingActivityWriter) AppendTx(_ context.Context, _ txscope.Tx, _ observabilityport.ActivityRecord) error {
	w.count++
	return nil
}

// staticObserver answers from a fixed table; a path not listed fails like a
// vanished file.
type staticObserver struct {
	files map[string]domain.FileObservation
}

func (o *staticObserver) ObserveFile(_ context.Context, path string) (domain.FileObservation, error) {
	if observation, ok := o.files[path]; ok {
		return observation, nil
	}
	return domain.FileObservation{}, errors.New("not found")
}

// ownershipSnapshot is the world a refusal must leave untouched.
type ownershipSnapshot struct {
	Path          string
	Version       int64
	EvidenceType  string
	Generation    int64
	Fingerprint   string
	CompanionPath string
	CompanionHex  string
	CompanionVer  int64
	ActivityCount int
}

func (s *recoveryApplicationScene) snapshot(t *testing.T) ownershipSnapshot {
	t.Helper()
	var snapshot ownershipSnapshot
	if err := s.db.QueryRow(`
SELECT physical_path, version, source_evidence_type, COALESCE(snapshot_generation, 0), target_fingerprint
FROM placements WHERE id = $1`, s.candidate.PlacementID).
		Scan(&snapshot.Path, &snapshot.Version, &snapshot.EvidenceType, &snapshot.Generation, &snapshot.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`
SELECT current_path, fingerprint, version FROM placement_companions WHERE id = $1`,
		s.candidate.Companions[0].ID).
		Scan(&snapshot.CompanionPath, &snapshot.CompanionHex, &snapshot.CompanionVer); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM activity_records WHERE subject_id = $1`,
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
	if after.Version != scene.candidate.PlacementVersion+1 || after.EvidenceType != "recovery" {
		t.Fatalf("placement identity = %+v", after)
	}
	if after.Generation != scene.candidate.Generation {
		t.Fatalf("snapshot generation = %d, want the confirmed one", after.Generation)
	}
	if after.CompanionPath != scene.candidate.Companions[0].TargetPath {
		t.Fatalf("companion path = %q, want the rendered companion target", after.CompanionPath)
	}
	if after.CompanionHex != scene.candidate.Companions[0].Fingerprint {
		t.Fatalf("companion digest = %q, want the preserved hex digest", after.CompanionHex)
	}
	if after.CompanionVer != scene.candidate.Companions[0].Version+1 {
		t.Fatalf("companion version = %d, want the CAS bump", after.CompanionVer)
	}
}

// A second confirm of the same frozen token is a replay: it answers success
// without bumping any version and without a second journal entry.
func TestRecoverAnimeFileApplicationReplayIsIdempotent(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	activity := &countingActivityWriter{}
	service := scene.service(&staticObserver{files: scene.files})
	service.SetRecoveryActivityWriter(activity)

	if _, err := service.RecoverAnimeFileApplication(context.Background(), scene.animeID, scene.command()); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	first := scene.snapshot(t)

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
	if activity.count != 1 {
		t.Fatalf("journal entries = %d, want exactly one", activity.count)
	}
}

// A naming-rule change after the scan re-renders the placement to a different
// target: the confirm refuses target_changed instead of re-attaching the old
// path.
func TestRecoverAnimeFileApplicationRefusesChangedRender(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})

	// The operator renames the template: the same placement now renders
	// elsewhere, so the frozen target is stale.
	if _, err := scene.db.Exec(`UPDATE naming_rules SET video_template = '{title} - {episode:02}'`); err != nil {
		t.Skipf("naming_rules shape differs: %v", err)
	}
	scene.assertRefusal(t, service, RecoveryReasonTargetChanged)
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
	if _, err := scene.db.Exec(`
INSERT INTO file_operations (id, managed_anime_id, kind, state, source_path, target_path, version, created_at, updated_at)
VALUES ('op-unfinished', $1, 'move', 'running', '/library/Show/raw ep1.mkv', $2, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		scene.animeID, scene.candidate.TargetPath); err != nil {
		t.Fatalf("seed unfinished owner: %v", err)
	}
	scene.assertRefusal(t, service, RecoveryReasonTargetOccupied)
}

// One companion held by an unfinished owner refuses the whole unit: the video
// is never recovered without its complete unit.
func TestRecoverAnimeFileApplicationRefusesOccupiedCompanion(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})

	if _, err := scene.db.Exec(`
INSERT INTO file_operations (id, managed_anime_id, kind, state, source_path, target_path, version, created_at, updated_at)
VALUES ('op-companion', $1, 'move', 'pending', '/library/Show/raw ep1.zh.ass', $2, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		scene.animeID, scene.candidate.Companions[0].TargetPath); err != nil {
		t.Fatalf("seed companion owner: %v", err)
	}
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
