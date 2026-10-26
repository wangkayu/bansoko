package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

func openReplacementTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	ctx := context.Background()
	// The owner tests start from the pre-migration state: no stored retention
	// default template (so the create with expected version 0 and the
	// absent-default code-default path stay exercised), and only the ManagedAnime
	// identities the tests use, so every other id still reports absent.
	if _, err := db.ExecContext(ctx,
		`DELETE FROM global_config_sections WHERE section_name = 'replacement'`); err != nil {
		t.Fatal(err)
	}
	for _, animeID := range []string{"anime-1", "anime-a", "anime-b", "anime-c"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Replacement')`, animeID); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func replacementPolicy(t *testing.T, mode AdmissionMode, profile string) ReplacementPolicy {
	t.Helper()
	return ReplacementPolicy{
		AdmissionMode:  mode,
		QualityProfile: json.RawMessage(profile),
	}
}

func expectPolicyInvalid(t *testing.T, err error) {
	t.Helper()
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 422 || business.Code != "POLICY_INVALID" {
		t.Fatalf("expected 422 POLICY_INVALID, got %v", err)
	}
}

func TestDefaultReplacementPolicyAndDigest(t *testing.T) {
	defaults := DefaultReplacementPolicy()
	if err := ValidateReplacementPolicy(defaults); err != nil {
		t.Fatalf("code defaults must validate: %v", err)
	}
	if defaults.AdmissionMode != ModeAll {
		t.Fatalf("default admission mode must be all, got %s", defaults.AdmissionMode)
	}
	if defaults.Digest() != defaults.Digest() {
		t.Fatal("digest must be stable")
	}
	changed := defaults
	changed.AdmissionMode = ModeSingleCopy
	if changed.Digest() == defaults.Digest() {
		t.Fatal("digest must change with the policy")
	}
}

