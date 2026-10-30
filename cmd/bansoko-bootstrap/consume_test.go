package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	embeddedupdate "bansoko/internal/update/journal"
)

// --- test harness -----------------------------------------------------------

const (
	testTargetRelease = "rel-b"
	testPrevRelease   = "rel-a"
)

// seedConsumeJournal creates an operation record advanced through the given
// steps, applying mutate before the first Create so handoff fields (backup
// id, pointer bookkeeping) persist the way the real updater writes them.
func seedConsumeJournal(t *testing.T, root string, opID string,
	mutate func(*embeddedupdate.Record), steps ...embeddedupdate.Step) embeddedupdate.Record {
	t.Helper()
	flowJournal, err := embeddedupdate.Open(filepath.Join(root, journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer flowJournal.Close()
	record := embeddedupdate.Record{
		OperationID:     opID,
		Channel:         "embedded",
		TargetReleaseID: testTargetRelease,
		BackupID:        "bk1",
		ManifestDigest:  strings.Repeat("a", 64),
	}
	if mutate != nil {
		mutate(&record)
	}
	stored, createErr := flowJournal.Create(record)
	if createErr != nil {
		t.Fatal(createErr)
	}
	version := stored.Version
	for _, step := range steps {
		advanced, advanceErr := flowJournal.Advance(opID, version, step, "", "", nil)
		if advanceErr != nil {
			t.Fatalf("seed advance to %s: %v", step, advanceErr)
		}
		version = advanced.Version
	}
	final, found, loadErr := flowJournal.Load()
	if loadErr != nil || !found {
		t.Fatalf("load seeded record: found=%v err=%v", found, loadErr)
	}
	return final
}

// fxRecorder is a scripted consumeFX implementation recording every call in
// order for effect-sequence assertions.
type fxRecorder struct {
	calls []string

	verifyBackupErr error
	treeErrors      map[string]error
	migrateErr      error
	restoreErr      error
	launchErr       error

	probeQueue     []bool      // consumed per call; nil = always ready
	spawnRealChild bool        // first launch spawns /bin/sleep for ownership probes
	realSpawnN     int         // launches so far, used to limit the real child
	realCmds       []*exec.Cmd // real spawned children (for ownership assertions)

	launches    []string // binaryPath of each launch, in order
	promotes    []string
	removed     []string
	cleaned     int
	cleanupErr  error
	cleared     bool
	settled     int
	released    int
	settleErrs  []error
	releaseErrs []error
}

func (r *fxRecorder) fx() *consumeFX {
	return &consumeFX{
		StopMaintenance: func() error {
			r.calls = append(r.calls, "stop_maintenance")
			return nil
		},
		StartMaintenance: func() error {
			r.calls = append(r.calls, "start_maintenance")
			return nil
		},
		VerifyBackup: func(backupID string) error {
			r.calls = append(r.calls, "verify_backup:"+backupID)
			return r.verifyBackupErr
		},
		VerifyTargetTree: func(releaseID string) error {
			r.calls = append(r.calls, "verify_tree:"+releaseID)
			if r.treeErrors != nil {
				return r.treeErrors[releaseID]
			}
			return nil
		},
		RunMigrate: func(releaseID string) error {
			r.calls = append(r.calls, "migrate:"+releaseID)
			return r.migrateErr
		},
		RestoreDatabase: func(backupID string) error {
			r.calls = append(r.calls, "restore:"+backupID)
			return r.restoreErr
		},
		RemoveRelease: func(releaseID string) error {
			r.calls = append(r.calls, "remove:"+releaseID)
			r.removed = append(r.removed, releaseID)
			// Realistic effect: the rejected tree is actually gone, so a
			// fallback that still needs it cannot pass by accident.
			return os.RemoveAll(filepath.Join(currentFXRoot, releasesDirName, releaseID))
		},
		CleanupSucceeded: func(record embeddedupdate.Record) error {
			r.calls = append(r.calls, "cleanup_succeeded:"+record.OperationID)
			r.cleaned++
			return r.cleanupErr
		},
		PromoteCurrent: func(token string) error {
			r.calls = append(r.calls, "promote:"+token)
			r.promotes = append(r.promotes, token)
			return writeCurrentPointerAtomically(currentFXRoot, token)
		},
		ClearCurrent: func() error {
			r.calls = append(r.calls, "clear_pointer")
			r.cleared = true
			// Realistic effect: clearing the pointer removes the file, so a
			// silent base downgrade is observable in the pointer assertions.
			if err := os.Remove(filepath.Join(currentFXRoot, currentPointerName)); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		},
		LaunchTarget: func(binaryPath, schemaDir, frontendDir string, env map[string]string) (*childProcess, error) {
			r.calls = append(r.calls, "launch:"+binaryPath)
			r.launches = append(r.launches, binaryPath)
			if r.launchErr != nil {
				return nil, r.launchErr
			}
			r.realSpawnN++
			if r.spawnRealChild && r.realSpawnN == 1 {
				cmd := exec.Command("/bin/sleep", "60")
				child, startErr := spawnChild(cmd)
				if startErr != nil {
					return nil, startErr
				}
				r.realCmds = append(r.realCmds, cmd)
				tCleanupTrack = append(tCleanupTrack, child)
				return child, nil
			}
			return &childProcess{}, nil
		},
		ProbeReady: func() bool {
			r.calls = append(r.calls, "probe")
			if r.probeQueue == nil {
				return true
			}
			answer := r.probeQueue[0]
			r.probeQueue = r.probeQueue[1:]
			return answer
		},
		SettleOp: func(operationID string) error {
			r.calls = append(r.calls, "settle:"+operationID)
			r.settled++
			if len(r.settleErrs) > 0 {
				err := r.settleErrs[0]
				r.settleErrs = r.settleErrs[1:]
				return err
			}
			return nil
		},
		ReleaseOp: func(operationID string) error {
			r.calls = append(r.calls, "release:"+operationID)
			r.released++
			if len(r.releaseErrs) > 0 {
				err := r.releaseErrs[0]
				r.releaseErrs = r.releaseErrs[1:]
				return err
			}
			return nil
		},
		SettleExactOp: func(operationID, _ string, _ int64) error {
			r.calls = append(r.calls, "settle:"+operationID)
			r.settled++
			if len(r.settleErrs) > 0 {
				err := r.settleErrs[0]
				r.settleErrs = r.settleErrs[1:]
				return err
			}
			return nil
		},
		ReleaseExactOp: func(operationID, _ string, _ int64, _ string) error {
			r.calls = append(r.calls, "release:"+operationID)
			r.released++
			if len(r.releaseErrs) > 0 {
				err := r.releaseErrs[0]
				r.releaseErrs = r.releaseErrs[1:]
				return err
			}
			return nil
		},
	}
}

// currentFXRoot backs PromoteCurrent inside the recorder: consumeFX closures
// run within tests whose temp roots change per case; the harness rebinds it.
var currentFXRoot string

// tCleanupTrack keeps real children alive only for their assertion window.
var tCleanupTrack []*childProcess

func cleanupChildren() map[*exec.Cmd]error {
	results := map[*exec.Cmd]error{}
	for _, child := range tCleanupTrack {
		if child.cmd != nil && child.cmd.Process != nil {
			_ = child.Kill()
			results[child.cmd] = child.cmd.Wait()
		}
	}
	tCleanupTrack = nil
	return results
}

func tinyGate() readinessGate {
	return readinessGate{
		Interval:      time.Millisecond,
		Deadline:      250 * time.Millisecond,
		GraceFloor:    0,
		FailThreshold: 3,
	}
}

func pointerTokenForTest(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, currentPointerName))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

func mustCount(calls []string, prefix string) int {
	count := 0
	for _, entry := range calls {
		if strings.HasPrefix(entry, prefix) {
			count++
		}
	}
	return count
}

// drive is one harness helper: seed + run + assert record came back.
func drive(t *testing.T, root string, record embeddedupdate.Record, rec *fxRecorder) consumeOutcome {
	t.Helper()
	currentFXRoot = root
	t.Cleanup(func() { cleanupChildren(); currentFXRoot = "" })

	flowJournal, openErr := embeddedupdate.Open(filepath.Join(root, journalFileName))
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer flowJournal.Close()

	outcome := runConsume(flowJournal, record, rec.fx(), tinyGate(), root)
	final, _, _ := flowJournal.Load()
	recordCheckStep := final.Step
	t.Logf("driven outcome kind=%d step=%s", outcome.kind, recordCheckStep)
	return outcome
}

// --- state-machine branch tests ---------------------------------------------

func TestCleanupSucceededOperationRemovesOnlyOperationWork(t *testing.T) {
	root := t.TempDir()
	record := embeddedupdate.Record{
		OperationID: "op-clean", Step: embeddedupdate.StepSucceeded,
		TargetReleaseID: testTargetRelease, CurrentReleaseID: testTargetRelease,
		PreviousReleaseID: testPrevRelease, BackupID: "bk-clean",
	}
	for _, dir := range []string{
		relDirOf(root, testTargetRelease), relDirOf(root, testPrevRelease),
		relDirOf(root, "rel-unrelated"), filepath.Join(root, "downloads", record.OperationID),
		filepath.Join(root, "downloads", "op-other"), filepath.Join(releaseRoot(root), ".tmp-"+record.OperationID),
		filepath.Join(root, backupsDirName),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		backupsDumpPath(root, record.BackupID), backupsDumpPath(root, record.BackupID) + ".sha256",
		backupsDumpPath(root, "bk-other"), backupsDumpPath(root, "bk-other") + ".sha256",
	} {
		if err := os.WriteFile(path, []byte("artifact"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := cleanupSucceededOperation(root, record); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		relDirOf(root, testPrevRelease), filepath.Join(root, "downloads", record.OperationID),
		filepath.Join(releaseRoot(root), ".tmp-"+record.OperationID),
		backupsDumpPath(root, record.BackupID), backupsDumpPath(root, record.BackupID) + ".sha256",
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("successful operation artifact remains at %s: %v", path, err)
		}
	}
	for _, path := range []string{
		relDirOf(root, testTargetRelease), relDirOf(root, "rel-unrelated"),
		filepath.Join(root, "downloads", "op-other"),
		backupsDumpPath(root, "bk-other"), backupsDumpPath(root, "bk-other") + ".sha256",
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unrelated or current artifact removed at %s: %v", path, err)
		}
	}
	if err := cleanupSucceededOperation(root, record); err != nil {
		t.Fatalf("idempotent cleanup replay: %v", err)
	}
}

func TestCleanupSucceededOperationWithoutPreviousOverlayKeepsTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(relDirOf(root, testTargetRelease), 0o700); err != nil {
		t.Fatal(err)
	}
	record := embeddedupdate.Record{
		OperationID: "op-base", Step: embeddedupdate.StepSucceeded,
		TargetReleaseID: testTargetRelease, CurrentReleaseID: testTargetRelease, BackupID: "bk-base",
	}
	if err := cleanupSucceededOperation(root, record); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(relDirOf(root, testTargetRelease)); err != nil {
		t.Fatalf("base update cleanup removed current target: %v", err)
	}
}

