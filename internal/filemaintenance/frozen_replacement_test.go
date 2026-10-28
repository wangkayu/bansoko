package filemaintenance

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/storedsections"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/qualityprofile"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

// seedAnimeSubscription seeds one managed anime with its single Subscription.
func seedAnimeSubscription(t *testing.T, db *sql.DB, animeID, subID, subState string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ($1, 'Show')`, animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ($1, $2, 'custom', 'Show', $3)`, subID, animeID, subState); err != nil {
		t.Fatal(err)
	}
}

// seedInstanceReplacement inserts the complete per-instance retention policy;
// an empty profile is the built-in default.
func seedInstanceReplacement(t *testing.T, db *sql.DB, animeID string, mode AdmissionMode, profile string, version int64) {
	t.Helper()
	if profile == "" {
		profile = string(qualityprofile.DefaultJSON())
	}
	if _, err := db.Exec(`
INSERT INTO anime_business_configs
(managed_anime_id, admission_mode, quality_profile, replacement_source_default_version, replacement_version, updated_at)
VALUES ($1, $2, $3::jsonb, 1, $4, CURRENT_TIMESTAMP)
ON CONFLICT (managed_anime_id) DO UPDATE SET admission_mode = EXCLUDED.admission_mode, quality_profile = EXCLUDED.quality_profile, replacement_source_default_version = EXCLUDED.replacement_source_default_version, replacement_version = EXCLUDED.replacement_version, updated_at = EXCLUDED.updated_at`,
		animeID, string(mode), profile, version); err != nil {
		t.Fatal(err)
	}
}

// released fills the frozen release.* comparison values from the title-parse
// fields, as acceptance freezes them (explicit Values win).
func released(f Facts) Facts {
	values := qualityprofile.NewValues()
	values.Set(qualityprofile.KeyReleaseResolution, f.Resolution)
	values.Set(qualityprofile.KeyRtResolution, f.Resolution)
	values.Set(qualityprofile.KeyReleaseLang, f.Language)
	values.Set(qualityprofile.KeyReleaseRevision, f.Revision)
	for key, list := range f.Values {
		values.Set(key, list...)
	}
	f.Values = values.Values
	return f
}

// compareValuesJSON is the stored compare_values document of facts.
func compareValuesJSON(f Facts) string {
	values := qualityprofile.NewValues()
	for key, list := range released(f).Values {
		values.Set(key, list...)
	}
	values.Subgroup = f.Subgroup
	return string(values.JSON())
}

// subgroup is a frozen subgroup source (the stable group identity).
func subgroup(provider, groupRef string) *qualityprofile.SubgroupSource {
	return &qualityprofile.SubgroupSource{Provider: provider, GroupRef: groupRef}
}

// rankProfile is a one-item profile ranking key's values, best first.
func rankProfile(key string, best ...string) Profile {
	ranks := make([][]string, 0, len(best))
	for _, value := range best {
		ranks = append(ranks, []string{value})
	}
	return Profile{Version: qualityprofile.Version, Items: []qualityprofile.Item{{Key: key, Ranks: ranks}}}
}

// resolutionProfile ranks release.resolution, best first.
func resolutionProfile(best ...string) Profile {
	return rankProfile(qualityprofile.KeyReleaseResolution, best...)
}

// seedFilePlacement seeds the full verified release chain (release -> candidate
// facts -> download task -> artifact -> video artifact file -> completed file
// placement + coverage) so evaluate can resolve the
// candidate placement and every quality fact server-side. It returns the
// placement id ("plc-file_operation-<operation>").
func seedFilePlacement(t *testing.T, db *sql.DB, animeID, subID, releaseID string, coverage []string, facts Facts, createdAt string) string {
	t.Helper()
	group := facts.Group
	if group == "" {
		group = "G"
	}
	// Acceptance freezes a stable group identity for every source; fixtures
	// that name a title group share one identity per title group so they pool
	// together, and a fixture without a title group froze none (legacy facts).
	if facts.Subgroup == nil && facts.Group != "" {
		facts.Subgroup = subgroup(qualityprofile.CustomSubgroupProvider, "group-"+facts.Group)
	}
	opID := "op-" + releaseID
	placementID := "plc-file_operation-" + opID
	coverageRaw, err := json.Marshal(coverage)
	if err != nil {
		t.Fatal(err)
	}
	fingerprintJSON := fmt.Sprintf(`{"sha256":"%s"}`, releaseID+"-fp")
	tx, err := disposabledb.Owner(t).Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	insert := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatalf("seed chain for %s: %v", releaseID, err)
		}
	}
	insert(`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ($1, $2, 'custom_rss', $3, $3, $4, 'Show')`,
		"src-"+releaseID, subID, "https://feeds.example/rss/"+releaseID, group)
	title := fmt.Sprintf("[%s] Show - 01 [%s]", group, facts.Resolution)
	if facts.Group == "" {
		title = fmt.Sprintf("Show - 01 [%s]", facts.Resolution)
	}
	if facts.Resolution == "" {
		if facts.Group == "" {
			title = "Show - 01 [1080p]"
		} else {
			title = fmt.Sprintf("[%s] Show - 01 [1080p]", group)
		}
	}
	insert(`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ($1, $2, $3, $4::jsonb, '{}', 'discovered', 'new')`,
		releaseID, "src-"+releaseID, "key-"+releaseID, fmt.Sprintf(`{"title":%q}`, title))
	insert(`INSERT INTO release_candidate_facts
(release_id, release_version, parser_version, input_fingerprint, raw_evidence, facts, coverage_keys,
 resolution, revision, source, language, "group", size_bytes, compare_values)
VALUES ($1, 1, 'candidate-facts-v1', $2, '{}', '{}', $3::jsonb, $4, $5, $6, $7, $8, $9, $10::jsonb)`,
		releaseID, "fp-"+releaseID, string(coverageRaw), facts.Resolution, facts.Revision, facts.Source, facts.Language, facts.Group, facts.Size, compareValuesJSON(facts))
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "acq-"+releaseID, "dt-"+releaseID, releaseID, "dt-"+releaseID, `{}`, `{}`, "completed", `null`, `[{"file_ref":"ep.mkv","ordinal":0,"relative_path":"ep.mkv","exact_size":0,"media_kind":"video","identity_evidence":{}}]`)
	insert(`UPDATE releases SET download_task_id = $1, verdict = 'submitted' WHERE id = $2`, "dt-"+releaseID, releaseID)
	insert(`INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ($1, $2, (SELECT acquisition_id FROM download_tasks WHERE id=$2), '{}', '{}', 'verified', CURRENT_TIMESTAMP)`,
		"art-"+releaseID, "dt-"+releaseID)
	insert(`INSERT INTO artifact_files
(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, subtitle_binding_state)
VALUES ($1, $2, (SELECT acquisition_id FROM artifacts WHERE id=$2), $3, $4, $5, 'video', 0, $6::jsonb, 'not_applicable')`,
		"af-"+releaseID, "art-"+releaseID, "ep.mkv", "ep.mkv", "/downloads/ep.mkv", fingerprintJSON)
	acquisitionfixture.CommitOperations(t, tx, "art-"+releaseID, []acquisitionfixture.PlanOperation{{ID: opID, FileID: "af-" + releaseID, FileRef: "ep.mkv", Source: "/downloads/ep.mkv", Target: "/library/Show/ep.mkv", Operation: "copy", Collision: "version", Key: "op-" + releaseID, Size: 0, Fingerprint: json.RawMessage(fingerprintJSON), Render: json.RawMessage(`{}`), Config: json.RawMessage(`{}`)}})
	insert(`UPDATE file_operations SET state='completed',observed_at=CURRENT_TIMESTAMP,target_fingerprint=source_fingerprint WHERE id=$1`, opID)
	insert(`SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE owner_type='file_operation' AND owner_id=$1`, opID)
	insert(`INSERT INTO placements
(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, verification_state, state, version, created_at, updated_at)
VALUES ($1, $2, $3, 'file_operation', $4, $5, $5, $6, 'candidate-facts-v1', 'verified', 'active', 1, $7::timestamptz, $7::timestamptz)`,
		placementID, "/library/Show/ep.mkv", animeID, opID, releaseID+"-fp", releaseID, createdAt)
	for ordinal, key := range coverage {
		insert(`INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ($1, $2, $3, 'candidate-facts-v1', $4)`,
			placementID, key, ordinal, opID)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return placementID
}

