package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// legacyJournalJSON is a durable record written before manifest persistence
// existed: it must keep loading unchanged, so that old volumes fail closed at
// the recovery layer (which refuses a record without a manifest copy) instead
// of breaking the journal reader itself.
const legacyJournalJSON = `{
  "schema_version": 1,
  "record": {
    "schema_version": 1,
    "operation_id": "embedded-legacy",
    "channel": "embedded",
    "step": "accepted",
    "version": 0,
    "target_release_id": "release-legacy",
    "target_version": "0.9.0-legacy",
    "target_schema_version": "036",
    "manifest_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "created_at": "2026-09-01T00:00:00Z",
    "updated_at": "2026-09-01T00:00:00Z"
  }
}`

func TestAdvanceExpectedCASesOnTheDurableStep(t *testing.T) {
	journal := mustOpen(t, filepath.Join(t.TempDir(), "journal.json"))
	record, err := journal.Create(testRecord("op-step-cas"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := journal.AdvanceExpected(record.OperationID, record.Version, StepStaged,
		StepFetched, "", "", nil); !errors.Is(err, ErrStepConflict) {
		t.Fatalf("advance from a wrong expected step = %v, want ErrStepConflict", err)
	}
	if _, err := journal.AdvanceExpected(record.OperationID, record.Version+1, StepAccepted,
		StepFetched, "", "", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("advance with a stale version = %v, want ErrConflict", err)
	}
	advanced, err := journal.AdvanceExpected(record.OperationID, record.Version, StepAccepted,
		StepFetched, "", "operator resume", nil)
	if err != nil {
		t.Fatalf("advance with the exact step and version: %v", err)
	}
	if advanced.Step != StepFetched || advanced.Version != record.Version+1 {
		t.Fatalf("advanced record = %s v%d, want fetched v%d", advanced.Step, advanced.Version, record.Version+1)
	}

	// A stale operator decision must never be merged into a newer checkpoint.
	if _, err := journal.AdvanceExpected(record.OperationID, record.Version, StepAccepted,
		StepStaged, "", "", nil); !errors.Is(err, ErrStepConflict) && !errors.Is(err, ErrConflict) {
		t.Fatalf("stale operator decision = %v, want a typed conflict", err)
	}
	stored, found, loadErr := journal.Load()
	if loadErr != nil || !found || stored.Step != StepFetched || stored.Version != advanced.Version {
		t.Fatalf("refused advances mutated the journal: found=%v err=%v record=%+v", found, loadErr, stored)
	}
}

func TestAdvanceExpectedRequiresAnExpectedStep(t *testing.T) {
	journal := mustOpen(t, filepath.Join(t.TempDir(), "journal.json"))
	if _, err := journal.Create(testRecord("op-required-step")); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.AdvanceExpected("op-required-step", 0, "", StepFetched, "", "", nil); err == nil {
		t.Fatal("a conditional advance without an expected step must be refused")
	}
}

func TestSnapshotReadsWithoutClaimingTheWriterLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := mustOpen(t, path)
	record, err := journal.Create(testRecord("op-snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Advance(record.OperationID, record.Version, StepFetched, "", "checkpoint", nil); err != nil {
		t.Fatal(err)
	}
	before, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}

	// The writer still holds the exclusive flock: a read-only observer must not
	// need it, and must not be able to take it.
	if _, openErr := Open(path); !errors.Is(openErr, ErrLocked) {
		t.Fatalf("second writer open error = %v, want ErrLocked", openErr)
	}
	snapshot, found, err := Snapshot(path)
	if err != nil || !found {
		t.Fatalf("lock-free snapshot: found=%v err=%v", found, err)
	}
	if snapshot.Step != StepFetched || snapshot.OperationID != record.OperationID {
		t.Fatalf("snapshot = %+v, want the durable fetched record", snapshot)
	}
	if snapshot.ManifestPath != "" || snapshot.ManifestSize != 0 {
		t.Fatalf("snapshot invented manifest copy fields: %+v", snapshot)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a lock-free snapshot mutated the journal file")
	}
}

func TestSnapshotFailsClosedOnCorruptionWithoutQuarantining(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.json")
	corrupt := []byte("{ this is not a journal")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := Snapshot(path)
	if !errors.Is(err, ErrJournalCorrupted) {
		t.Fatalf("snapshot of a corrupt journal error = %v, want ErrJournalCorrupted", err)
	}
	// A read-only observer must never destroy or move evidence.
	preserved, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("snapshot removed the corrupt journal: %v", readErr)
	}
	if !bytes.Equal(preserved, corrupt) {
		t.Fatal("snapshot rewrote the corrupt journal")
	}
	leftovers, globErr := filepath.Glob(filepath.Join(dir, "*.corrupt-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(leftovers) != 0 {
		t.Fatalf("snapshot quarantined the journal: %v", leftovers)
	}
}

func TestSnapshotDistinguishesAbsentFromEmptyJournal(t *testing.T) {
	dir := t.TempDir()
	if _, found, err := Snapshot(filepath.Join(dir, "missing.json")); err != nil || found {
		t.Fatalf("snapshot of an absent journal: found=%v err=%v", found, err)
	}
	// An empty path is a caller bug, not an absent journal.
	if _, _, err := Snapshot("  "); err == nil {
		t.Fatal("snapshot without a path must be refused")
	}
	if _, _, err := Snapshot(filepath.Join(dir, "missing", "journal.json")); err != nil {
		t.Fatalf("snapshot of a missing directory must be an empty read: %v", err)
	}
}

func TestLegacyRecordWithoutManifestCopyStaysReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	if err := os.WriteFile(path, []byte(legacyJournalJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	journal := mustOpen(t, path)
	record, found, err := journal.Load()
	if err != nil || !found {
		t.Fatalf("load legacy journal: found=%v err=%v", found, err)
	}
	if record.OperationID != "embedded-legacy" || record.Step != StepAccepted {
		t.Fatalf("legacy record = %+v, want the accepted operation", record)
	}
	if record.ManifestPath != "" || record.ManifestSize != 0 {
		t.Fatalf("legacy record decoded manifest copy fields: %+v", record)
	}

	snapshot, found, err := Snapshot(path)
	if err != nil || !found {
		t.Fatalf("snapshot legacy journal: found=%v err=%v", found, err)
	}
	if snapshot.ManifestPath != "" || snapshot.ManifestSize != 0 {
		t.Fatalf("legacy snapshot decoded manifest copy fields: %+v", snapshot)
	}

	// A record written by the current code binds the copy and round-trips it.
	updated, err := journal.Advance(record.OperationID, record.Version, StepFetched, "", "legacy",
		func(r *Record) {
			r.ManifestPath = "manifests/embedded-legacy.json"
			r.ManifestSize = 4096
		})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, _, err := journal.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ManifestPath != "manifests/embedded-legacy.json" || reloaded.ManifestSize != 4096 {
		t.Fatalf("manifest copy fields did not survive the checkpoint: %+v", reloaded)
	}
	if updated.Version == record.Version {
		t.Fatal("checkpoint did not advance the version")
	}
}

func TestSnapshotIgnoresAnEmptyEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"record":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := Snapshot(path); err != nil || found {
		t.Fatalf("empty envelope snapshot: found=%v err=%v", found, err)
	}
	if strings.Contains(legacyJournalJSON, `"manifest_path"`) {
		t.Fatal("legacy fixture unexpectedly carries manifest copy fields")
	}
}

func TestPreparedAndAbortIntentTransitions(t *testing.T) {
	legal := []struct{ from, to Step }{
		{StepPrepared, StepAccepted},
		{StepPrepared, StepFailed},
		{StepPrepared, StepAbortPending},
		{StepAccepted, StepAbortPending},
		{StepFetched, StepAbortPending},
		{StepStaged, StepAbortPending},
		{StepMaintenanceEntered, StepAbortPending},
		{StepBackupVerified, StepAbortPending},
		{StepAbortPending, StepFailed},
	}
	for _, transition := range legal {
		if !CanAdvance(transition.from, transition.to) {
			t.Fatalf("%s -> %s must be legal", transition.from, transition.to)
		}
	}
	illegal := []struct{ from, to Step }{
		{StepPrepared, StepFetched},
		{StepPrepared, StepStaged},
		{StepAbortPending, StepAccepted},
		{StepAbortPending, StepPendingMigration},
		{StepAbortPending, StepFenceClosurePending},
	}
	for _, transition := range illegal {
		if CanAdvance(transition.from, transition.to) {
			t.Fatalf("%s -> %s must be refused", transition.from, transition.to)
		}
	}
}

func TestRecoveryDecisionTupleIsWriteOnce(t *testing.T) {
	newDecision := func(record *Record) {
		record.RecoveryAction = RecoveryActionResume
		record.RecoveryFromStep = StepAccepted
		record.RecoveryFromVersion = 0
		record.RecoveryReferenceType = RecoveryReferenceType
		record.RecoveryReferenceHash = strings.Repeat("a", 64)
	}

	journal := mustOpen(t, filepath.Join(t.TempDir(), "journal.json"))
	record, err := journal.Create(testRecord("op-recovery-tuple"))
	if err != nil {
		t.Fatal(err)
	}
	decided, err := journal.AdvanceExpected(record.OperationID, record.Version, record.Step, record.Step, "", "decision", newDecision)
	if err != nil {
		t.Fatalf("write complete decision tuple: %v", err)
	}
	unchanged, err := journal.AdvanceExpected(decided.OperationID, decided.Version, decided.Step, StepFetched, "", "checkpoint", nil)
	if err != nil {
		t.Fatalf("carry decision tuple to next checkpoint: %v", err)
	}

	mutations := []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "clear", mutate: func(r *Record) {
			r.RecoveryAction = ""
			r.RecoveryFromStep = ""
			r.RecoveryFromVersion = 0
			r.RecoveryReferenceType = ""
			r.RecoveryReferenceHash = ""
		}},
		{name: "action", mutate: func(r *Record) { r.RecoveryAction = RecoveryActionAbort }},
		{name: "step", mutate: func(r *Record) { r.RecoveryFromStep = StepFetched }},
		{name: "version", mutate: func(r *Record) { r.RecoveryFromVersion++ }},
		{name: "reference type", mutate: func(r *Record) { r.RecoveryReferenceType = "other" }},
		{name: "reference hash", mutate: func(r *Record) { r.RecoveryReferenceHash = strings.Repeat("b", 64) }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := journal.AdvanceExpected(unchanged.OperationID, unchanged.Version, unchanged.Step,
				unchanged.Step, "", "mutate", tc.mutate); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("tuple mutation error = %v, want ErrInvalidTransition", err)
			}
		})
	}

	partialJournal := mustOpen(t, filepath.Join(t.TempDir(), "journal.json"))
	partial, err := partialJournal.Create(testRecord("op-partial-recovery-tuple"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partialJournal.AdvanceExpected(partial.OperationID, partial.Version, partial.Step, partial.Step,
		"", "partial", func(r *Record) { r.RecoveryAction = RecoveryActionResume }); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("partial decision error = %v, want ErrInvalidTransition", err)
	}
}

