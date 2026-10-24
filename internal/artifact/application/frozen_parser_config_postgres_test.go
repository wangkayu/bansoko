package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bansoko/internal/download/domain"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/parser"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// frozenProfilePattern is a deliberately discriminating frozen profile: the
// bundled defaults resolve the fixture file name as `unsupported` (no episode
// token at all), so a per-file episode of 42 can only come from this frozen
// config. That makes the round-trip test below prove the frozen profile ran,
// instead of merely that some profile parsed the name.
const (
	frozenProfilePattern = `#(?P<episode>\d{3})`
	frozenProfileFile    = "Show #042.mkv"
)

// frozenProfileConfig builds the exact (config, fingerprint) pair release
// evaluation freezes onto a media fact snapshot for a non-empty profile.
func frozenProfileConfig(t *testing.T) (string, string) {
	t.Helper()
	config, err := parser.MarshalProfileConfig([]string{frozenProfilePattern})
	if err != nil {
		t.Fatalf("marshal frozen profile: %v", err)
	}
	selection, err := parser.NewSelection(parser.ParserRegex, config, 0)
	if err != nil {
		t.Fatalf("build frozen selection: %v", err)
	}
	if selection.ConfigFingerprint == parser.NilConfigFingerprint() {
		t.Fatal("fixture profile must not fingerprint as the empty profile")
	}
	return string(selection.Parser.Config), selection.ConfigFingerprint
}

