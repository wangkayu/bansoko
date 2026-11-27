package bootstrap

import (
	"errors"

	updateapp "bansoko/internal/update/application"
	updatefence "bansoko/internal/update/fence"
	embeddedupdate "bansoko/internal/update/journal"
)

type takeoverFX struct {
	VerifyEvidence    func(embeddedupdate.Record) error
	AcquireFence      func(embeddedupdate.Record) error
	LookupFence       func(string) (updatefence.Row, bool, error)
	CaptureBackup     func(embeddedupdate.Record) (digest, installID string, err error)
	CheckMigration    func(embeddedupdate.Record) error
	Migrate           func(embeddedupdate.Record) error
	Probe             func(embeddedupdate.Record, string, bool) error
	DropCurrent       func(string) error
	RestoreCurrent    func(string) error
	VerifyOverlay     func(embeddedupdate.Record) error
	PrepareRecovery   func(embeddedupdate.Record) (embeddedupdate.DatabaseRecovery, error)
	ReconcileRecovery func(embeddedupdate.Record) error
	SettleExact       func(embeddedupdate.Record, string) error
	Retire            func(embeddedupdate.Record) error
}

type takeoverFlow struct {
	journal *embeddedupdate.FileJournal
	fx      *takeoverFX
	root    string
	record  embeddedupdate.Record
}

