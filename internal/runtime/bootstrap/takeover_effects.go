package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/google/uuid"

	backupapp "bansoko/internal/backup/application"
	"bansoko/internal/runtime/buildinfo"
	"bansoko/internal/runtime/deploymentidentity"
	"bansoko/internal/runtime/migrations/varspreflight"
	"bansoko/internal/update/adapter/pgexec"
	updateapp "bansoko/internal/update/application"
	updatefence "bansoko/internal/update/fence"
	embeddedupdate "bansoko/internal/update/journal"
)

func currentBaseAnchor(build buildinfo.BuildInfo) embeddedupdate.BaseAnchor {
	a := embeddedupdate.BaseAnchor{Platform: runtime.GOOS, Architecture: runtime.GOARCH, Build: build}
	if !a.Build.Ordered() || a.Platform != "linux" {
		return a
	}
	a.ExecutableHash, _ = hashFileSHA256(baseServerPath)
	return a
}

func previousOverlay(releaseID string, proof overlayIdentityProof) embeddedupdate.PreviousOverlay {
	return embeddedupdate.PreviousOverlay{ReleaseID: releaseID, Build: proof.Build,
		ProducingOp: proof.OperationID, PayloadDigest: "sha256:" + strings.TrimPrefix(proof.PayloadDigest, "sha256:")}
}

func takeoverOutcome(j *embeddedupdate.FileJournal, live embeddedupdate.Record, found bool, anchor embeddedupdate.BaseAnchor, prev embeddedupdate.PreviousOverlay) (embeddedupdate.Record, error) {
	matches := func(r embeddedupdate.Record) bool {
		return (r.Step == embeddedupdate.StepFailed || r.Step == embeddedupdate.StepRolledBack) &&
			embeddedupdate.ValidFenceClosure(r.FenceClosure) && matchesTakeoverPair(r, anchor, prev)
	}
	if found && matches(live) {
		return live, nil
	}
	entries, err := j.ArchivedRecords()
	var newest embeddedupdate.Record
	for _, r := range entries {
		if matches(r) && r.UpdatedAt.After(newest.UpdatedAt) {
			newest = r
		}
	}
	return newest, err
}

func matchesTakeoverPair(r embeddedupdate.Record, a embeddedupdate.BaseAnchor, p embeddedupdate.PreviousOverlay) bool {
	return r.Takeover != nil && r.Takeover.Anchor.Fingerprint() == a.Fingerprint() && r.Takeover.Previous == p
}

var errTakeoverIncompatible = errors.New("takeover restore prerequisites are not met")

func proveProducingFence(ctx context.Context, lookup func(context.Context, string) (updatefence.Row, bool, error), proof overlayIdentityProof) error {
	digest, err := proof.Manifest.CanonicalDigest()
	if err != nil {
		return fmt.Errorf("digest previous overlay manifest: %w", err)
	}
	row, found, err := lookup(ctx, proof.OperationID)
	if err != nil || !found || !row.Released || row.State != "settled" || row.AdmittedBy != updatefence.AdmittedByEmbedded ||
		row.OperationID != proof.OperationID || row.Sequence != proof.Manifest.ReleaseSequence || row.ManifestDigest != digest {
		return errors.New("previous overlay has no exact closed producing fence")
	}
	return nil
}

