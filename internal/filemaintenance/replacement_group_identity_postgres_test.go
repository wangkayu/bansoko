package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/qualityprofile"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

// customGroup is the stable group identity of the custom RSS source
// seedFilePlacement creates for a release ("src-<release>").
func customGroup(releaseID string) *qualityprofile.SubgroupSource {
	return subgroup(qualityprofile.CustomSubgroupProvider, "src-"+releaseID)
}

// moveReleaseToSource re-points a seeded release at another (seeded) RSS
// source and freezes that source's group identity in its candidate facts.
func moveReleaseToSource(t *testing.T, db *sql.DB, releaseID, sourceReleaseID string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE releases SET rss_source_id = $2 WHERE id = $1`, releaseID, "src-"+sourceReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
UPDATE release_candidate_facts
SET compare_values = jsonb_set(compare_values, '{subgroup}', jsonb_build_object('provider', 'custom', 'group_ref', $2::text))
WHERE release_id = $1`, releaseID, "src-"+sourceReleaseID); err != nil {
		t.Fatal(err)
	}
}

// seedScanManagedPlacement records an active scan-managed placement (source
// evidence reconciliation, replacement-eligible) at path; qualityFactsRef is
// the release that claimed it ("" = unclaimed).
func seedScanManagedPlacement(t *testing.T, db *sql.DB, id, animeID, path, fingerprint, qualityFactsRef string, coverage []string, createdAt string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, snapshot_generation,
 verification_state, state, replacement_eligible, version, created_at, updated_at)
VALUES ($1, $2, $3, 'reconciliation', $4, $5, $5, NULLIF($6, ''), 'facts-v1', 1, 'verified', 'active', true, 1, $7::timestamptz, $7::timestamptz)`,
		id, path, animeID, animeID+":1:"+path, fingerprint, qualityFactsRef, createdAt); err != nil {
		t.Fatal(err)
	}
	for ordinal, key := range coverage {
		if _, err := db.Exec(`
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ($1, $2, $3, 'facts-v1', $1)`, id, key, ordinal); err != nil {
			t.Fatal(err)
		}
	}
}

func setAdmissionMode(t *testing.T, db *sql.DB, mode AdmissionMode) {
	t.Helper()
	if _, err := db.Exec(`