// poolRef builds the frozen placement evidence for a decision pool.
func poolRef(placementID string, version int64, fingerprint string) PlacementRef {
	return PlacementRef{PlacementID: placementID, Version: version, Fingerprint: fingerprint}
}

// seedFrozenDecision inserts one frozen decision (pools with winner/losers).
func seedFrozenDecision(t *testing.T, db *sql.DB, id, animeID, candidatePlacementID string, subVersion, policyVersion int64, mode AdmissionMode, winner *PlacementRef, losers []PlacementRef, state string) {
	t.Helper()
	pool := PoolEvidence{
		PoolKey: "tv:e1", Verdict: "replace", Reason: "candidate strictly better",
		Winner: winner, Losers: losers,
	}
	poolsRaw, err := json.Marshal([]PoolEvidence{pool})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := decisionInputDigest(animeID, "release-new", candidatePlacementID, subVersion, policyVersion, mode, qualityprofile.Version, "candidate-facts-v1", "fp", "tv:e1", nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("repl:%s:%s:%s", animeID, "release-new", digest[:32])
	if _, err := db.Exec(`
INSERT INTO replacement_decisions
(id, idempotency_key, managed_anime_id, candidate_release_id, candidate_placement_id,
 policy_version, profile_version, subscription_version, input_digest,
 candidate_facts_ref, snapshot_generation, coverage_key, coverage_keys, admission_mode, pools, verdict, state, version, created_at, updated_at)
VALUES ($1, $2, $3, 'release-new', $4, $5, 'profile-v1', $6, $7, 'release-new:facts', NULL,
 'tv:e1', '["tv:e1"]', $8, $9::jsonb, 'replace', $10, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT (idempotency_key) DO NOTHING`,
		id, key, animeID, candidatePlacementID, policyVersion, subVersion, digest, string(mode), string(poolsRaw), state); err != nil {
		t.Fatal(err)
	}
}

// decisionCounts snapshots the destructive-effect counters of one anime.
func decisionCounts(t *testing.T, db *sql.DB, animeID string) (placements int, deletions int, superseded int) {
	t.Helper()
	if err := db.QueryRow(`SELECT COUNT(*) FROM placements WHERE managed_anime_id = $1`, animeID).Scan(&placements); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_deletions WHERE managed_anime_id = $1`, animeID).Scan(&deletions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM placements WHERE managed_anime_id = $1 AND state = 'superseded'`, animeID).Scan(&superseded); err != nil {
		t.Fatal(err)
	}
	return
}