func createTakeoverIntent(parent context.Context, j *embeddedupdate.FileJournal, root, ownerURL string, db *sql.DB, prior embeddedupdate.Record, found bool) (embeddedupdate.Record, error) {
	var none embeddedupdate.Record
	if same, err := isCurrentExecutable(baseServerPath); err != nil || !same {
		return none, errors.New("takeover initiation must run from the immutable image binary")
	}
	if _, err := os.Lstat(filepath.Join(root, pendingMarkerName)); !errors.Is(err, os.ErrNotExist) {
		return none, errors.New("pending overlay evidence prevents takeover")
	}
	pointer := deploymentidentity.ReadCurrentPointer(root)
	if !pointer.Readable || pointer.Corrupt || found && prior.CurrentReleaseID != "" && prior.CurrentReleaseID != pointer.Token {
		return none, errors.New("previous overlay pointer is unproven")
	}
	proof, err := verifyOverlayIdentity(root, pointer.Token)
	if err != nil {
		return none, errors.New("previous overlay tree is unproven")
	}
	build, docker := readImageAnchor()
	a := currentBaseAnchor(build)
	p := previousOverlay(pointer.Token, proof)
	if !docker || !a.Valid() || !p.Build.Ordered() || !compatibleImageAnchor(build, proof.Manifest) ||
		a.Build.Series != p.Build.Series || a.Build.Lane != p.Build.Lane || a.Build.ReleaseSequence <= p.Build.ReleaseSequence {
		return none, errors.New("takeover release ordering or image proof changed")
	}
	priorOutcome, err := takeoverOutcome(j, prior, found, a, p)
	if err != nil || priorOutcome.OperationID != "" {
		return none, errors.New("cannot initiate a takeover without new, proven evidence")
	}
	ctx, cancel := context.WithTimeout(parent, dbCallTimeout)
	defer cancel()
	store := updatefence.New(db)
	if err := proveProducingFence(ctx, store.Lookup, proof); err != nil {
		return none, err
	}
	if rows, err := store.ListOpenAll(ctx); err != nil || len(rows) != 0 {
		return none, errors.New("an unresolved database fence prevents takeover")
	}
	restore, err := newOperationRestore(root, ownerURL)
	if err != nil {
		return none, fmt.Errorf("%w: %v", errTakeoverIncompatible, err)
	}
	if err := pgexec.CheckTools(pgexec.DefaultCandidatePaths); err != nil {
		return none, fmt.Errorf("%w: %v", errTakeoverIncompatible, err)
	}
	live, err := restore.pinDatabase(ctx, db)
	if err != nil {
		return none, fmt.Errorf("%w: %v", errTakeoverIncompatible, err)
	}
	if found {
		if prior.Active() || prior.Step == embeddedupdate.StepRollbackBlocked {
			return none, errors.New("cannot archive an unresolved operation")
		}
		if prior.FenceClosure != embeddedupdate.FenceClosureNotRequired {
			row, present, err := store.Lookup(ctx, prior.OperationID)
			if err != nil || !present || !row.Released || row.OperationID != prior.OperationID ||
				row.Sequence != prior.TargetReleaseSequence || row.ManifestDigest != prior.ManifestDigest ||
				row.AdmittedBy != prior.Channel || !terminalFenceStateMatches(prior.Step, row.State) {
				return none, errors.New("prior terminal operation has no exact closed fence proof")
			}
		}
		if prior.Takeover != nil && prior.Step == embeddedupdate.StepSucceeded {
			if err := retireOverlay(root, prior); err != nil {
				return none, fmt.Errorf("complete overlay retirement before archival: %w", err)
			}
		}
		if err := j.ArchiveTerminalProven(); err != nil {
			return none, err
		}
	}
	op := "takeover-" + uuid.NewString()
	r, err := j.Create(embeddedupdate.Record{OperationID: op, OperationKind: embeddedupdate.KindBaseTakeover,
		Channel: updatefence.AdmittedByEmbedded, Step: embeddedupdate.StepPrepared, TargetReleaseID: build.BuildID,
		TargetVersion: build.Version, TargetSchemaVersion: build.SchemaVersion, TargetLane: build.Lane,
		TargetReleaseSequence: build.ReleaseSequence, ManifestDigest: a.Fingerprint(), PreviousReleaseID: p.ReleaseID,
		CurrentReleaseID: p.ReleaseID, RollbackSourceCaptured: true, LiveDatabase: &live,
		Takeover: &embeddedupdate.TakeoverEvidence{TargetSource: "base", PreviousSource: "overlay", Anchor: a, Previous: p}})
	if err == nil {
		updateapp.ApplyFixtureStop(string(embeddedupdate.StepPrepared))
	}
	return r, err
}