// seedFrozenProfileArtifact seeds one release artifact chain plus a frozen
// profile on its media fact snapshot. The artifact work item is claimed but not
// handled, so each test asserts its own outcome shape.
func seedFrozenProfileArtifact(t *testing.T, fingerprint, config string) (*sql.DB, *Service, workitems.Claim, *workitems.Store) {
	t.Helper()
	db := openArtifactTestDB(t)
	t.Cleanup(func() { db.Close() })
	root := t.TempDir()
	videoPath := filepath.Join(root, frozenProfileFile)
	const size = 20
	if err := os.WriteFile(videoPath, []byte("frozen-profile-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalPath, err := filepath.EvalSymlinks(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	// The frozen file path is the torrent manifest path (a single-file torrent:
	// the file itself, first level), not the absolute path on disk.
	correlation := `{"external_id":"tag:frozen-profile","artifact_path":"` + canonicalPath +
		`","file_count":1,"files":[{"path":"` + frozenProfileFile + `","size_bytes":` + strconv.Itoa(size) +
		`,"media_kind":"video","progress":1}]}`
	seedArtifactOwner(t, db, "artifact-frozen-profile", "task-frozen-profile", "completed", "pending", correlation, `{}`)
	seedMovieFactSnapshot(t, db, "snapshot-frozen-profile", "release-artifact-test", "tv", 1, "7", fingerprint, config)

	claim, store := claimArtifactWorkItem(t, db, "artifact-frozen-profile",
		[]byte(`{"download_task_id":"task-frozen-profile","provider_task_id":"tag:frozen-profile","local_root":"`+root+`"}`))
	at := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	inspector := &fakeInspector{facts: domain.ArtifactFacts{
		Path: canonicalPath, SizeBytes: size, SHA256: strings.Repeat("f", 64), ObservedAt: at, MediaKind: "video",
	}}
	service, err := NewService(db, inspector, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	service.SetSettleDelay(0)
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	return db, service, claim, store
}

// A non-empty frozen profile must survive the PostgreSQL JSONB round trip: the
// fingerprint recomputed from the stored parser_config equals the stored
// fingerprint, and per-file recognition runs exactly that profile.
func TestFrozenParserConfigRoundTripsThroughPostgres(t *testing.T) {
	config, fingerprint := frozenProfileConfig(t)
	db, service, claim, store := seedFrozenProfileArtifact(t, fingerprint, config)
	ctx := context.Background()

	outcome, err := service.Handle(ctx, claim)
	if err != nil {
		t.Fatalf("frozen profile inspection must run the frozen selection: %v", err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "verified" {
		t.Fatalf("frozen profile inspection outcome=%+v", outcome)
	}
	finishArtifactOutcome(t, db, store, claim, outcome)

	// The stored config still fingerprints to the stored value: nothing about
	// the JSONB round trip may move the digest the rebuild verifies against.
	var storedConfig, storedFingerprint string
	if err := db.QueryRowContext(ctx, `
SELECT parser_config::text, parser_config_fingerprint
FROM media_fact_snapshots WHERE id = 'snapshot-frozen-profile'`).Scan(&storedConfig, &storedFingerprint); err != nil {
		t.Fatal(err)
	}
	if storedConfig == "" || storedConfig == "null" {
		t.Fatalf("frozen profile config was not persisted: %q", storedConfig)
	}
	rebuilt, err := parser.NewSelection(parser.ParserRegex, json.RawMessage(storedConfig), 0)
	if err != nil {
		t.Fatalf("rebuild the stored config: %v", err)
	}
	if rebuilt.ConfigFingerprint != storedFingerprint {
		t.Fatalf("stored config fingerprints to %q but the snapshot persisted %q", rebuilt.ConfigFingerprint, storedFingerprint)
	}

	// Per-file recognition ran the frozen profile.
	var effectiveRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT effective_facts FROM media_fact_files
WHERE snapshot_id = 'snapshot-frozen-profile' AND media_kind = 'video'`).Scan(&effectiveRaw); err != nil {
		t.Fatal(err)
	}
	var effective facts.EffectiveFileFacts
	if err := json.Unmarshal(effectiveRaw, &effective); err != nil {
		t.Fatalf("decode frozen per-file facts: %v", err)
	}
	identity := effective.Identity
	if identity.State != episodeidentity.StateResolved || identity.Kind != episodeidentity.KindTVEpisode {
		t.Fatalf("per-file identity = %+v, want a resolved TV episode from the frozen profile", identity)
	}
	if identity.Episode == nil || string(*identity.Episode) != "42" {
		t.Fatalf("per-file episode = %v, want 42 from the frozen profile pattern (the bundled defaults cannot parse %q)",
			identity.Episode, frozenProfileFile)
	}
	if identity.CoverageKey == nil || *identity.CoverageKey != "tv:s1:e42" {
		t.Fatalf("per-file coverage = %v, want tv:s1:e42", identity.CoverageKey)
	}
}

// A frozen config whose persisted fingerprint belongs to another config
// contradicts itself: per-file recognition fails closed and the whole artifact
// verification rolls back, instead of silently running the config as-is or
// degrading to the bundled defaults.
func TestFrozenParserFingerprintMismatchFailsClosedPostgres(t *testing.T) {
	config, _ := frozenProfileConfig(t)
	otherConfig, err := parser.MarshalProfileConfig([]string{`EP(?P<episode>\d+)`})
	if err != nil {
		t.Fatal(err)
	}
	other, err := parser.NewSelection(parser.ParserRegex, otherConfig, 0)
	if err != nil {
		t.Fatal(err)
	}
	db, service, claim, store := seedFrozenProfileArtifact(t, other.ConfigFingerprint, config)
	ctx := context.Background()

	_, err = service.Handle(ctx, claim)
	if err == nil {
		t.Fatal("a mismatched frozen fingerprint must fail preparation closed")
	}
	_ = store
	if !errors.Is(err, ErrFrozenParserEvidence) {
		t.Fatalf("error %v does not report frozen parser evidence", err)
	}

	// The verification transaction rolled back: no per-file facts, artifact not
	// verified, so nothing downstream can consume the contradicted evidence.
	var fileFacts int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM media_fact_files WHERE snapshot_id = 'snapshot-frozen-profile'`).Scan(&fileFacts); err != nil {
		t.Fatal(err)
	}
	if fileFacts != 0 {
		t.Fatalf("contradicted evidence must not freeze per-file facts, got %d rows", fileFacts)
	}
	var artifactState string
	if err := db.QueryRowContext(ctx, `
SELECT state FROM artifacts WHERE id = 'artifact-frozen-profile'`).Scan(&artifactState); err != nil {
		t.Fatal(err)
	}
	if artifactState == "verified" {
		t.Fatal("a contradicted frozen fingerprint must not verify the artifact")
	}
}