func placementState(t *testing.T, db *sql.DB, placementID string) string {
	t.Helper()
	var state string
	if err := db.QueryRow(`SELECT state FROM placements WHERE id = $1`, placementID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// ---------------------------------------------------------------------------
// Pure pool logic: all / per_group / single_copy + typed blocked reasons
// ---------------------------------------------------------------------------

func TestEvaluatePoolsAllModeNeverProducesLosers(t *testing.T) {
	candidate := released(Facts{Resolution: "480p"})
	candidateRef := poolRef("plc-candidate", 1, "fp-c")
	current := []PlacementFacts{{PlacementID: "plc-old", Version: 1, Fingerprint: "fp-o", Coverage: []string{"tv:e1"}, Facts: released(Facts{Resolution: "1080p"}), FactsOK: true}}
	profile := resolutionProfile("2160p", "1080p", "720p", "480p")
	pools := EvaluatePools(ModeAll, []string{"tv:e1"}, candidate, candidateRef, current, profile, nil)
	if len(pools) != 1 || pools[0].Verdict != "queue" || pools[0].Winner != nil || len(pools[0].Losers) != 0 {
		t.Fatalf("all-mode must queue without winners/losers: %+v", pools)
	}
	if poolsVerdict(pools) != "queue" {
		t.Fatalf("all-mode decision verdict must be queue: %s", poolsVerdict(pools))
	}
}

func TestEvaluatePoolsPerGroupPoolsByCoveragePlusGroup(t *testing.T) {
	profile := resolutionProfile("2160p", "1080p", "720p", "480p")
	candidate := released(Facts{Resolution: "1080p", Group: "GroupA", Subgroup: subgroup("mikan", "583")})
	candidateRef := poolRef("plc-candidate", 1, "fp-c")
	current := []PlacementFacts{
		// Same source group, a different bracketed title group: same pool.
		{PlacementID: "plc-a", Version: 1, Fingerprint: "fp-a", Coverage: []string{"tv:e1"}, Facts: released(Facts{Resolution: "720p", Group: "GroupA-Renamed", Subgroup: subgroup("mikan", "583")}), FactsOK: true},
		// Same coverage, the same bracketed title but another source group:
		// another pool, never compared here.
		{PlacementID: "plc-b", Version: 1, Fingerprint: "fp-b", Coverage: []string{"tv:e1"}, Facts: released(Facts{Resolution: "480p", Group: "GroupA", Subgroup: subgroup("mikan", "612")}), FactsOK: true},
		{PlacementID: "plc-c", Version: 1, Fingerprint: "fp-c2", Coverage: []string{"tv:e1"}, Facts: released(Facts{Resolution: "2160p", Group: "GroupB", Subgroup: subgroup("anibt", "583")}), FactsOK: true},
	}
	pools := EvaluatePools(ModePerGroup, []string{"tv:e1"}, candidate, candidateRef, current, profile, nil)
	if len(pools) != 1 || pools[0].Verdict != "replace" {
		t.Fatalf("per_group must replace inside the group pool: %+v", pools)
	}
	if pools[0].PoolKey != "tv:e1|mikan:583" {
		t.Fatalf("per_group pool key must be coverage+provider:group_ref: %s", pools[0].PoolKey)
	}
	if len(pools[0].Losers) != 1 || pools[0].Losers[0].PlacementID != "plc-a" {
		t.Fatalf("only the same-source-group placement may be a loser: %+v", pools[0].Losers)
	}
	if pools[0].Winner == nil || pools[0].Winner.PlacementID != "plc-candidate" {
		t.Fatalf("winner must be the candidate: %+v", pools[0].Winner)
	}
}

func TestEvaluatePoolsSingleCopyCrossesGroups(t *testing.T) {
	profile := resolutionProfile("2160p", "1080p", "720p", "480p")
	candidate := released(Facts{Resolution: "2160p", Group: "GroupC"})
	candidateRef := poolRef("plc-candidate", 1, "fp-c")
	current := []PlacementFacts{
		{PlacementID: "plc-a", Version: 1, Fingerprint: "fp-a", Coverage: []string{"tv:e1"}, Facts: released(Facts{Resolution: "720p", Group: "GroupA"}), FactsOK: true},
		{PlacementID: "plc-b", Version: 1, Fingerprint: "fp-b", Coverage: []string{"tv:e1"}, Facts: released(Facts{Resolution: "1080p", Group: "GroupB"}), FactsOK: true},
	}
	pools := EvaluatePools(ModeSingleCopy, []string{"tv:e1"}, candidate, candidateRef, current, profile, nil)
	if len(pools) != 1 || pools[0].Verdict != "replace" {
		t.Fatalf("single_copy must replace across groups: %+v", pools)
	}
	if pools[0].PoolKey != "tv:e1" {
		t.Fatalf("single_copy pool key must be the coverage key: %s", pools[0].PoolKey)
	}
	if len(pools[0].Losers) != 2 {
		t.Fatalf("both groups must be losers: %+v", pools[0].Losers)
	}
}

func TestEvaluatePoolsPerGroupMissingGroupBlocks(t *testing.T) {
	profile := Profile{Version: qualityprofile.Version}
	candidate := released(Facts{Resolution: "1080p"}) // no stable group identity
	pools := EvaluatePools(ModePerGroup, []string{"tv:e1"}, candidate, poolRef("plc-candidate", 1, "fp-c"), nil, profile, nil)
	if pools[0].Verdict != "blocked" || pools[0].Reason != "group_missing" {
		t.Fatalf("per_group without group must block with group_missing: %+v", pools)
	}
	if poolsVerdict(pools) != "blocked" {
		t.Fatalf("whole decision must be blocked: %s", poolsVerdict(pools))
	}
}

func TestEvaluatePoolsCoverageUnresolvedBlocks(t *testing.T) {
	profile := Profile{Version: qualityprofile.Version}
	pools := EvaluatePools(ModeSingleCopy, []string{"unresolved"}, Facts{}, poolRef("plc-candidate", 1, "fp-c"), nil, profile, nil)
	if pools[0].Verdict != "blocked" || pools[0].Reason != "coverage_unresolved" {
		t.Fatalf("unresolved coverage must block: %+v", pools)
	}
	pools = EvaluatePools(ModeSingleCopy, nil, Facts{}, poolRef("plc-candidate", 1, "fp-c"), nil, profile, nil)
	if pools[0].Verdict != "blocked" || pools[0].Reason != "coverage_unresolved" {
		t.Fatalf("missing coverage must block: %+v", pools)
	}
}

// A scan-managed file no release has claimed (FactsOK false) has no group and
// no values: any candidate beats it, in single_copy and in every per_group
// pool of its keys.
func TestEvaluatePoolsUnclaimedScanFileLosesToAnyCandidate(t *testing.T) {
	profile := resolutionProfile("1080p", "720p")
	candidate := released(Facts{Resolution: "1080p"})
	candidateRef := poolRef("plc-candidate", 1, "fp-c")
	current := []PlacementFacts{
		// FactsOK false: no release chain for this placement.
		{PlacementID: "plc-unknown", Version: 1, Fingerprint: "fp-u", Coverage: []string{"tv:e1"}, FactsOK: false},
	}
	pools := EvaluatePools(ModeSingleCopy, []string{"tv:e1"}, candidate, candidateRef, current, profile, nil)
	if pools[0].Verdict != "replace" {
		t.Fatalf("a scoring candidate must replace a facts-missing placement instead of blocking: %+v", pools)
	}
	if len(pools[0].Losers) != 1 || pools[0].Losers[0].PlacementID != "plc-unknown" {
		t.Fatalf("the facts-missing placement must be the frozen loser: %+v", pools[0].Losers)
	}
	// Even a candidate that ranks lowest on every item beats it.
	zero := released(Facts{Resolution: "unknown-res"})
	pools = EvaluatePools(ModeSingleCopy, []string{"tv:e1"}, zero, candidateRef, current, profile, nil)
	if pools[0].Verdict != "replace" || len(pools[0].Losers) != 1 || pools[0].Reason != ReasonExistingWithoutFacts {
		t.Fatalf("any candidate must replace an unclaimed scan-managed file: %+v", pools)
	}
	// per_group: the group-less file is in every group's pool of its key, so
	// candidates of two different groups each replace it; nothing blocks.
	for _, group := range []*qualityprofile.SubgroupSource{subgroup("mikan", "1"), subgroup("custom", "src-9")} {
		grouped := released(Facts{Resolution: "720p", Subgroup: group})
		pools = EvaluatePools(ModePerGroup, []string{"tv:e1"}, grouped, candidateRef, current, profile, nil)
		if len(pools) != 1 || pools[0].Verdict != "replace" || pools[0].PoolKey != "tv:e1|"+group.Key() ||
			len(pools[0].Losers) != 1 || pools[0].Losers[0].PlacementID != "plc-unknown" {
			t.Fatalf("per_group %s must replace the group-less file: %+v", group.Key(), pools)
		}
	}
	// all mode never compares.
	pools = EvaluatePools(ModeAll, []string{"tv:e1"}, zero, candidateRef, current, profile, nil)
	if pools[0].Verdict != "queue" || len(pools[0].Losers) != 0 {
		t.Fatalf("all mode must leave the scan-managed file alone: %+v", pools)
	}
}

// A custom RSS source is its own group: two custom sources never share a
// per_group pool, and a provider group with the same bracketed title is
// another pool too.
func TestEvaluatePoolsPerGroupCustomSourceIsItsOwnGroup(t *testing.T) {
	profile := resolutionProfile("2160p", "1080p", "720p")
	candidate := released(Facts{Resolution: "2160p", Group: "Same", Subgroup: subgroup(qualityprofile.CustomSubgroupProvider, "src-1")})
	current := []PlacementFacts{
		{PlacementID: "plc-same-source", Version: 1, Fingerprint: "fp-1", Coverage: []string{"tv:e1"}, Facts: released(Facts{Resolution: "720p", Group: "Same", Subgroup: subgroup(qualityprofile.CustomSubgroupProvider, "src-1")}), FactsOK: true},
		{PlacementID: "plc-other-source", Version: 1, Fingerprint: "fp-2", Coverage: []string{"tv:e1"}, Facts: released(Facts{Resolution: "720p", Group: "Same", Subgroup: subgroup(qualityprofile.CustomSubgroupProvider, "src-2")}), FactsOK: true},
		{PlacementID: "plc-provider", Version: 1, Fingerprint: "fp-3", Coverage: []string{"tv:e1"}, Facts: released(Facts{Resolution: "720p", Group: "Same", Subgroup: subgroup("mikan", "src-1")}), FactsOK: true},
	}
	pools := EvaluatePools(ModePerGroup, []string{"tv:e1"}, candidate, poolRef("plc-candidate", 1, "fp-c"), current, profile, nil)
	if len(pools) != 1 || pools[0].PoolKey != "tv:e1|custom:src-1" || pools[0].Verdict != "replace" ||
		len(pools[0].Losers) != 1 || pools[0].Losers[0].PlacementID != "plc-same-source" {
		t.Fatalf("custom source pool = %+v", pools)
	}
}

func TestEvaluatePoolsUnrankedCandidateRetains(t *testing.T) {
	profile := resolutionProfile("1080p", "720p")
	candidate := released(Facts{Resolution: "unknown-res"}) // not in the rank list: 0 score
	candidateRef := poolRef("plc-candidate", 1, "fp-c")
	current := []PlacementFacts{
		{PlacementID: "plc-a", Version: 1, Fingerprint: "fp-a", Coverage: []string{"tv:e1"}, Facts: released(Facts{Resolution: "720p"}), FactsOK: true},
	}
	pools := EvaluatePools(ModeSingleCopy, []string{"tv:e1"}, candidate, candidateRef, current, profile, nil)
	if pools[0].Verdict != "retain" {
		t.Fatalf("unranked candidate must retain, not block: %+v", pools)
	}
	if pools[0].Winner == nil || pools[0].Winner.PlacementID != "plc-a" {
		t.Fatalf("retain must freeze the current winner: %+v", pools[0].Winner)
	}
}

func TestEvaluatePoolsSubgroupScoreDecidesFirst(t *testing.T) {
	// Real titles: ANi (1080p) vs SampleGroup (1080p). The default profile reads
	// the subscription's subgroup score first; equal scores fall through to
	// the other items and then to size.
	profile := qualityprofile.Default()
	candidate := released(Facts{Resolution: "1080p", Group: "ANi", Size: sizePtr(1 << 30), Score: int64Ptr(60)})
	candidateRef := poolRef("plc-candidate", 1, "fp-c")
	current := []PlacementFacts{
		{PlacementID: "plc-samplegroup", Version: 1, Fingerprint: "fp-l", Coverage: []string{"tv:e1"},
			Facts: released(Facts{Resolution: "1080p", Group: "SampleGroup", Size: sizePtr(2 << 30), Score: int64Ptr(90)}), FactsOK: true},
	}
	pools := EvaluatePools(ModeSingleCopy, []string{"tv:e1"}, candidate, candidateRef, current, profile, nil)
	if pools[0].Verdict != "retain" || len(pools[0].Steps) != 1 || pools[0].Steps[0].Key != qualityprofile.KeySubgroupScore {
		t.Fatalf("the lower subgroup score must retain on the first step: %+v", pools)
	}
	candidate.Score = int64Ptr(95)
	pools = EvaluatePools(ModeSingleCopy, []string{"tv:e1"}, candidate, candidateRef, current, profile, nil)
	if pools[0].Verdict != "replace" {
		t.Fatalf("the higher subgroup score must replace despite the smaller size: %+v", pools)
	}
	// No score on either side: everything ties and the size decides.
	candidate.Score, current[0].Facts.Score = nil, nil
	pools = EvaluatePools(ModeSingleCopy, []string{"tv:e1"}, candidate, candidateRef, current, profile, nil)
	if pools[0].Verdict != "retain" {
		t.Fatalf("equal items must fall to size (current larger): %+v", pools)
	}
	candidate.Size = sizePtr(3 << 30)
	pools = EvaluatePools(ModeSingleCopy, []string{"tv:e1"}, candidate, candidateRef, current, profile, nil)
	if pools[0].Verdict != "replace" || len(pools[0].Losers) != 1 || pools[0].Losers[0].PlacementID != "plc-samplegroup" {
		t.Fatalf("equal items with a larger size must replace: %+v", pools)
	}
	if last := pools[0].Steps[len(pools[0].Steps)-1]; last.Key != qualityprofile.SizeStep || last.Result != qualityprofile.StepBetter {
		t.Fatalf("the size step must decide: %+v", pools[0].Steps)
	}
	// A source without a score ranks below any scored source.
	candidate.Size = sizePtr(1 << 30)
	current[0].Facts.Score = int64Ptr(-50)
	pools = EvaluatePools(ModeSingleCopy, []string{"tv:e1"}, candidate, candidateRef, current, profile, nil)
	if pools[0].Verdict != "retain" {
		t.Fatalf("no score must rank below a negative score: %+v", pools)
	}
}

// per_group compares only inside one group, so the subgroup score is inert
// there.
func TestEvaluatePoolsPerGroupIgnoresSubgroupScore(t *testing.T) {
	profile := qualityprofile.Default()
	candidate := released(Facts{Resolution: "1080p", Group: "GroupA", Subgroup: subgroup("anibt", "a"), Score: int64Ptr(1)})
	current := []PlacementFacts{{PlacementID: "plc-a", Version: 1, Fingerprint: "fp-a", Coverage: []string{"tv:e1"},
		Facts: released(Facts{Resolution: "720p", Group: "GroupA", Subgroup: subgroup("anibt", "a"), Score: int64Ptr(100)}), FactsOK: true}}
	pools := EvaluatePools(ModePerGroup, []string{"tv:e1"}, candidate, poolRef("plc-candidate", 1, "fp-c"), current, profile, nil)
	if pools[0].Verdict != "replace" {
		t.Fatalf("per_group must ignore the subgroup score: %+v", pools)
	}
	for _, step := range pools[0].Steps {
		if step.Key == qualityprofile.KeySubgroupScore {
			t.Fatalf("per_group compared the subgroup score: %+v", pools[0].Steps)
		}
	}
}

func TestEvaluatePoolsTieRetainsCurrentWinner(t *testing.T) {
	profile := resolutionProfile("1080p", "720p")
	candidate := released(Facts{Resolution: "720p"})
	candidateRef := poolRef("plc-candidate", 1, "fp-c")
	created := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	current := []PlacementFacts{
		{PlacementID: "plc-old", Version: 1, Fingerprint: "fp-o", Coverage: []string{"tv:e1"}, Facts: released(Facts{Resolution: "720p"}), FactsOK: true, CreatedAt: created},
	}
	pools := EvaluatePools(ModeSingleCopy, []string{"tv:e1"}, candidate, candidateRef, current, profile, nil)
	if pools[0].Verdict != "retain" {
		t.Fatalf("equal quality must retain the current winner: %+v", pools)
	}
	if pools[0].Winner == nil || pools[0].Winner.PlacementID != "plc-old" {
		t.Fatalf("retain must freeze the current winner: %+v", pools[0].Winner)
	}
	if len(pools[0].Losers) != 0 {
		t.Fatalf("retain must not produce losers: %+v", pools[0].Losers)
	}
}

// ---------------------------------------------------------------------------
// Evaluate server: server-read facts, frozen decision, idempotency
// ---------------------------------------------------------------------------

func openFrozenTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	seedAnimeSubscription(t, db, "anime-1", "sub-1", "active")
	seedInstanceReplacement(t, db, "anime-1", ModePerGroup,
		`{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]}]}`, 1)
	return db
}

func TestEvaluateServerFreezesDecisionFromServerFacts(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")

	service := NewReplacementDecisionService(db)
	view, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if view.Verdict != "replace" || view.State != "admitted" {
		t.Fatalf("expected replace/admitted, got %s/%s", view.Verdict, view.State)
	}
	if view.CandidatePlacementId == nil || *view.CandidatePlacementId != candidate {
		t.Fatalf("candidate placement must be frozen: %v", view.CandidatePlacementId)
	}
	if view.SubscriptionVersion == nil || *view.SubscriptionVersion != 1 {
		t.Fatalf("subscription version must freeze: %v", view.SubscriptionVersion)
	}
	if view.PolicyVersion != 1 || view.ProfileVersion != qualityprofile.Version {
		t.Fatalf("policy version/profile must freeze: %d %s", view.PolicyVersion, view.ProfileVersion)
	}
	if view.InputDigest == nil || *view.InputDigest == "" {
		t.Fatal("input digest must freeze")
	}
	if view.Pools == nil || len(*view.Pools) != 1 {
		t.Fatalf("pools must freeze: %+v", view.Pools)
	}
	pool := (*view.Pools)[0]
	if pool.Winner == nil || pool.Winner.PlacementId != candidate {
		t.Fatalf("pool winner must be the candidate: %+v", pool.Winner)
	}
	if pool.Losers == nil || len(*pool.Losers) != 1 || (*pool.Losers)[0].PlacementId != "plc-file_operation-op-release-old" {
		t.Fatalf("pool loser must be the older placement: %+v", pool.Losers)
	}
	// Idempotent: the same frozen input returns the same decision.
	again, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
	if err != nil {
		t.Fatalf("re-evaluate: %v", err)
	}
	if again.Id != view.Id {
		t.Fatalf("identical evaluate must reuse the decision: %s != %s", again.Id, view.Id)
	}
}

func TestEvaluateServerReadsModeFromInstancePolicy(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	// The instance policy is single_copy; the old client-driven shape would
	// have submitted all/per_group. The server must use the policy mode.
	if _, err := db.Exec(`
UPDATE anime_business_configs SET admission_mode = 'single_copy', replacement_version = replacement_version + 1
WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "2160p", Group: "GroupB"}, "2026-08-01T00:00:00Z")

	service := NewReplacementDecisionService(db)
	view, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if view.AdmissionMode != "single_copy" {
		t.Fatalf("mode must come from the instance policy: %s", view.AdmissionMode)
	}
	if view.Verdict != "replace" || len(*view.Pools) != 1 {
		t.Fatalf("single_copy must compare across groups: %+v", view)
	}
}

func TestEvaluateServerAllModeQueuesWithoutLosers(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(`
UPDATE anime_business_configs SET admission_mode = 'all', replacement_version = replacement_version + 1
WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "2160p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "480p", Group: "GroupA"}, "2026-08-01T00:00:00Z")

	service := NewReplacementDecisionService(db)
	view, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if view.Verdict != "queue" || view.State != "admitted" {
		t.Fatalf("all-mode must queue even when worse: %s/%s", view.Verdict, view.State)
	}
	pool := (*view.Pools)[0]
	if pool.Winner != nil || pool.Losers != nil {
		t.Fatalf("all-mode must freeze no winner/losers: %+v", pool)
	}
}

func TestEvaluateServerBlockedOnMissingGroup(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	// Candidate has no stable group identity.
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "1080p"}, "2026-08-01T00:00:00Z")

	service := NewReplacementDecisionService(db)
	view, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if view.Verdict != "blocked" || view.State != "blocked" {
		t.Fatalf("per_group without group must be blocked: %s/%s", view.Verdict, view.State)
	}
	if pool := (*view.Pools)[0]; pool.Verdict != "blocked" || pool.Reason == nil || *pool.Reason != "group_missing" {
		t.Fatalf("typed blocked reason expected: %+v", pool)
	}
}

