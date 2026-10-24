package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"bansoko/internal/runtime/deploymentidentity"
	"bansoko/internal/update/adapter/pgexec"
	updateapp "bansoko/internal/update/application"
	updatefence "bansoko/internal/update/fence"
	embeddedupdate "bansoko/internal/update/journal"
)

const maxPendingMarkerBytes = 16 << 10

func terminalFenceStateMatches(step embeddedupdate.Step, state string) bool {
	switch step {
	case embeddedupdate.StepSucceeded:
		return state == "settled"
	case embeddedupdate.StepFailed, embeddedupdate.StepRolledBack:
		return state == "failed"
	default:
		return false
	}
}

type pendingMarker struct {
	OperationID       string `json:"operation_id"`
	ReleaseID         string `json:"release_id"`
	ManifestDigest    string `json:"manifest_digest"`
	ReleaseSequence   int64  `json:"release_sequence"`
	PreviousReleaseID string `json:"previous_release_id"`
}

// pendingMarkerMemberCount counts the pending marker's top-level JSON members by
// name occurrence. A repeated name collapses into a single map key, so the map
// length alone cannot prove that the document really declares five fields, and
// an ambiguous marker must fail closed instead of resolving last-wins.
func pendingMarkerMemberCount(data []byte) (int, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return 0, errors.New("pending marker must be a JSON object")
	}
	members := 0
	for decoder.More() {
		if _, err := decoder.Token(); err != nil {
			return 0, err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return 0, err
		}
		members++
	}
	return members, nil
}