// indexOfCall returns the first index whose entry starts with prefix, or -1.
func indexOfCall(calls []string, prefix string) int {
	for i, entry := range calls {
		if strings.HasPrefix(entry, prefix) {
			return i
		}
	}
	return -1
}

// TestConsumeStopsMaintenanceResponderBeforeTargetLaunch pins the port handover
// order: the supervised application must never race the health responder for the
// listen address, so the responder is stopped immediately before the launch.
func TestConsumeStopsMaintenanceResponderBeforeTargetLaunch(t *testing.T) {
	root := t.TempDir()
	writeReleaseTree(t, root, testPrevRelease)
	seedReleaseProvenance(t, root, testPrevRelease)
	writeReleaseTree(t, root, testTargetRelease)
	writePointer(t, root, testPrevRelease)
	recorder := &fxRecorder{}

	record := seedConsumeJournal(t, root, "op-responder-order", nil,
		embeddedupdate.StepAccepted, embeddedupdate.StepFetched, embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration)

	outcome := drive(t, root, record, recorder)
	if outcome.kind != outcomeExit || outcome.exitCode != 0 {
		t.Fatalf("outcome = %+v, want clean exit", outcome)
	}
	stopIdx := indexOfCall(recorder.calls, "stop_maintenance")
	launchIdx := indexOfCall(recorder.calls, "launch:")
	if stopIdx == -1 || launchIdx == -1 || stopIdx > launchIdx {
		t.Fatalf("stop=%d launch=%d calls=%v, want the responder stopped before the target launch", stopIdx, launchIdx, recorder.calls)
	}
}