func TestEvaluateServerQualityTieKeepsIncumbentWithoutSubgroupItem(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	// Cross-group comparison needs single_copy (per_group never compares
	// different groups).
	if _, err := db.Exec(`
UPDATE anime_business_configs SET admission_mode = 'single_copy', replacement_version = replacement_version + 1
WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}
	// Ranked subgroup policies: SampleGroup scores 90, ANi scores 60 (anime
	// exact rules, the same shape the Subgroups tab persists).
	// The merged config row already exists (seedInstanceReplacement), so the
	// subgroups override needs no separate owner row.
	if _, err := db.Exec(`
INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value)
VALUES ('anime-1', 'subgroups', 'subgroups', 'subgroups-v1',
 '{"max_rss":3,"entries":[{"provider":"anibt","group_ref":"SampleGroup","selection_mode":"auto","score":90,"display_name":"SampleGroup","effective_selected":true},{"provider":"anibt","group_ref":"ANi","selection_mode":"auto","score":60,"display_name":"ANi","effective_selected":true}]}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	storedsections.Seed(t, db, "anime-1")
	// The profile does not list subgroup.score: equal quality keeps the
	// incumbent whatever the group scores.
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "SampleGroup"}, "2026-07-01T00:00:00Z")
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "ANi"}, "2026-08-01T00:00:00Z")

	service := NewReplacementDecisionService(db)
	view, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if view.Verdict != "keep" {
		t.Fatalf("equal quality must keep the incumbent: %s", view.Verdict)
	}
	pool := (*view.Pools)[0]
	if pool.Winner == nil || pool.Winner.PlacementId != "plc-file_operation-op-release-old" {
		t.Fatalf("incumbent placement must remain the winner: %+v", pool.Winner)
	}
	if pool.Losers != nil && len(*pool.Losers) != 0 {
		t.Fatalf("retain must not produce losers: %+v", pool.Losers)
	}
}

