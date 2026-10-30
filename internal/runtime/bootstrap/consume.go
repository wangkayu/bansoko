// Consume flow for the embedded-update handoff (task Sub B Step 4). This file
// implements the PID1 side of the chain: consuming a durable journal record
// from pending_migration onward, running the overlay migration, promoting the
// release pointer, supervising the target server with a bounded readiness
// probe, and driving the automatic rollback chain (readiness failure →
// restore verified backup → previous/base fallback) or the fail-closed
// rollback_blocked terminal state.
//
// Effect discipline: every journal checkpoint is written AFTER the effect it
// describes landed and was verified; the two exceptions are the deliberate
// pre-effect gates rollback_started (gating the destructive pg_restore
// --clean) and readiness_failed (the observed threshold breach itself).
//
// Cross-restart idempotency: every consume-family step has an explicit resume
// branch — migrations replay (forward-only runner is idempotent), restores
// replay (--clean --if-exists), pointer promotions and tree removals are
// idempotent filesystem effects.
package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"bansoko/internal/runtime/deploymentidentity"
	"bansoko/internal/runtime/deploymentprovenance"
	updateradapter "bansoko/internal/update/adapter/pgexec"
	updateapp "bansoko/internal/update/application"
	updatefence "bansoko/internal/update/fence"
	embeddedupdate "bansoko/internal/update/journal"
	"bansoko/internal/update/packagemaker"
)

const (
	backupsDirName = "backups"
	readyzPath     = "/api/v1/readyz"

	defaultProbeInterval       = 2 * time.Second
	defaultReadinessDeadline   = 120 * time.Second
	defaultReadinessGraceFloor = 12 * time.Second
	defaultReadinessThreshold  = 3
	probeHTTPTimeout           = 2500 * time.Millisecond
	migrateTimeout             = 15 * time.Minute
	pgToolTimeout              = 10 * time.Minute
	dbCallTimeout              = 30 * time.Second
)

// readinessGate bounds the supervised readiness decision so a broken
// promotion converges to an automatic rollback instead of hanging forever.
type readinessGate struct {
	Interval      time.Duration
	Deadline      time.Duration
	GraceFloor    time.Duration
	FailThreshold int
}

func defaultReadinessGate() readinessGate {
	return readinessGate{
		Interval:      defaultProbeInterval,
		Deadline:      defaultReadinessDeadline,
		GraceFloor:    defaultReadinessGraceFloor,
		FailThreshold: defaultReadinessThreshold,
	}
}

// childProcess is one supervised target binary owned by this bootstrap
// process. Bootstrap retains kill authority: it is the parent process and the
// only component allowed to terminate a failed promotion target.
type childProcess struct {
	cmd *exec.Cmd
}

// Kill hard-stops the child (SIGKILL semantics) as required by the rollback
// decision path.
func (c *childProcess) Kill() error {
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	return c.cmd.Process.Kill()
}

// Terminate asks the child to shut down gracefully (container stop path).
func (c *childProcess) Terminate() error {
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	return c.cmd.Process.Signal(syscall.SIGTERM)
}

// WaitMonitored blocks until the child exits, forwarding SIGTERM/SIGINT so a
// docker stop reaches the served binary. Returns the forwarded exit code.
// A nil-command child (fake/test placeholder) resolves to exit code 0.
func (c *childProcess) WaitMonitored() int {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)

	done := make(chan int, 1)
	go func() {
		done <- exitCodeOf(c.cmd, c.cmd.Wait())
	}()
	for {
		select {
		case received := <-sigs:
			logf("forwarding %v to supervised server", received)
			if sigErr := c.Terminate(); sigErr != nil {
				logf("signal forward failed (%v); killing child", sigErr)
				_ = c.Kill()
			}
		case code := <-done:
			return code
		}
	}
}

func exitCodeOf(cmd *exec.Cmd, err error) int {
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && cmd.ProcessState != nil {
			if status, statusOK := cmd.ProcessState.Sys().(syscall.WaitStatus); statusOK && status.Signaled() {
				return 128 + int(status.Signal())
			}
			return cmd.ProcessState.ExitCode()
		}
		logf("supervised child wait error: %v", err)
		return 1
	}
	return 0
}

func spawnChild(cmd *exec.Cmd) (*childProcess, error) {
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &childProcess{cmd: cmd}, nil
}

// consumeFX holds every externally observable effect of the consume flow so
// the state-machine orchestration stays fully unit-testable. Real wiring
// lives in newRealConsumeFX.
type consumeFX struct {
	VerifyBackup     func(backupID string) error
	VerifyTargetTree func(releaseID string) error
	RunMigrate       func(releaseID string) error
	RestoreDatabase  func(backupID string) error
	RemoveRelease    func(releaseID string) error
	CleanupSucceeded func(record embeddedupdate.Record) error
	PromoteCurrent   func(token string) error
	ClearCurrent     func() error
	LaunchTarget     func(binaryPath, schemaDir, frontendDir string, env map[string]string) (*childProcess, error)
	ProbeReady       func() bool
	StopMaintenance  func() error
	StartMaintenance func() error
	SettleOp         func(operationID string) error
	ReleaseOp        func(operationID string) error
	SettleExactOp    func(operationID, manifestDigest string, expectedSequence int64) error
	ReleaseExactOp   func(operationID, manifestDigest string, expectedSequence int64, terminalState string) error
}

// consumeOutcome tells runConsumeFlow how to finish.
type consumeOutcomeKind int

const (
	outcomeExit         consumeOutcomeKind = iota // supervision completed; forward exit code
	outcomeExecFallback                           // steady exec of previous/base (pre-effect abort)
	outcomeParked                                 // fail-closed park (blocked or unrecoverable)
)

type consumeOutcome struct {
	kind              consumeOutcomeKind
	exitCode          int
	blockedReason     string
	blockedStep       embeddedupdate.Step
	blockedIsTerminal bool // true only when journal reached rollback_blocked
	serveBinaryPath   string
	serveSchemaDir    string
	serveFrontendDir  string
	serveOverlayRelID string
}

// relay carries mutable knowledge through the phases.
type consumeFlow struct {
	updatesRoot string
	journal     *embeddedupdate.FileJournal
	fx          *consumeFX
	gate        readinessGate

	opID    string
	target  string
	prev    string // durable rollback source token; "" means base, only meaningful with RollbackSourceCaptured
	record  embeddedupdate.Record
	jClosed bool
	child   *childProcess
}

const envListenAddress = "BANSOKO_LISTEN_ADDRESS"

func releaseRoot(updatesRoot string) string { return filepath.Join(updatesRoot, releasesDirName) }

func relDirOf(updatesRoot, token string) string {
	return filepath.Join(releaseRoot(updatesRoot), token)
}
func releaseServerBin(dir string) string   { return filepath.Join(dir, packagemaker.ServerEntryPath) }
func releaseMigrateBin(dir string) string  { return filepath.Join(dir, packagemaker.MigrateEntryPath) }
func releaseSchemaDir(dir string) string   { return filepath.Join(dir, "db/migrations") }
func releaseFrontendDir(dir string) string { return filepath.Join(dir, "frontend/dist") }

func backupsDumpPath(updatesRoot, backupID string) string {
	return filepath.Join(updatesRoot, backupsDirName, backupID+".dump")
}