func TestSaveDefaultCASAndValidation(t *testing.T) {
	db := openReplacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	svc := NewReplacementPolicyService(db)

	policy := replacementPolicy(t, ModePerGroup,
		`{"version":"profile-v1","items":[{"key":"release.revision","ranks":[]},{"key":"release.resolution","ranks":[["1080p"],["720p"]]}]}`)
	saved, err := svc.SaveDefault(ctx, 0, policy, "owner", "web")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 {
		t.Fatalf("expected version 1, got %d", saved.Version)
	}

	// Stale expected version must be the typed 409 conflict.
	_, err = svc.SaveDefault(ctx, 0, policy, "owner", "web")
	var conflict *apperrors.VersionConflict
	if !errors.As(err, &conflict) || conflict.CurrentVersion != 1 {
		t.Fatalf("expected version conflict with current 1, got %v", err)
	}

	// Invalid admission mode is rejected without a version change.
	_, err = svc.SaveDefault(ctx, 1, replacementPolicy(t, "everyone", `{"version":"profile-v1","items":[]}`), "owner", "web")
	expectPolicyInvalid(t, err)

	// Incomplete/wrongly typed profile JSON is rejected.
	_, err = svc.SaveDefault(ctx, 1, replacementPolicy(t, ModeAll, `{"dimensions":"not-an-array"}`), "owner", "web")
	expectPolicyInvalid(t, err)
	_, err = svc.SaveDefault(ctx, 1, replacementPolicy(t, ModeAll, `{"version":"profile-v1","items":[],"cutoff":{"x":1}}`), "owner", "web")
	expectPolicyInvalid(t, err)

	// A profile-v1 document and a duplicate value set are rejected.
	_, err = svc.SaveDefault(ctx, 1, replacementPolicy(t, ModeAll, `{"version":"profile-v0","dimensions":["resolution"],"ranks":{},"codec_enabled":false}`), "owner", "web")
	expectPolicyInvalid(t, err)
	_, err = svc.SaveDefault(ctx, 1, replacementPolicy(t, ModeAll, `{"version":"profile-v1","items":[{"key":"anibt.language","ranks":[["CHS","CHT"],["cht","chs"]]}]}`), "owner", "web")
	expectPolicyInvalid(t, err)

	// Correct CAS succeeds and bumps the version exactly once.
	next := replacementPolicy(t, ModeSingleCopy, `{"version":"profile-v1","items":[{"key":"rt.source","ranks":[["WEB-DL"]]}]}`)
	updated, err := svc.SaveDefault(ctx, 1, next, "owner", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 {
		t.Fatalf("expected version 2, got %d", updated.Version)
	}
	row, err := svc.GetDefault(ctx)
	if err != nil || row == nil {
		t.Fatalf("read saved default: row=%v err=%v", row, err)
	}
	if row.Version != 2 || row.Policy.AdmissionMode != ModeSingleCopy || row.Policy.Digest() != next.Digest() {
		t.Fatalf("saved default mismatch: %+v", row)
	}
}

// TestGetInstanceAbsentReportsNil proves the documented "(nil when absent)"
// contract: a missing instance row must not leak the raw sql.ErrNoRows to the
// transport boundary (where it degraded into an unknown 500).
func TestGetInstanceAbsentReportsNil(t *testing.T) {
	db := openReplacementTestDB(t)
	defer db.Close()
	svc := NewReplacementPolicyService(db)
	row, err := svc.GetInstance(context.Background(), "absent-anime")
	if err != nil {
		t.Fatalf("absent instance must report nil, got err=%v", err)
	}
	if row != nil {
		t.Fatalf("absent instance must report nil, got row=%+v", row)
	}
}

func TestCopyDefaultToInstanceTx(t *testing.T) {
	db := openReplacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	svc := NewReplacementPolicyService(db)

	policy := replacementPolicy(t, ModePerGroup,
		`{"version":"profile-v1","items":[{"key":"release.revision","ranks":[]},{"key":"release.resolution","ranks":[]}]}`)
	if _, err := svc.SaveDefault(ctx, 0, policy, "owner", "web"); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CopyDefaultToInstanceTx(ctx, txscope.Wrap(tx), "anime-1"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	row, err := svc.GetInstance(ctx, "anime-1")
	if err != nil || row == nil {
		t.Fatalf("read instance policy: row=%v err=%v", row, err)
	}
	if row.Version != 1 || row.SourceDefaultVersion != 1 {
		t.Fatalf("expected version 1 from default 1, got %+v", row)
	}
	if row.Policy.AdmissionMode != ModePerGroup || row.Policy.Digest() != policy.Digest() {
		t.Fatalf("instance must copy the complete default: %+v", row.Policy)
	}
	// The fields must be stored complete (no NULL=inherit).
	var nulls int
	if err := db.QueryRowContext(ctx, `
SELECT (admission_mode IS NULL)::int + (quality_profile IS NULL)::int
FROM anime_business_configs WHERE managed_anime_id = 'anime-1'`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 0 {
		t.Fatalf("instance policy must have no inherited fields, got %d nulls", nulls)
	}
}

func TestCopyDefaultToInstanceTxWithoutDefaultUsesCodeDefaults(t *testing.T) {
	db := openReplacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	svc := NewReplacementPolicyService(db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CopyDefaultToInstanceTx(ctx, txscope.Wrap(tx), "anime-1"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	row, err := svc.GetInstance(ctx, "anime-1")
	if err != nil || row == nil {
		t.Fatalf("read instance policy: row=%v err=%v", row, err)
	}
	defaults := DefaultReplacementPolicy()
	if row.Policy.Digest() != defaults.Digest() || row.Version != 1 || row.SourceDefaultVersion != 1 {
		t.Fatalf("absent default must fall back to versioned code defaults: %+v", row)
	}
	// The singleton was initialized so provenance stays truthful.
	def, err := svc.GetDefault(ctx)
	if err != nil || def == nil || def.Version != 1 {
		t.Fatalf("singleton default must be initialized: def=%v err=%v", def, err)
	}
}

func TestSaveInstanceCAS(t *testing.T) {
	db := openReplacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	svc := NewReplacementPolicyService(db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CopyDefaultToInstanceTx(ctx, txscope.Wrap(tx), "anime-1"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	replacement := replacementPolicy(t, ModeAll, `{"version":"profile-v1","items":[{"key":"rt.source","ranks":[["WEB-DL"]]}]}`)
	saved, err := svc.SaveInstance(ctx, "anime-1", 1, replacement, "owner", "web")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != 2 || saved.Policy.Digest() != replacement.Digest() {
		t.Fatalf("expected version 2 with the replacement, got %+v", saved)
	}

	// Stale CAS conflicts and must not partially write any field.
	_, err = svc.SaveInstance(ctx, "anime-1", 1, replacementPolicy(t, ModePerGroup, `{"version":"profile-v1","items":[]}`), "owner", "web")
	var conflict *apperrors.VersionConflict
	if !errors.As(err, &conflict) || conflict.CurrentVersion != 2 {
		t.Fatalf("expected version conflict with current 2, got %v", err)
	}
	row, err := svc.GetInstance(ctx, "anime-1")
	if err != nil {
		t.Fatal(err)
	}
	if row.Version != 2 || row.Policy.Digest() != replacement.Digest() || row.Policy.AdmissionMode != ModeAll {
		t.Fatalf("conflict must leave the instance untouched: %+v", row)
	}

	// Unknown instance is a typed 404.
	_, err = svc.SaveInstance(ctx, "anime-missing", 0, replacement, "owner", "web")
	var notFound *apperrors.NotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("expected typed not found, got %v", err)
	}
}

func TestSaveInstanceProducesNoEffects(t *testing.T) {
	db := openReplacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	svc := NewReplacementPolicyService(db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CopyDefaultToInstanceTx(ctx, txscope.Wrap(tx), "anime-1"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	type effectTotals struct{ placements, decisions, deletions, workItems int }
	counts := func() effectTotals {
		t.Helper()
		var totals effectTotals
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM placements`).Scan(&totals.placements); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM replacement_decisions`).Scan(&totals.decisions); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM replacement_deletions`).Scan(&totals.deletions); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history`).Scan(&totals.workItems); err != nil {
			t.Fatal(err)
		}
		return totals
	}
	before := counts()
	if before != (effectTotals{}) {
		t.Fatalf("fixture must start empty, got %+v", before)
	}
	for version := int64(1); version <= 3; version++ {
		if _, err := svc.SaveInstance(ctx, "anime-1", version, replacementPolicy(t, ModePerGroup, `{"version":"profile-v1","items":[{"key":"release.revision","ranks":[]}]}`), "owner", "web"); err != nil {
			t.Fatal(err)
		}
	}
	if after := counts(); after != before {
		t.Fatalf("instance saves must not touch Placement/Cleanup/Decision/WorkItem owners: before=%+v after=%+v", before, after)
	}
}

func TestGlobalDefaultChangeLeavesInstancesUntouched(t *testing.T) {
	db := openReplacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	svc := NewReplacementPolicyService(db)

	first := replacementPolicy(t, ModeAll, `{"version":"profile-v1","items":[{"key":"release.revision","ranks":[]},{"key":"release.resolution","ranks":[]}]}`)
	if _, err := svc.SaveDefault(ctx, 0, first, "owner", "web"); err != nil {
		t.Fatal(err)
	}
	for _, animeID := range []string{"anime-a", "anime-b"} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.CopyDefaultToInstanceTx(ctx, txscope.Wrap(tx), animeID); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	beforeA, err := svc.GetInstance(ctx, "anime-a")
	if err != nil {
		t.Fatal(err)
	}

	// Change the global default: future instances only.
	second := replacementPolicy(t, ModeSingleCopy, `{"version":"profile-v1","items":[{"key":"rt.source","ranks":[]},{"key":"subgroup.score","ranks":[]}]}`)
	if _, err := svc.SaveDefault(ctx, 1, second, "owner", "web"); err != nil {
		t.Fatal(err)
	}

	// The existing instance keeps value, version, and digest.
	afterA, err := svc.GetInstance(ctx, "anime-a")
	if err != nil {
		t.Fatal(err)
	}
	if afterA.Version != beforeA.Version || afterA.Policy.Digest() != beforeA.Policy.Digest() || afterA.Policy.AdmissionMode != ModeAll {
		t.Fatalf("global change must not re-interpret existing instances: before=%+v after=%+v", beforeA, afterA)
	}

	// A new instance copies the new default.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CopyDefaultToInstanceTx(ctx, txscope.Wrap(tx), "anime-c"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	instanceC, err := svc.GetInstance(ctx, "anime-c")
	if err != nil {
		t.Fatal(err)
	}
	if instanceC.Version != 1 || instanceC.SourceDefaultVersion != 2 || instanceC.Policy.Digest() != second.Digest() {
		t.Fatalf("new instance must copy the new default: %+v", instanceC)
	}
	// anime-b is also untouched.
	afterB, err := svc.GetInstance(ctx, "anime-b")
	if err != nil {
		t.Fatal(err)
	}
	if afterB.Version != 1 || afterB.Policy.Digest() != first.Digest() {
		t.Fatalf("existing instance b must be untouched: %+v", afterB)
	}

	// No ReplacementDecision or WorkItem anywhere.
	var decisions, workItems int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM replacement_decisions`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history`).Scan(&workItems); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 || workItems != 0 {
		t.Fatalf("default/instance writes must create no decisions or work items: decisions=%d work_items=%d", decisions, workItems)
	}
}

func TestReplacementWritesLeaveOtherRulesPolicyUntouched(t *testing.T) {
	db := openReplacementTestDB(t)
	defer db.Close()
	ctx := context.Background()
	svc := NewReplacementPolicyService(db)
	policies := NewPolicyService(db)

	// The other Rules keep their global + anime sparse override rows.
	if _, err := policies.SavePolicy(ctx, GlobalScope, 0, PolicyRow{Enabled: true, Safety: json.RawMessage(`{"max_operations":100}`)}, "owner", "web"); err != nil {
		t.Fatal(err)
	}
	interval := 3600
	if _, err := policies.SavePolicy(ctx, "anime:anime-1", 0, PolicyRow{Enabled: true, ScanIntervalSeconds: &interval, Safety: json.RawMessage(`{"max_operations":100}`)}, "owner", "web"); err != nil {
		t.Fatal(err)
	}
	var rowsBefore int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM filemaintenance_policies`).Scan(&rowsBefore); err != nil {
		t.Fatal(err)
	}

	// Replacement default and instance writes must not add or rewrite those rows.
	if _, err := svc.SaveDefault(ctx, 0, DefaultReplacementPolicy(), "owner", "web"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CopyDefaultToInstanceTx(ctx, txscope.Wrap(tx), "anime-1"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveInstance(ctx, "anime-1", 1, DefaultReplacementPolicy(), "owner", "web"); err != nil {
		t.Fatal(err)
	}
	var rowsAfter int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM filemaintenance_policies`).Scan(&rowsAfter); err != nil {
		t.Fatal(err)
	}
	if rowsAfter != rowsBefore {
		t.Fatalf("retention writes must not touch the other Rules owner: before=%d after=%d", rowsBefore, rowsAfter)
	}
	effective, err := policies.GetEffective(ctx, "anime-1")
	if err != nil {
		t.Fatal(err)
	}
	if effective.Source != "override" || effective.ScanIntervalSeconds == nil || *effective.ScanIntervalSeconds != 3600 {
		t.Fatalf("other Rules must still read global + sparse override: %+v", effective)
	}
}