// TestConsumeRestartsMaintenanceResponderAfterRejectedTarget proves the held
// responder comes back when the supervised target is rejected, so an operator can
// still observe the parked/rollback state through the health endpoint instead of
// losing the listener to the failed child.
func TestConsumeRestartsMaintenanceResponderAfterRejectedTarget(t *testing.T) {
	root := t.TempDir()
	writeReleaseTree(t, root, testPrevRelease)
	seedReleaseProvenance(t, root, testPrevRelease)
	writeReleaseTree(t, root, testTargetRelease)
	writePointer(t, root, testPrevRelease)
	recorder := &fxRecorder{probeQueue: []bool{false, false, false, true, true, true}}

	record := seedConsumeJournal(t, root, "op-responder-restart", nil,
		embeddedupdate.StepAccepted, embeddedupdate.StepFetched, embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration)

	_ = drive(t, root, record, recorder)

	firstLaunch := indexOfCall(recorder.calls, "launch:")
	restart := indexOfCall(recorder.calls, "start_maintenance")
	if firstLaunch == -1 || restart == -1 || restart < firstLaunch {
		t.Fatalf("launch=%d restart=%d calls=%v, want the responder restarted after the rejected target", firstLaunch, restart, recorder.calls)
	}
	secondStop := -1
	for i := restart + 1; i < len(recorder.calls); i++ {
		if recorder.calls[i] == "stop_maintenance" {
			secondStop = i
			break
		}
	}
	if secondStop == -1 {
		t.Fatalf("calls=%v, want a second responder stop before the rollback fallback launch", recorder.calls)
	}
}

// TestResponderLifecycleRejectsDuplicateStartAndRebindsAfterStop pins the
// single-owner lifecycle contract: a second start while active is an error, a
// stop with no listener is an error, and a restart after stop binds a fresh
// listener instead of recycling a closed handle.
func TestResponderLifecycleRejectsDuplicateStartAndRebindsAfterStop(t *testing.T) {
	starts, stops := 0, 0
	lifecycle := &responderLifecycle{startResponder: func() (func() error, error) {
		starts++
		return func() error { stops++; return nil }, nil
	}}
	if err := lifecycle.Stop(); err == nil {
		t.Fatal("stopping an inactive responder succeeded")
	}
	if err := lifecycle.Start(); err != nil {
		t.Fatalf("first start failed: %v", err)
	}
	if err := lifecycle.Start(); err == nil {
		t.Fatal("a duplicate start while the responder is active succeeded")
	}
	if err := lifecycle.Stop(); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if err := lifecycle.Start(); err != nil {
		t.Fatalf("restart after stop failed: %v", err)
	}
	if err := lifecycle.Stop(); err != nil {
		t.Fatalf("final stop failed: %v", err)
	}
	if starts != 2 || stops != 2 {
		t.Fatalf("starts=%d stops=%d, want 2/2 (each start binds a fresh listener)", starts, stops)
	}
}

