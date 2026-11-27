package bootstrap

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"bansoko/internal/backup/adapter/pgarchive"
	backupapp "bansoko/internal/backup/application"
	"bansoko/internal/runtime/bootsecrets"
	"bansoko/internal/runtime/database"
	"bansoko/internal/runtime/migrations"
	"bansoko/internal/update/adapter/pgexec"
	updateapp "bansoko/internal/update/application"
	updatefence "bansoko/internal/update/fence"
	embeddedupdate "bansoko/internal/update/journal"
)

type operationRestore struct {
	root    string
	owner   *pgarchive.Cluster
	runtime *pgarchive.Cluster
	tools   *pgarchive.Tools
}

func newOperationRestore(root, ownerURL string) (*operationRestore, error) {
	owner, err := pgarchive.NewCluster(ownerURL)
	if err != nil {
		return nil, err
	}
	serving, err := pgarchive.NewCluster(os.Getenv(bootsecrets.EnvRuntimeDatabaseURL))
	if err != nil || serving.Database() != owner.Database() {
		return nil, errors.New("empty restore requires the deployment's separate runtime login")
	}
	tools, err := pgarchive.NewTools(ownerURL, pgexec.DefaultCandidatePaths)
	if err != nil {
		return nil, err
	}
	return &operationRestore{root: root, owner: owner, runtime: serving, tools: tools}, nil
}

func (s *operationRestore) previousSchema(r embeddedupdate.Record) (string, error) {
	if !r.RollbackSourceCaptured {
		return "", errors.New("rollback source was not captured")
	}
	if r.PreviousReleaseID == "" {
		return baseAssetDir(baseSchemaDir), nil
	}
	if _, err := verifyOverlayIdentity(s.root, r.PreviousReleaseID); err != nil {
		return "", errors.New("previous overlay identity is unproven")
	}
	return releaseSchemaDir(relDirOf(s.root, r.PreviousReleaseID)), nil
}

func (s *operationRestore) verifyDump(ctx context.Context, backupID, expected string) (string, string, error) {
	if !embeddedupdate.ValidID(backupID) {
		return "", "", errors.New("invalid operation backup ID")
	}
	dump, err := verifyBackupSidecar(s.root, backupID)
	if err != nil {
		return "", "", err
	}
	digest, err := hashFileSHA256(dump)
	if err != nil || expected != "" && digest != expected {
		return "", "", errors.New("backup differs from the pinned digest")
	}
	if err := s.tools.Verify(ctx, dump); err != nil {
		return "", "", errors.New("pg_restore could not list the operation backup")
	}
	return dump, digest, nil
}

func readRestoreInstallID(ctx context.Context, db *sql.DB) (string, error) {
	var id string
	if err := db.QueryRowContext(ctx, `SELECT install_id FROM public.instance_bootstrap WHERE singleton_id = 1`).Scan(&id); err != nil || id == "" || id == "pending-install-id" {
		return "", errors.New("could not prove the installed instance ID")
	}
	return id, nil
}

