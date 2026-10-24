package persistence

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

const zeroHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

type maintenancePurge func(*Repository, context.Context, txscope.Tx, int) (int64, error)

func runMaintenancePurge(t *testing.T, db *sql.DB, purge maintenancePurge, batchSize int) int64 {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin purge transaction: %v", err)
	}
	defer tx.Rollback()
	deleted, err := purge(NewRepository(db), context.Background(), txscope.Wrap(tx), batchSize)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit purge transaction: %v", err)
	}
	return deleted
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return count
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func seedManagedAnime(t *testing.T, db *sql.DB, anime string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Maintenance Show ' || $1)`, anime)
}

// TestPurgeExpiredIdempotencyRecords: every kind's replay record goes after 30
// days, a younger one stays, and one call deletes at most the batch, oldest
// first.
func TestPurgeExpiredIdempotencyRecords(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	owner := disposabledb.Owner(t)
	seedManagedAnime(t, owner, "anime-idem")
	insert := func(kind, key, age string) {
		t.Helper()
		switch kind {
		case "business_apply", "metadata_match":
			mustExec(t, owner, `
INSERT INTO idempotency_records(kind, idempotency_key, request_hash, managed_anime_id, result_snapshot, created_at)
VALUES ($1, $2, $3, 'anime-idem', '{}', CURRENT_TIMESTAMP - $4::interval)`, kind, key, zeroHash, age)
		case "local_cleanup":
			mustExec(t, owner, `
INSERT INTO idempotency_records(kind, idempotency_key, request_hash, owner_type, owner_id, result_snapshot, created_at)
VALUES ('local_cleanup', $1, $2, 'managed_anime', 'anime-gone', '{}', CURRENT_TIMESTAMP - $3::interval)`, key, zeroHash, age)
		default:
			t.Fatalf("unsupported kind %s", kind)
		}
	}
	insert("business_apply", "apply-oldest", "40 days")
	insert("local_cleanup", "cleanup-old", "35 days")
	insert("metadata_match", "match-old", "31 days")
	insert("business_apply", "apply-young", "29 days")

	if deleted := runMaintenancePurge(t, db, (*Repository).PurgeExpiredIdempotencyRecords, 2); deleted != 2 {
		t.Fatalf("first batch deleted %d, want 2", deleted)
	}
	if n := countRows(t, owner, `SELECT count(*) FROM idempotency_records WHERE idempotency_key IN ('apply-oldest', 'cleanup-old')`); n != 0 {
		t.Fatalf("the two oldest records must go first, %d left", n)
	}
	if n := countRows(t, owner, `SELECT count(*) FROM idempotency_records WHERE idempotency_key = 'match-old'`); n != 1 {
		t.Fatal("a record beyond the batch limit was deleted")
	}
	if deleted := runMaintenancePurge(t, db, (*Repository).PurgeExpiredIdempotencyRecords, 500); deleted != 1 {
		t.Fatalf("second batch deleted %d, want 1", deleted)
	}
	if n := countRows(t, owner, `SELECT count(*) FROM idempotency_records`); n != 1 {
		t.Fatalf("only the record inside the window stays, %d left", n)
	}
	if n := countRows(t, owner, `SELECT count(*) FROM idempotency_records WHERE idempotency_key = 'apply-young'`); n != 1 {
		t.Fatal("a record inside the replay window was deleted")
	}
}

// TestPurgeExpiredNotificationAdmissions: only settled admissions older than 90
// days go; a pending admission is live resolver work at any age and a settled
// one inside the window stays as the dedupe ledger.
func TestPurgeExpiredNotificationAdmissions(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	owner := disposabledb.Owner(t)
	insert := func(id, state, age string) {
		t.Helper()
		mustExec(t, owner, `
INSERT INTO notification_admissions(id, trigger_kind, source_owner_type, source_owner_id, source_owner_version,
    result_key, occurred_at, event_catalog_version, event_template_catalog_version, payload, state, created_at)
VALUES ($1, 'release_discovered', 'release', 'release-' || $1, 1, 'anime:a', CURRENT_TIMESTAMP - $3::interval,
    'v1', 'v1', '{}', $2, CURRENT_TIMESTAMP - $3::interval)`, id, state, age)
	}
	insert("resolved-old", "resolved", "100 days")
	insert("failed-old", "admission_failed", "95 days")
	insert("resolved-older", "resolved", "120 days")
	insert("pending-old", "pending_target_resolution", "200 days")
	insert("resolved-window", "resolved", "60 days")

	if deleted := runMaintenancePurge(t, db, (*Repository).PurgeExpiredNotificationAdmissions, 2); deleted != 2 {
		t.Fatalf("first batch deleted %d, want 2", deleted)
	}
	if n := countRows(t, owner, `SELECT count(*) FROM notification_admissions WHERE id IN ('resolved-older', 'resolved-old')`); n != 0 {
		t.Fatalf("the two oldest settled admissions must go first, %d left", n)
	}
	if deleted := runMaintenancePurge(t, db, (*Repository).PurgeExpiredNotificationAdmissions, 500); deleted != 1 {
		t.Fatalf("second batch deleted %d, want 1", deleted)
	}
	for _, kept := range []string{"pending-old", "resolved-window"} {
		if n := countRows(t, owner, `SELECT count(*) FROM notification_admissions WHERE id = $1`, kept); n != 1 {
			t.Fatalf("admission %s must be kept", kept)
		}
	}
	if n := countRows(t, owner, `SELECT count(*) FROM notification_admissions`); n != 2 {
		t.Fatalf("admissions left %d, want 2", n)
	}
}