// runTakeover holds the same journal writer lock as overlay consumption. Probe
// effects supervise their own isolated child and reap it before returning.
func runTakeover(j *embeddedupdate.FileJournal, record embeddedupdate.Record, fx *takeoverFX, root string) consumeOutcome {
	f := takeoverFlow{journal: j, record: record, fx: fx, root: root}
	for {
		switch f.record.Step {
		case embeddedupdate.StepPrepared:
			row, found, err := fx.LookupFence(record.OperationID)
			if err != nil {
				return f.park("cannot inspect takeover fence")
			}
			if err := fx.VerifyEvidence(f.record); err != nil {
				return f.block(err, "takeover admission evidence changed")
			}
			if !found {
				if err := fx.AcquireFence(f.record); err != nil {
					return f.park("cannot acquire takeover fence")
				}
				updateapp.ApplyFixtureStop("takeover_fence_inserted")
				row, found, err = fx.LookupFence(record.OperationID)
			}
			if err != nil || !found || !exactOpenFence(f.record, row) {
				return f.block(errors.New("takeover fence identity is not exact"), "cannot admit takeover")
			}
			if !f.advance(embeddedupdate.StepAccepted, "", "exact takeover fence acquired", nil) {
				return f.park("cannot persist acceptance")
			}
		case embeddedupdate.StepAccepted:
			if !f.advance(embeddedupdate.StepMaintenanceEntered, "", "public listener in maintenance", nil) {
				return f.park("cannot persist maintenance entry")
			}
		case embeddedupdate.StepMaintenanceEntered:
			if err := f.requireOpenFence(); err != nil {
				return f.block(err, "cannot prove fence before backup")
			}
			digest, installID, err := fx.CaptureBackup(f.record)
			if err != nil {
				return f.abort(err)
			}
			if !f.advance(embeddedupdate.StepBackupVerified, "", "operation-owned backup and scratch restore verified", func(r *embeddedupdate.Record) {
				r.BackupID, r.BackupDigest, r.InstallID = "embedded-"+r.OperationID, digest, installID
			}) {
				return f.park("cannot pin verified backup")
			}
		case embeddedupdate.StepBackupVerified:
			if !f.advance(embeddedupdate.StepPendingMigration, "", "backup pinned before migration", nil) {
				return f.park("cannot persist pending migration")
			}
		case embeddedupdate.StepPendingMigration:
			if err := f.checkMigration(); err != nil {
				return f.abort(err)
			}
			if !f.advance(embeddedupdate.StepMigrationStarted, "", "owner migration armed", nil) {
				return f.park("cannot persist migration boundary")
			}
		case embeddedupdate.StepMigrationStarted:
			// The migration runner verifies checksums and only applies missing
			// migrations, including on replay across its commit boundary.
			if err := f.checkMigration(); err != nil {
				return f.rollback(err)
			}
			if err := fx.Migrate(f.record); err != nil {
				return f.rollback(err)
			}
			updateapp.ApplyFixtureStop("takeover_migration_committed")
			if !f.advance(embeddedupdate.StepMigrationSucceeded, "", "base migration verified", nil) {
				return f.park("cannot persist migration outcome")
			}
		case embeddedupdate.StepMigrationSucceeded:
			if err := fx.Probe(f.record, "base", true); err != nil {
				return f.rollback(err)
			}
			if !f.advance(embeddedupdate.StepBaseReady, "", "private base probe passed", nil) {
				return f.park("cannot persist base readiness")
			}
		case embeddedupdate.StepBaseReady:
			if err := fx.DropCurrent(f.record.PreviousReleaseID); err != nil {
				return f.rollback(err)
			}
			if !f.advance(embeddedupdate.StepPromoted, "", "previous pointer removed and parent directory synced", func(r *embeddedupdate.Record) {
				r.CurrentReleaseID = ""
			}) {
				return f.park("cannot persist pointer promotion")
			}
		case embeddedupdate.StepPromoted:
			if err := fx.Probe(f.record, "base", false); err != nil {
				return f.rollback(err)
			}
			return f.close(embeddedupdate.StepSucceeded, "")
		case embeddedupdate.StepRollbackStarted:
			if f.record.DatabaseRecovery == nil {
				pin, err := fx.PrepareRecovery(f.record)
				if err != nil {
					return f.block(err, "empty restore verification failed")
				}
				if !f.advance(embeddedupdate.StepRollbackStarted, f.record.LastError, "database identities pinned before rename", func(r *embeddedupdate.Record) { r.DatabaseRecovery = &pin }) {
					return f.park("cannot pin restore cutover")
				}
			}
			if err := fx.ReconcileRecovery(f.record); err != nil {
				return f.block(err, "database cutover did not converge")
			}
			if !f.advance(embeddedupdate.StepDatabaseRestored, f.record.LastError, "restored database verified", nil) {
				return f.park("cannot persist database restore")
			}
		case embeddedupdate.StepDatabaseRestored:
			if err := fx.VerifyOverlay(f.record); err != nil {
				return f.block(err, "previous overlay is unproven")
			}
			if err := fx.RestoreCurrent(f.record.PreviousReleaseID); err != nil {
				return f.block(err, "previous pointer could not be restored")
			}
			if err := fx.Probe(f.record, "overlay", false); err != nil {
				return f.block(err, "previous overlay did not pass readiness")
			}
			return f.close(embeddedupdate.StepRolledBack, f.record.LastError)
		case embeddedupdate.StepFenceClosurePending:
			target := f.record.FenceClosureTarget
			if target == embeddedupdate.StepSucceeded {
				if err := fx.Probe(f.record, "base", false); err != nil {
					return f.block(err, "base could not be reproved before fence closure replay")
				}
			} else if err := fx.Probe(f.record, "overlay", false); err != nil {
				return f.block(err, "previous overlay could not be reproved before fence closure replay")
			}
			return f.finish(target)
		default:
			return f.park("unhandled takeover checkpoint")
		}
	}
}

func exactOpenFence(r embeddedupdate.Record, row updatefence.Row) bool {
	return row.OperationID == r.OperationID && row.AdmittedBy == updatefence.AdmittedByEmbedded && !row.Released &&
		row.State == "active" && row.Sequence == r.TargetReleaseSequence && row.ManifestDigest == r.ManifestDigest
}

func (f *takeoverFlow) requireOpenFence() error {
	row, found, err := f.fx.LookupFence(f.record.OperationID)
	if err != nil || !found || !exactOpenFence(f.record, row) {
		return errors.New("exact open takeover fence is unproven")
	}
	return nil
}

func (f *takeoverFlow) checkMigration() error {
	if err := f.requireOpenFence(); err != nil {
		return err
	}
	if err := f.fx.VerifyEvidence(f.record); err != nil {
		return err
	}
	return f.fx.CheckMigration(f.record)
}

