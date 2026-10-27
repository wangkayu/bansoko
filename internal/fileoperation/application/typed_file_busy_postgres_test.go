//go:build linux || darwin

package application

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	"bansoko/internal/workitems"
	"golang.org/x/sys/unix"
)

func newOverwriteRuntimeFixture(t *testing.T) typedRuntimeFixture {
	t.Helper()
	x := newTypedRuntimeFixture(t, func(p map[string]any) {
		p["effective_sections"].(map[string]any)[animedomain.SectionFileEffect] = map[string]any{"operation": "copy", "collision_policy": "overwrite"}
	}, false)
	if raw, err := os.ReadFile(x.target); err != nil || string(raw) != fixtureOverwriteIncumbentBytes {
		t.Fatalf("overwrite incumbent %q %v", raw, err)
	}
	return x
}

// renameAnswer makes every replacing rename answer errno while *errno != 0.
func renameAnswer(errno *unix.Errno) filesystem.OSFileExecutor {
	return filesystem.OSFileExecutor{Hooks: filesystem.ExecutorHooks{PublicationRename: func(string, string) error {
		if *errno != 0 {
			return *errno
		}
		return nil
	}}}
}

type busyOperationRow struct {
	state, code    string
	retries        int
	permit         bool
	liveWork       int
	busySince      string
	dueIn          time.Duration
	deliveryResult string
}

func (x typedRuntimeFixture) busyRow(t *testing.T) busyOperationRow {
	t.Helper()
	var r busyOperationRow
	var code, outcome, busySince sql.NullString
	var dueSeconds float64
	if err := x.db.QueryRow(`SELECT o.state,o.failure_code,o.import_retry_count,o.publication_permit_id IS NOT NULL,
 (SELECT count(*) FROM work_items w WHERE w.owner_type='file_operation' AND w.owner_id=o.id),
 (SELECT w.input_snapshot->>'busy_since' FROM work_items w WHERE w.owner_type='file_operation' AND w.owner_id=o.id),
 COALESCE((SELECT EXTRACT(EPOCH FROM w.available_at-clock_timestamp()) FROM work_items w WHERE w.owner_type='file_operation' AND w.owner_id=o.id),0),
 (SELECT f.outcome FROM acquisition_files f WHERE f.acquisition_id=o.acquisition_id AND f.file_ref=o.file_ref)
 FROM file_operations o WHERE o.id=$1`, x.opID).Scan(&r.state, &code, &r.retries, &r.permit, &r.liveWork, &busySince, &dueSeconds, &outcome); err != nil {
		t.Fatal(err)
	}
	r.code, r.busySince, r.deliveryResult = code.String, busySince.String, outcome.String
	r.dueIn = time.Duration(dueSeconds * float64(time.Second))
	return r
}

func (x typedRuntimeFixture) makeDue(t *testing.T, busySince string) {
	t.Helper()
	query := `UPDATE work_items SET available_at=clock_timestamp()-interval '1 second' WHERE owner_type='file_operation' AND owner_id=$1`
	args := []any{x.opID}
	if busySince != "" {
		query = `UPDATE work_items SET available_at=clock_timestamp()-interval '1 second', input_snapshot=jsonb_set(input_snapshot,'{busy_since}',to_jsonb($2::text)) WHERE owner_type='file_operation' AND owner_id=$1`
		args = append(args, busySince)
	}
	if _, err := ownerTestDB(t).Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func assertNoPermitTemp(t *testing.T, target string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".bansoko-permit-") {
			t.Fatalf("staged temp left in the target directory: %s", entry.Name())
		}
	}
}