UPDATE anime_business_configs SET admission_mode = $1, retention_version = retention_version + 1
WHERE managed_anime_id = 'anime-1'`, string(mode)); err != nil {
		t.Fatal(err)
	}
}

func decisionPools(t *testing.T, db *sql.DB, releaseID string) []PoolEvidence {
	t.Helper()
	var raw []byte
	if err := db.QueryRow(`SELECT pools FROM replacement_decisions WHERE candidate_release_id = $1 AND state <> 'stale' ORDER BY created_at DESC LIMIT 1`, releaseID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var pools []PoolEvidence
	if err := json.Unmarshal(raw, &pools); err != nil {
		t.Fatal(err)
	}
	return pools
}

func loserIDs(pools []PoolEvidence) map[string]bool {
	out := map[string]bool{}
	for _, pool := range pools {
		for _, loser := range pool.Losers {
			out[loser.PlacementID] = true
		}
	}
	return out
}

// per_group pools by the frozen (provider, group ref) — here the custom RSS
// source — never by the bracketed title group: a release of the same source
// with another title group competes, a release of another source with the
// same title group does not.
func TestPGPerGroupPoolsByStableGroupIdentity(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	sameSource := seedFilePlacement(t, db, "anime-1", "sub-1", "pg-same-source", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "OtherTitle", Subgroup: customGroup("pg-new")}, "2026-07-01T00:00:00Z")
	otherSource := seedFilePlacement(t, db, "anime-1", "sub-1", "pg-other-source", []string{"tv:s1:e1"},
		Facts{Resolution: "480p", Group: "SameTitle", Subgroup: customGroup("pg-other-source")}, "2026-07-02T00:00:00Z")
	candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "pg-new", []string{"tv:s1:e1"},
		Facts{Resolution: "1080p", Group: "SameTitle", Subgroup: customGroup("pg-new")}, "2026-08-01T00:00:00Z")
	moveReleaseToSource(t, db, "pg-same-source", "pg-new")

	view, err := evaluateSingle(NewReplacementDecisionService(db).EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "pg-new"}))
	if err != nil {
		t.Fatal(err)
	}
	if view.Verdict != "replace" || view.Pools == nil || len(*view.Pools) != 1 {
		t.Fatalf("per_group decision = %+v", view)
	}
	pool := (*view.Pools)[0]
	if pool.PoolKey != "tv:s1:e1|custom:src-pg-new" {
		t.Fatalf("pool key = %s, want coverage + custom source identity", pool.PoolKey)
	}
	if pool.Losers == nil || len(*pool.Losers) != 1 || (*pool.Losers)[0].PlacementId != sameSource {
		t.Fatalf("only the same-source placement may lose (not %s): %+v", otherSource, pool.Losers)
	}
	if pool.Winner == nil || pool.Winner.PlacementId != candidate {
		t.Fatalf("winner = %+v", pool.Winner)
	}
}

// A scan-managed file no release has claimed is a managed file without a
// group or values: in single_copy and per_group any candidate replaces it and
// the loser is deleted like any managed loser; all mode leaves it alone.
func TestPGUnclaimedScanManagedFileIsReplacedAndDeleted(t *testing.T) {
	for _, mode := range []AdmissionMode{ModeSingleCopy, ModePerGroup, ModeAll} {
		t.Run(string(mode), func(t *testing.T) {
			db := openFrozenTestDB(t)
			defer db.Close()
			ctx := context.Background()
			setAdmissionMode(t, db, mode)
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			scanPath := filepath.Join(dir, "Scanned", "EP01.mkv")
			if err := os.MkdirAll(filepath.Dir(scanPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(scanPath, []byte("scanned-file"), 0o644); err != nil {
				t.Fatal(err)
			}
			executor := filesystem.NewOSCleanupExecutor(dir)
			observed, err := executor.ObserveMetadataFile(ctx, scanPath)
			if err != nil {
				t.Fatal(err)
			}
			seedScanManagedPlacement(t, db, "plc-scan", "anime-1", scanPath, observed.Fingerprint, "", []string{"tv:s1:e1"}, "2026-07-01T00:00:00Z")
			// The candidate ranks lowest on every item and still wins.
			candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "scan-new", []string{"tv:s1:e1"},
				Facts{Resolution: "unranked", Group: "G"}, "2026-08-01T00:00:00Z")

			store := cleanupWorkItemStore(t, db)
			cleanup := NewCleanupService(db)
			cleanup.SetFileExecutor(executor)
			cleanup.SetWorkItems(store)
			service := NewReplacementDecisionService(db)
			service.SetCleanupHandoff(cleanup)
			view, err := evaluateSingle(service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "scan-new"}))
			if err != nil {
				t.Fatal(err)
			}
			if mode == ModeAll {
				if view.Verdict != "queue" {
					t.Fatalf("all mode must queue: %+v", view)
				}
				if state := placementState(t, db, "plc-scan"); state != "active" {
					t.Fatalf("all mode must keep the scan-managed file: %s", state)
				}
				if _, err := os.Stat(scanPath); err != nil {
					t.Fatalf("all mode must leave the file: %v", err)
				}
				return
			}
			if view.Verdict != "replace" {
				t.Fatalf("%s: any candidate must replace an unclaimed scan-managed file: %+v", mode, view)
			}
			if losers := loserIDs(decisionPools(t, db, "scan-new")); !losers["plc-scan"] {
				t.Fatalf("%s: the scan-managed file must be the frozen loser: %v", mode, losers)
			}
			if err := service.SettleForPlacement(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			if state := placementState(t, db, "plc-scan"); state != "superseded" {
				t.Fatalf("scan-managed loser state = %s", state)
			}
			claim, err := store.ClaimDue(ctx, "scan-delete", []string{workitems.KindCleanupDelete}, time.Minute)
			if err != nil || claim.ID == "" {
				t.Fatalf("claim delete: %+v %v", claim, err)
			}
			outcome, err := cleanup.DeleteHandler().Handle(ctx, claim)
			if err != nil || outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
				t.Fatalf("delete outcome=%+v err=%v", outcome, err)
			}
			if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(scanPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the scan-managed loser must be deleted: %v", err)
			}
			var state string
			if err := db.QueryRow(`SELECT state FROM cleanup_operations WHERE placement_id = 'plc-scan'`).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != string(CleanupDeleted) {
				t.Fatalf("cleanup state = %s, want deleted", state)
			}
		})
	}
}

// A claimed scan-managed file carries its release's group and frozen values:
// per_group compares it only with candidates of that group.
func TestPGClaimedScanManagedFilePoolsByReleaseGroup(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	// The claiming release: its chain freezes 720p and its source identity.
	seedFilePlacement(t, db, "anime-1", "sub-1", "claim-rel", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "G", Subgroup: customGroup("claim-rel")}, "2026-06-01T00:00:00Z")
	// The release's own file placement is gone; the scan-managed file it
	// claimed holds the episode.
	if _, err := db.Exec(`UPDATE placements SET state = 'released', released_at = now(), released_by = 'test', release_request_key = 'test-release' WHERE quality_facts_ref = 'claim-rel'`); err != nil {
		t.Fatal(err)
	}
	seedScanManagedPlacement(t, db, "plc-claimed", "anime-1", "/library/Show/claimed.mkv", `{"size_bytes":12}`, "claim-rel", []string{"tv:s1:e1"}, "2026-07-01T00:00:00Z")

	service := NewReplacementDecisionService(db)
	// Another group's better candidate: another pool, the claimed file stays.
	seedFilePlacement(t, db, "anime-1", "sub-1", "claim-other", []string{"tv:s1:e1"},
		Facts{Resolution: "1080p", Group: "G", Subgroup: customGroup("claim-other")}, "2026-08-01T00:00:00Z")
	if _, err := service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "claim-other"}); err != nil {
		t.Fatal(err)
	}
	if losers := loserIDs(decisionPools(t, db, "claim-other")); losers["plc-claimed"] {
		t.Fatalf("another group's candidate must not compare with the claimed file: %v", losers)
	}
	// The claiming group's better candidate replaces it.
	seedFilePlacement(t, db, "anime-1", "sub-1", "claim-same", []string{"tv:s1:e1"},
		Facts{Resolution: "1080p", Group: "G", Subgroup: customGroup("claim-rel")}, "2026-08-02T00:00:00Z")
	moveReleaseToSource(t, db, "claim-same", "claim-rel")
	if _, err := service.EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "claim-same"}); err != nil {
		t.Fatal(err)
	}
	pools := decisionPools(t, db, "claim-same")
	if !loserIDs(pools)["plc-claimed"] || len(pools) != 1 || pools[0].PoolKey != "tv:s1:e1|custom:src-claim-rel" {
		t.Fatalf("the claiming group's candidate must replace the claimed file: %+v", pools)
	}
}

// A placement the latest calibrated scan reports missing (or changed) holds
// no coverage: it is neither pooled nor compared, so it neither keeps a
// candidate out nor becomes a loser.
func TestPGMissingPlacementIsNeitherPooledNorCompared(t *testing.T) {
	for _, disposition := range []string{"", "missing", "fingerprint_conflict"} {
		t.Run("disposition="+disposition, func(t *testing.T) {
			db := openFrozenTestDB(t)
			defer db.Close()
			ctx := context.Background()
			setAdmissionMode(t, db, ModeSingleCopy)
			old := seedFilePlacement(t, db, "anime-1", "sub-1", "missing-old", []string{"tv:s1:e1"},
				Facts{Resolution: "2160p", Group: "G"}, "2026-07-01T00:00:00Z")
			seedFilePlacement(t, db, "anime-1", "sub-1", "missing-new", []string{"tv:s1:e1"},
				Facts{Resolution: "720p", Group: "G"}, "2026-08-01T00:00:00Z")
			if disposition != "" {
				seedCompletedGeneration(t, db, "anime-1", 1, map[string]string{})
				if _, err := db.Exec(`