func (f *takeoverFlow) advance(to embeddedupdate.Step, lastErr, detail string, mutate func(*embeddedupdate.Record)) bool {
	r, err := f.journal.AdvanceExpected(f.record.OperationID, f.record.Version, f.record.Step, to, lastErr, detail, mutate)
	if err != nil {
		logf("takeover checkpoint %s -> %s refused: %v", f.record.Step, to, err)
		return false
	}
	f.record = r
	logf("takeover checkpoint: %s", to)
	updateapp.ApplyFixtureStop(string(to))
	return true
}

func (f *takeoverFlow) park(reason string) consumeOutcome {
	logf("MAINTENANCE fail-closed: takeover %s at %s: %s", f.record.OperationID, f.record.Step, reason)
	return consumeOutcome{kind: outcomeParked, blockedReason: reason, blockedStep: f.record.Step,
		blockedIsTerminal: f.record.Step == embeddedupdate.StepRollbackBlocked}
}

func (f *takeoverFlow) block(cause error, detail string) consumeOutcome {
	if !f.advance(embeddedupdate.StepRollbackBlocked, cause.Error(), detail, nil) {
		return f.park("cannot persist rollback_blocked: " + detail)
	}
	return f.park(detail)
}

func (f *takeoverFlow) abort(cause error) consumeOutcome {
	if err := f.fx.VerifyOverlay(f.record); err != nil {
		return f.block(err, "previous overlay is unproven after pre-migration abort")
	}
	return f.close(embeddedupdate.StepFailed, cause.Error())
}

func (f *takeoverFlow) rollback(cause error) consumeOutcome {
	if !f.advance(embeddedupdate.StepRollbackStarted, cause.Error(), "arming empty-database rollback", nil) {
		return f.park("cannot persist rollback intent")
	}
	return runTakeover(f.journal, f.record, f.fx, f.root)
}

func (f *takeoverFlow) close(target embeddedupdate.Step, lastErr string) consumeOutcome {
	if !f.advance(embeddedupdate.StepFenceClosurePending, lastErr, "recording exact fence closure intent", func(r *embeddedupdate.Record) {
		r.FenceClosureTarget = target
		if target != embeddedupdate.StepSucceeded {
			r.CurrentReleaseID = r.PreviousReleaseID
		}
	}) {
		return f.park("cannot persist fence closure intent")
	}
	return f.finish(target)
}

func (f *takeoverFlow) finish(target embeddedupdate.Step) consumeOutcome {
	terminal := string(embeddedupdate.StepFailed)
	if target == embeddedupdate.StepSucceeded {
		terminal = string(target)
	}
	if err := f.fx.SettleExact(f.record, terminal); err != nil {
		return f.park("exact fence closure is retryable")
	}
	updateapp.ApplyFixtureStop("takeover_fence_closed")
	if !f.advance(target, f.record.LastError, "exact fence closure verified", func(r *embeddedupdate.Record) {
		r.FenceClosure = embeddedupdate.FenceClosureClosed
	}) {
		return f.park("cannot persist terminal takeover checkpoint")
	}
	if target == embeddedupdate.StepSucceeded {
		if f.fx.Retire != nil {
			if err := f.fx.Retire(f.record); err != nil {
				logf("takeover succeeded; overlay retirement deferred: %v", err)
			}
		}
		return consumeOutcome{kind: outcomeExecFallback, serveBinaryPath: baseServerPath,
			takeoverOp: f.record.OperationID, selectionReason: "base_takeover_succeeded",
			serveSchemaDir: baseSchemaDir, serveFrontendDir: baseFrontendDir}
	}
	releaseID := f.record.PreviousReleaseID
	dir := relDirOf(f.root, releaseID)
	reason := "takeover_previously_failed"
	if target == embeddedupdate.StepRolledBack {
		reason = "takeover_rolled_back"
	}
	return consumeOutcome{kind: outcomeExecFallback, serveBinaryPath: releaseServerBin(dir),
		takeoverOp: f.record.OperationID, selectionReason: reason,
		serveSchemaDir: releaseSchemaDir(dir), serveFrontendDir: releaseFrontendDir(dir), serveOverlayRelID: releaseID}
}
