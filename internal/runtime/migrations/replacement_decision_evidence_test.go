package migrations

import (
	"context"
	"testing"
)

// TestReplacementDecisionAdmittedEvidenceAndWorkItemShape proves the admitted
// decision evidence constraint, the file_maintenance work item owner shape
// (replacement_evaluate needs no scan row, reconciliation_scan does), and that
// frozen evidence persists with a NULL snapshot generation.
func TestReplacementDecisionAdmittedEvidenceAndWorkItemShape(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Verify(ctx); err != nil {
		t.Fatalf("verify installed schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-evidence', 'Evidence')`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `
	INSERT INTO replacement_decisions
	(id, idempotency_key, managed_anime_id, candidate_release_id, policy_version, profile_version,
	 candidate_facts_ref, snapshot_generation, coverage_key, coverage_keys, admission_mode, pools, verdict, state, version)
	VALUES ('dec-illegal-admitted', 'repl:illegal:1', 'anime-evidence', 'release-illegal', 1, 'profile-v1',
	 'release-illegal:facts', 1, 'unresolved', '[]', 'single_copy', '[]', 'replace', 'admitted', 1)`); err == nil {
		t.Fatal("incomplete admitted decision must be rejected")
	}

	// The file-maintenance context is not a scan lifecycle parent: a
	// pre-acquisition replacement item may precede any scan row, but it still
	// belongs to an existing ManagedAnime lifecycle owner.
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-no-scan', 'No Scan')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id, owner_type, owner_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version) VALUES ('wi-orphan', 'file_maintenance', 'anime-no-scan', 'replacement_evaluate', 'repl:wi:orphan', 3, '{}', 1)`); err != nil {
		t.Fatalf("missing scan context must remain legal: %v", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO reconciliation_scans(managed_anime_id, state, generation, scan_scope, progress, capacity_stats, input_fingerprint) VALUES ('anime-evidence', 'idle', 0, '{}', '{}', '{}', 'seed')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id, owner_type, owner_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version) VALUES ('wi-orphan-recon', 'file_maintenance', 'anime-no-scan', 'reconciliation_scan', 'repl:wi:orphan-recon', 3, '{}', 1)`); err == nil {
		t.Fatal("reconciliation_scan must reject an orphan file-maintenance owner")
	}

	// The replacement_evaluate shape branch exists and remains valid
	// without a reconciliation parent row.
	if _, err := db.ExecContext(ctx, `
INSERT INTO work_items(id, owner_type, owner_id, kind, idempotency_key,
    max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-evaluate', 'file_maintenance', 'anime-evidence', 'replacement_evaluate',
    'repl:wi:post', 3, '{}', 1)`); err != nil {
		t.Fatalf("replacement_evaluate work item must be legal: %v", err)
	}
	// The other shape branches still reject mismatched kinds.
	if _, err := db.ExecContext(ctx, `
INSERT INTO work_items(id, owner_type, owner_id, kind, idempotency_key,
    max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-evaluate-bad', 'file_maintenance', 'anime-evidence', 'cleanup_delete',
    'repl:wi:bad', 3, '{}', 1)`); err == nil {
		t.Fatal("file_maintenance owner must reject cleanup kinds")
	}

	if _, err := db.ExecContext(ctx, `
	INSERT INTO placements
	(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint, target_fingerprint, facts_version, verification_state, state, version)
	VALUES ('plc-new', '/library/new.mkv', 'anime-evidence', 'reconciliation', 'obs-new', 'source-fp', 'target-fp', 'parser-v1', 'verified', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO replacement_decisions
(id, idempotency_key, managed_anime_id, candidate_release_id, candidate_placement_id,
 policy_version, profile_version, subscription_version, input_digest, candidate_facts_ref,
 snapshot_generation, coverage_key, coverage_keys, admission_mode, pools, verdict, state, version, created_at, updated_at)
VALUES ('dec-frozen', 'repl:frozen:1', 'anime-evidence', 'release-new', 'plc-new', 1, 'profile-v1', 1,
 'sha256:abc', 'release-new:facts', NULL, 'tv:s1:e1', '["tv:s1:e1"]', 'single_copy', '[]', 'replace',
 'admitted', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("frozen decision must persist: %v", err)
	}
	var frozenCandidate, frozenDigest, frozenMode, frozenState string
	var frozenSubscription int64
	if err := db.QueryRowContext(ctx, `SELECT candidate_placement_id, subscription_version, input_digest, admission_mode, state FROM replacement_decisions WHERE id = 'dec-frozen'`).Scan(&frozenCandidate, &frozenSubscription, &frozenDigest, &frozenMode, &frozenState); err != nil {
		t.Fatal(err)
	}
	if frozenCandidate != "plc-new" || frozenSubscription != 1 || frozenDigest != "sha256:abc" || frozenMode != "single_copy" || frozenState != "admitted" {
		t.Fatalf("frozen decision evidence = %s/%d/%s/%s/%s", frozenCandidate, frozenSubscription, frozenDigest, frozenMode, frozenState)
	}
	var snapshotNull bool
	if err := db.QueryRowContext(ctx, `SELECT snapshot_generation IS NULL FROM replacement_decisions WHERE id = 'dec-frozen'`).Scan(&snapshotNull); err != nil {
		t.Fatal(err)
	}
	if !snapshotNull {
		t.Fatal("evaluate-driven decisions carry no snapshot generation")
	}
}

// TestReplacementDecisionFrozenEvidenceColumns proves the installed schema has
// the frozen-evidence columns and the state enum includes stale.
func TestReplacementDecisionFrozenEvidenceColumns(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	for _, column := range []string{"candidate_placement_id", "subscription_version", "input_digest", "stale_reason"} {
		var count int
		if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM information_schema.columns
WHERE table_schema = 'public' AND table_name = 'replacement_decisions' AND column_name = $1`, column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("frozen evidence column %s is missing", column)
		}
	}
	var staleAllowed bool
	if err := db.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE contype = 'c' AND conrelid = 'replacement_decisions'::regclass
      AND pg_get_constraintdef(oid) LIKE '%stale%'
)`).Scan(&staleAllowed); err != nil {
		t.Fatal(err)
	}
	if !staleAllowed {
		t.Fatal("state check must allow stale")
	}
}

// TestReplacementDecisionCandidateReleaseIntegrity proves an admitted
// pre-acquisition decision may only name its own candidate release as the
// synthetic winner; foreign or mixed synthetic winners are rejected.
func TestReplacementDecisionCandidateReleaseIntegrity(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-release-integrity', 'Release integrity')`); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO replacement_decisions
	(id, idempotency_key, managed_anime_id, candidate_release_id, candidate_placement_id,
	 policy_version, profile_version, subscription_version, input_digest, candidate_facts_ref,
	 snapshot_generation, coverage_key, coverage_keys, admission_mode, pools, verdict, state, version)
	VALUES ($1, $2, 'anime-release-integrity', $3, NULL, 1, 'profile-v1', 1, 'sha256:input',
	 'release:facts', NULL, 'tv:s1:e1', '["tv:s1:e1"]', 'single_copy', $4::jsonb, 'replace', 'admitted', 1)`

	validPools := `[{"pool_key":"tv:s1:e1","winner":{"placement_id":"release:rel-integrity","version":1}}]`
	if _, err := db.ExecContext(ctx, insert, "dec-valid", "key-valid", "rel-integrity", validPools); err != nil {
		t.Fatalf("matching candidate release must persist: %v", err)
	}
	// Non-synthetic winner references retain the existing pools schema semantics.
	if _, err := db.ExecContext(ctx, insert, "dec-nonsynthetic", "key-nonsynthetic", "rel-integrity", `[{"winner":{"placement_id":"placement-real"}},{"winner":{"placement_id":"release:rel-integrity"}}]`); err != nil {
		t.Fatalf("non-synthetic winner reference must retain existing semantics: %v", err)
	}
	for name, pools := range map[string]string{
		"mismatched synthetic candidate": `[{"winner":{"placement_id":"release:other-release"}}]`,
		"multiple synthetic candidates":  `[{"winner":{"placement_id":"release:rel-integrity"}},{"winner":{"placement_id":"release:other-release"}}]`,
	} {
		if _, err := db.ExecContext(ctx, insert, "dec-bad-"+name, "key-bad-"+name, "rel-integrity", pools); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}