INSERT INTO reconciliation_files(managed_anime_id, generation, batch_index, file_path, fingerprint, size_bytes, facts, observed_at, disposition)
VALUES ('anime-1', 1, 0, '/library/Show/ep.mkv', 'fp', 1, jsonb_build_object('placement_id', $1::text), now(), $2)`, old, disposition); err != nil {
					t.Fatal(err)
				}
			}
			view, err := evaluateSingle(NewReplacementDecisionService(db).EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "missing-new"}))
			if err != nil {
				t.Fatal(err)
			}
			pool := (*view.Pools)[0]
			if disposition == "" {
				if view.Verdict == "replace" {
					t.Fatalf("a present better file must keep the worse candidate out: %+v", view)
				}
				return
			}
			if view.Verdict != "replace" || (pool.Losers != nil && len(*pool.Losers) != 0) {
				t.Fatalf("%s placement must neither keep the key nor lose: %+v", disposition, pool)
			}
			if state := placementState(t, db, old); state != "active" {
				t.Fatalf("%s placement must stay active: %s", disposition, state)
			}
		})
	}
}

// A policy change has no immediate filesystem effect: switching all ->
// single_copy with two files of one episode and no new candidate creates no
// evaluation, decision, supersede or deletion; a policy-triggered evaluation
// of a placed release never compares or settles its placed keys.
func TestPGPolicySwitchWithoutNewCandidateDeletesNothing(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	setAdmissionMode(t, db, ModeAll)
	seedFilePlacement(t, db, "anime-1", "sub-1", "switch-old", []string{"tv:s1:e1"},
		Facts{Resolution: "720p", Group: "G"}, "2026-07-01T00:00:00Z")
	seedFilePlacement(t, db, "anime-1", "sub-1", "switch-new", []string{"tv:s1:e1"},
		Facts{Resolution: "1080p", Group: "G"}, "2026-08-01T00:00:00Z")
	store := cleanupWorkItemStore(t, db)
	trigger := NewReplacementTriggerService(db, store)
	var version int
	if err := db.QueryRow(`SELECT retention_version FROM anime_business_configs WHERE managed_anime_id = 'anime-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	policy := NewService(NewPolicyService(db), NewRetentionPolicyService(db), nil, nil, nil)
	policy.SetReplacementTrigger(trigger)
	if _, err := policy.SaveInstanceRetentionPolicy(ctx, "anime-1", generated.InstanceRetentionPolicyInput{
		AdmissionMode:   generated.RetentionAdmissionModeSingleCopy,
		QualityProfile:  generated.QualityProfile{Version: "profile-v1", Items: []generated.QualityCompareItem{{Key: "release.resolution", Ranks: [][]string{{"1080p"}, {"720p"}}}}},
		ExpectedVersion: version,
	}, "test", "http"); err != nil {
		t.Fatal(err)
	}
	if err := trigger.TriggerPolicySaved(ctx, "anime-1"); err != nil {
		t.Fatal(err)
	}
	if count := countLiveEvaluateItems(t, db, "anime-1"); count != 0 {
		t.Fatalf("a policy change without an unplaced candidate must not evaluate: %d items", count)
	}
	// A policy-triggered evaluation that names a placed release re-decides
	// nothing it placed.
	service := NewReplacementDecisionService(db)
	cleanup := NewCleanupService(db)
	cleanup.SetWorkItems(store)
	service.SetCleanupHandoff(cleanup)
	views, err := service.EvaluateApplication(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "switch-new", unplacedOnly: true})
	if err != nil || len(views) != 0 {
		t.Fatalf("unplaced-only evaluation of a placed release = %+v, %v", views, err)
	}
	input := json.RawMessage(`{"candidate_release_id":"switch-new","trigger":"policy_saved"}`)
	outcome, err := service.EvaluateHandler().Handle(ctx, workitems.Claim{
		ID: "policy-item", Kind: workitems.KindReplacementEvaluate,
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: "anime-1"},
		ExpectedOwnerVersion: 1, FencingToken: 1, Attempt: 1, MaxAttempts: 3,
		IdempotencyKey: "replacement_evaluate:anime-1", InputSnapshot: input,
	})
	if err != nil || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("policy evaluate outcome=%+v err=%v", outcome, err)
	}
	_, cleanups, superseded := decisionCounts(t, db, "anime-1")
	var decisions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_decisions WHERE managed_anime_id = 'anime-1'`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 || cleanups != 0 || superseded != 0 {
		t.Fatalf("a policy change must have no effect: decisions=%d cleanups=%d superseded=%d", decisions, cleanups, superseded)
	}
}

// The pool and its losers come only from the subscription's own managed
// placements: another anime's placement of the same episode, and a released
// (unmanaged) file of this anime, are never members or losers.
func TestPGPoolHoldsOnlyTheAnimesManagedPlacements(t *testing.T) {
	db := openFrozenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	setAdmissionMode(t, db, ModeSingleCopy)
	seedAnimeSubscription(t, db, "anime-2", "sub-2", "active")
	seedInstanceRetention(t, db, "anime-2", ModeSingleCopy, "", 1)
	foreign := seedFilePlacement(t, db, "anime-2", "sub-2", "foreign-old", []string{"tv:s1:e1"},
		Facts{Resolution: "480p", Group: "G"}, "2026-07-01T00:00:00Z")
	released := seedFilePlacement(t, db, "anime-1", "sub-1", "released-old", []string{"tv:s1:e1"},
		Facts{Resolution: "480p", Group: "G"}, "2026-07-02T00:00:00Z")
	if _, err := db.Exec(`UPDATE placements SET state = 'released', released_at = now(), released_by = 'test', release_request_key = 'test-released' WHERE id = $1`, released); err != nil {
		t.Fatal(err)
	}
	seedFilePlacement(t, db, "anime-1", "sub-1", "own-new", []string{"tv:s1:e1"},
		Facts{Resolution: "1080p", Group: "G"}, "2026-08-01T00:00:00Z")
	view, err := evaluateSingle(NewReplacementDecisionService(db).EvaluateServer(ctx, EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "own-new"}))
	if err != nil {
		t.Fatal(err)
	}
	if losers := loserIDs(decisionPools(t, db, "own-new")); len(losers) != 0 {
		t.Fatalf("only the anime's managed placements may lose (foreign %s, released %s): %v / %+v", foreign, released, losers, view)
	}
}