func TestConsumePendingMigrationHappyPathSucceedsAndSettles(t *testing.T) {
	root := t.TempDir()
	writeReleaseTree(t, root, testPrevRelease)
	seedReleaseProvenance(t, root, testPrevRelease)
	writeReleaseTree(t, root, testTargetRelease)
	writePointer(t, root, testPrevRelease)
	recorder := &fxRecorder{}

	record := seedConsumeJournal(t, root, "op-happy", nil,
		embeddedupdate.StepAccepted, embeddedupdate.StepFetched, embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration)

	outcome := drive(t, root, record, recorder)

	if outcome.kind != outcomeExit || outcome.exitCode != 0 {
		t.Fatalf("outcome = %+v, want clean exit", outcome)
	}
	final := reloadRecordForTest(t, root)
	if final.Step != embeddedupdate.StepSucceeded {
		t.Fatalf("final step %q, want succeeded", final.Step)
	}
	if final.FenceClosure != embeddedupdate.FenceClosureClosed {
		t.Fatalf("final closure proof = %q, want %q (the terminal checkpoint records the proven close)",
			final.FenceClosure, embeddedupdate.FenceClosureClosed)
	}
	if final.CurrentReleaseID != testTargetRelease {
		t.Fatalf("CurrentReleaseID %q, want rel-b", final.CurrentReleaseID)
	}
	if final.PreviousReleaseID != testPrevRelease {
		t.Fatalf("PreviousReleaseID %q, want rel-a", final.PreviousReleaseID)
	}
	if final.Attempts.Migrate != 1 {
		t.Fatalf("migrate attempts %d, want 1", final.Attempts.Migrate)
	}
	if got := pointerTokenForTest(t, root); got != testTargetRelease {
		t.Fatalf("pointer token %q, want rel-b", got)
	}
	if recorder.settled != 1 || recorder.released != 0 {
		t.Fatalf("settle/release counts %d/%d, want 1/0", recorder.settled, recorder.released)
	}
	if recorder.cleaned != 1 {
		t.Fatalf("successful cleanup calls = %d, want 1", recorder.cleaned)
	}
	if got := mustCount(recorder.calls, "verify_backup:"); got != 1 {
		t.Fatalf("verify_backup calls %d, want 1", got)
	}
	for _, want := range []string{"verify_tree:", "migrate:", "promote:"} {
		if got := mustCount(recorder.calls, want); got != 1 {
			t.Fatalf("%s calls %d, want 1 (calls=%v)", want, got, recorder.calls)
		}
	}
	if len(recorder.launches) != 1 ||
		filepath.Dir(filepath.Dir(recorder.launches[0])) != filepath.Join(releaseRoot(root)) ||
		filepath.Base(filepath.Dir(recorder.launches[0])) != testTargetRelease {
		t.Fatalf("launches %v, want exactly one target-server launch under releases/rel-b", recorder.launches)
	}
}

func TestConsumeSuccessCleanupFailureDoesNotReopenUpdate(t *testing.T) {
	root := t.TempDir()
	writeReleaseTree(t, root, testPrevRelease)
	seedReleaseProvenance(t, root, testPrevRelease)
	writeReleaseTree(t, root, testTargetRelease)
	writePointer(t, root, testPrevRelease)
	recorder := &fxRecorder{cleanupErr: errors.New("temporary cleanup failure")}
	record := seedConsumeJournal(t, root, "op-cleanup-failure", nil,
		embeddedupdate.StepAccepted, embeddedupdate.StepFetched, embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration)

	outcome := drive(t, root, record, recorder)
	if outcome.kind != outcomeExit || outcome.exitCode != 0 {
		t.Fatalf("outcome = %+v, want successful update despite cleanup failure", outcome)
	}
	final := reloadRecordForTest(t, root)
	if final.Step != embeddedupdate.StepSucceeded || final.FenceClosure != embeddedupdate.FenceClosureClosed {
		t.Fatalf("cleanup failure changed terminal success: %+v", final)
	}
	if recorder.cleaned != 1 || recorder.released != 0 || mustCount(recorder.calls, "restore:") != 0 {
		t.Fatalf("cleanup failure reopened rollback or fence: calls=%v", recorder.calls)
	}
}

func TestConsumeFenceClosureFailureStaysDurableAndReplays(t *testing.T) {
	root := t.TempDir()
	writeReleaseTree(t, root, testPrevRelease)
	seedReleaseProvenance(t, root, testPrevRelease)
	writeReleaseTree(t, root, testTargetRelease)
	writePointer(t, root, testPrevRelease)
	recorder := &fxRecorder{settleErrs: []error{errors.New("temporary database outage")}}
	record := seedConsumeJournal(t, root, "op-fence-retry", nil,
		embeddedupdate.StepAccepted, embeddedupdate.StepFetched, embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration)

	first := drive(t, root, record, recorder)
	if first.kind != outcomeParked || first.blockedStep != embeddedupdate.StepFenceClosurePending {
		t.Fatalf("first outcome = %+v, want parked fence closure pending", first)
	}
	pending := reloadRecordForTest(t, root)
	if pending.Step != embeddedupdate.StepFenceClosurePending || pending.FenceClosureTarget != embeddedupdate.StepSucceeded {
		t.Fatalf("pending record = %+v, want durable succeeded closure target", pending)
	}

	second := drive(t, root, pending, recorder)
	if second.kind != outcomeExit {
		t.Fatalf("replay outcome = %+v, want clean exit", second)
	}
	final := reloadRecordForTest(t, root)
	if final.Step != embeddedupdate.StepSucceeded {
		t.Fatalf("replayed final step = %q, want succeeded", final.Step)
	}
	if final.FenceClosure != embeddedupdate.FenceClosureClosed {
		t.Fatalf("replayed closure proof = %q, want %q", final.FenceClosure, embeddedupdate.FenceClosureClosed)
	}
	if recorder.settled != 2 {
		t.Fatalf("settle calls = %d, want initial attempt plus replay", recorder.settled)
	}
	if recorder.cleaned != 1 {
		t.Fatalf("successful replay cleanup calls = %d, want 1", recorder.cleaned)
	}
}