func TestAbortOriginTupleIsWriteOnce(t *testing.T) {
	journal := mustOpen(t, filepath.Join(t.TempDir(), "journal.json"))
	record, err := journal.Create(testRecord("op-abort-tuple"))
	if err != nil {
		t.Fatal(err)
	}
	decided, err := journal.AdvanceExpected(record.OperationID, record.Version, record.Step, StepAbortPending,
		"", "abort intent", func(r *Record) {
			r.AbortFrom = StepAccepted
			r.AbortFromVersion = 0
		})
	if err != nil {
		t.Fatalf("write complete abort tuple: %v", err)
	}
	unchanged, err := journal.AdvanceExpected(decided.OperationID, decided.Version, decided.Step, decided.Step, "", "retry", nil)
	if err != nil {
		t.Fatalf("carry abort tuple across retry: %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "clear", mutate: func(r *Record) { r.AbortFrom = ""; r.AbortFromVersion = 0 }},
		{name: "step", mutate: func(r *Record) { r.AbortFrom = StepFetched }},
		{name: "version", mutate: func(r *Record) { r.AbortFromVersion++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := journal.AdvanceExpected(unchanged.OperationID, unchanged.Version, unchanged.Step,
				unchanged.Step, "", "mutate", tc.mutate); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("abort tuple mutation error = %v, want ErrInvalidTransition", err)
			}
		})
	}

	partialJournal := mustOpen(t, filepath.Join(t.TempDir(), "journal.json"))
	partial, err := partialJournal.Create(testRecord("op-partial-abort-tuple"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partialJournal.AdvanceExpected(partial.OperationID, partial.Version, partial.Step, StepAbortPending,
		"", "partial", func(r *Record) { r.AbortFromVersion = 4 }); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("partial abort tuple error = %v, want ErrInvalidTransition", err)
	}
}

func TestArchiveTerminalRequiresAProvenSettledClosure(t *testing.T) {
	advanceTo := func(t *testing.T, steps ...Step) *FileJournal {
		t.Helper()
		journal := mustOpen(t, filepath.Join(t.TempDir(), "journal.json"))
		record, err := journal.Create(testRecord("op-archive-proof"))
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range steps {
			record, err = journal.Advance(record.OperationID, record.Version, step, "", "test", nil)
			if err != nil {
				t.Fatalf("advance to %s: %v", step, err)
			}
		}
		return journal
	}

	t.Run("rollback_blocked stays durable", func(t *testing.T) {
		journal := advanceTo(t, StepFetched, StepStaged, StepMaintenanceEntered, StepBackupVerified,
			StepPendingMigration, StepRollbackStarted, StepRollbackBlocked)
		if err := journal.ArchiveTerminalProven(); err == nil {
			t.Fatal("rollback_blocked was archived")
		}
		if _, found, _ := journal.Load(); !found {
			t.Fatal("rollback_blocked record vanished after the refused archive")
		}
	})

	t.Run("unsettled closure target", func(t *testing.T) {
		journal := mustOpen(t, filepath.Join(t.TempDir(), "journal.json"))
		record, err := journal.Create(testRecord("op-target"))
		if err != nil {
			t.Fatal(err)
		}
		record, err = journal.Advance(record.OperationID, record.Version, StepFailed, "", "test", func(r *Record) {
			r.FenceClosureTarget = StepSucceeded
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := journal.ArchiveTerminalProven(); err == nil {
			t.Fatal("a record with an unsettled closure target was archived")
		}
	})

	t.Run("fence_closure_pending stays live", func(t *testing.T) {
		journal := mustOpen(t, filepath.Join(t.TempDir(), "journal.json"))
		record, err := journal.Create(testRecord("op-pending-closure"))
		if err != nil {
			t.Fatal(err)
		}
		record, err = journal.Advance(record.OperationID, record.Version, StepFailed, "", "test", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := journal.AdvanceExpected(record.OperationID, record.Version, StepFailed,
			StepFenceClosurePending, "", "pending", func(r *Record) { r.FenceClosureTarget = StepFailed }); err != nil {
			t.Fatal(err)
		}
		if err := journal.ArchiveTerminalProven(); err == nil {
			t.Fatal("a record awaiting fence closure was archived")
		}
	})

	t.Run("proven entry point requires the closure proof", func(t *testing.T) {
		journal := advanceTo(t, StepFailed)
		if err := journal.ArchiveTerminalProven(); err == nil {
			t.Fatal("a record without a closure proof was archived")
		}
		record, found, err := journal.Load()
		if err != nil || !found {
			t.Fatalf("load: found=%v err=%v", found, err)
		}
		if _, advErr := journal.AdvanceExpected(record.OperationID, record.Version, StepFailed, StepFailed,
			"", "closure proven", func(r *Record) { r.FenceClosure = FenceClosureClosed }); advErr != nil {
			t.Fatal(advErr)
		}
		if err := journal.ArchiveTerminalProven(); err != nil {
			t.Fatalf("ArchiveTerminalProven after the proof: %v", err)
		}
		if _, found, err := Snapshot(journal.path); err != nil || found {
			t.Fatalf("archive left a live record: found=%v err=%v", found, err)
		}
	})

	t.Run("not_required is a valid closure proof", func(t *testing.T) {
		journal := mustOpen(t, filepath.Join(t.TempDir(), "journal.json"))
		record := testRecord("op-no-fence")
		record.Step = StepPrepared
		prepared, err := journal.Create(record)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := journal.AdvanceExpected(prepared.OperationID, prepared.Version, StepPrepared, StepFailed,
			"", "admission refused before the fence was acquired",
			func(r *Record) { r.FenceClosure = FenceClosureNotRequired }); err != nil {
			t.Fatal(err)
		}
		if err := journal.ArchiveTerminalProven(); err != nil {
			t.Fatalf("ArchiveTerminalProven for a fence-less operation: %v", err)
		}
	})

	t.Run("a record created directly at failed cannot claim no fence", func(t *testing.T) {
		journal := mustOpen(t, filepath.Join(t.TempDir(), "journal.json"))
		record := testRecord("op-direct-failed")
		record.Step = StepFailed
		record.FenceClosure = FenceClosureNotRequired
		if _, err := journal.Create(record); err != nil {
			t.Fatal(err)
		}
		if err := journal.ArchiveTerminalProven(); err == nil {
			t.Fatal("an operation created directly at failed was archived with the no-fence proof")
		}
	})
}

func TestJournalReadsAreBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.json")
	huge := bytes.Repeat([]byte("x"), int(maxJournalBytes)+1)
	if err := os.WriteFile(path, huge, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Snapshot(path); !errors.Is(err, ErrJournalCorrupted) {
		t.Fatalf("snapshot of an oversized journal = %v, want ErrJournalCorrupted", err)
	}
	preserved, readErr := os.ReadFile(path)
	if readErr != nil || len(preserved) != len(huge) {
		t.Fatalf("snapshot touched the oversized journal: %d bytes, err %v", len(preserved), readErr)
	}
	// The writer path quarantines instead of reading without bound.
	journal := &FileJournal{path: path, clock: func() time.Time { return time.Now().UTC() }}
	if _, err := journal.readEnvelope(); !errors.Is(err, ErrJournalCorrupted) {
		t.Fatalf("readEnvelope of an oversized journal = %v, want ErrJournalCorrupted", err)
	}
}

// TestClosureProofSemantics pins the durable proof rules by validating records
// directly: the advance path enforces the same rules on write (see
// TestAdvanceEnforcesClosureAndTargetInvariants), and the archive enforces them
// on read.
func TestClosureProofSemantics(t *testing.T) {
	fenceProofVia := func(proof string, steps ...Step) Record {
		return Record{Step: steps[len(steps)-1], FenceClosure: proof, History: eventsOf(steps...)}
	}
	reachedAccepted := []Step{StepPrepared, StepAccepted, StepFailed}

	cases := []struct {
		name    string
		record  Record
		wantErr bool
	}{
		{name: "succeeded requires a proven close", record: fenceProofVia("", StepAccepted, StepSucceeded), wantErr: true},
		{name: "succeeded with a proven close", record: fenceProofVia(FenceClosureClosed, StepAccepted, StepSucceeded)},
		{name: "succeeded cannot claim no fence", record: fenceProofVia(FenceClosureNotRequired, StepAccepted, StepSucceeded), wantErr: true},
		{name: "rolled_back requires a proven close", record: fenceProofVia("", StepAccepted, StepRolledBack), wantErr: true},
		{name: "rolled_back with a proven close", record: fenceProofVia(FenceClosureClosed, StepAccepted, StepRolledBack)},
		{name: "failed with a proven close", record: fenceProofVia(FenceClosureClosed, StepPrepared, StepAccepted, StepFailed)},
		{name: "failed without any proof", record: fenceProofVia("", StepPrepared, StepAccepted, StepFailed), wantErr: true},
		{name: "failed may claim no fence when it never reached accepted", record: fenceProofVia(FenceClosureNotRequired, StepPrepared, StepFailed)},
		{name: "failed after an abort intent from prepared may claim no fence", record: fenceProofVia(FenceClosureNotRequired, StepPrepared, StepAbortPending, StepFailed)},
		{name: "failed after repeated failed advances still proves the path", record: fenceProofVia(FenceClosureNotRequired, StepPrepared, StepFailed, StepFailed)},
		{name: "failed that reached accepted cannot claim no fence", record: fenceProofVia(FenceClosureNotRequired, reachedAccepted...), wantErr: true},
		{name: "failed after an abort intent from accepted cannot claim no fence", record: fenceProofVia(FenceClosureNotRequired, StepPrepared, StepAccepted, StepAbortPending, StepFailed), wantErr: true},
		{name: "a record created directly at failed cannot claim no fence", record: fenceProofVia(FenceClosureNotRequired, StepFailed), wantErr: true},
		{name: "a proven close is accepted regardless of the history shape", record: fenceProofVia(FenceClosureClosed, StepFailed)},
		{name: "an unproven path fails closed", record: fenceProofVia(FenceClosureNotRequired, StepPrepared, StepAbortPending, StepAbortPending, StepFailed)},
		{name: "an unknown proof fails closed", record: fenceProofVia("whatever", StepPrepared, StepFailed), wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateClosureProof(tc.record)
			if tc.wantErr && err == nil {
				t.Fatalf("proof %q with history %v was accepted", tc.record.FenceClosure, tc.record.History)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("proof %q with history %v was refused: %v", tc.record.FenceClosure, tc.record.History, err)
			}
		})
	}

	// A record with no history at all must fail closed.
	if err := validateClosureProof(Record{Step: StepFailed, FenceClosure: FenceClosureNotRequired}); err == nil {
		t.Fatal("the no-fence proof was accepted without any durable history")
	}
}

func eventsOf(steps ...Step) []TransitionEvent {
	events := make([]TransitionEvent, 0, len(steps))
	for _, step := range steps {
		events = append(events, TransitionEvent{Step: step})
	}
	return events
}

// TestAdvanceEnforcesClosureAndTargetInvariants pins the write-time rules that
// depend on the record's own fields.
func TestAdvanceEnforcesClosureAndTargetInvariants(t *testing.T) {
	newJournal := func(t *testing.T, initialStep Step) (*FileJournal, Record) {
		t.Helper()
		journal := mustOpen(t, filepath.Join(t.TempDir(), "journal.json"))
		record := testRecord("op-advance-proof")
		record.Step = initialStep
		created, err := journal.Create(record)
		if err != nil {
			t.Fatal(err)
		}
		return journal, created
	}

	t.Run("succeeded requires the proven close at the advance", func(t *testing.T) {
		journal, record := newJournal(t, StepPromoted)
		if _, err := journal.Advance(record.OperationID, record.Version, StepSucceeded, "", "", nil); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("advance to succeeded without a proof = %v, want ErrInvalidTransition", err)
		}
		advanced, err := journal.AdvanceExpected(record.OperationID, record.Version, StepPromoted, StepSucceeded,
			"", "close verified", func(r *Record) { r.FenceClosure = FenceClosureClosed })
		if err != nil {
			t.Fatalf("advance to succeeded with the proof: %v", err)
		}
		if advanced.FenceClosure != FenceClosureClosed {
			t.Fatalf("advanced proof = %q, want closed", advanced.FenceClosure)
		}
	})

	t.Run("rolled_back requires the proven close at the advance", func(t *testing.T) {
		journal, record := newJournal(t, StepDatabaseRestored)
		if _, err := journal.Advance(record.OperationID, record.Version, StepRolledBack, "", "", nil); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("advance to rolled_back without a proof = %v, want ErrInvalidTransition", err)
		}
	})

	t.Run("failed with a bogus no-fence proof is refused", func(t *testing.T) {
		journal, record := newJournal(t, StepAccepted)
		if _, err := journal.AdvanceExpected(record.OperationID, record.Version, StepAccepted, StepFailed,
			"", "abort", func(r *Record) { r.FenceClosure = FenceClosureNotRequired }); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("advance to failed claiming no fence after accepted = %v, want ErrInvalidTransition", err)
		}
		// The refused write must not have moved anything.
		stored, found, loadErr := journal.Load()
		if loadErr != nil || !found || stored.Step != StepAccepted || stored.FenceClosure != "" {
			t.Fatalf("refused advance mutated the journal: %+v (found %v err %v)", stored, found, loadErr)
		}
	})

	t.Run("a fence-less failed advance is accepted", func(t *testing.T) {
		journal, record := newJournal(t, StepPrepared)
		advanced, err := journal.AdvanceExpected(record.OperationID, record.Version, StepPrepared, StepFailed,
			"", "admission refused", func(r *Record) { r.FenceClosure = FenceClosureNotRequired })
		if err != nil {
			t.Fatalf("advance to failed with the fence-less proof: %v", err)
		}
		if advanced.FenceClosure != FenceClosureNotRequired {
			t.Fatalf("advanced proof = %q, want not_required", advanced.FenceClosure)
		}
	})

	t.Run("entering fence closure pending requires a terminal target", func(t *testing.T) {
		journal, record := newJournal(t, StepFailed)
		if _, err := journal.Advance(record.OperationID, record.Version, StepFenceClosurePending, "", "", nil); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("advance into fence_closure_pending without a target = %v, want ErrInvalidTransition", err)
		}
		if _, err := journal.AdvanceExpected(record.OperationID, record.Version, StepFailed, StepFenceClosurePending,
			"", "pending", func(r *Record) { r.FenceClosureTarget = StepAccepted }); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("advance into fence_closure_pending with a non-terminal target = %v, want ErrInvalidTransition", err)
		}
		pending, err := journal.AdvanceExpected(record.OperationID, record.Version, StepFailed, StepFenceClosurePending,
			"", "pending", func(r *Record) { r.FenceClosureTarget = StepSucceeded })
		if err != nil {
			t.Fatalf("advance into fence_closure_pending: %v", err)
		}
		if pending.FenceClosureTarget != StepSucceeded {
			t.Fatalf("target = %s, want succeeded", pending.FenceClosureTarget)
		}
	})

	t.Run("pending closure only settles onto its recorded target", func(t *testing.T) {
		journal, record := newJournal(t, StepFailed)
		pending, err := journal.AdvanceExpected(record.OperationID, record.Version, StepFailed, StepFenceClosurePending,
			"", "pending", func(r *Record) { r.FenceClosureTarget = StepSucceeded })
		if err != nil {
			t.Fatal(err)
		}
		if _, err := journal.AdvanceExpected(pending.OperationID, pending.Version, StepFenceClosurePending, StepFailed,
			"", "wrong target", func(r *Record) { r.FenceClosure = FenceClosureClosed }); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("settle onto a different target = %v, want ErrInvalidTransition", err)
		}
		settled, err := journal.AdvanceExpected(pending.OperationID, pending.Version, StepFenceClosurePending, StepSucceeded,
			"", "close verified", func(r *Record) { r.FenceClosure = FenceClosureClosed })
		if err != nil {
			t.Fatalf("settle onto the recorded target: %v", err)
		}
		if settled.Step != StepSucceeded || settled.FenceClosure != FenceClosureClosed {
			t.Fatalf("settled record = %+v, want succeeded with the proof", settled)
		}
	})
}