func newRealTakeoverFX(parent context.Context, root, ownerURL string, db *sql.DB) (*takeoverFX, error) {
	restore, err := newOperationRestore(root, ownerURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, dbCallTimeout)
	defer cancel()
	if err := pgexec.CheckTools(pgexec.DefaultCandidatePaths); err != nil {
		return nil, err
	}
	admin, err := restore.owner.Open(ctx, "postgres")
	if err != nil {
		return nil, errors.New("takeover requires a working postgres maintenance connection")
	}
	defer admin.Close()
	if err := backupapp.CheckRestoreConnection(ctx, admin); err != nil {
		return nil, err
	}
	store := updatefence.New(db)
	verifyOverlay := func(r embeddedupdate.Record) error {
		proof, err := verifyOverlayIdentity(root, r.PreviousReleaseID)
		if err != nil || previousOverlay(r.PreviousReleaseID, proof) != r.Takeover.Previous {
			return errors.New("previous overlay no longer matches the pinned provenance")
		}
		return nil
	}
	return &takeoverFX{
		Interrupted: func() bool { return parent.Err() != nil },
		VerifyEvidence: func(r embeddedupdate.Record) error {
			base, docker := readImageAnchor()
			if a := currentBaseAnchor(base); !docker || !a.Valid() || a.Fingerprint() != r.ManifestDigest {
				return errors.New("immutable image identity changed during takeover")
			}
			p := deploymentidentity.ReadCurrentPointer(root)
			if !p.Readable || p.Corrupt || p.Token != r.PreviousReleaseID {
				return errors.New("takeover no longer owns the previous current pointer")
			}
			return verifyOverlay(r)
		},
		VerifyOverlay: verifyOverlay,
		AcquireFence: func(r embeddedupdate.Record) error {
			ctx, cancel := context.WithTimeout(parent, dbCallTimeout)
			defer cancel()
			return store.Acquire(ctx, r.OperationID, updatefence.AdmittedByEmbedded, r.TargetReleaseSequence, r.ManifestDigest)
		},
		LookupFence: func(op string) (updatefence.Row, bool, error) {
			ctx, cancel := context.WithTimeout(parent, dbCallTimeout)
			defer cancel()
			return store.Lookup(ctx, op)
		},
		CaptureBackup: func(r embeddedupdate.Record) (string, string, error) {
			ctx, cancel := context.WithTimeout(parent, dbCallTimeout)
			defer cancel()
			if err := proveRestoreSource(ctx, db, r); err != nil {
				return "", "", err
			}
			id := r.LiveDatabase.InstallID
			r.InstallID = id
			schema, err := restore.previousSchema(r)
			if err != nil {
				return "", "", err
			}
			backupID, err := writeVerifiedOperationBackup(root, r.OperationID, ownerURL, db, func(ctx context.Context, scratch string) error {
				return restore.verifyDatabase(ctx, scratch, schema, r)
			})
			if err != nil {
				return "", "", errors.New("operation backup or private scratch restore failed")
			}
			digest, err := hashFileSHA256(backupsDumpPath(root, backupID))
			return digest, id, err
		},
		CheckMigration: func(r embeddedupdate.Record) error {
			ctx, cancel := context.WithTimeout(parent, pgToolTimeout)
			defer cancel()
			if err := proveRestoreSource(ctx, db, r); err != nil {
				return err
			}
			if _, _, err := restore.verifyDump(ctx, r.BackupID, r.BackupDigest); err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(baseSchemaDir, varspreflight.Migration)); errors.Is(err, os.ErrNotExist) {
				return nil
			} else if err != nil {
				return err
			}
			return varspreflight.CheckPending(ctx, db)
		},
		Migrate: func(embeddedupdate.Record) error {
			if err := runMigrationChildContext(parent, commandFor(baseServerPath, "migrate"), envWithOverrides(map[string]string{envSchemaDir: baseSchemaDir, "BANSOKO_DATABASE_URL": ownerURL})); err != nil {
				return errors.New("owner migration failed for the verified image base")
			}
			return nil
		},
		Probe: func(r embeddedupdate.Record, source string, previousPointer bool) error {
			p := deploymentidentity.ReadCurrentPointer(root)
			if p.Corrupt || p.Presented != (previousPointer || source == "overlay") || p.Presented && p.Token != r.PreviousReleaseID {
				return errors.New("current pointer contradicts private probe target")
			}
			if source == "base" {
				if !previousPointer && os.Getenv("BANSOKO_ENVIRONMENT") == "test" && os.Getenv("BANSOKO_EMBEDDED_FIXTURE_FAIL_READINESS") == "1" {
					return errors.New("embedded fixture: injected post-promotion readiness failure")
				}
				base, docker := readImageAnchor()
				if !docker || currentBaseAnchor(base).Fingerprint() != r.ManifestDigest {
					return errors.New("private probe base anchor changed")
				}
				return runPrivateProbe(parent, root, privateProbeTarget{binary: baseServerPath, schema: baseSchemaDir, frontend: baseFrontendDir,
					build: base, env: deploymentIdentityEnv(deploymentidentity.SourceBase, "", "", "")})
			}
			if err := verifyOverlay(r); err != nil {
				return err
			}
			dir := relDirOf(root, r.PreviousReleaseID)
			pinned := r.Takeover.Previous
			return runPrivateProbe(parent, root, privateProbeTarget{binary: releaseServerBin(dir), schema: releaseSchemaDir(dir), frontend: releaseFrontendDir(dir),
				build: pinned.Build, env: deploymentIdentityEnvWithBuild(deploymentidentity.SourceOverlay, pinned.ReleaseID, strings.TrimPrefix(pinned.PayloadDigest, "sha256:"), pinned.ProducingOp, pinned.Build.BuildID)})
		},
		DropCurrent: func(previous string) error {
			p := deploymentidentity.ReadCurrentPointer(root)
			if p.Presented {
				if p.Corrupt || p.Token != previous {
					return errors.New("refusing to clear a foreign current pointer")
				}
				if err := os.Remove(filepath.Join(root, currentPointerName)); err != nil {
					return err
				}
			}
			if err := syncDirectory(root); err != nil {
				return err
			}
			updateapp.ApplyFixtureStop("takeover_pointer_unlinked")
			return nil
		},
		RestoreCurrent: func(previous string) error { return restoreOwnedPointer(root, "", previous) },
		PrepareRecovery: func(r embeddedupdate.Record) (embeddedupdate.DatabaseRecovery, error) {
			return restore.prepareContext(parent, r)
		},
		ReconcileRecovery: func(r embeddedupdate.Record) error { return restore.reconcileContext(parent, r) },
		SettleExact: func(r embeddedupdate.Record, terminal string) error {
			ctx, cancel := context.WithTimeout(parent, dbCallTimeout)
			defer cancel()
			return store.ReconcileExactCurrentStateOrProveClosed(ctx, r.OperationID, updatefence.AdmittedByEmbedded, r.ManifestDigest, r.TargetReleaseSequence, terminal)
		},
		Retire: func(r embeddedupdate.Record) error { return retireOverlay(root, r) },
	}, nil
}