// An overwrite whose rename onto the existing file is refused busy (an SMB
// sharing violation is EBUSY) leaves the old file in place, removes its temp
// and is requeued: its evidence and attempt budget are reset and the next
// item is due in five minutes, carrying the window start. Once the file is
// released the next run replaces it.
func TestTypedOverwriteBusyRequeuesThenPublishes(t *testing.T) {
	x := newOverwriteRuntimeFixture(t)
	errno := unix.EBUSY
	x.s.executor = renameAnswer(&errno)
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("busy run: handled=%v err=%v", handled, err)
	}
	row := x.busyRow(t)
	if row.state != ownerStatePending || row.code != "" || row.retries != 1 || row.permit || row.liveWork != 1 || row.deliveryResult != "" {
		t.Fatalf("busy overwrite not requeued: %+v", row)
	}
	if row.busySince == "" || row.dueIn < 4*time.Minute || row.dueIn > 6*time.Minute {
		t.Fatalf("next item busy_since=%q due in %s", row.busySince, row.dueIn)
	}
	if raw, err := os.ReadFile(x.target); err != nil || string(raw) != fixtureOverwriteIncumbentBytes {
		t.Fatalf("refused overwrite changed the target: %q %v", raw, err)
	}
	assertNoPermitTemp(t, x.target)
	var lastCode string
	if err := x.db.QueryRow(`SELECT last_error_code FROM work_item_outcomes WHERE owner_type='file_operation' AND owner_id=$1 ORDER BY terminal_at DESC, id DESC LIMIT 1`, x.opID).Scan(&lastCode); err != nil || lastCode != domain.CodeTargetBusy {
		t.Fatalf("closed item code=%q err=%v", lastCode, err)
	}

	// Still busy on the next hop: the window start is carried unchanged.
	x.makeDue(t, "")
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("second busy run: handled=%v err=%v", handled, err)
	}
	if again := x.busyRow(t); again.retries != 2 || again.busySince != row.busySince {
		t.Fatalf("second hop %+v, want the same busy_since %q", again, row.busySince)
	}

	errno = 0
	x.makeDue(t, "")
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("released run: handled=%v err=%v", handled, err)
	}
	if done := x.busyRow(t); done.state != ownerStateCompleted || done.deliveryResult != "succeeded" || done.liveWork != 0 {
		t.Fatalf("released overwrite not completed: %+v", done)
	}
	if raw, err := os.ReadFile(x.target); err != nil || string(raw) != "source-content" {
		t.Fatalf("target after release %q %v", raw, err)
	}
	assertNoPermitTemp(t, x.target)
}

// After 24 hours of refusals the overwrite fails with TARGET_BUSY (no
// external effect: the old file is in place and no temp is left).
func TestTypedOverwriteBusyWindowExhaustedFails(t *testing.T) {
	x := newOverwriteRuntimeFixture(t)
	errno := unix.EACCES
	x.s.executor = renameAnswer(&errno)
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("busy run: handled=%v err=%v", handled, err)
	}
	x.makeDue(t, time.Now().Add(-25*time.Hour).UTC().Format(time.RFC3339Nano))
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("exhausted run: handled=%v err=%v", handled, err)
	}
	row := x.busyRow(t)
	if row.state != ownerStateFailed || row.code != domain.CodeTargetBusy || row.liveWork != 0 || row.deliveryResult != "failed" {
		t.Fatalf("exhausted busy overwrite: %+v", row)
	}
	if got := x.delivery(t, "primary"); got.code != domain.CodeTargetBusy {
		t.Fatalf("delivery result: %+v", got)
	}
	if raw, err := os.ReadFile(x.target); err != nil || string(raw) != fixtureOverwriteIncumbentBytes {
		t.Fatalf("target after exhaustion %q %v", raw, err)
	}
	assertNoPermitTemp(t, x.target)
	var notices int
	if err := x.db.QueryRow(`SELECT count(*) FROM notification_admissions WHERE trigger_kind='file_operation_failed' AND source_owner_id=$1`, x.opID).Scan(&notices); err != nil || notices != 1 {
		t.Fatalf("file_operation_failed notices=%d err=%v", notices, err)
	}
}

// A read-only mount (EROFS) is a permission failure, not busy: terminal on
// the first run, with the staged temp removed.
func TestTypedOverwriteReadOnlyFailsWithoutRequeue(t *testing.T) {
	x := newOverwriteRuntimeFixture(t)
	errno := unix.EROFS
	x.s.executor = renameAnswer(&errno)
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("read-only run: handled=%v err=%v", handled, err)
	}
	row := x.busyRow(t)
	if row.state != ownerStateFailed || row.code != domain.CodeTargetPermissionDenied || row.retries != 0 || row.liveWork != 0 {
		t.Fatalf("read-only overwrite: %+v", row)
	}
	assertNoPermitTemp(t, x.target)
}

// The requeue command accepts only the busy shape: a claim whose evidence
// is not a refused-busy overwrite is refused with zero writes.
func TestFileBusyRequeueRefusesOtherEvidence(t *testing.T) {
	x := newOverwriteRuntimeFixture(t)
	c, err := x.store.ClaimDue(t.Context(), "busy-shape", []string{workitems.KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	before := planGraphSnapshot(t, x.db)
	var raw []byte
	err = x.db.QueryRow(`SELECT public.file_busy_requeue($1,$2,$3,'busy',300,'86400 seconds'::interval)`, c.Owner.ID, c.ID, c.FencingToken).Scan(&raw)
	if err == nil || !strings.Contains(err.Error(), "FILE_BUSY_REQUEUE_EVIDENCE") {
		t.Fatalf("requeue without busy evidence = %s %v", raw, err)
	}
	if planGraphSnapshot(t, x.db) != before {
		t.Fatal("refused requeue wrote graph")
	}
}
