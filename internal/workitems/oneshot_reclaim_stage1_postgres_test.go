package workitems

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
)

func seedAdoptedReclaimMarker(t *testing.T, db *sql.DB, marker string, deadline time.Time) {
	t.Helper()
	insertOneShotMarker(t, db, marker, OneShotStateReclaimPending, &deadline)
	owner := disposabledb.Owner(t)
	execReclaimFixture(t, owner, `UPDATE one_shot_acquisitions SET download_task_id=NULL WHERE id=$1`, marker)
	execReclaimFixture(t, owner, `INSERT INTO managed_anime(id,title) VALUES ('anime-'||$1,'Adopted fixture')`, marker)
	execReclaimFixture(t, owner, `INSERT INTO subscriptions(id,managed_anime_id,origin,instance_label,state)
VALUES ('sub-'||$1,'anime-'||$1,'custom','adopted fixture','active')`, marker)
	execReclaimFixture(t, owner, `INSERT INTO rss_sources(id,subscription_id,source_kind,locator,locator_display,group_name,title)
VALUES ('src-'||$1,'sub-'||$1,'custom_rss','https://feeds.example.com/adopted.xml','https://feeds.example.com/adopted.xml','Adopted','Adopted fixture')`, marker)
	execReclaimFixture(t, owner, `INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,verdict,reason_code)
VALUES ('rel-'||$1,'src-'||$1,'adopted:'||$1,'{}','{}','adopted_existing','one_shot_fixture_adopted')`, marker)
}