// A placed file compares with the values its release froze at acceptance,
// not with a re-read of the title: the old file's title says 1080p but its
// frozen AniBT resolution is 720p.
func TestEvaluateServerComparesPlacedFileWithFrozenValues(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(`
UPDATE anime_business_configs
SET admission_mode = 'single_copy', quality_profile = $1::jsonb, replacement_version = replacement_version + 1
WHERE managed_anime_id = 'anime-1'`, `{"version":"profile-v1","items":[{"key":"anibt.resolution","ranks":[["1080p"],["720p"]]}]}`); err != nil {
		t.Fatal(err)
	}
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "GroupA", Values: map[string][]string{qualityprofile.KeyAniBTResolution: {"720p"}}}, "2026-07-01T00:00:00Z")
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "GroupB", Values: map[string][]string{qualityprofile.KeyAniBTResolution: {"1080p"}}}, "2026-08-01T00:00:00Z")

	view, err := evaluateSingle(NewReplacementDecisionService(db).EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if view.Verdict != "replace" {
		t.Fatalf("the frozen AniBT resolution must decide: %+v", view)
	}
	pool := (*view.Pools)[0]
	if pool.Steps == nil || len(*pool.Steps) != 1 {
		t.Fatalf("the decision must freeze its steps: %+v", pool.Steps)
	}
	step := (*pool.Steps)[0]
	if step.Key != qualityprofile.KeyAniBTResolution || string(step.Result) != qualityprofile.StepBetter || *step.Candidate != "1080p" || *step.Current != "720p" {
		t.Fatalf("step = %+v", step)
	}
}

// subgroup.score reads the subscription's effective score of each release's
// frozen RSS source group (provider, group ref); a release without a source
// ranks lowest.
func TestEvaluateServerSubgroupScoreFromProviderSelections(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(`
UPDATE anime_business_configs
SET admission_mode = 'single_copy', quality_profile = $1::jsonb, replacement_version = replacement_version + 1
WHERE managed_anime_id = 'anime-1'`, string(qualityprofile.DefaultJSON())); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, ref string
		score   int
	}{{"sel-high", "group-high", 90}, {"sel-low", "group-low", 10}} {
		if _, err := db.Exec(`
INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,effective_selected,effective_score,available,availability_generation)
VALUES ($1,'sub-1','anibt',$2,$2,'auto',TRUE,$3,TRUE,1)`, row.id, row.ref, row.score); err != nil {
			t.Fatal(err)
		}
	}
	high := &qualityprofile.SubgroupSource{Provider: "anibt", GroupRef: "group-high"}
	low := &qualityprofile.SubgroupSource{Provider: "anibt", GroupRef: "group-low"}
	// The incumbent has the better title quality but the lower group score.
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "2160p", Group: "GroupA", Subgroup: low}, "2026-07-01T00:00:00Z")
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "GroupB", Subgroup: high}, "2026-08-01T00:00:00Z")
	// No RSS source group: ranks below every scored source.
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-orphan", []string{"tv:e2"},
		Facts{Resolution: "2160p", Group: "GroupC"}, "2026-07-01T00:00:00Z")
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-low", []string{"tv:e2"},
		Facts{Resolution: "480p", Group: "GroupD", Subgroup: low}, "2026-08-01T00:00:00Z")

	service := NewReplacementDecisionService(db)
	view, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if view.Verdict != "replace" {
		t.Fatalf("the higher group score must replace despite the lower resolution: %+v", view)
	}
	if step := (*(*view.Pools)[0].Steps)[0]; step.Key != qualityprofile.KeySubgroupScore || string(step.Result) != qualityprofile.StepBetter {
		t.Fatalf("the subgroup score must decide first: %+v", step)
	}
	orphan, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-low"}))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if orphan.Verdict != "replace" {
		t.Fatalf("a scored source must beat a file without a source group: %+v", orphan)
	}
}

func TestEvaluateServerSubscriptionStoppedIsNoop(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")

	service := NewReplacementDecisionService(db)
	_, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-new"}))
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != codeSubscriptionStopped {
		t.Fatalf("expected typed SUBSCRIPTION_STOPPED, got %v", err)
	}
	var decisions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_decisions`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 {
		t.Fatalf("stopped evaluate must create no decision: %d", decisions)
	}
}

func TestEvaluateServerCandidatePlacementMissing(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	// Release without a video placement (facts exist, no file operation).
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	service := NewReplacementDecisionService(db)
	_, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "release-old-other"}))
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != codeCandidatePlacementMissing {
		t.Fatalf("expected typed CANDIDATE_PLACEMENT_MISSING, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Settle: frozen winner/losers only, stale revalidation, frozen mode
// ---------------------------------------------------------------------------

func TestSettleKeepsFrozenWinnerOlderThanLoser(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	// The frozen winner was created BEFORE the loser: settlement must keep the
	// frozen winner and never re-rank by created_at.
	seedPlacement(t, db, "plc-winner", "anime-1", "/library/Winner/EP01.mkv", "fp-w", []string{"tv:e1"}, "2026-07-01T00:00:00Z")
	seedPlacement(t, db, "plc-loser", "anime-1", "/library/Loser/EP01.mkv", "fp-l", []string{"tv:e1"}, "2026-08-01T00:00:00Z")
	seedFrozenDecision(t, db, "dec-frozen", "anime-1", "plc-winner", 1, 1, ModeSingleCopy,
		&PlacementRef{PlacementID: "plc-winner", Version: 1, Fingerprint: "fp-w"},
		[]PlacementRef{{PlacementID: "plc-loser", Version: 1, Fingerprint: "fp-l"}}, "admitted")

	cleanup := NewCleanupService(db)
	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(cleanup)
	if err := service.SettleDecision(ctx, "dec-frozen"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if state := placementState(t, db, "plc-winner"); state != "active" {
		t.Fatalf("frozen winner must stay active, got %s", state)
	}
	if state := placementState(t, db, "plc-loser"); state != "superseded" {
		t.Fatalf("frozen loser must be superseded, got %s", state)
	}
	var decisionState string
	if err := db.QueryRow(`SELECT state FROM replacement_decisions WHERE id = 'dec-frozen'`).Scan(&decisionState); err != nil {
		t.Fatal(err)
	}
	if decisionState != "settled" {
		t.Fatalf("decision must be settled, got %s", decisionState)
	}
	var cleanupCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_deletions WHERE placement_id = 'plc-loser'`).Scan(&cleanupCount); err != nil {
		t.Fatal(err)
	}
	if cleanupCount != 1 {
		t.Fatalf("loser must be handed to cleanup: %d", cleanupCount)
	}
}