func restoreOwnedPointer(root, target, previous string) error {
	p := deploymentidentity.ReadCurrentPointer(root)
	if p.Corrupt || p.Presented && (!p.Readable || p.Token != target && p.Token != previous) {
		return errors.New("refusing to replace a foreign current pointer")
	}
	if p.Token == previous {
		return syncDirectory(root)
	}
	if previous != "" {
		return writeCurrentPointerAtomically(root, previous)
	}
	if err := os.Remove(filepath.Join(root, currentPointerName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(root)
}

// runTakeoverEntry uses the same lock and terminal serving path as consume.
// Constructing sql.DB lazily is essential after the first database rename.
func runTakeoverEntry(root string, initiate bool) int {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	stop, err := startMaintenanceResponder(root, "", "")
	if err != nil {
		logf("MAINTENANCE responder failed: %v", err)
		return 1
	}
	defer func() { _ = stop() }()
	outcome, err := consumeTakeover(ctx, root, initiate)
	if ctx.Err() != nil {
		return 1
	}
	if errors.Is(err, errTakeoverIncompatible) {
		// Re-arbitrate after dropping the writer lock: a replacement marker or
		// unresolved journal must not be bypassed by the fallback.
		chosen := planLaunch("server", root)
		if chosen.mode == modeTakeover && chosen.reasonCode == "base_newer_verified" {
			tree := relDirOf(root, chosen.releaseID)
			chosen = overlayPlan(chosen, chosen.releaseID, releaseServerBin(tree), releaseServerBin(tree), releaseSchemaDir(tree), releaseFrontendDir(tree))
			chosen.reasonCode = "takeover_incompatible"
			logf("takeover_incompatible: %v", err)
			if err := stop(); err != nil {
				return 1
			}
			return launchPlan(chosen, root)
		}
	}
	if err != nil || outcome.kind == outcomeParked {
		if err != nil {
			logf("MAINTENANCE takeover parked: %v", err)
		}
		<-ctx.Done()
		return 124
	} else {
		if err := stop(); err != nil {
			logf("MAINTENANCE responder shutdown failed: %v", err)
			return 1
		}
		return execSteadyServe(root, outcome)
	}
}

func consumeTakeover(ctx context.Context, root string, initiate bool) (consumeOutcome, error) {
	var none consumeOutcome
	if same, err := isCurrentExecutable(baseServerPath); err != nil || !same {
		return none, errors.New("takeover must run from the immutable image binary")
	}
	j, err := embeddedupdate.Open(filepath.Join(root, journalFileName))
	if err != nil {
		return none, err
	}
	defer j.Close()
	if _, err := os.Lstat(filepath.Join(root, pendingMarkerName)); !errors.Is(err, os.ErrNotExist) {
		return none, errors.New("pending marker prevents takeover")
	}
	r, found, err := j.Load()
	if err != nil {
		return none, err
	}
	if found && (r.Step == embeddedupdate.StepRollbackBlocked || r.Active() && (r.Takeover == nil || r.SchemaVersion != embeddedupdate.FormatVersion)) {
		return none, errors.New("unresolved journal prevents takeover")
	}
	dsn := maintenanceDatabaseURL()
	if dsn == "" {
		return none, errors.New("takeover requires the owner database URL")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return none, errors.New("invalid owner database URL")
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	if !found || !r.Active() {
		if !initiate {
			return none, errors.New("takeover no longer has an active journal")
		}
		r, err = createTakeoverIntent(ctx, j, root, dsn, db, r, found)
		if err != nil {
			return none, err
		}
	}
	fx, err := newRealTakeoverFX(ctx, root, dsn, db)
	if err != nil {
		return none, err
	}
	return runTakeover(j, r, fx, root), nil
}