func TestConsumeMigrationFailureRestoresAndRollsBackToPrevious(t *testing.T) {
	root := t.TempDir()
	writeReleaseTree(t, root, testPrevRelease)
	seedReleaseProvenance(t, root, testPrevRelease)
	writeReleaseTree(t, root, testTargetRelease)
	writePointer(t, root, testPrevRelease)
	recorder := &fxRecorder{migrateErr: errors.New("migration boom")}

	record := seedConsumeJournal(t, root, "op-migfail", nil,
		embeddedupdate.StepAccepted, embeddedupdate.StepFetched, embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration)

	outcome := drive(t, root, record, recorder)

	if outcome.kind != outcomeExit {
		t.Fatalf("outcome kind %+v, want supervised rollback exit", outcome)
	}
	final := reloadRecordForTest(t, root)
	if final.Step != embeddedupdate.StepRolledBack {
		t.Fatalf("final step %q, want rolled_back", final.Step)
	}
	if final.Attempts.Migrate != 1 {
		t.Fatalf("migrate attempts %d, want 1", final.Attempts.Migrate)
	}
	if got := pointerTokenForTest(t, root); got != testPrevRelease {
		t.Fatalf("pointer %q, want restored rel-a", got)
	}
	// The recorder never deletes on disk; removal is asserted via effect log.
	if len(recorder.removed) == 0 || recorder.removed[len(recorder.removed)-1] != testTargetRelease {
		t.Fatalf("removals %v, want target tree removed after restore", recorder.removed)
	}
	if recorder.settled != 0 || recorder.released != 1 {
		t.Fatalf("settle/release counts %d/%d, want 0/1", recorder.settled, recorder.released)
	}
	if got := mustCount(recorder.calls, "restore:b"); got != 1 {
		t.Fatalf("restore calls %d, want 1 (calls=%v)", got, recorder.calls)
	}
	// The fallback supervision round must serve the previous release.
	lastLaunch := recorder.launches[len(recorder.launches)-1]
	if !strings.Contains(lastLaunch, filepath.Join(testPrevRelease)+string(os.PathSeparator)) {
		t.Fatalf("fallback launch %s does not point at previous release", lastLaunch)
	}
	// The captured source must be durable and must still name the previous
	// overlay, not the rejected target and not base.
	if !final.RollbackSourceCaptured || final.PreviousReleaseID != testPrevRelease {
		t.Fatalf("captured rollback source = %+v, want durable rel-a", final)
	}
	if final.CurrentReleaseID != testPrevRelease {
		t.Fatalf("CurrentReleaseID %q, want rel-a after rollback", final.CurrentReleaseID)
	}
	if info, statErr := os.Stat(filepath.Join(root, releasesDirName, testPrevRelease)); statErr != nil || !info.IsDir() {
		t.Fatalf("previous overlay tree must survive rollback: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, releasesDirName, testTargetRelease)); !os.IsNotExist(statErr) {
		t.Fatalf("rejected target tree must be removed, stat err = %v", statErr)
	}
}

func TestConsumeRestoreFailureParksRollbackBlocked(t *testing.T) {
	root := t.TempDir()
	writeReleaseTree(t, root, testPrevRelease)
	seedReleaseProvenance(t, root, testPrevRelease)
	writePointer(t, root, testPrevRelease)
	recorder := &fxRecorder{restoreErr: errors.New("pg_restore exploded")}

	prevMutation := func(r *embeddedupdate.Record) {
		r.PreviousReleaseID = testPrevRelease
		r.RollbackSourceCaptured = true
	}
	record := seedConsumeJournal(t, root, "op-restorefail", prevMutation,
		embeddedupdate.StepAccepted, embeddedupdate.StepFetched, embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration, embeddedupdate.StepMigrationSucceeded,
		embeddedupdate.StepPromoted, embeddedupdate.StepReadinessFailed,
		embeddedupdate.StepRollbackStarted)

	outcome := drive(t, root, record, recorder)

	if outcome.kind != outcomeParked || !outcome.blockedIsTerminal {
		t.Fatalf("outcome %+v, want terminal parked blocked", outcome)
	}
	final := reloadRecordForTest(t, root)
	if final.Step != embeddedupdate.StepRollbackBlocked {
		t.Fatalf("final step %q, want rollback_blocked", final.Step)
	}
	if got := pointerTokenForTest(t, root); got != testPrevRelease {
		t.Fatalf("pointer %q changed during blocked rollback, want untouched rel-a", got)
	}
	if got := mustCount(recorder.calls, "probe"); got != 0 {
		t.Fatalf("blocked path probed serving %d times, want none", got)
	}
	if got := mustCount(recorder.calls, "promote:"); got != 0 {
		t.Fatalf("blocked path promoted %d times, want none", got)
	}
}

func TestConsumeRollbackBlockedNeverRetriesOrReleasesFence(t *testing.T) {
	root := t.TempDir()
	flowJournal, err := embeddedupdate.Open(filepath.Join(root, journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	created, err := flowJournal.Create(embeddedupdate.Record{
		OperationID:     "blocked-retry",
		Channel:         "embedded",
		TargetReleaseID: "release-blocked",
		Step:            embeddedupdate.StepRollbackBlocked,
	})
	if err != nil {
		t.Fatal(err)
	}
	fenceClosed := false
	fx := &consumeFX{
		ReleaseExactOp: func(string, string, int64, string) error { fenceClosed = true; return nil },
		SettleExactOp:  func(string, string, int64) error { fenceClosed = true; return nil },
	}
	outcome := runConsume(flowJournal, created, fx, tinyGate(), root)
	if outcome.kind != outcomeParked || !outcome.blockedIsTerminal {
		t.Fatalf("rollback-blocked retry outcome = %+v, want terminal park", outcome)
	}
	if fenceClosed {
		t.Fatal("rollback-blocked retry must not close its owner fence")
	}
	flowJournal.Close()
}

func TestConsumeReadinessBreachKillsTargetAndRollsBack(t *testing.T) {
	root := t.TempDir()
	writeReleaseTree(t, root, testPrevRelease)
	seedReleaseProvenance(t, root, testPrevRelease)
	writeReleaseTree(t, root, testTargetRelease)
	// The journal below is seeded at promoted, so the target was promoted by an
	// earlier operation and its provenance already exists: that is the state a
	// replay from that checkpoint always finds.
	seedReleaseProvenance(t, root, testTargetRelease)
	writePointer(t, root, testPrevRelease)
	recorder := &fxRecorder{probeQueue: []bool{false, false, false, true}, spawnRealChild: true}

	// A promoted journal from the current version always carries the rollback
	// source captured before migration; legacy promoted journals without it are
	// covered by TestConsumeLegacyPromotedJournalWithoutSourceBlocksRollback.
	captured := func(r *embeddedupdate.Record) {
		r.PreviousReleaseID = testPrevRelease
		r.RollbackSourceCaptured = true
	}
	record := seedConsumeJournal(t, root, "op-readiness", captured,
		embeddedupdate.StepAccepted, embeddedupdate.StepFetched, embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration, embeddedupdate.StepMigrationSucceeded,
		embeddedupdate.StepPromoted)

	outcome := drive(t, root, record, recorder)

	if outcome.kind != outcomeExit {
		t.Fatalf("outcome %+v, want exit after rollback fallback", outcome)
	}
	final := reloadRecordForTest(t, root)
	if final.Step != embeddedupdate.StepRolledBack {
		t.Fatalf("final step %q, want rolled_back after readiness breach", final.Step)
	}
	if final.Attempts.Readiness == 0 {
		t.Fatal("readiness attempt counter stayed empty despite breaches")
	}
	if got := pointerTokenForTest(t, root); got != testPrevRelease {
		t.Fatalf("pointer %q, want previous rel-a", got)
	}
	if recorder.released != 1 || recorder.settled != 0 {
		t.Fatalf("fence outcome released=%d settled=%d, want 1/0", recorder.released, recorder.settled)
	}
	if len(recorder.removed) == 0 || recorder.removed[len(recorder.removed)-1] != testTargetRelease {
		t.Fatalf("removals %v, want target tree removed", recorder.removed)
	}
	if len(recorder.realCmds) == 0 {
		t.Skip("/bin/sleep unavailable for child-process ownership probe")
	}
	processState := recorder.realCmds[0].ProcessState
	if processState == nil || !processState.Sys().(syscall.WaitStatus).Signaled() {
		t.Fatalf("rejected target process state = %v, want kill signal after threshold breach", processState)
	}
}

func TestConsumeSeedResumeMatrix(t *testing.T) {
	for tcName, tc := range map[string]struct {
		steps        []embeddedupdate.Step
		wantStep     embeddedupdate.Step
		wantRestoreN int
	}{
		"readiness_failed": {
			// Seeded through the full legal transition chain so the CAS
			// history matches what the real updater/bootstrap writes.
			steps: []embeddedupdate.Step{
				embeddedupdate.StepFetched, embeddedupdate.StepStaged,
				embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
				embeddedupdate.StepPendingMigration, embeddedupdate.StepMigrationSucceeded,
				embeddedupdate.StepPromoted, embeddedupdate.StepReadinessFailed,
			},
			wantStep:     embeddedupdate.StepRolledBack,
			wantRestoreN: 1,
		},
		"rollback_started": {
			steps: []embeddedupdate.Step{
				embeddedupdate.StepFetched, embeddedupdate.StepStaged,
				embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
				embeddedupdate.StepPendingMigration, embeddedupdate.StepRollbackStarted,
			},
			wantStep:     embeddedupdate.StepRolledBack,
			wantRestoreN: 1,
		},
		"database_restored": {
			steps: []embeddedupdate.Step{
				embeddedupdate.StepFetched, embeddedupdate.StepStaged,
				embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
				embeddedupdate.StepPendingMigration, embeddedupdate.StepRollbackStarted,
				embeddedupdate.StepDatabaseRestored,
			},
			wantStep:     embeddedupdate.StepRolledBack,
			wantRestoreN: 0,
		},
	} {
		t.Run(tcName, func(t *testing.T) {
			root := t.TempDir()
			writeReleaseTree(t, root, testPrevRelease)
			seedReleaseProvenance(t, root, testPrevRelease)
			writePointer(t, root, testPrevRelease)
			recorder := &fxRecorder{}
			mutate := func(r *embeddedupdate.Record) {
				r.PreviousReleaseID = testPrevRelease
				r.RollbackSourceCaptured = true
			}
			record := seedConsumeJournal(t, root, "op-"+tcName, mutate, tc.steps...)

			outcome := drive(t, root, record, recorder)
			if outcome.kind != outcomeExit {
				t.Fatalf("outcome %+v, want supervised exit", outcome)
			}
			final := reloadRecordForTest(t, root)
			if final.Step != tc.wantStep {
				t.Fatalf("final step %q, want %s", final.Step, tc.wantStep)
			}
			if got := mustCount(recorder.calls, "restore:"); got != tc.wantRestoreN {
				t.Fatalf("restore calls %d, want %d", got, tc.wantRestoreN)
			}
		})
	}
}

func TestConsumePreHandoffJournalStaysUnconsumedFromServerEntry(t *testing.T) {
	// Directly covers planLaunch classification rather than driving effects:
	// accepted..backup_verified remain maintenance-fenced from server boots.
	root := t.TempDir()
	journalWithSteps(t, root, "op-prehandoff",
		embeddedupdate.StepFetched)
	got := planLaunch("server", root)
	if got.mode != modeMaintenance {
		t.Fatalf("pre-handoff step classified %+v, want maintenance", got)
	}
	migrated := planLaunch("migrate", root)
	if migrated.mode != modeMaintenance {
		t.Fatalf("migrate job on handoff family must stay fenced, got %+v", migrated)
	}
}

// --- filesystem / helper unit tests -----------------------------------------

func TestWriteCurrentPointerAtomicallyValidatesTokens(t *testing.T) {
	root := t.TempDir()
	if err := writeCurrentPointerAtomically(root, "../escape"); err == nil {
		t.Fatal("traversal token promoted")
	}
	if err := writeCurrentPointerAtomically(root, ""); err == nil {
		t.Fatal("empty token promoted")
	}
	if err := writeCurrentPointerAtomically(root, "rel-x"); err != nil {
		t.Fatalf("valid token refused: %v", err)
	}
	if err := writeCurrentPointerAtomically(root, "rel-y"); err != nil {
		t.Fatalf("overwrite refused: %v", err)
	}
	if got := pointerTokenForTest(t, root); got != "rel-y" {
		t.Fatalf("pointer after overwrite %q", got)
	}
}

func TestVerifyBackupSidecarRejectsTamperAndAbsence(t *testing.T) {
	root := t.TempDir()
	backups := filepath.Join(root, backupsDirName)
	if mkdirErr := os.MkdirAll(backups, 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	dump := filepath.Join(backups, "bk9.dump")
	if writeErr := os.WriteFile(dump, []byte("archive-bytes"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	digest, hashErr := hashFileSHA256(dump)
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	if sidecarErr := os.WriteFile(dump+".sha256", []byte(digest+"\n"), 0o600); sidecarErr != nil {
		t.Fatal(sidecarErr)
	}
	if _, verifyErr := verifyBackupSidecar(root, "bk9"); verifyErr != nil {
		t.Fatalf("intact backup rejected: %v", verifyErr)
	}
	if tamperErr := os.WriteFile(dump, []byte("tampered-bytes!"), 0o600); tamperErr != nil {
		t.Fatal(tamperErr)
	}
	if _, verifyErr := verifyBackupSidecar(root, "bk9"); verifyErr == nil {
		t.Fatal("tampered dump accepted by sidecar check")
	}
	if _, missingErr := verifyBackupSidecar(root, "bk-missing"); missingErr == nil {
		t.Fatal("missing backup accepted")
	}
}

func TestProbeTargetBaseDerivesLoopbackEndpoint(t *testing.T) {
	for addr, want := range map[string]string{
		"0.0.0.0:8080": "127.0.0.1:8080",
		"":             "127.0.0.1:8080",
		// A literal bind address is probed as-is: looping back would miss a
		// server bound to a single interface.
		"192.168.1.5:81": "192.168.1.5:81",
	} {
		if got := probeTargetBase(addr); got != want {
			t.Fatalf("probeTargetBase(%q)=%q, want %q", addr, got, want)
		}
	}
}

// --- rollback provenance ------------------------------------------------
//
// These cover the two ways the pre-promotion pointer evidence can be lost:
// a crash between the pointer rename and the promoted checkpoint, and a
// journal written before the source was captured at all. Neither may adopt the
// rejected target or silently downgrade to base.

func TestConsumePromotionCheckpointCrashDoesNotAdoptTargetAsPrevious(t *testing.T) {
	root := t.TempDir()
	writeReleaseTree(t, root, testPrevRelease)
	seedReleaseProvenance(t, root, testPrevRelease)
	writeReleaseTree(t, root, testTargetRelease)
	// Crash window: promotion already renamed the pointer to the target while
	// the journal still records migration_succeeded.
	writePointer(t, root, testTargetRelease)
	recorder := &fxRecorder{}

	record := seedConsumeJournal(t, root, "op-promote-crash", nil,
		embeddedupdate.StepAccepted, embeddedupdate.StepFetched, embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration, embeddedupdate.StepMigrationSucceeded)

	outcome := drive(t, root, record, recorder)

	if outcome.kind != outcomeParked {
		t.Fatalf("outcome %+v, want fail-closed park", outcome)
	}
	final := reloadRecordForTest(t, root)
	if final.RollbackSourceCaptured || final.PreviousReleaseID != "" {
		t.Fatalf("record adopted a rollback source across the crash window: %+v", final)
	}
	if embeddedupdate.Terminal(final.Step) {
		t.Fatalf("crash-window journal reached terminal step %q", final.Step)
	}
	if got := pointerTokenForTest(t, root); got != testTargetRelease {
		t.Fatalf("pointer %q, want the crash-window pointer left untouched", got)
	}
	if got := mustCount(recorder.calls, "promote:"); got != 0 {
		t.Fatalf("promote calls %d, want 0 while the source is unprovable", got)
	}
}

func TestConsumeLegacyPromotedJournalWithoutSourceBlocksRollback(t *testing.T) {
	root := t.TempDir()
	writeReleaseTree(t, root, testPrevRelease)
	seedReleaseProvenance(t, root, testPrevRelease)
	writeReleaseTree(t, root, testTargetRelease)
	// Promotion happened before this build captured sources, and readiness then
	// breached: the pointer names the target, so the fallback is unknowable.
	writePointer(t, root, testTargetRelease)
	recorder := &fxRecorder{}

	record := seedConsumeJournal(t, root, "op-legacy-promoted", nil,
		embeddedupdate.StepAccepted, embeddedupdate.StepFetched, embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration, embeddedupdate.StepMigrationSucceeded,
		embeddedupdate.StepPromoted, embeddedupdate.StepReadinessFailed)

	outcome := drive(t, root, record, recorder)

	if outcome.kind != outcomeParked || !outcome.blockedIsTerminal {
		t.Fatalf("outcome %+v, want terminal rollback_blocked park", outcome)
	}
	final := reloadRecordForTest(t, root)
	if final.Step != embeddedupdate.StepRollbackBlocked {
		t.Fatalf("final step %q, want rollback_blocked", final.Step)
	}
	if got := pointerTokenForTest(t, root); got != testTargetRelease {
		t.Fatalf("pointer %q, want the rejected target left in place rather than base", got)
	}
	if got := mustCount(recorder.calls, "clear_pointer"); got != 0 {
		t.Fatalf("clear_pointer calls %d, want 0 for an ambiguous source", got)
	}
}

func TestConsumePromotedLegacyJournalRecoversRecordedSource(t *testing.T) {
	root := t.TempDir()
	writeReleaseTree(t, root, testPrevRelease)
	seedReleaseProvenance(t, root, testPrevRelease)
	writeReleaseTree(t, root, testTargetRelease)
	// The pointer already moved to the target, but the promoted checkpoint had
	// recorded the previous release before the explicit captured flag existed.
	writePointer(t, root, testTargetRelease)
	recorder := &fxRecorder{}
	legacy := func(r *embeddedupdate.Record) { r.PreviousReleaseID = testPrevRelease }

	record := seedConsumeJournal(t, root, "op-promoted-legacy", legacy,
		embeddedupdate.StepAccepted, embeddedupdate.StepFetched, embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration, embeddedupdate.StepMigrationSucceeded,
		embeddedupdate.StepPromoted, embeddedupdate.StepReadinessFailed)

	outcome := drive(t, root, record, recorder)

	if outcome.kind != outcomeExit {
		t.Fatalf("outcome %+v, want supervised rollback exit", outcome)
	}
	final := reloadRecordForTest(t, root)
	if final.Step != embeddedupdate.StepRolledBack {
		t.Fatalf("final step %q, want rolled_back", final.Step)
	}
	if !final.RollbackSourceCaptured || final.PreviousReleaseID != testPrevRelease {
		t.Fatalf("recorded source was not adopted durably: %+v", final)
	}
	if got := pointerTokenForTest(t, root); got != testPrevRelease {
		t.Fatalf("pointer %q, want restored rel-a", got)
	}
}

func TestConsumeMigrationFailureFallsBackToBaseWithoutPointer(t *testing.T) {
	root := t.TempDir()
	// No previous overlay and no pointer: base is the only rollback source.
	writeReleaseTree(t, root, testTargetRelease)
	recorder := &fxRecorder{migrateErr: errors.New("migration boom")}

	record := seedConsumeJournal(t, root, "op-base-fallback", nil,
		embeddedupdate.StepAccepted, embeddedupdate.StepFetched, embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration)

	outcome := drive(t, root, record, recorder)

	if outcome.kind != outcomeExit {
		t.Fatalf("outcome %+v, want supervised base fallback exit", outcome)
	}
	final := reloadRecordForTest(t, root)
	if final.Step != embeddedupdate.StepRolledBack {
		t.Fatalf("final step %q, want rolled_back", final.Step)
	}
	if !final.RollbackSourceCaptured || final.PreviousReleaseID != "" {
		t.Fatalf("base fallback must be captured explicitly: %+v", final)
	}
	if got := pointerTokenForTest(t, root); got != "" {
		t.Fatalf("pointer %q, want cleared for base fallback", got)
	}
}

// --- journal access helper ---------------------------------------------------

func reloadRecordForTest(t *testing.T, root string) embeddedupdate.Record {
	t.Helper()
	flowJournal, openErr := embeddedupdate.Open(filepath.Join(root, journalFileName))
	if openErr != nil {
		t.Fatalf("reload journal: %v", openErr)
	}
	defer flowJournal.Close()
	record, found, loadErr := flowJournal.Load()
	if loadErr != nil || !found {
		t.Fatalf("reload record: found=%v err=%v", found, loadErr)
	}
	return record
}

func TestReadPendingMarkerRequiresExactFields(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, pendingMarkerName)
	valid := `{"operation_id":"marker-op","release_id":"release-2","manifest_digest":"` + strings.Repeat("a", 64) + `","release_sequence":2,"previous_release_id":"release-1"}`
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	marker, err := readPendingMarker(path)
	if err != nil || marker.OperationID != "marker-op" || marker.ReleaseSequence != 2 {
		t.Fatalf("marker=%+v err=%v", marker, err)
	}
	for _, raw := range []string{
		`{"operation_id":"marker-op"}`,
		strings.TrimSuffix(valid, "}") + `,"extra":true}`,
		strings.Replace(valid, `"release_sequence":2`, `"release_sequence":"2"`, 1),
	} {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readPendingMarker(path); err == nil {
			t.Fatalf("accepted malformed marker %s", raw)
		}
	}
}