func seedScan(t *testing.T, db *sql.DB, anime string, generation int64) {
	t.Helper()
	mustExec(t, db, `
INSERT INTO reconciliation_scans(managed_anime_id, state, generation, scan_scope, progress, capacity_stats, input_fingerprint, completed_at)
VALUES ($1, 'completed', $2, '{}', '{}', '{}', 'fingerprint', CURRENT_TIMESTAMP)`, anime, generation)
}

func seedAlignment(t *testing.T, db *sql.DB, id, anime string, generation int64, state, completedAge string) {
	t.Helper()
	mustExec(t, db, `
INSERT INTO alignment_operations(id, idempotency_key, managed_anime_id, scan_generation, observation_ref, candidate_ref,
    policy_version, metadata_version, rules_version, naming_version, source_path, source_fingerprint, target_path,
    target_contract, dry_run_result, state, completed_at)
VALUES ($1, 'align:' || $1, $2, $3::bigint, $2 || ':' || $3::bigint::text || ':/library/' || $1, 'candidate', 1, 1, 1, 1,
    '/library/' || $1 || '.mkv', 'sha256:source', '/library/target-' || $1 || '.mkv', '{}', '{}', $4,
    CURRENT_TIMESTAMP - NULLIF($5, '')::interval)`, id, anime, generation, state, completedAge)
}

// TestPurgeExpiredAlignmentOperations: a settled alignment of a superseded
// generation goes after 30 days. The current generation, an open alignment, a
// recent one and one still holding a WorkItem (live or retained outcome) stay.
func TestPurgeExpiredAlignmentOperations(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	owner := disposabledb.Owner(t)
	seedManagedAnime(t, owner, "anime-align")
	seedScan(t, owner, "anime-align", 3)

	seedAlignment(t, owner, "align-completed-old", "anime-align", 1, "completed", "40 days")
	seedAlignment(t, owner, "align-blocked-old", "anime-align", 2, "blocked", "35 days")
	seedAlignment(t, owner, "align-canceled-old", "anime-align", 2, "canceled", "31 days")
	seedAlignment(t, owner, "align-current-gen", "anime-align", 3, "completed", "40 days")
	seedAlignment(t, owner, "align-recent", "anime-align", 1, "failed", "5 days")
	seedAlignment(t, owner, "align-ready", "anime-align", 1, "ready", "")
	seedAlignment(t, owner, "align-live-item", "anime-align", 1, "completed", "45 days")
	mustExec(t, owner, `
INSERT INTO work_items(id, owner_type, owner_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-align-live', 'alignment_operation', 'align-live-item', 'alignment_execute', 'align:execute:align-live-item', 3, '{}', 1)`)
	seedAlignment(t, owner, "align-outcome", "anime-align", 1, "completed", "50 days")
	mustExec(t, owner, `
INSERT INTO work_item_outcomes(id, owner_type, owner_id, kind, state, idempotency_key, max_attempts, input_snapshot,
    expected_owner_version, retention_days, terminal_at, retention_until)
VALUES ('wio-align', 'alignment_operation', 'align-outcome', 'alignment_execute', 'succeeded', 'align:execute:align-outcome',
    3, '{}', 1, 7, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '7 days')`)

	if deleted := runMaintenancePurge(t, db, (*Repository).PurgeExpiredAlignmentOperations, 2); deleted != 2 {
		t.Fatalf("first batch deleted %d, want 2", deleted)
	}
	if n := countRows(t, owner, `SELECT count(*) FROM alignment_operations WHERE id IN ('align-completed-old', 'align-blocked-old')`); n != 0 {
		t.Fatalf("the two oldest eligible alignments must go first, %d left", n)
	}
	if deleted := runMaintenancePurge(t, db, (*Repository).PurgeExpiredAlignmentOperations, 500); deleted != 1 {
		t.Fatalf("second batch deleted %d, want 1", deleted)
	}
	for _, kept := range []string{"align-current-gen", "align-recent", "align-ready", "align-live-item", "align-outcome"} {
		if n := countRows(t, owner, `SELECT count(*) FROM alignment_operations WHERE id = $1`, kept); n != 1 {
			t.Fatalf("alignment %s must be kept", kept)
		}
	}
}