// TestArchiveReadSeparatesVerifiedFromLegacy pins the archive contract: an
// entry without a complete terminal proof is reported as unverified evidence
// with a reason instead of failing the read, it never appears in the verified
// view, and the file on disk is left untouched.
func TestArchiveReadSeparatesVerifiedFromLegacy(t *testing.T) {
	readArchive := func(t *testing.T, record any) ([]ArchivedEntry, []Record, string) {
		t.Helper()
		dir := t.TempDir()
		archiveDir := filepath.Join(dir, "archive")
		if err := os.MkdirAll(archiveDir, 0o700); err != nil {
			t.Fatal(err)
		}
		envelope := map[string]any{"schema_version": FormatVersion, "record": record}
		data, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(archiveDir, "journal-op-1-1.json")
		if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		journal := &FileJournal{path: filepath.Join(dir, "journal.json"), clock: func() time.Time { return time.Now().UTC() }}
		entries, readErr := journal.ReadArchive()
		if readErr != nil {
			t.Fatalf("ReadArchive: %v", readErr)
		}
		verified, verifiedErr := journal.ArchivedRecords()
		if verifiedErr != nil {
			t.Fatalf("ArchivedRecords: %v", verifiedErr)
		}
		preserved, preserveErr := os.ReadFile(path)
		if preserveErr != nil {
			t.Fatal(preserveErr)
		}
		if string(preserved) != string(data) {
			t.Fatal("reading the archive rewrote the file")
		}
		return entries, verified, string(data)
	}

	history := []TransitionEvent{{Step: StepPrepared}, {Step: StepFailed}}
	base := Record{
		SchemaVersion:         FormatVersion,
		OperationID:           "embedded-archived-op",
		Channel:               "embedded",
		Step:                  StepFailed,
		TargetReleaseID:       "release-archived",
		TargetReleaseSequence: 3,
		ManifestDigest:        strings.Repeat("a", 64),
		History:               history,
	}

	t.Run("an unproven entry is legacy evidence", func(t *testing.T) {
		entries, verified, _ := readArchive(t, base)
		if len(entries) != 1 || entries[0].Verified {
			t.Fatalf("entries = %+v, want one unverified entry", entries)
		}
		if !strings.Contains(entries[0].Reason, "fence closure") {
			t.Fatalf("legacy reason = %q, want the missing closure proof", entries[0].Reason)
		}
		if len(verified) != 0 {
			t.Fatalf("legacy evidence appeared in the verified view: %+v", verified)
		}
	})

	t.Run("a proven close is verified evidence", func(t *testing.T) {
		record := base
		record.FenceClosure = FenceClosureClosed
		entries, verified, _ := readArchive(t, record)
		if len(entries) != 1 || !entries[0].Verified || entries[0].Reason != "" {
			t.Fatalf("entries = %+v, want one verified entry", entries)
		}
		if len(verified) != 1 || verified[0].FenceClosure != FenceClosureClosed {
			t.Fatalf("verified view = %+v, want the closed record", verified)
		}
	})

	t.Run("the no-fence proof is verified only on a legal fence-less path", func(t *testing.T) {
		legal := base
		legal.FenceClosure = FenceClosureNotRequired
		if entries, verified, _ := readArchive(t, legal); !entries[0].Verified || len(verified) != 1 {
			t.Fatalf("fence-less entry = %+v / verified %+v, want verified", entries, verified)
		}
		reachedAccepted := legal
		reachedAccepted.History = []TransitionEvent{{Step: StepPrepared}, {Step: StepAccepted}, {Step: StepFailed}}
		entries, verified, _ := readArchive(t, reachedAccepted)
		if entries[0].Verified || len(verified) != 0 {
			t.Fatalf("an operation that reached accepted was verified fence-less: %+v", entries)
		}
	})

	t.Run("rollback_blocked is never verified evidence", func(t *testing.T) {
		record := base
		record.Step = StepRollbackBlocked
		record.FenceClosure = FenceClosureClosed
		entries, verified, _ := readArchive(t, record)
		if entries[0].Verified || len(verified) != 0 {
			t.Fatalf("rollback_blocked was treated as verified evidence: %+v", entries)
		}
	})

	t.Run("a succeeded record without a close is legacy", func(t *testing.T) {
		record := base
		record.Step = StepSucceeded
		entries, verified, _ := readArchive(t, record)
		if entries[0].Verified || len(verified) != 0 {
			t.Fatalf("succeeded without a close was verified: %+v", entries)
		}
		record.FenceClosure = FenceClosureClosed
		entries, verified, _ = readArchive(t, record)
		if !entries[0].Verified || len(verified) != 1 {
			t.Fatalf("succeeded with a close = %+v / verified %+v, want verified", entries, verified)
		}
	})

	t.Run("a record without a release-sequence floor is legacy", func(t *testing.T) {
		record := base
		record.FenceClosure = FenceClosureClosed
		record.TargetReleaseSequence = 0
		entries, verified, _ := readArchive(t, record)
		if entries[0].Verified || len(verified) != 0 {
			t.Fatalf("a sequence-less record was verified evidence: %+v", entries)
		}
		if !strings.Contains(entries[0].Reason, "release sequence") {
			t.Fatalf("legacy reason = %q, want the missing sequence floor", entries[0].Reason)
		}
	})

	t.Run("an undecodable entry is legacy, not fatal", func(t *testing.T) {
		dir := t.TempDir()
		archiveDir := filepath.Join(dir, "archive")
		if err := os.MkdirAll(archiveDir, 0o700); err != nil {
			t.Fatal(err)
		}
		raw := []byte("{ not an archive")
		path := filepath.Join(archiveDir, "journal-op-broken-1.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		journal := &FileJournal{path: filepath.Join(dir, "journal.json"), clock: func() time.Time { return time.Now().UTC() }}
		entries, err := journal.ReadArchive()
		if err != nil {
			t.Fatalf("ReadArchive with a broken entry: %v", err)
		}
		if len(entries) != 1 || entries[0].Verified || !strings.Contains(entries[0].Reason, "decodable") {
			t.Fatalf("broken entry = %+v, want an unverified entry with a reason", entries)
		}
		verified, verifiedErr := journal.ArchivedRecords()
		if verifiedErr != nil || len(verified) != 0 {
			t.Fatalf("verified view = %+v (err %v), want empty", verified, verifiedErr)
		}
		if preserved, readErr := os.ReadFile(path); readErr != nil || string(preserved) != string(raw) {
			t.Fatal("the broken archive file was rewritten")
		}
	})
}

// TestPostHandoffFailuresGoThroughTheRollbackChain pins the tightened
// post-handoff transitions: past the migration there is no direct `failed`
// abort anymore, and the pre-migration backup abort stays available.
func TestPostHandoffFailuresGoThroughTheRollbackChain(t *testing.T) {
	if !CanAdvance(StepPendingMigration, StepFailed) {
		t.Fatal("the pre-migration backup abort (pending_migration -> failed) must stay legal")
	}
	for _, from := range []Step{StepMigrationSucceeded, StepPromoted, StepReadinessFailed} {
		if CanAdvance(from, StepFailed) {
			t.Fatalf("%s -> failed must be refused: the rollback chain owns post-handoff failures", from)
		}
	}
	// The rollback chain itself stays intact.
	for _, transition := range []struct{ from, to Step }{
		{StepMigrationSucceeded, StepRollbackStarted},
		{StepPromoted, StepReadinessFailed},
		{StepReadinessFailed, StepRollbackStarted},
		{StepRollbackStarted, StepDatabaseRestored},
		{StepDatabaseRestored, StepRolledBack},
		{StepDatabaseRestored, StepRollbackBlocked},
	} {
		if !CanAdvance(transition.from, transition.to) {
			t.Fatalf("%s -> %s must stay legal", transition.from, transition.to)
		}
	}
}

// TestOpenRefusesSymlinkedJournalLayout pins the open-path hardening: a symlink
// at the journal, the lock file or the journal directory fails closed.
func TestOpenRefusesSymlinkedJournalLayout(t *testing.T) {
	t.Run("symlinked lock file", func(t *testing.T) {
		dir := t.TempDir()
		outside := t.TempDir()
		path := filepath.Join(dir, "journal.json")
		target := filepath.Join(outside, "stolen.lock")
		if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path+".lock"); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := Open(path); err == nil {
			t.Fatal("Open followed a symlinked lock file")
		}
		data, readErr := os.ReadFile(target)
		if readErr != nil || string(data) != "x" {
			t.Fatalf("the lock target was modified through the symlink: %q (%v)", data, readErr)
		}
	})

	t.Run("symlinked journal file", func(t *testing.T) {
		dir := t.TempDir()
		outside := t.TempDir()
		path := filepath.Join(dir, "journal.json")
		target := filepath.Join(outside, "journal.json")
		if err := os.WriteFile(target, []byte(legacyJournalJSON), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		// A symlinked journal is never read through the link: opening fails
		// closed instead of treating the link target as durable state.
		if _, err := Open(path); err == nil {
			t.Fatal("Open read a symlinked journal file")
		}
		preserved, readErr := os.ReadFile(target)
		if readErr != nil || string(preserved) != legacyJournalJSON {
			t.Fatalf("the symlink target was rewritten: %v", readErr)
		}
	})

	t.Run("symlinked journal directory", func(t *testing.T) {
		outside := t.TempDir()
		root := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "updates")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := Open(filepath.Join(root, "updates", "journal.json")); err == nil {
			t.Fatal("Open accepted a symlinked journal directory")
		}
		entries, readErr := os.ReadDir(outside)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("the journal layout escaped into the symlink target: %v", entries)
		}
	})
}