// cleanupSucceededOperation removes only artifacts owned by one operation whose
// terminal success is already durable. Until that point the previous release
// and backup are rollback inputs; afterwards the current release is the only
// release this operation still needs. The cleanup is idempotent so a terminal
// replay can safely finish an interrupted attempt.
func cleanupSucceededOperation(updatesRoot string, record embeddedupdate.Record) error {
	if record.Step != embeddedupdate.StepSucceeded {
		return errors.New("refusing operation cleanup before durable success")
	}
	if !embeddedupdate.ValidID(record.OperationID) {
		return errors.New("refusing operation cleanup with an invalid operation id")
	}
	var cleanupErrs []error
	for _, path := range []string{
		filepath.Join(updatesRoot, "downloads", record.OperationID),
		filepath.Join(releaseRoot(updatesRoot), ".tmp-"+record.OperationID),
	} {
		if err := os.RemoveAll(path); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove successful update workspace: %w", err))
		}
	}
	current := strings.TrimSpace(record.CurrentReleaseID)
	if current == "" {
		current = record.TargetReleaseID
	}
	if previous := strings.TrimSpace(record.PreviousReleaseID); previous != "" {
		if !validReleaseToken(previous) || previous == current || previous == record.TargetReleaseID {
			cleanupErrs = append(cleanupErrs, errors.New("refusing to remove an invalid or current previous release"))
		} else if err := os.RemoveAll(relDirOf(updatesRoot, previous)); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove previous release after success: %w", err))
		}
	}
	if !embeddedupdate.ValidID(record.BackupID) {
		cleanupErrs = append(cleanupErrs, errors.New("refusing backup cleanup with an invalid backup id"))
	} else {
		dumpPath := backupsDumpPath(updatesRoot, record.BackupID)
		for _, path := range []string{dumpPath, dumpPath + ".sha256"} {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("remove successful update backup artifact: %w", err))
			}
		}
	}
	return errors.Join(cleanupErrs...)
}

func trimProcessOutput(out []byte) string {
	text := strings.TrimSpace(string(out))
	const capLen = 400
	if len(text) > capLen {
		return text[:capLen] + "…(trimmed)"
	}
	return text
}

// reload refreshes the local record view from disk before deciding.
func (f *consumeFlow) reload() bool {
	rec, found, err := f.journal.Load()
	if err != nil || !found || rec.OperationID != f.opID {
		logf("journal unreadable or foreign during consume: found=%v err=%v", found, err)
		return false
	}
	f.record = rec
	return true
}

// advance CAS-moves the operation forward against the freshly loaded record.
// A refused terminal-write is never silently skipped: callers park fail-closed
// because proceeding without the durable proof would break convergence.
func (f *consumeFlow) advance(to embeddedupdate.Step, lastErr, detail string,
	mutate func(*embeddedupdate.Record)) bool {
	rec, found, loadErr := f.journal.Load()
	if loadErr != nil || !found || rec.OperationID != f.opID {
		logf("journal unreadable before advancing to %s: found=%v err=%v", to, found, loadErr)
		return false
	}
	advanced, advErr := f.journal.Advance(f.opID, rec.Version, to, lastErr, detail, mutate)
	if advErr != nil {
		logf("journal advance %s -> %s refused: %v", rec.Step, to, advErr)
		return false
	}
	f.record = advanced
	logf("journal checkpoint: %s", to)
	// The checkpoint above is durable before the crash injection may terminate
	// the process here: a crash window must leave exactly the state a real crash
	// at this step would, and the container restart policy owns the replay. The
	// injection is inert outside non-production deployments.
	updateapp.ApplyFixtureStop(string(to))
	return true
}

func (f *consumeFlow) closeJournalOnce() {
	if !f.jClosed {
		if err := f.journal.Close(); err != nil {
			logf("journal close failed; keeping fail-closed state: %v", err)
			return
		}
		f.jClosed = true
	}
}

