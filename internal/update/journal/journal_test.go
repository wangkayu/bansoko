package journal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRecord(operationID string) Record {
	return Record{
		OperationID:           operationID,
		Channel:               "embedded",
		TargetReleaseID:       "release-20260827-01",
		TargetVersion:         "0.9.0-rc.10",
		TargetSchemaVersion:   "036",
		TargetReleaseSequence: 42,
		PayloadDigest:         "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}
}

func mustOpen(t *testing.T, path string) *FileJournal {
	t.Helper()
	journal, err := Open(path)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	return journal
}

func TestCreateAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := mustOpen(t, path)

	record, err := journal.Create(testRecord("op-1"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if record.Step != StepAccepted || record.Version != 0 {
		t.Fatalf("created record at unexpected initial state %+v", record)
	}

	reloaded, found, err := journal.Load()
	if err != nil || !found {
		t.Fatalf("load after create: found=%v err=%v", found, err)
	}
	if reloaded.OperationID != "op-1" || reloaded.CreatedAt.IsZero() || len(reloaded.History) != 1 {
		t.Fatalf("loaded record incomplete: %+v", reloaded)
	}
}

func TestCreateIsIdempotentForSameOperationOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := mustOpen(t, path)

	if _, err := journal.Create(testRecord("op-1")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	sameOp, err := journal.Create(testRecord("op-1"))
	if err != nil || sameOp.OperationID != "op-1" {
		t.Fatalf("same-operation create must be idempotent, got (%+v, %v)", sameOp, err)
	}
	differentOp := testRecord("op-2")
	if _, err := journal.Create(differentOp); !errors.Is(err, ErrDuplicateOperation) {
		t.Fatalf("second operation create error = %v, want ErrDuplicateOperation", err)
	}
}

func TestAdvanceCASAndTransitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := mustOpen(t, path)
	record, err := journal.Create(testRecord("op-cas"))
	if err != nil {
		t.Fatal(err)
	}

	versions := []int64{record.Version, 0, 0, 0, 0, 0, 0, 0, 0}
	steps := []Step{
		StepFetched, StepStaged, StepMaintenanceEntered,
		StepBackupVerified, StepPendingMigration, StepMigrationSucceeded,
		StepPromoted, StepSucceeded,
	}
	for i, step := range steps {
		advanced, advanceErr := journal.Advance(record.OperationID, versions[i], step, "", "", func(r *Record) {
			if step == StepPromoted {
				r.CurrentReleaseID = record.TargetReleaseID
			}
			if step == StepSucceeded {
				// A terminal step is only reachable with the proven close that
				// the real writer records.
				r.FenceClosure = FenceClosureClosed
			}
		})
		if advanceErr != nil {
			t.Fatalf("advance to %s: %v", step, advanceErr)
		}
		versions[i+1] = advanced.Version
	}

	final, _, loadErr := journal.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if final.Step != StepSucceeded || final.Version != int64(len(steps)) {
		t.Fatalf("final state = %s v%d, want succeeded v%d", final.Step, final.Version, len(steps))
	}
	if final.CurrentReleaseID == "" {
		t.Fatal("mutated field lost across checkpoints")
	}
	if len(final.History) != len(steps)+1 { // created event + advances
		t.Fatalf("history length = %d, want %d", len(final.History), len(steps)+1)
	}
	if !Terminal(final.Step) {
		t.Fatal("succeeded must be terminal")
	}

	// Stale version CAS rejection.
	if _, staleErr := journal.Advance(record.OperationID, 1, StepFailed, "", "", nil); !errors.Is(staleErr, ErrConflict) {
		t.Fatalf("stale-version advance error = %v, want ErrConflict", staleErr)
	}
}

func TestAdvanceRejectsIllegalTransitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := mustOpen(t, path)
	record, err := journal.Create(testRecord("op-illegal"))
	if err != nil {
		t.Fatal(err)
	}

	if _, jumpErr := journal.Advance(record.OperationID, 0, StepPromoted, "", "", nil); !errors.Is(jumpErr, ErrInvalidTransition) {
		t.Fatalf("accepted -> promoted must be rejected, got %v", jumpErr)
	}

	// Idempotent replay to the same step is legal and bumps the version.
	replayed, replayErr := journal.Advance(record.OperationID, 0, StepAccepted, "", "retry marker", nil)
	if replayErr != nil || replayed.Version != 1 {
		t.Fatalf("same-step replay error = %v version = %d, want nil/1", replayErr, replayed.Version)
	}
	if last := replayed.History[len(replayed.History)-1]; last.Detail != "retry marker" {
		t.Fatalf("replay event detail missing: %+v", last)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	first := mustOpen(t, path)
	record, err := first.Create(testRecord("op-persist"))
	if err != nil {
		t.Fatal(err)
	}
	if _, advanceErr := first.Advance(record.OperationID, 0, StepFetched, "", "", nil); advanceErr != nil {
		t.Fatal(advanceErr)
	}
	if closeErr := first.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	second := mustOpen(t, path)
	reopened, found, loadErr := second.Load()
	if loadErr != nil || !found || reopened.Step != StepFetched || reopened.Version != 1 {
		t.Fatalf("reopened state = (%+v, found=%v, err=%v)", reopened, found, loadErr)
	}
}

func TestCorruptJournalIsQuarantined(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.json")
	if writeErr := os.WriteFile(path, []byte("{not json"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	_, openErr := Open(path)
	if !errors.Is(openErr, ErrJournalCorrupted) {
		t.Fatalf("open corrupt error = %v, want ErrJournalCorrupted", openErr)
	}
	entries, readDirErr := os.ReadDir(dir)
	if readDirErr != nil {
		t.Fatal(readDirErr)
	}
	corruptFiles, liveFiles := 0, 0
	for _, entry := range entries {
		switch name := entry.Name(); {
		case strings.HasPrefix(name, "journal.corrupt-"):
			corruptFiles++
			info, statErr := entry.Info()
			if statErr != nil || info.Size() != int64(len("{not json")) {
				t.Fatal("quarantined file lost original bytes")
			}
		case name == "journal.json":
			liveFiles++
		}
	}
	if corruptFiles != 1 || liveFiles != 0 {
		t.Fatalf("quarantine layout wrong (corrupt=%d live=%d); original must be renamed away", corruptFiles, liveFiles)
	}

	// The next open starts clean instead of crash-looping.
	next := mustOpen(t, path)
	_, found, loadErr := next.Load()
	if loadErr != nil || found {
		t.Fatalf("post-quarantine load = (found=%v, err=%v), want clean empty journal", found, loadErr)
	}
}

func TestSecondProcessLockFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.json")
	first := mustOpen(t, path)
	defer first.Close()

	if _, lockErr := Open(path); !errors.Is(lockErr, ErrLocked) {
		t.Fatalf("second open error = %v, want ErrLocked", lockErr)
	}
}

func TestRollbackChainTransitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := mustOpen(t, path)
	record, err := journal.Create(testRecord("op-rollback"))
	if err != nil {
		t.Fatal(err)
	}
	steps := []Step{
		StepFetched, StepStaged, StepMaintenanceEntered, StepBackupVerified,
		StepPendingMigration, StepMigrationSucceeded, StepPromoted, StepReadinessFailed,
		StepRollbackStarted, StepDatabaseRestored, StepRolledBack,
	}
	version := record.Version
	for _, step := range steps {
		advanced, advanceErr := journal.Advance(record.OperationID, version, step, "injected failure", "",
			func(r *Record) {
				if step == StepRolledBack {
					r.FenceClosure = FenceClosureClosed
				}
			})
		if advanceErr != nil {
			t.Fatalf("rollback step %s: %v", step, advanceErr)
		}
		version = advanced.Version
	}
	if last := steps[len(steps)-1]; last != StepRolledBack || !Terminal(StepRolledBack) {
		t.Fatal("rollback chain must end in a terminal state")
	}
}

func TestFenceClosurePendingIsNotTerminalAndCanReachOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := mustOpen(t, path)
	record, err := journal.Create(testRecord("op-fence"))
	if err != nil {
		t.Fatal(err)
	}
	steps := []Step{StepFetched, StepStaged, StepMaintenanceEntered, StepBackupVerified,
		StepPendingMigration, StepMigrationSucceeded, StepPromoted, StepFenceClosurePending, StepSucceeded}
	version := record.Version
	for _, step := range steps {
		record, err = journal.Advance(record.OperationID, version, step, "", "", func(r *Record) {
			switch step {
			case StepFenceClosurePending:
				r.FenceClosureTarget = StepSucceeded
			case StepSucceeded:
				r.FenceClosure = FenceClosureClosed
			}
		})
		if err != nil {
			t.Fatalf("advance %s: %v", step, err)
		}
		version = record.Version
	}
	if Terminal(StepFenceClosurePending) {
		t.Fatal("fence closure pending must remain active")
	}
	if !Terminal(record.Step) || record.Step != StepSucceeded {
		t.Fatalf("final step = %s, want terminal succeeded", record.Step)
	}
}