func TestSettleOnlySupersedesFrozenLosers(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedPlacement(t, db, "plc-winner", "anime-1", "/library/W/EP01.mkv", "fp-w", []string{"tv:e1"}, "2026-07-01T00:00:00Z")
	seedPlacement(t, db, "plc-loser", "anime-1", "/library/L/EP01.mkv", "fp-l", []string{"tv:e1"}, "2026-08-01T00:00:00Z")
	// Same anime, active, NOT listed in the decision: must stay untouched.
	seedPlacement(t, db, "plc-other", "anime-1", "/library/O/EP02.mkv", "fp-o", []string{"tv:e2"}, "2026-06-01T00:00:00Z")
	seedFrozenDecision(t, db, "dec-frozen", "anime-1", "plc-winner", 1, 1, ModeSingleCopy,
		&PlacementRef{PlacementID: "plc-winner", Version: 1, Fingerprint: "fp-w"},
		[]PlacementRef{{PlacementID: "plc-loser", Version: 1, Fingerprint: "fp-l"}}, "admitted")

	cleanup := NewCleanupService(db)
	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(cleanup)
	if err := service.SettleDecision(ctx, "dec-frozen"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if state := placementState(t, db, "plc-other"); state != "active" {
		t.Fatalf("placement outside the decision must stay active: %s", state)
	}
	var otherVersion int64
	if err := db.QueryRow(`SELECT version FROM placements WHERE id = 'plc-other'`).Scan(&otherVersion); err != nil {
		t.Fatal(err)
	}
	if otherVersion != 1 {
		t.Fatalf("placement outside the decision must keep its version: %d", otherVersion)
	}
	if state := placementState(t, db, "plc-loser"); state != "superseded" {
		t.Fatalf("frozen loser must be superseded: %s", state)
	}
}

func TestSettleCleanupGetsFrozenAdmissionMode(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	for _, mode := range []AdmissionMode{ModePerGroup, ModeSingleCopy} {
		anime := fmt.Sprintf("anime-%s", mode)
		sub := fmt.Sprintf("sub-%s", mode)
		seedAnimeSubscription(t, db, anime, sub, "active")
		seedInstanceReplacement(t, db, anime, mode, ``, 1)
		seedPlacement(t, db, "plc-"+string(mode)+"-w", anime, "/library/W/EP01.mkv", "fp-w", []string{"tv:e1"}, "2026-07-01T00:00:00Z")
		seedPlacement(t, db, "plc-"+string(mode)+"-l", anime, "/library/L/EP01.mkv", "fp-l", []string{"tv:e1"}, "2026-08-01T00:00:00Z")
		seedFrozenDecision(t, db, "dec-"+string(mode), anime, "plc-"+string(mode)+"-w", 1, 1, mode,
			&PlacementRef{PlacementID: "plc-" + string(mode) + "-w", Version: 1, Fingerprint: "fp-w"},
			[]PlacementRef{{PlacementID: "plc-" + string(mode) + "-l", Version: 1, Fingerprint: "fp-l"}}, "admitted")

		cleanup := NewCleanupService(db)
		service := NewReplacementDecisionService(db)
		service.SetCleanupHandoff(cleanup)
		if err := service.SettleDecision(ctx, "dec-"+string(mode)); err != nil {
			t.Fatalf("settle %s: %v", mode, err)
		}
		var admissionMode string
		if err := db.QueryRow(`
SELECT admission_mode FROM replacement_deletions WHERE managed_anime_id = $1`, anime).Scan(&admissionMode); err != nil {
			t.Fatal(err)
		}
		if admissionMode != string(mode) {
			t.Fatalf("cleanup must receive the frozen admission_mode %s, got %s", mode, admissionMode)
		}
	}
}

// settleStaleFixture seeds the standard replace decision plus its winners and
// losers so each stale test can mutate exactly one frozen input.
func settleStaleFixture(t *testing.T, db *sql.DB) *ReplacementDecisionService {
	t.Helper()
	seedPlacement(t, db, "plc-winner", "anime-1", "/library/W/EP01.mkv", "fp-w", []string{"tv:e1"}, "2026-07-01T00:00:00Z")
	seedPlacement(t, db, "plc-loser", "anime-1", "/library/L/EP01.mkv", "fp-l", []string{"tv:e1"}, "2026-08-01T00:00:00Z")
	seedFrozenDecision(t, db, "dec-frozen", "anime-1", "plc-winner", 1, 1, ModeSingleCopy,
		&PlacementRef{PlacementID: "plc-winner", Version: 1, Fingerprint: "fp-w"},
		[]PlacementRef{{PlacementID: "plc-loser", Version: 1, Fingerprint: "fp-l"}}, "admitted")
	cleanup := NewCleanupService(db)
	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(cleanup)
	return service
}

// assertStaleWithZeroEffects proves the decision went stale with the expected
// typed reason and no Placement, ReplacementDeletion or physical change happened
// (nothing superseded beyond the seeded mutation, no cleanup admitted, and
// the placement versions stay exactly at the pre-settle baseline).
func assertStaleWithZeroEffects(t *testing.T, db *sql.DB, wantReason string, wantWinnerState, wantLoserState string, wantWinnerVersion, wantLoserVersion int64) {
	t.Helper()
	var state, reason string
	if err := db.QueryRow(`SELECT state, stale_reason FROM replacement_decisions WHERE id = 'dec-frozen'`).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "stale" || reason != wantReason {
		t.Fatalf("decision must be stale with %s, got %s/%s", wantReason, state, reason)
	}
	if st := placementState(t, db, "plc-winner"); st != wantWinnerState {
		t.Fatalf("winner state = %s, want %s", st, wantWinnerState)
	}
	if st := placementState(t, db, "plc-loser"); st != wantLoserState {
		t.Fatalf("loser state = %s, want %s", st, wantLoserState)
	}
	var winnerVersion, loserVersion int64
	if err := db.QueryRow(`SELECT version FROM placements WHERE id = 'plc-winner'`).Scan(&winnerVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT version FROM placements WHERE id = 'plc-loser'`).Scan(&loserVersion); err != nil {
		t.Fatal(err)
	}
	if winnerVersion != wantWinnerVersion || loserVersion != wantLoserVersion {
		t.Fatalf("settle must not bump placement versions: winner=%d (want %d) loser=%d (want %d)",
			winnerVersion, wantWinnerVersion, loserVersion, wantLoserVersion)
	}
	var deletions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_deletions`).Scan(&deletions); err != nil {
		t.Fatal(err)
	}
	if deletions != 0 {
		t.Fatalf("stale settle must create zero cleanup operations: %d", deletions)
	}
}

func TestSettleStaleOnSubscriptionStopped(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	service := settleStaleFixture(t, db)
	if _, err := db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}
	if err := service.SettleDecision(context.Background(), "dec-frozen"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	assertStaleWithZeroEffects(t, db, staleSubscriptionStopped, "active", "active", 1, 1)
}

func TestSettleStaleOnSubscriptionVersionChanged(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	service := settleStaleFixture(t, db)
	// A stop/resume epoch (or any Subscription write) bumps the version.
	if _, err := db.Exec(`UPDATE subscriptions SET version = version + 1, updated_at = CURRENT_TIMESTAMP WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}
	if err := service.SettleDecision(context.Background(), "dec-frozen"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	assertStaleWithZeroEffects(t, db, staleSubscriptionVersionChanged, "active", "active", 1, 1)
}

func TestSettleStaleOnPolicyVersionChanged(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	service := settleStaleFixture(t, db)
	if _, err := db.Exec(`
UPDATE anime_business_configs SET admission_mode = 'all', replacement_version = replacement_version + 1, updated_at = CURRENT_TIMESTAMP
WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}
	if err := service.SettleDecision(context.Background(), "dec-frozen"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	assertStaleWithZeroEffects(t, db, stalePolicyVersionChanged, "active", "active", 1, 1)
}

func TestSettleStaleOnWinnerVersionChanged(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	service := settleStaleFixture(t, db)
	if _, err := db.Exec(`UPDATE placements SET version = version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = 'plc-winner'`); err != nil {
		t.Fatal(err)
	}
	if err := service.SettleDecision(context.Background(), "dec-frozen"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	assertStaleWithZeroEffects(t, db, stalePlacementVersionChanged, "active", "active", 2, 1)
}

func TestSettleStaleOnLoserVersionChanged(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	service := settleStaleFixture(t, db)
	if _, err := db.Exec(`UPDATE placements SET version = version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = 'plc-loser'`); err != nil {
		t.Fatal(err)
	}
	if err := service.SettleDecision(context.Background(), "dec-frozen"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	assertStaleWithZeroEffects(t, db, stalePlacementVersionChanged, "active", "active", 1, 2)
}

func TestSettleStaleOnFingerprintChanged(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	service := settleStaleFixture(t, db)
	if _, err := db.Exec(`UPDATE placements SET target_fingerprint = 'tampered', updated_at = CURRENT_TIMESTAMP WHERE id = 'plc-loser'`); err != nil {
		t.Fatal(err)
	}
	if err := service.SettleDecision(context.Background(), "dec-frozen"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	assertStaleWithZeroEffects(t, db, stalePlacementFingerprintChanged, "active", "active", 1, 1)
}

func TestSettleStaleWhenWinnerAlreadySuperseded(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	service := settleStaleFixture(t, db)
	// Another decision retired the frozen winner: the frozen decision cannot
	// supersede its losers against a dead winner.
	if _, err := db.Exec(`UPDATE placements SET state = 'superseded', version = version + 1 WHERE id = 'plc-winner'`); err != nil {
		t.Fatal(err)
	}
	if err := service.SettleDecision(context.Background(), "dec-frozen"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	assertStaleWithZeroEffects(t, db, stalePlacementInactive, "superseded", "active", 2, 1)
}

// ---------------------------------------------------------------------------
// Settle crash replay: decision-bound convergence, no re-selection
// ---------------------------------------------------------------------------

func TestSettleReplayRegistersOneDeletionPerFrozenLoser(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedPlacement(t, db, "plc-winner", "anime-1", "/library/W/EP01.mkv", "fp-w", []string{"tv:e1"}, "2026-07-01T00:00:00Z")
	seedPlacement(t, db, "plc-loser", "anime-1", "/library/L/EP01.mkv", "fp-l", []string{"tv:e1"}, "2026-08-01T00:00:00Z")
	// A superseded placement of the SAME anime that is NOT in the decision:
	// convergence must never hand it off.
	seedPlacement(t, db, "plc-unrelated", "anime-1", "/library/U/EP02.mkv", "fp-u", []string{"tv:e2"}, "2026-05-01T00:00:00Z")
	seedFrozenDecision(t, db, "dec-frozen", "anime-1", "plc-winner", 1, 1, ModeSingleCopy,
		&PlacementRef{PlacementID: "plc-winner", Version: 1, Fingerprint: "fp-w"},
		[]PlacementRef{{PlacementID: "plc-loser", Version: 1, Fingerprint: "fp-l"}}, "admitted")

	if _, err := db.Exec(`UPDATE placements SET state = 'superseded', version = version + 1 WHERE id = 'plc-unrelated'`); err != nil {
		t.Fatal(err)
	}

	// The deletion obligation is registered in the same transaction that
	// supersedes the loser, so there is no window between the two.
	cleanup := NewCleanupService(db)
	cleanup.SetWorkItems(cleanupWorkItemStore(t, db))
	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(cleanup)
	if err := service.SettleDecision(ctx, "dec-frozen"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	var loserCleanups, unrelatedCleanups int
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_deletions WHERE placement_id = 'plc-loser'`).Scan(&loserCleanups); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_deletions WHERE placement_id = 'plc-unrelated'`).Scan(&unrelatedCleanups); err != nil {
		t.Fatal(err)
	}
	if loserCleanups != 1 || unrelatedCleanups != 0 {
		t.Fatalf("converge must hand off only the decision's frozen losers: loser=%d unrelated=%d", loserCleanups, unrelatedCleanups)
	}
	// Replay stays idempotent and never duplicates.
	if err := service.SettleDecision(ctx, "dec-frozen"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_deletions WHERE placement_id = 'plc-loser'`).Scan(&loserCleanups); err != nil {
		t.Fatal(err)
	}
	if loserCleanups != 1 {
		t.Fatalf("replay must not duplicate cleanup: %d", loserCleanups)
	}
}

func TestSettleForPlacementOnlyExecutesOwnDecision(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedPlacement(t, db, "plc-w1", "anime-1", "/library/W1/EP01.mkv", "fp-w1", []string{"tv:e1"}, "2026-07-01T00:00:00Z")
	seedPlacement(t, db, "plc-l1", "anime-1", "/library/L1/EP01.mkv", "fp-l1", []string{"tv:e1"}, "2026-08-01T00:00:00Z")
	seedPlacement(t, db, "plc-w2", "anime-1", "/library/W2/EP02.mkv", "fp-w2", []string{"tv:e2"}, "2026-07-02T00:00:00Z")
	seedPlacement(t, db, "plc-l2", "anime-1", "/library/L2/EP02.mkv", "fp-l2", []string{"tv:e2"}, "2026-08-02T00:00:00Z")
	seedFrozenDecision(t, db, "dec-1", "anime-1", "plc-w1", 1, 1, ModeSingleCopy,
		&PlacementRef{PlacementID: "plc-w1", Version: 1, Fingerprint: "fp-w1"},
		[]PlacementRef{{PlacementID: "plc-l1", Version: 1, Fingerprint: "fp-l1"}}, "admitted")
	seedFrozenDecision(t, db, "dec-2", "anime-1", "plc-w2", 1, 1, ModeSingleCopy,
		&PlacementRef{PlacementID: "plc-w2", Version: 1, Fingerprint: "fp-w2"},
		[]PlacementRef{{PlacementID: "plc-l2", Version: 1, Fingerprint: "fp-l2"}}, "admitted")

	cleanup := NewCleanupService(db)
	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(cleanup)
	// Settle for the second candidate only: dec-2 executes, dec-1 untouched.
	if err := service.SettleForPlacement(ctx, "plc-w2"); err != nil {
		t.Fatalf("settle for placement: %v", err)
	}
	if state := placementState(t, db, "plc-l2"); state != "superseded" {
		t.Fatalf("dec-2 loser must be superseded: %s", state)
	}
	if state := placementState(t, db, "plc-l1"); state != "active" {
		t.Fatalf("dec-1 loser must stay active: %s", state)
	}
	var dec1State, dec2State string
	if err := db.QueryRow(`SELECT state FROM replacement_decisions WHERE id = 'dec-1'`).Scan(&dec1State); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state FROM replacement_decisions WHERE id = 'dec-2'`).Scan(&dec2State); err != nil {
		t.Fatal(err)
	}
	if dec1State != "admitted" || dec2State != "settled" {
		t.Fatalf("only the own decision may settle: dec-1=%s dec-2=%s", dec1State, dec2State)
	}
}

func TestSettleNoDecisionIsNoop(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	seedPlacement(t, db, "plc-new", "anime-1", "/library/N/EP01.mkv", "fp-n", []string{"tv:e1"}, "2026-08-01T00:00:00Z")
	cleanup := NewCleanupService(db)
	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(cleanup)
	if err := service.SettleForPlacement(context.Background(), "plc-new"); err != nil {
		t.Fatalf("no-decision settle must be a no-op: %v", err)
	}
	placements, deletions, _ := decisionCounts(t, db, "anime-1")
	if placements != 1 || deletions != 0 {
		t.Fatalf("no-decision settle must change nothing: placements=%d deletions=%d", placements, deletions)
	}
}

// ---------------------------------------------------------------------------
// replacement_evaluate work item handler: real owner, no empty success
// ---------------------------------------------------------------------------

func TestEvaluateHandlerRunsRealOwner(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")

	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: "anime-1"},
		Kind:                 workitems.KindReplacementEvaluate,
		IdempotencyKey:       "repl-evaluate:anime-1:release-new",
		InputSnapshot:        json.RawMessage(`{"candidate_release_id":"release-new"}`),
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
	})
	if err != nil {
		tx.Rollback()
		t.Fatalf("create evaluate work item: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	service := NewReplacementDecisionService(db)
	cleanup := NewCleanupService(db)
	service.SetCleanupHandoff(cleanup)
	outcome, err := service.EvaluateHandler().Handle(ctx, workitems.Claim{
		ID: itemID, Kind: workitems.KindReplacementEvaluate,
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: "anime-1"},
		ExpectedOwnerVersion: 1, FencingToken: 1, Attempt: 1, MaxAttempts: 3,
		IdempotencyKey: "repl-evaluate:anime-1:release-new", InputSnapshot: json.RawMessage(`{"candidate_release_id":"release-new"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("expected succeed from the real owner, got %+v", outcome)
	}
	if outcome.OwnerResult.ReasonCode != "replacement_evaluated" {
		t.Fatalf("reason code must come from the persisted decision: %s", outcome.OwnerResult.ReasonCode)
	}
	// The decision exists with real frozen pools (not an empty handler). The
	// fixture has a candidate Placement already, so settlement supersedes the
	// frozen loser; both winner and loser intentionally share one physical path,
	// therefore same-path cleanup is suppressed.
	var poolsRaw []byte
	var verdict, state, staleReason sql.NullString
	if err := db.QueryRow(`SELECT verdict, state, stale_reason, pools FROM replacement_decisions WHERE managed_anime_id = 'anime-1'`).Scan(&verdict, &state, &staleReason, &poolsRaw); err != nil {
		t.Fatal(err)
	}
	if verdict.String != "replace" || state.String != "settled" {
		t.Fatalf("handler must persist and settle the real owner truth: %s/%s", verdict.String, state.String)
	}
	var pools []PoolEvidence
	if err := json.Unmarshal(poolsRaw, &pools); err != nil {
		t.Fatal(err)
	}
	if len(pools) != 1 || len(pools[0].Losers) != 1 || pools[0].Losers[0].PlacementID != "plc-file_operation-op-release-old" {
		t.Fatalf("handler decision must freeze the real loser: %+v", pools)
	}
	if state := placementState(t, db, "plc-file_operation-op-release-old"); state != "superseded" {
		t.Fatalf("frozen loser must be superseded by the handler settle: %s", state)
	}
	var deletions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_deletions WHERE managed_anime_id = 'anime-1'`).Scan(&deletions); err != nil {
		t.Fatal(err)
	}
	if deletions != 0 {
		t.Fatalf("same-path loser must not be handed to cleanup: %d", deletions)
	}
}

func TestEvaluateHandlerSubscriptionStoppedCompletesWithoutDecision(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-new", []string{"tv:e1"},
		Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")
	service := NewReplacementDecisionService(db)
	outcome, err := service.EvaluateHandler().Handle(ctx, workitems.Claim{
		Kind:                 workitems.KindReplacementEvaluate,
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: "anime-1"},
		ExpectedOwnerVersion: 1, FencingToken: 1, Attempt: 1, MaxAttempts: 3,
		InputSnapshot: json.RawMessage(`{"candidate_release_id":"release-new"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("stopped subscription must be a typed no-op, got %+v", outcome)
	}
	if outcome.OwnerResult.ReasonCode != "subscription_stopped" {
		t.Fatalf("expected subscription_stopped, got %s", outcome.OwnerResult.ReasonCode)
	}
	var decisions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_decisions`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 {
		t.Fatalf("stopped evaluate must create no decision: %d", decisions)
	}
}

func TestEvaluateHandlerRetriesMissingCandidatePlacement(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedFilePlacement(t, db, "anime-1", "sub-1", "release-old", []string{"tv:e1"},
		Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	service := NewReplacementDecisionService(db)
	outcome, err := service.EvaluateHandler().Handle(ctx, workitems.Claim{
		Kind:                 workitems.KindReplacementEvaluate,
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: "anime-1"},
		ExpectedOwnerVersion: 1, FencingToken: 1, Attempt: 1, MaxAttempts: 3,
		InputSnapshot: json.RawMessage(`{"candidate_release_id":"release-missing"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if outcome.Kind != worker.OutcomeRetry {
		t.Fatalf("missing candidate placement must retry, got %+v", outcome)
	}
	if outcome.ReasonCode != "candidate_placement_missing" {
		t.Fatalf("expected candidate_placement_missing, got %s", outcome.ReasonCode)
	}
}

func TestEvaluateHandlerInvalidClaimFails(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	service := NewReplacementDecisionService(db)
	outcome, err := service.EvaluateHandler().Handle(context.Background(), workitems.Claim{
		Kind:                 workitems.KindReplacementEvaluate,
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: "anime-1"},
		ExpectedOwnerVersion: 1, FencingToken: 1, Attempt: 1, MaxAttempts: 3,
		InputSnapshot: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("malformed claim must fail, got %+v", outcome)
	}
	if outcome.OwnerResult.ReasonCode != "replacement_claim_invalid" {
		t.Fatalf("expected replacement_claim_invalid, got %s", outcome.OwnerResult.ReasonCode)
	}
}

// ---------------------------------------------------------------------------
// Settlement registers the deletion; the cleanup owner deletes the loser.
// ---------------------------------------------------------------------------

func TestSettleDeletesLoserAndReplayIsIdempotent(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	winnerPath := filepath.Join(dir, "Winner", "EP01.mkv")
	loserPath := filepath.Join(dir, "Loser", "EP01.mkv")
	winnerContent := []byte("winner-file")
	if err := os.MkdirAll(filepath.Dir(winnerPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(loserPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(winnerPath, winnerContent, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loserPath, []byte("loser-file"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := filesystem.NewOSCleanupExecutor(dir)
	loserObservation, err := executor.ObserveMetadataFile(ctx, loserPath)
	if err != nil {
		t.Fatal(err)
	}
	winnerFP := sha256Hex(winnerContent)
	loserFP := loserObservation.Fingerprint

	seedPlacement(t, db, "plc-winner", "anime-1", winnerPath, winnerFP, []string{"tv:e1"}, "2026-07-01T00:00:00Z")
	seedPlacement(t, db, "plc-loser", "anime-1", loserPath, loserFP, []string{"tv:e1"}, "2026-08-01T00:00:00Z")
	seedFrozenDecision(t, db, "dec-frozen", "anime-1", "plc-winner", 1, 1, ModeSingleCopy,
		&PlacementRef{PlacementID: "plc-winner", Version: 1, Fingerprint: winnerFP},
		[]PlacementRef{{PlacementID: "plc-loser", Version: 1, Fingerprint: loserFP}}, "admitted")

	store := cleanupWorkItemStore(t, db)
	cleanup := NewCleanupService(db)
	cleanup.SetFileExecutor(executor)
	cleanup.SetWorkItems(store)
	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(cleanup)

	if err := service.SettleDecision(ctx, "dec-frozen"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	var cleanupID, state string
	if err := db.QueryRow(`SELECT id, state FROM replacement_deletions WHERE placement_id = 'plc-loser'`).Scan(&cleanupID, &state); err != nil {
		t.Fatal(err)
	}
	if state != string(CleanupAdmitted) {
		t.Fatalf("a loser with a recorded identity must be admitted for deletion: %s", state)
	}
	claim, err := store.ClaimDue(ctx, "settle-delete", []string{workitems.KindReplacementDelete}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := cleanup.DeleteHandler().Handle(ctx, claim)
	if err != nil || outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("delete outcome=%+v err=%v", outcome, err)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(loserPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loser file must be deleted: %v", err)
	}
	if _, err := os.Stat(winnerPath); err != nil {
		t.Fatalf("winner file must stay in place: %v", err)
	}

	// Replay: the settled decision is a no-op and never duplicates the
	// obligation.
	if err := service.SettleDecision(ctx, "dec-frozen"); err != nil {
		t.Fatalf("replay settle: %v", err)
	}
	var deletions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_deletions WHERE placement_id = 'plc-loser'`).Scan(&deletions); err != nil {
		t.Fatal(err)
	}
	if deletions != 1 {
		t.Fatalf("replay must not duplicate cleanup: %d", deletions)
	}
	if err := db.QueryRow(`SELECT state FROM replacement_deletions WHERE id = $1`, cleanupID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(CleanupDeleted) {
		t.Fatalf("cleanup state = %s, want deleted", state)
	}
	if state := placementState(t, db, "plc-loser"); state != "superseded" {
		t.Fatalf("loser must stay superseded: %s", state)
	}
	if state := placementState(t, db, "plc-winner"); state != "active" {
		t.Fatalf("frozen winner must stay active: %s", state)
	}
}
func TestSettleStaleLeavesPhysicalFilesUntouched(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	dir := t.TempDir()
	winnerPath := filepath.Join(dir, "Winner", "EP01.mkv")
	loserPath := filepath.Join(dir, "Loser", "EP01.mkv")
	if err := os.MkdirAll(filepath.Dir(winnerPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(loserPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(winnerPath, []byte("winner"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loserPath, []byte("loser"), 0o644); err != nil {
		t.Fatal(err)
	}
	winnerFP := sha256Hex([]byte("winner"))
	loserFP := sha256Hex([]byte("loser"))
	seedPlacement(t, db, "plc-winner", "anime-1", winnerPath, winnerFP, []string{"tv:e1"}, "2026-07-01T00:00:00Z")
	seedPlacement(t, db, "plc-loser", "anime-1", loserPath, loserFP, []string{"tv:e1"}, "2026-08-01T00:00:00Z")
	seedFrozenDecision(t, db, "dec-frozen", "anime-1", "plc-winner", 1, 1, ModeSingleCopy,
		&PlacementRef{PlacementID: "plc-winner", Version: 1, Fingerprint: winnerFP},
		[]PlacementRef{{PlacementID: "plc-loser", Version: 1, Fingerprint: loserFP}}, "admitted")
	if _, err := db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}

	cleanup := NewCleanupService(db)
	cleanup.SetFileExecutor(filesystem.NewOSCleanupExecutor(dir))
	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(cleanup)
	if err := service.SettleDecision(ctx, "dec-frozen"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if _, err := os.Stat(winnerPath); err != nil {
		t.Fatalf("winner file must be untouched: %v", err)
	}
	if _, err := os.Stat(loserPath); err != nil {
		t.Fatalf("loser file must be untouched: %v", err)
	}
	assertStaleWithZeroEffects(t, db, staleSubscriptionStopped, "active", "active", 1, 1)
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