func seedObservation(t *testing.T, db *sql.DB, anime string, generation int64, path string, derivedMissing, calibrated bool, age string) {
	t.Helper()
	if derivedMissing {
		mustExec(t, db, `
INSERT INTO reconciliation_files(managed_anime_id, generation, file_path, derived_missing, fingerprint, size_bytes, disposition, created_at)
VALUES ($1, $2, $3, TRUE, 'sha256:file', 1, 'missing', CURRENT_TIMESTAMP - $4::interval)`, anime, generation, path, age)
		return
	}
	mustExec(t, db, `
INSERT INTO reconciliation_files(managed_anime_id, generation, file_path, fingerprint, size_bytes, observed_at, disposition, created_at)
VALUES ($1, $2, $3, 'sha256:file', 1, CURRENT_TIMESTAMP, CASE WHEN $4 THEN 'present' ELSE NULL END, CURRENT_TIMESTAMP - $5::interval)`,
		anime, generation, path, calibrated, age)
}

// TestPurgeSupersededReconciliationFiles: old generations go; the current
// generation, the newest earlier observed generation and the newest calibrated
// generation stay, as does everything of an anime with file-maintenance work
// or an open alignment in flight, and rows younger than 30 days.
func TestPurgeSupersededReconciliationFiles(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	owner := disposabledb.Owner(t)

	// anime-recon: current generation 5 (running, not yet calibrated); 4 is the
	// previous observed generation; 3 is the newest calibrated one; 1 and 2 go.
	seedManagedAnime(t, owner, "anime-recon")
	seedScan(t, owner, "anime-recon", 5)
	seedObservation(t, owner, "anime-recon", 1, "/a/1.mkv", false, true, "90 days")
	seedObservation(t, owner, "anime-recon", 1, "/a/1b.mkv", true, false, "90 days")
	seedObservation(t, owner, "anime-recon", 2, "/a/2.mkv", false, true, "60 days")
	seedObservation(t, owner, "anime-recon", 3, "/a/3.mkv", false, true, "45 days")
	seedObservation(t, owner, "anime-recon", 4, "/a/4.mkv", false, false, "40 days")
	seedObservation(t, owner, "anime-recon", 5, "/a/5.mkv", false, false, "35 days")

	// anime-young: generation 1 is superseded but younger than 30 days.
	seedManagedAnime(t, owner, "anime-young")
	seedScan(t, owner, "anime-young", 3)
	seedObservation(t, owner, "anime-young", 1, "/y/1.mkv", false, true, "10 days")
	seedObservation(t, owner, "anime-young", 2, "/y/2.mkv", false, true, "5 days")
	seedObservation(t, owner, "anime-young", 3, "/y/3.mkv", false, true, "1 day")

	// anime-busy: a live file-maintenance WorkItem holds every generation.
	seedManagedAnime(t, owner, "anime-busy")
	seedScan(t, owner, "anime-busy", 3)
	seedObservation(t, owner, "anime-busy", 1, "/b/1.mkv", false, true, "90 days")
	seedObservation(t, owner, "anime-busy", 3, "/b/3.mkv", false, true, "40 days")
	mustExec(t, owner, `
INSERT INTO work_items(id, owner_type, owner_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-busy', 'file_maintenance', 'anime-busy', 'reconciliation_calibrate', 'recon:calibrate:anime-busy:1', 3, '{}', 1)`)

	// anime-aligning: an open alignment holds every generation.
	seedManagedAnime(t, owner, "anime-aligning")
	seedScan(t, owner, "anime-aligning", 3)
	seedObservation(t, owner, "anime-aligning", 1, "/l/1.mkv", false, true, "90 days")
	seedObservation(t, owner, "anime-aligning", 3, "/l/3.mkv", false, true, "40 days")
	seedAlignment(t, owner, "align-open", "anime-aligning", 3, "planned", "")

	if deleted := runMaintenancePurge(t, db, (*Repository).PurgeSupersededReconciliationFiles, 2); deleted != 2 {
		t.Fatalf("first batch deleted %d, want 2", deleted)
	}
	if n := countRows(t, owner, `SELECT count(*) FROM reconciliation_files WHERE managed_anime_id = 'anime-recon' AND generation = 1`); n != 0 {
		t.Fatalf("the oldest generation must go first, %d rows left", n)
	}
	if deleted := runMaintenancePurge(t, db, (*Repository).PurgeSupersededReconciliationFiles, 500); deleted != 1 {
		t.Fatalf("second batch deleted %d, want 1", deleted)
	}
	if n := countRows(t, owner, `SELECT count(*) FROM reconciliation_files WHERE managed_anime_id = 'anime-recon'`); n != 3 {
		t.Fatalf("generations 3, 4 and 5 must stay, %d rows left", n)
	}
	if n := countRows(t, owner, `SELECT count(*) FROM reconciliation_files WHERE managed_anime_id = 'anime-recon' AND generation < 3`); n != 0 {
		t.Fatalf("generations below the floor must be gone, %d rows left", n)
	}
	for anime, want := range map[string]int{"anime-young": 3, "anime-busy": 2, "anime-aligning": 2} {
		if n := countRows(t, owner, `SELECT count(*) FROM reconciliation_files WHERE managed_anime_id = $1`, anime); n != want {
			t.Fatalf("%s keeps %d rows, want %d", anime, n, want)
		}
	}
	// The scan owner row is never touched.
	if n := countRows(t, owner, `SELECT count(*) FROM reconciliation_scans`); n != 4 {
		t.Fatalf("scan owner rows %d, want 4", n)
	}
}