func TestFenceClosurePendingIsOnlyReachableFromTerminalEffectOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := mustOpen(t, path)
	record, err := journal.Create(testRecord("op-fence-guard"))
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []Step{StepFetched, StepStaged, StepMaintenanceEntered, StepBackupVerified, StepPendingMigration, StepMigrationSucceeded} {
		record, err = journal.Advance(record.OperationID, record.Version, step, "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := journal.Advance(record.OperationID, record.Version, StepFenceClosurePending, "", "", nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("migration_succeeded -> fence_closure_pending = %v, want invalid transition", err)
	}
}

func TestNormalizeRejectsMalformedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := mustOpen(t, path)

	bad := testRecord("../escape")
	bad.Channel = "embedded"
	if _, err := journal.Create(bad); err == nil {
		t.Fatal("operation id with traversal characters must be rejected")
	}

	channelless := testRecord("op-nochannel")
	channelless.Channel = ""
	if _, err := journal.Create(channelless); err == nil {
		t.Fatal("missing channel must be rejected")
	}
}

func TestClockInjectionOrdersTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := mustOpen(t, path)
	base := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	current := base
	journal.clock = func() time.Time {
		current = current.Add(time.Minute)
		return current.UTC()
	}

	record, err := journal.Create(testRecord("op-clock"))
	if err != nil {
		t.Fatal(err)
	}
	advanced, advanceErr := journal.Advance(record.OperationID, 0, StepFetched, "", "", nil)
	if advanceErr != nil {
		t.Fatal(advanceErr)
	}
	if !advanced.UpdatedAt.After(base) {
		t.Fatalf("timestamp not driven by injected clock: %v", advanced.UpdatedAt)
	}
}

func TestArchiveTerminalRecyclesWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := mustOpen(t, path)

	if _, err := journal.Create(testRecord("op-1")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := journal.Advance("op-1", 0, StepFailed, "admission rejected", "test", nil); err != nil {
		t.Fatalf("advance to failed: %v", err)
	}
	journal.Close()

	reopened := mustOpen(t, path)
	// A terminal record still occupies the serial chain: creating another
	// operation is refused until the terminal record is archived.
	if _, err := reopened.Create(testRecord("op-3")); err == nil {
		t.Fatalf("create while terminal op present must be refused (serial chain)")
	} else if !errors.Is(err, ErrDuplicateOperation) {
		t.Fatalf("expected duplicate-operation refusal, got %v", err)
	}
	record, found, loadErr := reopened.Load()
	if loadErr != nil || !found {
		t.Fatalf("load terminal record: found=%v err=%v", found, loadErr)
	}
	if _, proofErr := reopened.AdvanceExpected(record.OperationID, record.Version, record.Step, record.Step,
		"", "owner-scoped fence closure verified",
		func(r *Record) { r.FenceClosure = FenceClosureClosed }); proofErr != nil {
		t.Fatalf("persist closure proof: %v", proofErr)
	}
	if err := reopened.ArchiveTerminalProven(); err != nil {
		t.Fatalf("archive terminal: %v", err)
	}

	// After archiving the workspace accepts a new operation and the archived
	// evidence file exists.
	if _, err := reopened.Create(testRecord("op-4")); err != nil {
		t.Fatalf("create after archive: %v", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(path), "archive", "journal-op-1-*.json"))
	if globErr != nil || len(matches) != 1 {
		t.Fatalf("archived evidence missing: matches=%d err=%v", len(matches), globErr)
	}
	data, _ := os.ReadFile(matches[0])
	if !strings.Contains(string(data), `"operation_id": "op-1"`) {
		t.Fatalf("archived record lost identity: %s", data)
	}
}

func TestArchiveRefusesActiveOperation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := mustOpen(t, path)
	if _, err := journal.Create(testRecord("op-live")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := journal.ArchiveTerminalProven(); err == nil {
		t.Fatalf("archiving an active operation must fail closed")
	}
	if _, found, _ := journal.Load(); !found {
		t.Fatalf("active record vanished after refused archive")
	}
}

func TestArchiveWithoutRecordFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := mustOpen(t, path)
	if err := journal.ArchiveTerminalProven(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on empty journal, got %v", err)
	}
}