func execReclaimFixture(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestReclaimBatchSkipsRetainedRootsBeforeLimit(t *testing.T) {
	db := openTestDB(t)
	owner := disposabledb.Owner(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.BatchSize = 1
	ctx := context.Background()
	now := time.Now()

	const taskOnly = "retained-by-task"
	files := createOneShotChain(t, db, taskOnly, []string{"task-only.mkv"})
	failOneShotFileOperation(t, db, "fo-"+taskOnly, "art-"+taskOnly, files[0], "/library/task-only.mkv")
	settleOneShotRoot(t, db, taskOnly)
	execReclaimFixture(t, owner, `UPDATE acquisitions SET origin_managed_anime_ref=NULL WHERE id=$1`, "acq-"+taskOnly)
	insertOneShotMarker(t, db, taskOnly, OneShotStateReclaimPending, oneShotReclaimAt(now.Add(-4*time.Hour)))

	const ownerOnly = "adopted-retained-by-owner"
	createOneShotChain(t, db, "running-root", []string{"running.mkv"})
	seedAdoptedReclaimMarker(t, db, ownerOnly, now.Add(-3*time.Hour))
	execReclaimFixture(t, owner, `UPDATE acquisitions SET origin_managed_anime_ref=$1 WHERE id='acq-running-root'`, "anime-"+ownerOnly)

	const ready = "adopted-due"
	seedAdoptedReclaimMarker(t, db, ready, now.Add(-time.Hour))
	ids, err := service.oneShotDueIDs(ctx, []string{OneShotStateReclaimPending}, true)
	if err != nil || !slices.Equal(ids, []string{ready}) {
		t.Fatalf("reclaim candidates = %v, err=%v, want [%s]", ids, err, ready)
	}
	result, err := service.RunPass(ctx)
	if err != nil || result != (OneShotReclaimResult{Reclaimed: 1, Purged: 1}) || !result.Changed() {
		t.Fatalf("reclaim pass = %+v, err=%v, want the due adopted marker reclaimed and purged", result, err)
	}
	for _, marker := range []string{taskOnly, ownerOnly} {
		if status, err := service.reclaimOne(ctx, marker); err != nil || status != reclaimWaiting {
			t.Fatalf("reclaim %s = %q, err=%v, want waiting", marker, status, err)
		}
	}
	if got := countOneShotOwnerGraphRows(t, db, ready); got != 0 {
		t.Fatalf("adopted marker owner graph has %d rows after reclaim", got)
	}
}

func TestReclaimPassRevalidatesMarkerDeadlineAfterLockWait(t *testing.T) {
	db := openTestDB(t)
	owner := disposabledb.Owner(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "reclaim-extended-during-lock"
	seedAdoptedReclaimMarker(t, db, marker, time.Now().Add(-time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var pid int
	if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	var version int64
	var deadline time.Time
	if err := tx.QueryRowContext(ctx, `UPDATE one_shot_acquisitions
SET retention_until=CURRENT_TIMESTAMP+INTERVAL '1 day', version=version+1, updated_at=CURRENT_TIMESTAMP
WHERE id=$1 RETURNING version, retention_until`, marker).Scan(&version, &deadline); err != nil {
		t.Fatal(err)
	}
	type passReply struct {
		result OneShotReclaimResult
		err    error
	}
	done := make(chan passReply, 1)
	go func() {
		result, err := service.RunPass(ctx)
		done <- passReply{result, err}
	}()
	for {
		var waiting bool
		if err := owner.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatalf("reclaim never waited for the marker lock: %v", err)
		}
		if waiting {
			break
		}
		select {
		case reply := <-done:
			t.Fatalf("reclaim pass returned before the marker lock was released: %+v", reply)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	reply := <-done
	if reply.err != nil || reply.result != (OneShotReclaimResult{}) || reply.result.Changed() {
		t.Fatalf("pass result = %+v, err=%v, want no committed change", reply.result, reply.err)
	}
	var state string
	var gotVersion int64
	var gotDeadline time.Time
	if err := db.QueryRowContext(ctx, `SELECT state,version,retention_until FROM one_shot_acquisitions WHERE id=$1`, marker).Scan(&state, &gotVersion, &gotDeadline); err != nil {
		t.Fatal(err)
	}
	if state != OneShotStateReclaimPending || gotVersion != version || !gotDeadline.Equal(deadline) {
		t.Fatalf("marker after race = %s/%d/%v, want reclaim_pending/%d/%v", state, gotVersion, gotDeadline, version, deadline)
	}
	if status, err := service.reclaimOne(ctx, "missing-marker"); err != nil || status != reclaimContended {
		t.Fatalf("missing marker = %q, err=%v, want contended", status, err)
	}
}

func TestRecheckKeepsUnclassifiedOwnerBlockedAndCapsDeadline(t *testing.T) {
	db := openTestDB(t)
	owner := disposabledb.Owner(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const marker = "blocked-owner-dependent"
	seedAdoptedReclaimMarker(t, db, marker, time.Now().Add(-time.Hour))
	execReclaimFixture(t, owner, `INSERT INTO alignment_operations(id,idempotency_key,managed_anime_id,scan_generation,observation_ref,candidate_ref,
    policy_version,metadata_version,rules_version,naming_version,source_path,source_fingerprint,target_path,target_contract,dry_run_result)
VALUES ('align-fixture','align:fixture',$1,1,$1||':1:/library/source','candidate',1,1,1,1,'/library/source.mkv','sha256:source','/library/target.mkv','{}','{}')`, "anime-"+marker)
	if status, err := service.reclaimOne(ctx, marker); err != nil || status != reclaimBlocked {
		t.Fatalf("reclamation = %q, err=%v, want blocked", status, err)
	}

	// The same dependent must prevent recheck from moving back to pending,
	// including after the hard cap makes extension a no-op.
	execReclaimFixture(t, db, `UPDATE one_shot_acquisitions
SET terminal_at=CURRENT_TIMESTAMP-INTERVAL '31 days', retention_until=CURRENT_TIMESTAMP-INTERVAL '2 days' WHERE id=$1`, marker)
	type markerSnapshot struct {
		state, outcome, code, reason     string
		version                          int64
		terminal, deadline, updated, cap time.Time
	}
	read := func() markerSnapshot {
		t.Helper()
		var m markerSnapshot
		if err := db.QueryRowContext(ctx, `SELECT state,terminal_outcome,terminal_reason_code,terminal_reason,version,terminal_at,retention_until,updated_at,
terminal_at+INTERVAL '30 days' FROM one_shot_acquisitions WHERE id=$1`, marker).Scan(&m.state, &m.outcome, &m.code, &m.reason, &m.version, &m.terminal, &m.deadline, &m.updated, &m.cap); err != nil {
			t.Fatal(err)
		}
		return m
	}
	assertPass := func(want OneShotReclaimResult) {
		t.Helper()
		if got, err := service.RunPass(ctx); err != nil || got != want || got.Changed() != want.Changed() {
			t.Fatalf("pass = %+v, err=%v, want %+v", got, err, want)
		}
	}
	before := read()
	assertPass(OneShotReclaimResult{Extended: 1})
	capped := read()
	if capped.state != OneShotStateCleanupBlocked || capped.version != before.version+1 || !capped.deadline.Equal(capped.cap) {
		t.Fatalf("marker after extension = %+v, want cleanup_blocked with one version bump and deadline at cap", capped)
	}
	assertPass(OneShotReclaimResult{})
	assertPass(OneShotReclaimResult{})
	if got := read(); got != capped {
		t.Fatalf("capped rechecks changed the marker: before=%+v after=%+v", capped, got)
	}

	execReclaimFixture(t, owner, `DELETE FROM alignment_operations WHERE id='align-fixture'`)
	assertPass(OneShotReclaimResult{Rechecked: 1})
	unblocked := read()
	if unblocked.state != OneShotStateReclaimPending || unblocked.version != capped.version+1 || !unblocked.deadline.Equal(capped.cap) {
		t.Fatalf("marker after unblock = %+v, want reclaim_pending with deadline at cap", unblocked)
	}
	if unblocked.outcome != capped.outcome || unblocked.code != capped.code || unblocked.reason != capped.reason || !unblocked.terminal.Equal(capped.terminal) {
		t.Fatalf("unblock changed terminal evidence: before=%+v after=%+v", capped, unblocked)
	}
}

func TestReclaimCompactionRefusalRollsBackWithoutCountingBlocked(t *testing.T) {
	db := openTestDB(t)
	owner := disposabledb.Owner(t)
	service, err := NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "adopted-compact-retained"
	seedAdoptedReclaimMarker(t, db, marker, time.Now().Add(-time.Hour))
	const pinned = "pinned-root"
	files := createOneShotChain(t, db, pinned, []string{"pinned.mkv"})
	failOneShotFileOperation(t, db, "fo-"+pinned, "art-"+pinned, files[0], "/library/pinned.mkv")
	settleOneShotRoot(t, db, pinned)
	// The linked root is eligible by age but its release belongs to a different
	// owner. Only the compact routine can reject that release pin.
	execReclaimFixture(t, owner, `UPDATE acquisitions SET origin_managed_anime_ref=$1,
accepted_at=CURRENT_TIMESTAMP-(detail_retention_seconds+7200)*INTERVAL '1 second',
retention_anchor_at=CURRENT_TIMESTAMP-(detail_retention_seconds+7200)*INTERVAL '1 second',
terminal_at=CURRENT_TIMESTAMP-(detail_retention_seconds+3600)*INTERVAL '1 second',
detail_until=CURRENT_TIMESTAMP-INTERVAL '1 hour' WHERE id=$2`, "anime-"+marker, "acq-"+pinned)
	before := acquisitionfixture.Snapshot(t, owner)
	ctx := context.Background()
	if got, err := service.reclaimOne(ctx, marker); err != nil || got != reclaimCompactRetained {
		t.Fatalf("reclaim = %q, err=%v, want compact_retained", got, err)
	}
	if got, err := service.RunPass(ctx); err != nil || got != (OneShotReclaimResult{}) || got.Changed() {
		t.Fatalf("pass = %+v, err=%v, want no committed change", got, err)
	}
	if before != acquisitionfixture.Snapshot(t, owner) {
		t.Fatal("compaction refusal changed the marker or root graph")
	}
}
