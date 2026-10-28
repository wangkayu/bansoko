package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"bansoko/internal/parser"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/workitems"
)

// seedInstanceParserPolicyKind writes one instance parser-policy row with an
// explicit kind, so a hand-written fixture can select a non-default parser.
func seedInstanceParserPolicyKind(t *testing.T, db *sql.DB, animeID, kind string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO anime_business_configs(managed_anime_id, parser_kind, parser_source_default_version, parser_version)
VALUES ($1, $2, 1, 1)
ON CONFLICT (managed_anime_id) DO UPDATE
SET parser_kind = EXCLUDED.parser_kind,
    parser_version = anime_business_configs.parser_version + 1`,
		animeID, kind); err != nil {
		t.Fatalf("seed instance parser policy kind: %v", err)
	}
}

// The continuity regression: one non-default selected parser/profile must decide
// the Release verdict, the frozen media snapshot, the candidate-facts
// projection, and the frozen download policy, all with the same provenance, and
// nothing may re-derive an episode afterwards.
func TestSelectedParserContinuityPostgres(t *testing.T) {
	const kind = "test-continuity-parser"
	registerRecordingParser(t, kind, "42")
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	seedInstanceParserPolicyKind(t, db, "anime-rss-test", kind)
	if _, err := db.ExecContext(ctx, `UPDATE rss_sources SET episode_offset = 3 WHERE id = $1`, sourceID); err != nil {
		t.Fatal(err)
	}
	insertReleaseForEvaluation(t, db, sourceID, "release-continuity", "[Group] Test Show - 07 1080p", "https://downloads.example.test/show-07.torrent")

	// A program-owned torrent payload lets the accepted release reach the task
	// boundary and freeze its policy.
	seedNativeTorrentFile(t, db, "torrent-continuity", "release-continuity", "show-07.mkv", 1024, "show-07.torrent")

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	service.SetCandidateFactsRepository(facts.NewPGRepository(db))
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-continuity"}, workitems.KindReleaseEvaluate, currentReleaseVersion(t, db, "release-continuity"))
	finishReleaseClaim(t, store, service, claim)

	// verdict: the selected parser resolved 42, the link offset 3 applied
	// exactly once -> accepted.
	assertReleaseState(t, db, "release-continuity", "accepted_pending_download", "accepted", true)

	// media snapshot: same selection, same provenance, same typed result.
	var identityRaw, releaseFactsRaw []byte
	var snapshotKind, snapshotFingerprint string
	if err := db.QueryRowContext(ctx, `
SELECT release_identity, release_facts, parser_kind, parser_config_fingerprint
FROM media_fact_snapshots WHERE release_id = 'release-continuity'`).
		Scan(&identityRaw, &releaseFactsRaw, &snapshotKind, &snapshotFingerprint); err != nil {
		t.Fatal(err)
	}
	if snapshotKind != kind {
		t.Fatalf("media snapshot parser_kind = %q, want %q", snapshotKind, kind)
	}
	identity, err := facts.UnmarshalIdentity(identityRaw)
	if err != nil {
		t.Fatalf("decode frozen identity: %v", err)
	}
	if identity.Episode == nil || string(*identity.Episode) != "45" {
		t.Fatalf("frozen episode = %v, want 45 (selected parser 42 + link offset 3)", identity.Episode)
	}
	if identity.CoverageKey == nil || *identity.CoverageKey != "tv:e45" {
		t.Fatalf("frozen coverage key = %v, want tv:e45", identity.CoverageKey)
	}
	var frozenFacts facts.ReleaseFacts
	if err := json.Unmarshal(releaseFactsRaw, &frozenFacts); err != nil {
		t.Fatalf("decode frozen release facts: %v", err)
	}
	if frozenFacts.ParserProvenance == nil {
		t.Fatalf("frozen release facts carry no parser provenance")
	}
	provenance := frozenFacts.ParserProvenance
	if provenance.Kind != kind || provenance.ParseStatus != string(parser.StatusValue) || provenance.ParseShape != string(parser.ShapeSingle) {
		t.Fatalf("frozen provenance lost the selected parser typed result: %+v", provenance)
	}
	if provenance.Offset != 3 {
		t.Fatalf("frozen provenance offset evidence = %+v, want link offset 3", provenance)
	}

	// candidate facts: a projection of the same frozen identity.
	var coverageRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT coverage_keys FROM release_candidate_facts
WHERE release_id = 'release-continuity' ORDER BY release_version DESC LIMIT 1`).Scan(&coverageRaw); err != nil {
		t.Fatal(err)
	}
	var candidateCoverage []string
	if err := json.Unmarshal(coverageRaw, &candidateCoverage); err != nil {
		t.Fatal(err)
	}
	if len(candidateCoverage) != 1 || candidateCoverage[0] != "tv:e45" {
		t.Fatalf("candidate coverage = %v, want the frozen key tv:e45", candidateCoverage)
	}

	// download task frozen policy: the same selection, so a retry never reads
	// current instance policy.
	frozenPolicy := loadFrozenTaskPolicy(t, db, "release-continuity")
	if frozenPolicy.Parser == nil || frozenPolicy.Parser.Kind != kind {
		t.Fatalf("frozen download policy parser = %+v, want kind %q", frozenPolicy.Parser, kind)
	}
	if frozenPolicy.Parser.Offset != 3 || frozenPolicy.Parser.ParseStatus != string(parser.StatusValue) {
		t.Fatalf("frozen download policy lost the offset/typed result: %+v", frozenPolicy.Parser)
	}

	var native []byte
	if err := db.QueryRowContext(ctx, `SELECT a.input_canonical_utf8 FROM acquisitions a JOIN download_tasks t ON t.acquisition_id=a.id WHERE t.release_id='release-continuity'`).Scan(&native); err != nil {
		t.Fatal(err)
	}
	var input struct {
		Config struct {
			Parser struct {
				Selection struct {
					Value struct {
						Kind   string `json:"kind"`
						Offset int    `json:"offset"`
					} `json:"value"`
				} `json:"selection"`
				Provenance struct {
					State string `json:"state"`
					Value struct {
						PolicyVersion struct {
							Value int64 `json:"value"`
						} `json:"policy_version"`
						InputDigest struct {
							Value string `json:"value"`
						} `json:"input_digest"`
						ResultDigest struct {
							Value string `json:"value"`
						} `json:"result_digest"`
					} `json:"value"`
				} `json:"provenance"`
			} `json:"parser"`
		} `json:"config"`
	}
	if err := json.Unmarshal(native, &input); err != nil {
		t.Fatal(err)
	}
	p := input.Config.Parser
	if p.Selection.Value.Kind != kind || p.Selection.Value.Offset != 3 || p.Provenance.State != "known" || p.Provenance.Value.PolicyVersion.Value != provenance.PolicyVersion || p.Provenance.Value.InputDigest.Value != provenance.InputDigest || p.Provenance.Value.ResultDigest.Value != provenance.ResultDigest {
		t.Fatalf("native parser evidence diverged: %s", native)
	}

	// Tampering with the candidate-facts projection cannot move coverage or the
	// verdict: the lazy refresh re-projects the frozen identity.
	if _, err := db.ExecContext(ctx, `
UPDATE release_candidate_facts
SET coverage_keys = '["tv:e999"]'::jsonb, parser_version = 'stale'
WHERE release_id = 'release-continuity'`); err != nil {
		t.Fatal(err)
	}
	if err := facts.EnsureCurrentProfile(ctx, db, "release-continuity"); err != nil {
		t.Fatalf("refresh candidate facts: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT coverage_keys FROM release_candidate_facts
WHERE release_id = 'release-continuity' ORDER BY release_version DESC LIMIT 1`).Scan(&coverageRaw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(coverageRaw, &candidateCoverage); err != nil {
		t.Fatal(err)
	}
	if len(candidateCoverage) != 1 || candidateCoverage[0] != "tv:e45" {
		t.Fatalf("candidate coverage after refresh = %v, want the frozen key restored", candidateCoverage)
	}
	assertReleaseState(t, db, "release-continuity", "accepted_pending_download", "accepted", true)

	// A submitted Release is frozen: editing the instance kind cannot
	// move the already-frozen task policy.
	seedInstanceParserPolicyKind(t, db, "anime-rss-test", string(parser.ParserRegex))
	frozenPolicy = loadFrozenTaskPolicy(t, db, "release-continuity")
	if frozenPolicy.Parser == nil || frozenPolicy.Parser.Kind != kind || frozenPolicy.Parser.Offset != 3 {
		t.Fatalf("submitted policy drifted with the instance edit: %+v", frozenPolicy.Parser)
	}

	// A release with no frozen selected evidence is legacy: the lazy writer must
	// not invent candidate facts from the title.
	insertReleaseForEvaluation(t, db, sourceID, "release-legacy", "[Group] Test Show - 08 1080p", "")
	if err := facts.EnsureCurrentProfile(ctx, db, "release-legacy"); err != nil {
		t.Fatalf("legacy refresh: %v", err)
	}
	var legacyFacts int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM release_candidate_facts WHERE release_id = 'release-legacy'`).Scan(&legacyFacts); err != nil {
		t.Fatal(err)
	}
	if legacyFacts != 0 {
		t.Fatalf("legacy release has %d candidate facts rows, want none (no frozen evidence, no re-parse)", legacyFacts)
	}
}

// loadFrozenTaskPolicy decodes the frozen policy of one Release's download task.
func loadFrozenTaskPolicy(t *testing.T, db *sql.DB, releaseID string) frozenPolicySnapshot {
	t.Helper()
	var raw []byte
	if err := db.QueryRowContext(context.Background(), `
SELECT frozen_policy FROM download_tasks WHERE release_id = $1`, releaseID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var policy frozenPolicySnapshot
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatalf("decode frozen policy: %v", err)
	}
	return policy
}
