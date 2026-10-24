package migrations

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// I6: after retirement every effect entry fails closed even though the old
// child-row fences are gone. Work create is still refused for every owner kind
// that resolves a ManagedAnime owner chain.

func TestRetiredOwnerRejectsWorkCreateForEveryOwnerKind(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	seedCompletedFileOperation(t, db, "i6", "fop-i6")
	mustExecContext(t, db, `
UPDATE managed_anime
SET lifecycle_state='retired', fence_epoch=fence_epoch+1, retired_at=CURRENT_TIMESTAMP,
    retired_subscription_version=1, lifecycle_version=lifecycle_version+1
WHERE id='anime-i6'`)

	creates := []struct{ name, ownerType, kind, ownerID, idempotencyKey string }{
		{"rss_source_runtime", "rss_source_runtime", "rss_refresh", "src-i6", "rss_refresh:src-i6"},
		{"release", "release", "release_evaluate", "rel-i6", "release:rel-i6"},
		{"download_task", "download_task", "qb_submit", "dt-acq-i6", "dt-acq-i6"},
		{"artifact", "artifact", "artifact_inspect", "art-i6", "artifact:art-i6"},
		{"file_operation", "file_operation", "file_execute", "fop-i6", "fop-i6"},
		{"postprocessing_plan", "postprocessing_plan", "postprocess_plan", "plan-i6", "plan:plan-i6"},
		{"file_maintenance", "file_maintenance", "replacement_evaluate", "anime-i6", "repl:anime-i6"},
	}
	for _, tc := range creates {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.ExecContext(ctx, `
INSERT INTO work_items(id, owner_type, owner_id, kind, state, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ($1,$2,$3,$4,'available',$5,3,'{}',1)`,
				"wi-i6-"+tc.name, tc.ownerType, tc.ownerID, tc.kind, tc.idempotencyKey)
			assertMessage(t, err, "MANAGED_ANIME_RETIRED")
		})
	}
}

// I6: an available WorkItem that existed before retirement is never claimed.
func TestRetiredOwnerAvailableWorkIsNotClaimable(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, db, "i6claim")
	if _, err := db.ExecContext(ctx, `
INSERT INTO work_items(id, owner_type, owner_id, kind, state, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-i6claim', 'file_maintenance', 'anime-i6claim', 'replacement_evaluate', 'available', 'repl:anime-i6claim', 3, '{}', 1)`); err != nil {
		t.Fatalf("seed available work: %v", err)
	}
	mustExecContext(t, db, `
UPDATE managed_anime
SET lifecycle_state='retired', fence_epoch=fence_epoch+1, retired_at=CURRENT_TIMESTAMP,
    retired_subscription_version=1, lifecycle_version=lifecycle_version+1
WHERE id='anime-i6claim'`)

	var claimed int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_claim_due(60, ARRAY['replacement_evaluate'])`).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	if claimed != 0 {
		t.Fatalf("retired owner's available work was claimed: %d", claimed)
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM work_items WHERE id='wi-i6claim'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "available" {
		t.Fatalf("work item state = %s, want available", state)
	}
}

// I6: a leased WorkItem whose owner retires before admission cannot be admitted
// to an external effect.
func TestRetiredOwnerLeasedWorkAdmissionRejected(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, db, "i6lease")
	if _, err := db.ExecContext(ctx, `
INSERT INTO work_items(id, owner_type, owner_id, kind, state, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-i6lease', 'rss_source_runtime', 'src-i6lease', 'rss_refresh', 'available', 'rss_refresh:src-i6lease', 3, '{}', 1)`); err != nil {
		t.Fatalf("seed available work: %v", err)
	}
	var token int64
	if err := db.QueryRowContext(ctx, `SELECT fencing_token FROM work_item_claim_due(60, ARRAY['rss_refresh'])`).Scan(&token); err != nil {
		t.Fatalf("claim seeded work: %v", err)
	}
	mustExecContext(t, db, `
UPDATE managed_anime
SET lifecycle_state='retired', fence_epoch=fence_epoch+1, retired_at=CURRENT_TIMESTAMP,
    retired_subscription_version=1, lifecycle_version=lifecycle_version+1
WHERE id='anime-i6lease'`)

	var admitted int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_admit_managed_effect(false, 'wi-i6lease', $1)`, token).Scan(&admitted); err != nil {
		t.Fatal(err)
	}
	if admitted != 0 {
		t.Fatalf("retired owner's leased work was admitted: %d", admitted)
	}
	var admittedAt *string
	if err := db.QueryRowContext(ctx, `SELECT external_effect_admitted_at::text FROM work_item_history WHERE id='wi-i6lease'`).Scan(&admittedAt); err != nil {
		t.Fatal(err)
	}
	if admittedAt != nil {
		t.Fatalf("external effect admitted for a retired owner: %s", *admittedAt)
	}
}

// I6: an accept that carries the lifecycle version observed before retirement is
// refused with the qualification-management-changed boundary.
func TestRetiredOwnerAcceptRejectsStaleLifecycleVersion(t *testing.T) {
	db := installedDB(t)
	seedAcceptableRelease(t, db, "i6acc")
	mustExecContext(t, db, `
UPDATE managed_anime
SET lifecycle_state='retired', fence_epoch=fence_epoch+1, retired_at=CURRENT_TIMESTAMP,
    retired_subscription_version=1, lifecycle_version=lifecycle_version+1
WHERE id='anime-i6acc'`)

	_, err := callQueuedAccept(t, db, validQueued(t, "acq-i6acc", "key-i6acc", "i6acc"))
	if err == nil || !strings.Contains(err.Error(), "ACQ_QUALIFICATION_MANAGEMENT_CHANGED") {
		t.Fatalf("stale lifecycle accept = %v, want ACQ_QUALIFICATION_MANAGEMENT_CHANGED", err)
	}
}

// I6: after physical cleanup the owner row is gone, so every child insert fails
// on the foreign key (23503), and a retired owner still cannot be deleted
// without the cleanup flag.
func TestCleanedOwnerChildWritesFailOnForeignKey(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()

	retireAnime(t, db, "anime-i6fkdelete")
	assertMessage(t, func() error {
		_, err := db.ExecContext(ctx, `DELETE FROM managed_anime WHERE id='anime-i6fkdelete'`)
		return err
	}(), "MANAGED_ANIME_DELETE_FORBIDDEN")

	retireAnime(t, db, "anime-i6fk")
	epoch := retireEpoch(t, db, "anime-i6fk")
	if _, err := runLocalCleanup(ctx, db, "anime-i6fk", "cleanup-i6fk-key-0001", "sha256:"+strings.Repeat("a", 64), epoch); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	var ownerRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_anime WHERE id='anime-i6fk'`).Scan(&ownerRows); err != nil {
		t.Fatal(err)
	}
	if ownerRows != 0 {
		t.Fatalf("cleanup left the owner row: %d", ownerRows)
	}

	assertForeignKeyViolation(t, db, `INSERT INTO anime_metadata(managed_anime_id) VALUES ('anime-i6fk')`)
	assertForeignKeyViolation(t, db, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-i6fk-late', 'anime-i6fk', 'custom', 'late', 'active')`)
}

func mustExecContext(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