// cleanupSucceeded is deliberately outside the state transition: deleting
// rollback inputs is safe only after success is durable, and a cleanup error
// must never reopen the fence or turn that success into a rollback.
func (f *consumeFlow) cleanupSucceeded() {
	if f.fx.CleanupSucceeded == nil {
		return
	}
	if err := f.fx.CleanupSucceeded(f.record); err != nil {
		logf("update succeeded; operation cleanup deferred: %v", err)
	}
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// logDiagnosis dumps the durable evidence around a parked/blocked ending.
func (f *consumeFlow) logDiagnosis(header string) {
	rec, _, err := f.journal.Load()
	if err == nil {
		logf("%s", header)
		logf("DIAGNOSIS operation=%s step=%s last_error=%q", rec.OperationID, rec.Step, rec.LastError)
		logf("DIAGNOSIS target_release=%s previous_release=%s current_release=%s",
			rec.TargetReleaseID, rec.PreviousReleaseID, rec.CurrentReleaseID)
		tail := len(rec.History) - 5
		if tail < 0 {
			tail = 0
		}
		for _, event := range rec.History[tail:] {
			logf("DIAGNOSIS history %s at %s (%s)", event.Step, event.At.Format(time.RFC3339), event.Error)
		}
	} else {
		logf("DIAGNOSIS unavailable: %v", err)
	}
}

func (f *consumeFlow) parkActive(step embeddedupdate.Step, reason string) consumeOutcome {
	f.killChild()
	f.closeJournalOnce()
	f.logDiagnosis("MAINTENANCE fail-closed (active journal state preserved):")
	logf("MAINTENANCE %s", reason)
	return consumeOutcome{kind: outcomeParked, blockedStep: step, blockedReason: reason}
}

func (f *consumeFlow) parkBlockedTerminal(reason string) consumeOutcome {
	f.killChild()
	f.closeJournalOnce()
	f.logDiagnosis("ROLLBACK BLOCKED (never falling back to any serving version):")
	logf("MAINTENANCE %s", reason)
	return consumeOutcome{
		kind: outcomeParked, blockedStep: embeddedupdate.StepRollbackBlocked,
		blockedReason: reason, blockedIsTerminal: true,
	}
}

// captureRollbackSource resolves the release a rollback must restore. A missing
// pointer means the immutable base is the source; a pointer that exists but
// cannot be proven is an error so the caller parks instead of downgrading to
// base silently.
func captureRollbackSource(updatesRoot string) (string, error) {
	resolved, err := resolveCurrentRelease("server", updatesRoot)
	if err == nil {
		return resolved.releaseID, nil
	}
	if deploymentidentity.ReadCurrentPointer(updatesRoot).Presented {
		return "", fmt.Errorf("current overlay pointer is unusable: %w", err)
	}
	return "", nil
}

// sourceCaptureStepMoved reports whether the current pointer may already name
// the target, which makes an uncaptured rollback source unresolvable. Promotion
// renames the pointer before its checkpoint lands, so a replay can observe any
// of these steps with the pointer already advanced.
func sourceCaptureStepMoved(step embeddedupdate.Step) bool {
	switch step {
	case embeddedupdate.StepPromoted, embeddedupdate.StepReadinessFailed,
		embeddedupdate.StepRollbackStarted, embeddedupdate.StepDatabaseRestored,
		embeddedupdate.StepFenceClosurePending:
		return true
	}
	return false
}

// describeRollbackSource renders the durable rollback source for human evidence.
func describeRollbackSource(token string) string {
	if token == "" {
		return "base image binaries"
	}
	return "overlay release " + token
}

// ensureRollbackSource persists which release a rollback must restore before
// the first irreversible effect. It never re-derives the source from a pointer
// that promotion may already have moved: a promoted journal without a captured
// source stays uncaptured and fails closed when rollback actually needs it.
func (f *consumeFlow) ensureRollbackSource() bool {
	if f.record.RollbackSourceCaptured {
		prev := strings.TrimSpace(f.record.PreviousReleaseID)
		if prev == f.target {
			logf("ROLLBACK SOURCE captured value %q names the target release", prev)
			return false
		}
		if prev != "" && !validReleaseToken(prev) {
			logf("ROLLBACK SOURCE captured value %q is not a valid release token", prev)
			return false
		}
		f.prev = prev
		return true
	}
	if sourceCaptureStepMoved(f.record.Step) {
		// The pointer may already name the rejected target, so it can no longer
		// be read. A journal that already recorded a valid, distinct previous
		// release wrote it at the same checkpoint that moved the pointer and is
		// therefore trustworthy; anything else stays uncaptured and
		// phaseFinalizeRollback refuses to guess.
		recorded := strings.TrimSpace(f.record.PreviousReleaseID)
		if recorded == "" || recorded == f.target || !validReleaseToken(recorded) {
			f.prev = ""
			return true
		}
		if !f.advance(f.record.Step, f.record.LastError,
			"rollback source recovered from the promoted checkpoint as "+describeRollbackSource(recorded),
			func(r *embeddedupdate.Record) { r.RollbackSourceCaptured = true }) {
			return false
		}
		f.prev = recorded
		return true
	}
	source, captureErr := captureRollbackSource(f.updatesRoot)
	if captureErr != nil {
		logf("ROLLBACK SOURCE capture refused: %v", captureErr)
		return false
	}
	if source == f.target {
		logf("ROLLBACK SOURCE resolved to the target release %q before promotion", source)
		return false
	}
	if !f.advance(f.record.Step, f.record.LastError,
		"rollback source captured as "+describeRollbackSource(source), func(r *embeddedupdate.Record) {
			r.PreviousReleaseID = source
			r.RollbackSourceCaptured = true
		}) {
		return false
	}
	f.prev = source
	return true
}

// probeDecision summarizes one bounded readiness window.
type probeDecision struct {
	ready    bool
	failures int
	reason   string
}

// runProbeLoop polls until ready, the consecutive-failure threshold plus grace
// floor is reached, or the overall deadline expires.
func runProbeLoop(probe func() bool, gate readinessGate, startedAt time.Time) probeDecision {
	consecutive := 0
	for {
		if probe() {
			return probeDecision{ready: true, failures: consecutive}
		}
		consecutive++
		elapsed := time.Since(startedAt)
		if consecutive >= gate.FailThreshold && elapsed >= gate.GraceFloor {
			return probeDecision{failures: consecutive, reason: fmt.Sprintf(
				"readiness failed %d consecutive probes over %s", consecutive, elapsed.Truncate(time.Millisecond))}
		}
		if elapsed > gate.Deadline {
			return probeDecision{failures: consecutive, reason: fmt.Sprintf(
				"readiness deadline exceeded after %s (%d consecutive failures)", gate.Deadline, consecutive)}
		}
		time.Sleep(gate.Interval)
	}
}

// identityEnv derives deployment identity from the verified release tree and
// its durable provenance. A rollback to previous must use that release's own
// producer facts; it must never borrow the failed target's journal fields.
func (f *consumeFlow) identityEnv(releaseID string) (map[string]string, error) {
	if !validReleaseToken(releaseID) {
		return deploymentIdentityEnv(deploymentidentity.SourceBase, "", "", ""), nil
	}
	proof, err := verifyOverlayIdentity(f.updatesRoot, releaseID)
	if err != nil {
		return nil, fmt.Errorf("cannot prove supervised overlay %s identity: %w", releaseID, err)
	}
	return deploymentIdentityEnvWithBuild(deploymentidentity.SourceOverlay, releaseID, proof.PayloadDigest, proof.OperationID, proof.ExpectedProcessBuildID), nil
}

// launchRound spawns a supervised target and runs one readiness window. When
// the probe decides against the target it has already been killed (bootstrap
// is the parent and holds kill authority over promoted content). releaseID
// names the release the binary belongs to ("" for base) and feeds the identity
// env injection.
func (f *consumeFlow) launchRound(binaryPath, schemaDir, releaseID string) (*childProcess, probeDecision) {
	identity, identityErr := f.identityEnv(releaseID)
	if identityErr != nil {
		logf("MAINTENANCE fail-closed: %v", identityErr)
		return nil, probeDecision{reason: identityErr.Error()}
	}
	if f.fx.StopMaintenance != nil {
		if stopErr := f.fx.StopMaintenance(); stopErr != nil {
			logf("MAINTENANCE responder shutdown failed before application launch: %v", stopErr)
			return nil, probeDecision{reason: stopErr.Error()}
		}
	}
	child, launchErr := f.fx.LaunchTarget(binaryPath, schemaDir,
		releaseFrontendDir(filepath.Dir(binaryPath)), identity)
	if launchErr != nil {
		logf("target launch failed: %v", launchErr)
		if f.fx.StartMaintenance != nil {
			if startErr := f.fx.StartMaintenance(); startErr != nil {
				logf("MAINTENANCE responder restart failed after launch error: %v", startErr)
			}
		}
		return nil, probeDecision{reason: "target launch failed: " + launchErr.Error()}
	}
	logf("supervising target %s (schema=%s)", binaryPath, schemaDir)
	decision := runProbeLoop(f.fx.ProbeReady, f.gate, time.Now())
	logf("readiness round finished: ready=%v failures=%d reason=%q", decision.ready, decision.failures, decision.reason)
	if !decision.ready && child != nil {
		if killErr := child.Kill(); killErr != nil {
			logf("kill of rejected target returned %v", killErr)
		}
		if waitCode := child.WaitMonitored(); waitCode != 0 {
			logf("rejected target exited with code %d", waitCode)
		}
		if f.fx.StartMaintenance != nil {
			if startErr := f.fx.StartMaintenance(); startErr != nil {
				logf("MAINTENANCE responder restart failed after rejected target: %v", startErr)
			}
		}
	}
	return child, decision
}

// runConsume drives the whole post-handoff lifecycle. It never executes a
// steady-path syscall.Exec itself: outcomes either supervise children to exit
// or hand back an exec-fallback directive for runConsumeFlow.
func runConsume(j *embeddedupdate.FileJournal, record embeddedupdate.Record,
	fx *consumeFX, gate readinessGate, updatesRoot string) consumeOutcome {

	flow := &consumeFlow{
		updatesRoot: updatesRoot,
		journal:     j,
		fx:          fx,
		gate:        gate,
		opID:        record.OperationID,
		target:      record.TargetReleaseID,
	}
	if !flow.reload() {
		return flow.parkActive(record.Step, "consume entry could not load the journal")
	}
	if !validReleaseToken(flow.target) {
		return flow.parkActive(flow.record.Step,
			fmt.Sprintf("journal target release id %q escapes the releases layout", flow.target))
	}
	if !flow.ensureRollbackSource() {
		return flow.parkActive(flow.record.Step,
			"rollback source could not be captured durably; refusing to run further effects")
	}

	switch flow.record.Step {
	case embeddedupdate.StepRollbackBlocked:
		return flow.parkBlockedTerminal("rollback remains blocked; fence closure is forbidden until operator recovery")
	case embeddedupdate.StepPendingMigration:
		return flow.phasePendingMigration()
	case embeddedupdate.StepMigrationSucceeded:
		return flow.phasePromote()
	case embeddedupdate.StepPromoted:
		return flow.phaseSupervisePromoted()
	case embeddedupdate.StepReadinessFailed:
		return flow.phaseEnterRollback("")
	case embeddedupdate.StepRollbackStarted:
		return flow.phaseRestoreDatabase()
	case embeddedupdate.StepDatabaseRestored:
		return flow.phaseFinalizeRollback()
	case embeddedupdate.StepFenceClosurePending:
		return flow.phaseRetryFenceClosure()
	}
	return flow.parkActive(flow.record.Step, "no consume branch covers this journal step")
}

// phasePendingMigration verifies the pre-migration contract, then runs the
// overlay migration.
func (f *consumeFlow) phasePendingMigration() consumeOutcome {
	if backupErr := f.fx.VerifyBackup(f.record.BackupID); backupErr != nil {
		detail := fmt.Sprintf("backup %s unusable before migration: %v", f.record.BackupID, backupErr)
		// Pre-effect abort: nothing irreversible landed; legacy transition
		// pending_migration -> failed is legal and reopens admissions.
		if !f.advance(embeddedupdate.StepFailed, backupErr.Error(), detail, nil) {
			return f.parkActive(embeddedupdate.StepPendingMigration, "cannot persist backup-abort checkpoint")
		}
		if removeErr := f.fx.RemoveRelease(f.target); removeErr != nil {
			return f.parkActive(embeddedupdate.StepFailed, "backup abort cleanup failed: "+removeErr.Error())
		}
		if releaseErr := f.releaseFence(embeddedupdate.StepFailed); releaseErr != nil {
			if !f.advance(embeddedupdate.StepFenceClosurePending, releaseErr.Error(),
				"pre-migration backup abort requires owner-scoped fence closure",
				func(r *embeddedupdate.Record) { r.FenceClosureTarget = embeddedupdate.StepFailed }) {
				return f.parkActive(embeddedupdate.StepFailed,
					"backup abort and fence-closure checkpoint could not be persisted")
			}
			return f.parkActive(embeddedupdate.StepFenceClosurePending,
				"owner-scoped fence closure is retryable: "+releaseErr.Error())
		}
		f.closeJournalOnce()
		return execFallbackOutcome(f.updatesRoot, "pre-migration backup verification aborted the operation: "+detail)
	}

	treeErr := f.fx.VerifyTargetTree(f.target)
	if treeErr == nil {
		migrateErr := f.fx.RunMigrate(f.target)
		treeErr = migrateErr
		if treeErr == nil {
			if !f.advance(embeddedupdate.StepMigrationSucceeded, "",
				"overlay bansoko-migrate completed against staging schema",
				func(r *embeddedupdate.Record) { r.Attempts.Migrate++ }) {
				return f.parkActive(embeddedupdate.StepMigrationSucceeded,
					"cannot persist migration_succeeded; replay will re-run the idempotent migration")
			}
			return f.phasePromote()
		}
	}
	if !f.advance(embeddedupdate.StepRollbackStarted, treeErr.Error(),
		"overlay migration did not complete; starting automatic rollback",
		func(r *embeddedupdate.Record) { r.Attempts.Migrate++ }) {
		return f.parkActive(embeddedupdate.StepRollbackStarted,
			"cannot persist rollback_started; DB already touched by migrate, manual review required")
	}
	return f.phaseRestoreDatabase()
}

// phasePromote atomically moves the pointer to the migrated target, records
// both identities durably, then enters the supervised acceptance window. The
// rollback source was captured before migration and is never re-derived here:
// the pointer already names the target at this point.
// publishTargetProvenance publishes the operation binding for the release the
// pointer is about to name. It re-verifies the signed tree first and writes the
// record idempotently, so a replay after a crash between promotion and this
// effect converges instead of binding the release to two operations.
func (f *consumeFlow) publishTargetProvenance() error {
	releaseDir := relDirOf(f.updatesRoot, f.target)
	meta, manifest, err := verifyReleaseTreeIdentity(releaseDir)
	if err != nil {
		return fmt.Errorf("verify published release before provenance: %w", err)
	}
	if manifest.ReleaseID != f.target || manifest.EmbeddedUpdate == nil {
		return errors.New("published release identity changed before provenance publication")
	}
	if strings.TrimSpace(meta.BuildID) == "" {
		return errors.New("published release has no process build id for provenance")
	}
	return deploymentprovenance.WriteIfMatching(f.updatesRoot, deploymentprovenance.Record{
		SchemaVersion:          deploymentprovenance.FormatVersion,
		ReleaseID:              f.target,
		PayloadDigest:          manifest.EmbeddedUpdate.PayloadDigest,
		ProducingOperationID:   f.opID,
		ExpectedProcessBuildID: meta.BuildID,
	})
}

func (f *consumeFlow) phasePromote() consumeOutcome {
	if !f.record.RollbackSourceCaptured {
		return f.parkActive(embeddedupdate.StepMigrationSucceeded,
			"refusing to promote without a durably captured rollback source")
	}
	// The deployment provenance record binds the release tree to the operation
	// that produced it. Both the supervision probe below and the promoted server
	// prove their identity from it, so it must exist before the pointer moves:
	// promoting first would leave a release serving that cannot prove what it is,
	// which is indistinguishable from a tampered overlay.
	if err := f.publishTargetProvenance(); err != nil {
		return f.parkActive(embeddedupdate.StepMigrationSucceeded,
			"cannot publish promoted release provenance: "+err.Error())
	}
	prevToken := f.prev
	promoteErr := f.fx.PromoteCurrent(f.target)
	if promoteErr != nil {
		// The database was already migrated; promotion failing leaves no
		// provable convergence, so the verified backup becomes mandatory.
		if !f.advance(embeddedupdate.StepRollbackStarted, promoteErr.Error(),
			"pointer promotion failed; restoring verified backup", nil) {
			return f.parkActive(embeddedupdate.StepRollbackStarted,
				"pointer promotion failed and rollback could not be gated")
		}
		return f.phaseRestoreDatabase()
	}
	if !f.advance(embeddedupdate.StepPromoted, "",
		fmt.Sprintf("current pointer advanced to %s (rollback source=%s)", f.target, describeRollbackSource(prevToken)),
		func(r *embeddedupdate.Record) {
			r.CurrentReleaseID = f.target
		}) {
		return f.parkActive(embeddedupdate.StepPromoted,
			"cannot persist promoted checkpoint although pointer moved; a restart re-reads the durable rollback source")
	}
	return f.phaseSupervisePromoted()
}

// phaseSupervisePromoted proves the promoted build serves readiness. A breach
// kills the target (kill authority stays with bootstrap as parent) and hands
// over to the rollback chain.
func (f *consumeFlow) phaseSupervisePromoted() consumeOutcome {
	dir := relDirOf(f.updatesRoot, f.target)
	child, decision := f.launchRound(releaseServerBin(dir), releaseSchemaDir(dir), f.target)
	f.child = child
	if decision.ready {
		cumulative := f.record.Attempts.Readiness + decision.failures
		if !f.advance(embeddedupdate.StepFenceClosurePending, "",
			fmt.Sprintf("target release serving readiness (rounds saw %d failures)", decision.failures),
			func(r *embeddedupdate.Record) {
				r.Attempts.Readiness = cumulative
				r.FenceClosureTarget = embeddedupdate.StepSucceeded
			}) {
			f.killChild()
			return f.parkActive(embeddedupdate.StepPromoted,
				"cannot persist fence-closure checkpoint; restart will replay supervision deterministically")
		}
		if settleErr := f.settleFence(); settleErr != nil {
			return f.parkActive(embeddedupdate.StepFenceClosurePending,
				"owner-scoped fence closure is retryable: "+settleErr.Error())
		}
		if !f.advance(embeddedupdate.StepSucceeded, "", "owner-scoped fence closure verified",
			func(r *embeddedupdate.Record) { r.FenceClosure = embeddedupdate.FenceClosureClosed }) {
			return f.parkActive(embeddedupdate.StepFenceClosurePending,
				"fence closed but succeeded checkpoint could not be persisted")
		}
		f.cleanupSucceeded()
		f.closeJournalOnce()
		f.child = nil
		if child == nil {
			return consumeOutcome{kind: outcomeExit, exitCode: 0}
		}
		return consumeOutcome{kind: outcomeExit, exitCode: child.WaitMonitored()}
	}

	reason := decision.reason
	if reason == "" {
		reason = "supervised readiness window failed"
	}
	total := f.record.Attempts.Readiness + maxInt(decision.failures, 1)
	if !f.advance(embeddedupdate.StepReadinessFailed, reason,
		fmt.Sprintf("readiness threshold breached (cumulative attempts %d)", total),
		func(r *embeddedupdate.Record) { r.Attempts.Readiness = total }) {
		return f.parkActive(embeddedupdate.StepPromoted, "cannot persist readiness-failure checkpoint")
	}
	return f.phaseEnterRollback("")
}

// phaseEnterRollback gates the destructive restore behind a durable marker.
func (f *consumeFlow) phaseEnterRollback(carriedReason string) consumeOutcome {
	lastErr := carriedReason
	if lastErr == "" {
		lastErr = f.record.LastError
	}
	if !f.advance(embeddedupdate.StepRollbackStarted, lastErr,
		"automatic rollback armed against the verified backup", nil) {
		return f.parkActive(embeddedupdate.StepReadinessFailed,
			"cannot arm rollback; state stays durable for the next restart")
	}
	return f.phaseRestoreDatabase()
}

// phaseRestoreDatabase applies pg_restore --clean --if-exists of the verified
// backup into the main database. Replays are idempotent.
func (f *consumeFlow) phaseRestoreDatabase() consumeOutcome {
	if strings.TrimSpace(f.record.BackupID) == "" {
		if !f.advance(embeddedupdate.StepRollbackBlocked,
			errors.New("rollback reached without a recorded backup id").Error(),
			"no verifiable backup exists; automatic rollback cannot be proven", nil) {
			return f.parkActive(embeddedupdate.StepRollbackStarted, "cannot persist blocked state without backup id")
		}
		return f.parkBlockedTerminal("rollback has no recorded backup id")
	}
	restoreErr := f.fx.RestoreDatabase(f.record.BackupID)
	if restoreErr != nil {
		if !f.advance(embeddedupdate.StepRollbackBlocked, restoreErr.Error(),
			"main-database restore from backup "+f.record.BackupID+" failed", nil) {
			return f.parkActive(embeddedupdate.StepRollbackStarted, "restore failed and blocked state could not be persisted")
		}
		return f.parkBlockedTerminal("pg_restore --clean failed: " + restoreErr.Error())
	}
	if !f.advance(embeddedupdate.StepDatabaseRestored, "",
		"database restored from backup "+f.record.BackupID, nil) {
		return f.parkActive(embeddedupdate.StepRollbackStarted, "cannot persist database_restored")
	}
	return f.phaseFinalizeRollback()
}

// phaseFinalizeRollback restores the previous pointer (or clears it for base),
// removes the rejected release subtree, and proves the fallback actually
// serves. Only then does rolled_back land; otherwise blocked persists. An
// uncaptured, invalid, or self-referential source is never guessed: the
// operation stays rollback_blocked rather than downgrading to an unproven tree.
func (f *consumeFlow) phaseFinalizeRollback() consumeOutcome {
	if !f.record.RollbackSourceCaptured {
		if !f.advance(embeddedupdate.StepRollbackBlocked, "rollback source was never captured",
			"promotion may already have moved the pointer onto the rejected target", nil) {
			return f.parkActive(embeddedupdate.StepDatabaseRestored,
				"ambiguous rollback source and blocked state could not be persisted")
		}
		return f.parkBlockedTerminal("rollback source was never durably captured")
	}
	prev := strings.TrimSpace(f.record.PreviousReleaseID)
	if prev != "" && (!validReleaseToken(prev) || prev == f.target) {
		if !f.advance(embeddedupdate.StepRollbackBlocked,
			"captured rollback source is invalid: "+prev,
			"a rollback source must be a valid release token that is not the rejected target", nil) {
			return f.parkActive(embeddedupdate.StepDatabaseRestored,
				"invalid rollback source and blocked state could not be persisted")
		}
		return f.parkBlockedTerminal("captured rollback source is invalid: " + prev)
	}
	var pointerErr error
	if prev != "" {
		pointerErr = f.fx.PromoteCurrent(prev)
	} else {
		pointerErr = f.fx.ClearCurrent()
	}
	if pointerErr != nil {
		if !f.advance(embeddedupdate.StepRollbackBlocked, pointerErr.Error(),
			"cannot move the current pointer back for the rollback fallback", nil) {
			return f.parkActive(embeddedupdate.StepDatabaseRestored, "pointer restore failed; blocked not persisted")
		}
		return f.parkBlockedTerminal("pointer restoration failed: " + pointerErr.Error())
	}

	if removeErr := f.fx.RemoveRelease(f.target); removeErr != nil {
		if !f.advance(embeddedupdate.StepRollbackBlocked, removeErr.Error(),
			"cannot remove rejected release during rollback", nil) {
			return f.parkActive(embeddedupdate.StepDatabaseRestored, "release cleanup failed; blocked not persisted")
		}
		return f.parkBlockedTerminal("release cleanup failed: " + removeErr.Error())
	}

	serve, serveErr := resolveSteadyServe(f.updatesRoot)
	if serveErr != nil {
		if !f.advance(embeddedupdate.StepRollbackBlocked, serveErr.Error(),
			"rollback fallback identity could not be proven", nil) {
			return f.parkActive(embeddedupdate.StepDatabaseRestored, "fallback identity failed and blocked state could not be persisted")
		}
		return f.parkBlockedTerminal("rollback fallback is not provable: " + serveErr.Error())
	}
	child, decision := f.launchRound(serve.binaryPath, serve.schemaDir, serve.overlayRelID)
	f.child = child
	if !decision.ready {
		reason := decision.reason
		if reason == "" {
			reason = "rollback fallback did not reach readiness"
		}
		if !f.advance(embeddedupdate.StepRollbackBlocked, reason,
			"fallback ("+serve.describe()+") could not be proven serving", nil) {
			return f.parkActive(embeddedupdate.StepDatabaseRestored, "fallback refusal persisted nowhere")
		}
		return f.parkBlockedTerminal("rollback fallback is not servable: " + reason)
	}

	chosen := ""
	if serve.overlayRelID == prev && prev != "" {
		chosen = prev
	}
	if !f.advance(embeddedupdate.StepFenceClosurePending, "",
		fmt.Sprintf("rolled back to %s", serve.describe()),
		func(r *embeddedupdate.Record) {
			r.CurrentReleaseID = chosen
			r.FenceClosureTarget = embeddedupdate.StepRolledBack
		}) {
		f.killChild()
		return f.parkActive(embeddedupdate.StepDatabaseRestored,
			"cannot persist fence-closure checkpoint although fallback serves; restart replays finalize deterministically")
	}
	if releaseErr := f.releaseFence(embeddedupdate.StepRolledBack); releaseErr != nil {
		return f.parkActive(embeddedupdate.StepFenceClosurePending,
			"owner-scoped fence closure is retryable: "+releaseErr.Error())
	}
	if !f.advance(embeddedupdate.StepRolledBack, "", "owner-scoped fence closure verified",
		func(r *embeddedupdate.Record) { r.FenceClosure = embeddedupdate.FenceClosureClosed }) {
		return f.parkActive(embeddedupdate.StepFenceClosurePending,
			"fence closed but rolled_back checkpoint could not be persisted")
	}
	f.closeJournalOnce()
	f.child = nil
	if child == nil {
		return consumeOutcome{kind: outcomeExit, exitCode: 0}
	}
	return consumeOutcome{kind: outcomeExit, exitCode: child.WaitMonitored()}
}

func (f *consumeFlow) killChild() {
	if f.child == nil {
		return
	}
	if err := f.child.Kill(); err != nil {
		logf("kill supervised target returned %v", err)
	}
	f.child = nil
}

func (f *consumeFlow) phaseRetryFenceClosure() consumeOutcome {
	if f.record.Step == embeddedupdate.StepRollbackBlocked {
		return f.parkBlockedTerminal("rollback remains blocked; fence closure is forbidden until operator recovery")
	}
	target := f.record.FenceClosureTarget
	if target == "" && (f.record.Step == embeddedupdate.StepSucceeded || f.record.Step == embeddedupdate.StepRolledBack || f.record.Step == embeddedupdate.StepFailed) {
		target = f.record.Step
	}
	if target != embeddedupdate.StepSucceeded && target != embeddedupdate.StepRolledBack && target != embeddedupdate.StepFailed {
		return f.parkBlockedTerminal("fence closure retry has no valid terminal target")
	}
	var closeErr error
	if target == embeddedupdate.StepSucceeded {
		closeErr = f.settleFence()
	} else {
		// Both rollback and pre-handoff aborts close the owner-scoped fence as a
		// failed outcome. The latter can reach this branch when the journal's
		// failed checkpoint was durable but its SQL release was interrupted.
		closeErr = f.releaseFence(target)
	}
	if closeErr != nil {
		return f.parkActive(embeddedupdate.StepFenceClosurePending, "owner-scoped fence closure is retryable: "+closeErr.Error())
	}
	if !f.advance(target, "", "owner-scoped fence closure verified",
		func(r *embeddedupdate.Record) { r.FenceClosure = embeddedupdate.FenceClosureClosed }) {
		return f.parkActive(embeddedupdate.StepFenceClosurePending, "fence closed but terminal checkpoint could not be persisted")
	}
	if target == embeddedupdate.StepSucceeded {
		f.cleanupSucceeded()
	}
	f.closeJournalOnce()
	if target == embeddedupdate.StepFailed {
		return execFallbackOutcome(f.updatesRoot, "pre-handoff update failure fence closure verified")
	}
	return consumeOutcome{kind: outcomeExit, exitCode: 0}
}

func (f *consumeFlow) settleFence() error {
	if f.fx.SettleExactOp == nil || strings.TrimSpace(f.record.ManifestDigest) == "" {
		return errors.New("exact embedded fence settlement is not configured")
	}
	return f.fx.SettleExactOp(f.opID, f.record.ManifestDigest, f.record.TargetReleaseSequence)
}

func (f *consumeFlow) releaseFence(terminalState embeddedupdate.Step) error {
	if f.fx.ReleaseExactOp == nil || strings.TrimSpace(f.record.ManifestDigest) == "" {
		return errors.New("exact embedded fence release is not configured")
	}
	return f.fx.ReleaseExactOp(f.opID, f.record.ManifestDigest, f.record.TargetReleaseSequence, string(terminalState))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// steadyServe describes where normal serving comes from.
type steadyServe struct {
	binaryPath   string
	schemaDir    string
	frontendDir  string
	overlayRelID string
}

func (s steadyServe) describe() string {
	if s.overlayRelID != "" {
		return "overlay release " + s.overlayRelID
	}
	return "base image binaries"
}

// resolveSteadyServe re-decides normal serving from the pointer file and the
// complete release identity proof. A missing pointer means the immutable base
// is selected; a present but invalid pointer or missing provenance is a
// fail-closed error rather than a silent base downgrade.
func resolveSteadyServe(updatesRoot string) (steadyServe, error) {
	if resolved, err := resolveCurrentRelease("server", updatesRoot); err == nil {
		if _, identityErr := verifyOverlayIdentity(updatesRoot, resolved.releaseID); identityErr == nil {
			return steadyServe{
				binaryPath:   resolved.binaryPath,
				schemaDir:    releaseSchemaDir(resolved.path),
				frontendDir:  releaseFrontendDir(resolved.path),
				overlayRelID: resolved.releaseID,
			}, nil
		} else {
			return steadyServe{}, fmt.Errorf("current overlay release %s is not provable: %w", resolved.releaseID, identityErr)
		}
	} else if pointer := deploymentidentity.ReadCurrentPointer(updatesRoot); pointer.Presented {
		return steadyServe{}, fmt.Errorf("current overlay pointer is not usable: %w", err)
	}
	return steadyServe{
		binaryPath:  baseServerPath,
		schemaDir:   baseSchemaDir,
		frontendDir: baseFrontendDir,
	}, nil
}

func execFallbackOutcome(updatesRoot, reason string) consumeOutcome {
	serve, serveErr := resolveSteadyServe(updatesRoot)
	if serveErr != nil {
		logf("MAINTENANCE fail-closed: steady serving identity is unproven: %v", serveErr)
		return consumeOutcome{kind: outcomeParked, blockedReason: serveErr.Error()}
	}
	logf("falling back to steady serving (%s): %s", serve.describe(), reason)
	return consumeOutcome{
		kind:              outcomeExecFallback,
		blockedReason:     reason,
		serveBinaryPath:   serve.binaryPath,
		serveSchemaDir:    serve.schemaDir,
		serveFrontendDir:  serve.frontendDir,
		serveOverlayRelID: serve.overlayRelID,
	}
}

// writeCurrentPointerAtomically replaces the single-token pointer with
// temp-file + fsync + rename + parent-dir fsync so crashes never observe a
// torn or missing promotion mid-write.
func writeCurrentPointerAtomically(updatesRoot, token string) error {
	if !validReleaseToken(token) {
		return fmt.Errorf("refusing to promote invalid release token %q", token)
	}
	target := filepath.Join(updatesRoot, currentPointerName)
	temporary, err := os.CreateTemp(updatesRoot, ".current-*")
	if err != nil {
		return fmt.Errorf("create current pointer temp: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			if removeErr := os.Remove(temporaryName); removeErr != nil && !os.IsNotExist(removeErr) {
				logf("remove temporary current pointer failed: %v", removeErr)
			}
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect current pointer temp: %w", err)
	}
	if _, err := temporary.WriteString(token + "\n"); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write current pointer temp: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync current pointer temp: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close current pointer temp: %w", err)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return fmt.Errorf("commit current pointer: %w", err)
	}
	removeTemp = false
	dirHandle, dirErr := os.Open(updatesRoot)
	if dirErr != nil {
		return fmt.Errorf("open updates directory after pointer promotion: %w", dirErr)
	}
	if syncErr := dirHandle.Sync(); syncErr != nil {
		_ = dirHandle.Close()
		return fmt.Errorf("sync updates directory after pointer promotion: %w", syncErr)
	}
	if closeErr := dirHandle.Close(); closeErr != nil {
		return fmt.Errorf("close updates directory after pointer promotion: %w", closeErr)
	}
	return nil
}

// verifyBackupSidecar checks size-bearing presence and digest agreement of
// backups/<id>.dump against its .sha256 sidecar; the caller layers on the
// pg_restore --list parse proof.
func verifyBackupSidecar(updatesRoot, backupID string) (string, error) {
	dumpPath := backupsDumpPath(updatesRoot, backupID)
	info, statErr := os.Stat(dumpPath)
	if statErr != nil {
		return "", fmt.Errorf("backup dump missing: %w", statErr)
	}
	if info.Size() == 0 {
		return "", errors.New("backup dump is empty")
	}
	raw, readErr := os.ReadFile(dumpPath + ".sha256")
	if readErr != nil {
		return "", fmt.Errorf("backup checksum sidecar unreadable: %w", readErr)
	}
	expected := strings.TrimSpace(string(raw))
	digest, hashErr := hashFileSHA256(dumpPath)
	if hashErr != nil {
		return "", fmt.Errorf("hash backup dump: %w", hashErr)
	}
	if digest != expected {
		return "", fmt.Errorf("backup dump sha256 mismatch (want %s got %s)", expected, digest)
	}
	return dumpPath, nil
}

// probeTargetBase derives the loopback HTTP endpoint the readiness probe hits,
// derived from BANSOKO_LISTEN_ADDRESS only (wildcards collapse to 127.0.0.1).
func probeTargetBase(listenAddr string) string {
	host, port, splitErr := net.SplitHostPort(strings.TrimSpace(listenAddr))
	switch {
	case splitErr != nil || port == "":
		port = "8080"
		host = "127.0.0.1"
	case host == "", host == "::", host == "0.0.0.0":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// responderLifecycle owns the single maintenance responder of one consume run.
// Start binds a fresh listener, so a restart after Stop is a real bind and never
// a recycled closed handle; Stop closes the current listener or fails loudly when
// none is held. Exactly one owner starts and stops it.
type responderLifecycle struct {
	startResponder func() (func() error, error)
	mu             sync.Mutex
	stop           func() error
}

func (l *responderLifecycle) Start() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stop != nil {
		return errors.New("maintenance responder is already active")
	}
	stop, err := l.startResponder()
	if err != nil {
		return err
	}
	l.stop = stop
	return nil
}

func (l *responderLifecycle) Stop() error {
	l.mu.Lock()
	stop := l.stop
	l.stop = nil
	l.mu.Unlock()
	if stop == nil {
		return errors.New("maintenance responder is not active")
	}
	return stop()
}

// newRealConsumeFX wires the production effects: whitelisted PG17 tooling via
// the shared pgexec adapter, atomic pointer files, direct child processes and
// the fenced-lock SQL exported by the update application package. It never
// starts or stops the responder: the single consume-run owner does that.
func newRealConsumeFX(updatesRoot, databaseURL string, db *sql.DB, responder *responderLifecycle) (*consumeFX, error) {
	// The fixture must cross the readiness grace floor, not simulate a transient blip.
	fixtureReadinessFailuresRemaining := int(defaultReadinessGraceFloor/defaultProbeInterval) + 1
	if responder == nil || responder.startResponder == nil {
		return nil, errors.New("maintenance responder lifecycle is required")
	}
	executor, resolveErr := updateradapter.New(updateradapter.DefaultCandidatePaths)
	if resolveErr != nil {
		return nil, fmt.Errorf("resolve whitelisted PostgreSQL client tools: %w", resolveErr)
	}
	env, envErr := updateapp.PostgresChildEnv(databaseURL)
	if envErr != nil {
		return nil, envErr
	}
	dbName, dbNameErr := updateapp.PostgresDatabaseName(databaseURL)
	if dbNameErr != nil {
		return nil, dbNameErr
	}

	return &consumeFX{
		VerifyBackup: func(backupID string) error {
			dumpPath, sidecarErr := verifyBackupSidecar(updatesRoot, backupID)
			if sidecarErr != nil {
				return sidecarErr
			}
			ctx, cancel := context.WithTimeout(context.Background(), pgToolTimeout)
			defer cancel()
			if listErr := executor.ListContents(ctx, env, dumpPath); listErr != nil {
				return fmt.Errorf("pg_restore --list rejected backup archive: %w", listErr)
			}
			return nil
		},
		VerifyTargetTree: func(releaseID string) error {
			if !validReleaseToken(releaseID) {
				return fmt.Errorf("invalid release token %q", releaseID)
			}
			return verifyReleaseTree(relDirOf(updatesRoot, releaseID))
		},
		RunMigrate: func(releaseID string) error {
			dir := relDirOf(updatesRoot, releaseID)
			ctx, cancel := context.WithTimeout(context.Background(), migrateTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, releaseMigrateBin(dir))
			// Force the migrate child onto the owner DSN: the app container's
			// BANSOKO_DATABASE_URL is the runtime role, which must not (and cannot)
			// apply migrations or run DDL.
			cmd.Env = envWithOverrides(map[string]string{
				envSchemaDir:           releaseSchemaDir(dir),
				"BANSOKO_DATABASE_URL": databaseURL,
			})
			out, runErr := cmd.CombinedOutput()
			if runErr != nil {
				return fmt.Errorf("overlay bansoko-migrate failed: %w: %s", runErr, trimProcessOutput(out))
			}
			return nil
		},
		RestoreDatabase: func(backupID string) error {
			if os.Getenv("BANSOKO_EMBEDDED_FIXTURE_RESTORE_FAILURE") == "1" {
				return errors.New("embedded fixture: injected restore failure")
			}
			ctx, cancel := context.WithTimeout(context.Background(), pgToolTimeout)
			defer cancel()
			return executor.Restore(ctx, env, backupsDumpPath(updatesRoot, backupID), dbName)
		},
		RemoveRelease: func(releaseID string) error {
			if !validReleaseToken(releaseID) {
				return fmt.Errorf("refusing to remove invalid release token %q", releaseID)
			}
			if removeErr := os.RemoveAll(relDirOf(updatesRoot, releaseID)); removeErr != nil {
				return fmt.Errorf("remove rejected release tree %s: %w", releaseID, removeErr)
			}
			return nil
		},
		CleanupSucceeded: func(record embeddedupdate.Record) error {
			return cleanupSucceededOperation(updatesRoot, record)
		},
		PromoteCurrent: func(token string) error {
			return writeCurrentPointerAtomically(updatesRoot, token)
		},
		ClearCurrent: func() error {
			if removeErr := os.Remove(filepath.Join(updatesRoot, currentPointerName)); removeErr != nil &&
				!os.IsNotExist(removeErr) {
				return fmt.Errorf("clear current pointer: %w", removeErr)
			}
			if syncErr := syncDirectory(updatesRoot); syncErr != nil {
				return fmt.Errorf("sync updates directory after clearing current pointer: %w", syncErr)
			}
			return nil
		},
		LaunchTarget: func(binaryPath, schemaDir, frontendDir string, env map[string]string) (*childProcess, error) {
			cmd := exec.Command(binaryPath)
			overrides := map[string]string{
				envSchemaDir:   schemaDir,
				envFrontendDir: frontendDir,
			}
			for key, value := range env {
				overrides[key] = value
			}
			cmd.Env = envWithOverrides(overrides)
			return spawnChild(cmd)
		},
		ProbeReady: func() bool {
			if os.Getenv("BANSOKO_ENVIRONMENT") == "test" && os.Getenv("BANSOKO_EMBEDDED_FIXTURE_FAIL_READINESS") == "1" && fixtureReadinessFailuresRemaining > 0 {
				fixtureReadinessFailuresRemaining--
				return false
			}
			client := &http.Client{Timeout: probeHTTPTimeout}
			resp, err := client.Get("http://" + probeTargetBase(os.Getenv(envListenAddress)) + readyzPath)
			if err != nil {
				return false
			}
			defer resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		},
		SettleExactOp: func(operationID, manifestDigest string, expectedSequence int64) error {
			ctx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
			defer cancel()
			// Bootstrap owns this fence from acquisition to terminal outcome, so
			// the row is still 'active' here; the current-state CAS proves the
			// exact identity before settling it.
			return updatefence.New(db).ReconcileExactCurrentState(ctx, operationID, updatefence.AdmittedByEmbedded, manifestDigest, expectedSequence, "succeeded")
		},
		ReleaseExactOp: func(operationID, manifestDigest string, expectedSequence int64, terminalState string) error {
			ctx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
			defer cancel()
			return updatefence.New(db).ReconcileExactCurrentState(ctx, operationID, updatefence.AdmittedByEmbedded, manifestDigest, expectedSequence, terminalState)
		},
		StopMaintenance:  responder.Stop,
		StartMaintenance: responder.Start,
	}, nil
}

// runConsumeFlow is the single entry for both the pending-marker handoff and a
// journal-resumed handoff. It owns the maintenance responder lifecycle: start it
// before consuming the marker, hold it across backup, migration and handoff,
// stop it immediately before the application child is launched, and restart it
// when the flow parks. Every fail-closed park keeps the held listener serving
// health instead of binding a second one.
func runConsumeFlow(updatesRoot string) int {
	responder := &responderLifecycle{startResponder: func() (func() error, error) {
		// The durable marker is the only authority until the journal exists; after
		// that the maintenance handler reads the journal itself.
		step, operation := "", ""
		if _, err := os.Stat(filepath.Join(updatesRoot, pendingMarkerName)); err == nil {
			step = "pending_backup"
			if marker, markerErr := readPendingMarker(filepath.Join(updatesRoot, pendingMarkerName)); markerErr == nil {
				operation = marker.OperationID
			}
		}
		return startMaintenanceResponder(updatesRoot, step, operation)
	}}
	if err := responder.Start(); err != nil {
		logf("MAINTENANCE responder failed closed: %v", err)
		return 1
	}
	if _, err := os.Stat(filepath.Join(updatesRoot, pendingMarkerName)); err == nil {
		if err := consumePendingMarker(updatesRoot); err != nil {
			logf("MAINTENANCE fail-closed: pending marker consumption failed: %v", err)
			blockForever()
			return 124
		}
	}

	dsn := maintenanceDatabaseURL()
	if dsn == "" {
		logf("MAINTENANCE fail-closed: embedded-update consumption requires the owner database DSN")
		blockForever()
		return 124
	}

	flowJournal, openErr := embeddedupdate.Open(filepath.Join(updatesRoot, journalFileName))
	if openErr != nil {
		logf("MAINTENANCE fail-closed: consume journal unavailable: %v", openErr)
		blockForever()
		return 124
	}
	record, found, loadErr := flowJournal.Load()
	if loadErr != nil {
		if closeErr := flowJournal.Close(); closeErr != nil {
			logf("consume journal close failed after load error: %v", closeErr)
		}
		logf("MAINTENANCE fail-closed: journal unreadable in consume entry: %v", loadErr)
		blockForever()
		return 124
	}
	if found && record.Step == embeddedupdate.StepRollbackBlocked {
		if closeErr := flowJournal.Close(); closeErr != nil {
			logf("consume journal close failed while parking rollback-blocked state: %v", closeErr)
		}
		logf("MAINTENANCE fail-closed: rollback is blocked for operation %s; refusing to resume serving", record.OperationID)
		blockForever()
		return 124
	}
	if !found || !record.Active() {
		// Raced terminal between plan and consume (or empty workspace): no
		// active op exists, so resolve ordinary serving without touching any
		// fence row. Missing journal evidence never authorizes a stale sweep.
		if closeErr := flowJournal.Close(); closeErr != nil {
			logf("consume journal close failed before steady serving: %v", closeErr)
		}
		outcome := execFallbackOutcome(updatesRoot, "no active update operation remained at consume time")
		if outcome.kind == outcomeParked {
			blockForever()
			return 124
		}
		if err := responder.Stop(); err != nil {
			logf("MAINTENANCE responder shutdown failed before steady exec: %v", err)
			return 1
		}
		return execSteadyServe(updatesRoot, outcome)
	}
	if !consumeFamilySteps[record.Step] {
		if closeErr := flowJournal.Close(); closeErr != nil {
			logf("consume journal close failed while parking pre-handoff state: %v", closeErr)
		}
		logf("MAINTENANCE fail-closed: operation %s sits at pre-handoff step %s; refusing to resume without a marker", record.OperationID, record.Step)
		blockForever()
		return 124
	}

	db, sqlOpenErr := sql.Open("pgx", dsn)
	if sqlOpenErr == nil {
		db.SetMaxOpenConns(2)
		defer db.Close()
	}
	if sqlOpenErr != nil {
		if closeErr := flowJournal.Close(); closeErr != nil {
			logf("consume journal close failed while parking database error: %v", closeErr)
		}
		logf("MAINTENANCE fail-closed: consume database unavailable: %v", sqlOpenErr)
		blockForever()
		return 124
	}
	fx, fxErr := newRealConsumeFX(updatesRoot, dsn, db, responder)
	if fxErr != nil {
		if closeErr := flowJournal.Close(); closeErr != nil {
			logf("consume journal close failed while parking effect setup error: %v", closeErr)
		}
		logf("MAINTENANCE fail-closed: consume effects unavailable: %v", fxErr)
		blockForever()
		return 124
	}
	outcome := runConsume(flowJournal, record, fx, defaultReadinessGate(), updatesRoot)
	switch outcome.kind {
	case outcomeExit:
		if err := responder.Stop(); err != nil {
			logf("MAINTENANCE responder shutdown failed after supervision exit: %v", err)
		}
		return outcome.exitCode
	case outcomeParked:
		logf("MAINTENANCE parking for operator review (step=%s terminal=%v).", outcome.blockedStep, outcome.blockedIsTerminal)
		blockForever()
		return 124
	default:
		if err := responder.Stop(); err != nil {
			logf("MAINTENANCE responder shutdown failed before steady exec: %v", err)
			return 1
		}
		return execSteadyServe(updatesRoot, outcome)
	}
}

// execSteadyServe replaces the process image with the chosen steady target;
// failure to exec is fatal because any further decision would exceed the
// bootstrap contract. The deployment identity follows the served tree: an
// overlay fallback names its release (payload digest only when the journal
// can prove it for exactly that release), base serving clears overlay facts.
func execSteadyServe(updatesRoot string, outcome consumeOutcome) int {
	pairs := map[string]string{envSchemaDir: outcome.serveSchemaDir}
	if outcome.serveFrontendDir != "" {
		pairs[envFrontendDir] = outcome.serveFrontendDir
	}
	identity := deploymentIdentityEnv(deploymentidentity.SourceBase, "", "", "")
	if outcome.serveOverlayRelID != "" {
		proof, proofErr := verifyOverlayIdentity(updatesRoot, outcome.serveOverlayRelID)
		if proofErr != nil {
			logf("MAINTENANCE fail-closed: steady overlay identity is unproven: %v", proofErr)
			blockForever()
			return 124
		}
		identity = deploymentIdentityEnvWithBuild(deploymentidentity.SourceOverlay, outcome.serveOverlayRelID, proof.PayloadDigest, proof.OperationID, proof.ExpectedProcessBuildID)
	}
	for key, value := range identity {
		pairs[key] = value
	}
	if execErr := syscall.Exec(outcome.serveBinaryPath, []string{outcome.serveBinaryPath}, envWithOverrides(pairs)); execErr != nil {
		logf("exec of steady fallback %s failed: %v", outcome.serveBinaryPath, execErr)
		return 1
	}
	return 0
}