func (s *operationRestore) verifyDatabase(ctx context.Context, name, schema string, r embeddedupdate.Record) error {
	owner, err := s.owner.Open(ctx, name)
	if err != nil {
		return errors.New("cannot connect to restored database as its owner")
	}
	defer owner.Close()
	if err := (migrations.Runner{DB: owner, Dir: schema}).Verify(ctx); err != nil {
		return errors.New("restored database does not match the previous migration set")
	}
	id, err := readRestoreInstallID(ctx, owner)
	if err != nil || id != r.InstallID {
		return errors.New("restored install ID does not match the pinned instance")
	}
	row, found, err := updatefence.New(owner).Lookup(ctx, r.OperationID)
	if err != nil || !found || !exactOpenFence(r, row) {
		return errors.New("restored database lacks the exact open update fence")
	}
	app, err := s.runtime.Open(ctx, name)
	if err != nil {
		return errors.New("cannot log into restored database as the runtime role")
	}
	defer app.Close()
	if err := database.AssertRuntimeRoleSeparated(ctx, app); err != nil {
		return errors.New("restored runtime login is not separated from the schema owner")
	}
	tx, err := app.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin runtime-role verification transaction")
	}
	defer tx.Rollback()
	var fromRuntime, written string
	if err := tx.QueryRowContext(ctx, `SELECT install_id FROM public.instance_bootstrap WHERE singleton_id = 1`).Scan(&fromRuntime); err != nil || fromRuntime != id {
		return errors.New("restored instance row is not readable as the runtime role")
	}
	if err := tx.QueryRowContext(ctx, `UPDATE public.instance_bootstrap SET install_id = install_id WHERE singleton_id = 1 RETURNING install_id`).Scan(&written); err != nil || written != id {
		return errors.New("restored instance row is not writable as the runtime role")
	}
	return tx.Rollback()
}

func (s *operationRestore) pinBackup(r embeddedupdate.Record, db *sql.DB) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pgToolTimeout)
	defer cancel()
	_, digest, err := s.verifyDump(ctx, r.BackupID, r.BackupDigest)
	if err != nil {
		return "", "", err
	}
	id, err := readRestoreInstallID(ctx, db)
	return digest, id, err
}

func (s *operationRestore) prepare(r embeddedupdate.Record) (embeddedupdate.DatabaseRecovery, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pgToolTimeout)
	defer cancel()
	var empty embeddedupdate.DatabaseRecovery
	if r.BackupDigest == "" || r.InstallID == "" {
		return empty, errors.New("rollback backup and instance ID were not pinned before migration")
	}
	schema, err := s.previousSchema(r)
	if err != nil {
		return empty, err
	}
	dump, _, err := s.verifyDump(ctx, r.BackupID, r.BackupDigest)
	if err != nil {
		return empty, err
	}
	admin, err := s.owner.Open(ctx, "postgres")
	if err != nil {
		return empty, errors.New("cannot open owner connection to postgres for rollback")
	}
	defer admin.Close()
	key := sha256.Sum256([]byte(r.OperationID))
	ready, aside := fmt.Sprintf("bsk_update_%x_ready", key[:16]), fmt.Sprintf("bsk_update_%x_aside", key[:16])
	live := s.owner.Database()
	updateapp.ApplyFixtureStop("restore_before_create")
	if err := backupapp.EnsureOperationRestoreDatabase(ctx, admin, live, ready, aside); err != nil {
		return empty, err
	}
	if err := s.verifyDatabase(ctx, ready, schema, r); err != nil {
		if err := s.tools.RestoreInto(ctx, dump, ready); err != nil {
			return empty, errors.New("pg_restore failed to load the empty rollback database")
		}
		if err := s.verifyDatabase(ctx, ready, schema, r); err != nil {
			return empty, err
		}
	}
	pin, err := backupapp.PinRestoreCutover(ctx, admin, live, ready, aside)
	return embeddedupdate.DatabaseRecovery(pin), err
}

func (s *operationRestore) reconcile(r embeddedupdate.Record) error {
	if r.DatabaseRecovery == nil || r.DatabaseRecovery.Live != s.owner.Database() {
		return errors.New("rollback has no cutover identity matching the deployment database")
	}
	schema, err := s.previousSchema(r)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pgToolTimeout)
	defer cancel()
	admin, err := s.owner.Open(ctx, "postgres")
	if err != nil {
		return errors.New("cannot open owner connection to reconcile rollback")
	}
	defer admin.Close()
	if err := backupapp.ReconcileRestoreCutover(ctx, admin, backupapp.RestoreCutover(*r.DatabaseRecovery), updateapp.ApplyFixtureStop); err != nil {
		return err
	}
	return s.verifyDatabase(ctx, s.owner.Database(), schema, r)
}