func readPendingMarker(path string) (pendingMarker, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return pendingMarker{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPendingMarkerBytes {
		return pendingMarker{}, errors.New("pending marker is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPendingMarkerBytes+1))
	if err != nil || len(data) > maxPendingMarkerBytes {
		return pendingMarker{}, errors.New("pending marker could not be read within its size limit")
	}
	var fields map[string]json.RawMessage
	members, memberErr := pendingMarkerMemberCount(data)
	if err := json.Unmarshal(data, &fields); err != nil || memberErr != nil || len(fields) != 5 || members != 5 {
		return pendingMarker{}, errors.New("pending marker must be valid JSON with exactly five fields")
	}
	for _, key := range []string{"operation_id", "release_id", "manifest_digest", "release_sequence", "previous_release_id"} {
		if _, ok := fields[key]; !ok {
			return pendingMarker{}, fmt.Errorf("pending marker is missing %s", key)
		}
	}
	var marker pendingMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return pendingMarker{}, fmt.Errorf("decode pending marker: %w", err)
	}
	if !embeddedupdate.ValidID(marker.OperationID) || !validReleaseToken(marker.ReleaseID) ||
		!validRecoveryDigest(marker.ManifestDigest) || marker.ReleaseSequence < 1 ||
		(marker.PreviousReleaseID != "" && !validReleaseToken(marker.PreviousReleaseID)) {
		return pendingMarker{}, errors.New("pending marker identity is invalid")
	}
	return marker, nil
}

// markerFence is the exact fence surface marker consumption needs; the
// production *updatefence.Store satisfies it as-is. It exists so the marker
// state machine can be driven without a live PostgreSQL server, which the
// package's database-free test lane does not provide.
type markerFence interface {
	Lookup(ctx context.Context, operationID string) (updatefence.Row, bool, error)
	Acquire(ctx context.Context, operationID, admittedBy string, sequence int64, manifestDigest string) error
	ListOpen(ctx context.Context, admittedBy string) ([]updatefence.OpenFenceRow, error)
	ReconcileExactCurrentState(ctx context.Context, operationID, admittedBy, manifestDigest string, expectedSequence int64, terminalState string) error
}

// The marker path's PostgreSQL effects are indirections over the production
// implementations below: a test replaces them to script a fence outcome or a
// failed backup, and production always runs the real call. Nothing outside
// _test.go may reassign them.
var (
	openMarkerFence   = func(db *sql.DB) markerFence { return updatefence.New(db) }
	writeMarkerBackup = writeVerifiedMarkerBackup
)

func removePendingMarker(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func validRecoveryDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
func discardMarkerTarget(updatesRoot, markerPath string, marker pendingMarker) error {
	if err := removePendingMarker(markerPath); err != nil {
		return fmt.Errorf("remove pending marker: %w", err)
	}
	if err := os.RemoveAll(relDirOf(updatesRoot, marker.ReleaseID)); err != nil {
		return fmt.Errorf("discard unconsumed release: %w", err)
	}
	if err := syncDirectory(releaseRoot(updatesRoot)); err != nil {
		return fmt.Errorf("sync release directory after discard: %w", err)
	}
	return nil
}

func consumePendingMarker(updatesRoot string) error {
	markerPath := filepath.Join(updatesRoot, pendingMarkerName)
	marker, err := readPendingMarker(markerPath)
	if err != nil {
		return fmt.Errorf("invalid pending marker: %w", err)
	}
	journal, err := embeddedupdate.Open(filepath.Join(updatesRoot, journalFileName))
	if err != nil {
		return fmt.Errorf("open update journal: %w", err)
	}
	defer journal.Close()
	record, found, err := journal.Load()
	if err != nil {
		return fmt.Errorf("load update journal: %w", err)
	}
	if found {
		if record.Step == embeddedupdate.StepPendingMigration &&
			record.OperationID == marker.OperationID && record.TargetReleaseID == marker.ReleaseID &&
			record.TargetReleaseSequence == marker.ReleaseSequence && record.ManifestDigest == marker.ManifestDigest &&
			record.PreviousReleaseID == marker.PreviousReleaseID {
			if record.BackupID == "" {
				return errors.New("pending journal has no backup identity")
			}
			if _, err := verifyBackupSidecar(updatesRoot, record.BackupID); err != nil {
				return fmt.Errorf("pending journal backup is not verifiable: %w", err)
			}
			if err := removePendingMarker(markerPath); err != nil {
				return fmt.Errorf("remove marker already represented by pending journal: %w", err)
			}
			return nil
		} else if record.Active() || record.Step == embeddedupdate.StepRollbackBlocked {
			return errors.New("pending marker conflicts with an active or rollback-blocked journal")
		}
	}

	// A complete signed tree, including its embedded manifest identity, is the
	// trust root for all marker fields other than the operation id.
	meta, manifest, err := verifyReleaseTreeIdentity(relDirOf(updatesRoot, marker.ReleaseID))
	if err != nil {
		return fmt.Errorf("verify pending release tree: %w", err)
	}
	_ = meta
	if manifest.ReleaseID != marker.ReleaseID || manifest.ReleaseSequence != marker.ReleaseSequence {
		return errors.New("pending marker release identity differs from signed manifest")
	}
	digest, err := manifest.CanonicalDigest()
	if err != nil || digest != marker.ManifestDigest {
		return errors.New("pending marker digest differs from signed manifest")
	}
	current := deploymentCurrentID(updatesRoot)
	if current != marker.PreviousReleaseID {
		return fmt.Errorf("pending marker previous release %q differs from current release %q", marker.PreviousReleaseID, current)
	}

	dsn := maintenanceDatabaseURL()
	if strings.TrimSpace(dsn) == "" {
		return errors.New("pending marker consumption requires the owner database DSN")
	}
	db, err := sqlOpenBootstrap(dsn)
	if err != nil {
		return fmt.Errorf("open owner database: %w", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
	defer cancel()
	fenceStore := openMarkerFence(db)
	// Reuse the serial journal slot only after proving the previous terminal
	// closure against its exact durable owner row. Keep the immutable archive;
	// never erase a terminal record or infer closure from a new admission.
	if found && embeddedupdate.Terminal(record.Step) {
		old, present, lookupErr := fenceStore.Lookup(ctx, record.OperationID)
		if lookupErr != nil || !present || !old.Released || old.OperationID != record.OperationID ||
			old.AdmittedBy != record.Channel || old.ManifestDigest != record.ManifestDigest ||
			old.Sequence != record.TargetReleaseSequence || !terminalFenceStateMatches(record.Step, old.State) {
			return errors.New("previous terminal journal has no exact closed fence proof")
		}
		if err := journal.ArchiveTerminalProven(); err != nil {
			return fmt.Errorf("archive proven previous terminal journal: %w", err)
		}
	}
	row, rowFound, lookupErr := fenceStore.Lookup(ctx, marker.OperationID)
	if lookupErr != nil {
		return fmt.Errorf("read existing marker fence: %w", lookupErr)
	}
	if !rowFound {
		if err := fenceStore.Acquire(ctx, marker.OperationID, updatefence.AdmittedByEmbedded, marker.ReleaseSequence, marker.ManifestDigest); err != nil {
			if errors.Is(err, updatefence.ErrTargetStale) || errors.Is(err, updatefence.ErrSequenceMissing) {
				if discardErr := discardMarkerTarget(updatesRoot, markerPath, marker); discardErr != nil {
					return fmt.Errorf("discard replayed marker after sequence refusal: %w", discardErr)
				}
				logf("discarded replayed or lower-sequence marker for %s; continuing current release", marker.ReleaseID)
				return nil
			}
			return fmt.Errorf("acquire exact pending-marker fence: %w", err)
		}
		row, rowFound, lookupErr = fenceStore.Lookup(ctx, marker.OperationID)
		if lookupErr != nil {
			return fmt.Errorf("read acquired marker fence: %w", lookupErr)
		}
	}
	if rowFound && row.OperationID == marker.OperationID && row.AdmittedBy == updatefence.AdmittedByEmbedded &&
		row.Sequence == marker.ReleaseSequence && row.ManifestDigest == marker.ManifestDigest && row.Released && row.State == "failed" {
		if err := discardMarkerTarget(updatesRoot, markerPath, marker); err != nil {
			return fmt.Errorf("discard marker after previously closed backup failure: %w", err)
		}
		return nil
	}
	if !rowFound || row.OperationID != marker.OperationID || row.AdmittedBy != updatefence.AdmittedByEmbedded ||
		row.Sequence != marker.ReleaseSequence || row.ManifestDigest != marker.ManifestDigest || row.Released ||
		row.State != "active" {
		return errors.New("pending marker fence is not the exact open owner row")
	}
	openRows, listErr := fenceStore.ListOpen(ctx, updatefence.AdmittedByEmbedded)
	if listErr != nil || len(openRows) != 1 || openRows[0].OperationID != marker.OperationID {
		return errors.New("pending marker does not own the unique open embedded fence")
	}
	backupID, err := writeMarkerBackup(updatesRoot, marker, dsn, db)
	if err != nil {
		if closeErr := fenceStore.ReconcileExactCurrentState(context.Background(), marker.OperationID,
			updatefence.AdmittedByEmbedded, marker.ManifestDigest, marker.ReleaseSequence, "failed"); closeErr != nil {
			return fmt.Errorf("verified backup failed (%v) and exact fence closure failed: %w", err, closeErr)
		}
		if discardErr := discardMarkerTarget(updatesRoot, markerPath, marker); discardErr != nil {
			return fmt.Errorf("verified backup failed (%v) and marker discard failed: %w", err, discardErr)
		}
		return err
	}
	newRecord, err := journal.Create(embeddedupdate.Record{
		OperationID: marker.OperationID, Channel: updatefence.AdmittedByEmbedded,
		Step: embeddedupdate.StepPendingMigration, TargetReleaseID: marker.ReleaseID,
		TargetVersion: manifest.Version, TargetSchemaVersion: manifest.TargetMigrationVersion,
		TargetReleaseSequence: marker.ReleaseSequence, ManifestDigest: marker.ManifestDigest,
		BackupID: backupID, PreviousReleaseID: marker.PreviousReleaseID,
		RollbackSourceCaptured: true,
	})
	if err != nil {
		return fmt.Errorf("create pending_migration journal: %w", err)
	}

	if newRecord.Step != embeddedupdate.StepPendingMigration {
		return errors.New("journal did not land at pending_migration")
	}
	return removePendingMarker(markerPath)
}

// discardPreMarkerWork deletes the disposable pre-marker workspace (staging and
// unreferenced release trees) on a server start without a pending marker. A
// crash before the durable marker needs no other recovery. Any active journal,
// including rollback_blocked, is left untouched for planLaunch to consume or
// park.
func discardPreMarkerWork(updatesRoot string) error {
	if _, err := os.Lstat(filepath.Join(updatesRoot, pendingMarkerName)); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect pending marker before workspace cleanup: %w", err)
	}
	record, found, err := embeddedupdate.Snapshot(filepath.Join(updatesRoot, journalFileName))
	if err != nil {
		if errors.Is(err, embeddedupdate.ErrJournalCorrupted) {
			return nil // planLaunch quarantines the same corrupt journal and parks.
		}
		return fmt.Errorf("read journal before workspace cleanup: %w", err)
	}
	if found && (record.Active() || record.Step == embeddedupdate.StepRollbackBlocked) {
		return nil
	}
	return discardUnreferencedWork(updatesRoot)
}
func discardUnreferencedWork(updatesRoot string) error {
	if _, err := os.Lstat(filepath.Join(updatesRoot, pendingMarkerName)); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect pending marker before workspace cleanup: %w", err)
	}
	pointer := deploymentidentity.ReadCurrentPointer(updatesRoot)
	if pointer.Presented && (!pointer.Readable || pointer.Corrupt) {
		return errors.New("current release pointer is unreadable; refusing workspace cleanup")
	}
	current := pointer.Token
	previous := ""
	if record, found, err := embeddedupdate.Snapshot(filepath.Join(updatesRoot, journalFileName)); err != nil {
		return fmt.Errorf("read rollback source before workspace cleanup: %w", err)
	} else if found {
		previous = record.PreviousReleaseID
	}
	root := filepath.Join(updatesRoot, releasesDirName)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read release workspace: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == current || name == previous || !entry.IsDir() {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			return fmt.Errorf("discard unreferenced release work %s: %w", name, err)
		}
	}
	if err := syncDirectory(root); err != nil {
		return fmt.Errorf("sync release workspace cleanup: %w", err)
	}
	for _, dir := range []string{"staging", "downloads"} {
		if err := os.RemoveAll(filepath.Join(updatesRoot, dir)); err != nil {
			return fmt.Errorf("discard unreferenced %s workspace: %w", dir, err)
		}
	}
	return nil
}

// These small aliases keep all database/tooling details local to bootstrap.
func sqlOpenBootstrap(dsn string) (*sql.DB, error) { return sql.Open("pgx", dsn) }

func deploymentCurrentID(updatesRoot string) string {
	pointer := deploymentidentity.ReadCurrentPointer(updatesRoot)
	if !pointer.Readable {
		return ""
	}
	return pointer.Token
}

func writeVerifiedMarkerBackup(updatesRoot string, marker pendingMarker, dsn string, db *sql.DB) (string, error) {
	executor, err := pgexec.New(pgexec.DefaultCandidatePaths)
	if err != nil {
		return "", err
	}
	env, err := updateapp.PostgresChildEnv(dsn)
	if err != nil {
		return "", err
	}
	backupDir := filepath.Join(updatesRoot, backupsDirName)
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	backupID := "embedded-" + marker.OperationID
	if !embeddedupdate.ValidID(backupID) {
		return "", errors.New("marker operation id cannot form a backup id")
	}
	partial := filepath.Join(backupDir, "."+backupID+".partial")
	dumpPath := backupsDumpPath(updatesRoot, backupID)
	// A resumed handoff reuses the operation-owned artifact a previous attempt
	// already published with its sidecar. The window this covers is "backup
	// durable, journal not yet written": re-dumping there would repeat the most
	// expensive effect on every crash and would make that window non-convergent.
	// The reused bytes are still proof-restored below, so what the journal hands
	// off is verified exactly like a fresh dump.
	if markerBackupNeedsDump(updatesRoot, backupID) {
		_ = os.Remove(partial)
		ctx, cancel := context.WithTimeout(context.Background(), pgToolTimeout)
		defer cancel()
		if err := executor.Dump(ctx, env, partial); err != nil {
			return "", fmt.Errorf("pg_dump failed: %w", err)
		}
		info, err := os.Lstat(partial)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return "", errors.New("pg_dump produced an empty backup")
		}
		digest, err := hashFileSHA256(partial)
		if err != nil {
			return "", err
		}
		file, err := os.OpenFile(partial, os.O_RDWR, 0)
		if err != nil {
			return "", err
		}
		err = file.Sync()
		closeErr := file.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		if err := os.Rename(partial, dumpPath); err != nil {
			return "", err
		}
		if err := syncDirectory(backupDir); err != nil {
			return "", err
		}
		if err := writeAtomicFile(backupDir, dumpPath+".sha256", []byte(digest+"\n"), 0o600); err != nil {
			return "", err
		}
		// The verified artifact and its sidecar are durable and the journal does
		// not exist yet: this is the pending_backup crash window. The next start
		// reuses this artifact (above) instead of dumping again, so the replay
		// converges instead of dying in the same window forever.
		updateapp.ApplyFixtureStop("pending_backup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), pgToolTimeout)
	defer cancel()
	if err := proveMarkerBackup(ctx, executor, env, dumpPath, marker.OperationID, db); err != nil {
		return "", err
	}
	return backupID, nil
}

// markerBackupNeedsDump reports whether this handoff must take a fresh dump.
// Only an already published artifact whose sidecar digest agrees may be reused;
// absence, emptiness, an unreadable sidecar and a digest mismatch all fall back
// to a fresh dump, so a tampered or half-written artifact is never handed off.
func markerBackupNeedsDump(updatesRoot, backupID string) bool {
	_, err := verifyBackupSidecar(updatesRoot, backupID)
	return err != nil
}

func proveMarkerBackup(ctx context.Context, executor *pgexec.Executor, env []string, dumpPath, operationID string, db *sql.DB) error {
	name := "embedded_verify_" + strings.ReplaceAll(operationID, "-", "_")
	if len(name) > 63 {
		name = name[:63]
	}
	createCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	if _, err := db.ExecContext(createCtx, "DROP DATABASE IF EXISTS "+quoted); err != nil {
		return fmt.Errorf("clear scratch restore database: %w", err)
	}
	if _, err := db.ExecContext(createCtx, "CREATE DATABASE "+quoted); err != nil {
		return fmt.Errorf("create scratch restore database: %w", err)
	}
	restoreErr := executor.Restore(ctx, env, dumpPath, name)
	dropCtx, cancelDrop := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelDrop()
	_, dropErr := db.ExecContext(dropCtx, "DROP DATABASE IF EXISTS "+quoted)
	if restoreErr != nil {
		return fmt.Errorf("backup restore verification failed: %w", restoreErr)
	}
	if dropErr != nil {
		return fmt.Errorf("drop scratch restore database: %w", dropErr)
	}
	return nil
}

func writeAtomicFile(dir, path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(dir, ".bootstrap-backup-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}
