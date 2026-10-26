// Command bansoko-recovery runs the deployment recovery reconcile: it drives the
// typed reconciler registry over the core qB/DownloadTask, Artifact and
// FileOperation owners plus enabled optional effect owners, persists the
// orchestration checkpoint durably, and emits the machine-readable automation
// resume gate.
//
// It is deployment-owned: it only calls typed ReconcileUnknownEffect ports,
// never reads owner-private checkpoints, and never mutates any owner row
// outside the owner handlers' own application transactions.
//
// Usage:
//
//	bansoko-recovery start --operation <id> --window <id> --old-bundle <id> \
//	    --new-bundle <id> --backup <id> [--old-bundle-smoke=passed]
//	bansoko-recovery status [--operation <id>]
//	bansoko-recovery recover-fence --operation <id> --owner <owner> \
//	    --manifest-digest <digest> --expected-state active \
//	    --terminal-state <succeeded|failed|rolled_back> --evidence <ticket>
//
// Environment: BANSOKO_DATABASE_URL (the schema-owner connection of the deployment),
// BANSOKO_SECRET_ENCRYPTION_KEY or the generated key in BANSOKO_CONFIG_DIR/secrets.json
// (decrypts the typed qB connection secret),
// BANSOKO_SCHEMA_DIR (default db/migrations).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	artifactadapter "bansoko/internal/artifact/adapter"
	artifactapplication "bansoko/internal/artifact/application"
	configurationpersistence "bansoko/internal/configuration/adapter/persistence"
	configurationapplication "bansoko/internal/configuration/application"
	"bansoko/internal/deployment"
	downloadapplication "bansoko/internal/download/application"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/filemanager"
	filemanagerpersistence "bansoko/internal/filemanager/persistence"
	fileoperationapplication "bansoko/internal/fileoperation/application"
	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/runtime/bootsecrets"
	"bansoko/internal/runtime/config"
	"bansoko/internal/runtime/database"
	"bansoko/internal/runtime/migrations"
	updatefence "bansoko/internal/update/fence"
	"bansoko/internal/workitems"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "bansoko-recovery: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	start := flag.NewFlagSet("start", flag.ExitOnError)
	opID := start.String("operation", "", "deployment operation id (stable across retries)")
	windowID := start.String("window", "", "deployment window id")
	oldBundle := start.String("old-bundle", "", "old bundle identity")
	newBundle := start.String("new-bundle", "", "new bundle identity")
	backup := start.String("backup", "", "upgrade-preceding PostgreSQL backup identity")
	smoke := start.String("old-bundle-smoke", "", "operator assertion: old bundle auth/owner/readiness smoke passed")
	start.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: bansoko-recovery start|status|recover-fence [flags]")
	}

	status := flag.NewFlagSet("status", flag.ExitOnError)
	statusOp := status.String("operation", "", "operation id to inspect (default: latest run)")

	recoverFence := flag.NewFlagSet("recover-fence", flag.ExitOnError)
	recoverOperation := recoverFence.String("operation", "", "exact fence operation id")
	recoverOwner := recoverFence.String("owner", "", "exact fence owner: embedded")
	recoverDigest := recoverFence.String("manifest-digest", "", "exact signed manifest digest")
	recoverState := recoverFence.String("expected-state", "", "exact open fence state (active)")
	recoverTerminal := recoverFence.String("terminal-state", "", "recovery outcome: succeeded, failed or rolled_back")
	recoverEvidence := recoverFence.String("evidence", "", "human/operator evidence, such as a ticket reference")

	switch {
	case len(args) == 0:
		start.Usage()
		return fmt.Errorf("missing subcommand: start|status|recover-fence")
	case args[0] == "start":
		if err := start.Parse(args[1:]); err != nil {
			return err
		}
		return runStart(*opID, *windowID, *oldBundle, *newBundle, *backup, *smoke)
	case args[0] == "status":
		if err := status.Parse(args[1:]); err != nil {
			return err
		}
		return runStatus(*statusOp)
	case args[0] == "recover-fence":
		if err := recoverFence.Parse(args[1:]); err != nil {
			return err
		}
		return runRecoverFence(*recoverOperation, *recoverOwner, *recoverDigest, *recoverState, *recoverTerminal, *recoverEvidence)
	default:
		return fmt.Errorf("unknown subcommand %q: start|status|recover-fence", args[0])
	}
}

func runRecoverFence(operationID, owner, manifestDigest, expectedState, terminalState, evidence string) error {
	if strings.TrimSpace(operationID) == "" || strings.TrimSpace(owner) == "" ||
		strings.TrimSpace(manifestDigest) == "" || strings.TrimSpace(expectedState) == "" ||
		strings.TrimSpace(terminalState) == "" || strings.TrimSpace(evidence) == "" {
		return fmt.Errorf("recover-fence requires --operation --owner --manifest-digest --expected-state --terminal-state --evidence")
	}
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	if err := updatefence.New(db).RecoverExact(context.Background(), operationID, owner, manifestDigest, expectedState, terminalState, evidence); err != nil {
		return fmt.Errorf("recover exact update fence: %w", err)
	}
	fmt.Println(`{"state":"recovered","operation":"` + operationID + `"}`)
	return nil
}

func schemaDir() string {
	if dir := os.Getenv("BANSOKO_SCHEMA_DIR"); dir != "" {
		return dir
	}
	return "db/migrations"
}

func openDB() (*sql.DB, error) {
	dsn := os.Getenv("BANSOKO_DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("BANSOKO_DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{DSN: dsn})
	if err != nil {
		return nil, err
	}
	// The recovery CLI must run against the full schema; verify rather
	// than auto-migrate (the migration job owns schema changes).
	if err := (migrations.Runner{DB: db, Dir: schemaDir()}).Verify(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema verify: %w", err)
	}
	return db, nil
}

func runStart(operationID, windowID, oldBundle, newBundle, backup, smoke string) error {
	if operationID == "" || windowID == "" || oldBundle == "" || newBundle == "" || backup == "" {
		return fmt.Errorf("start requires --operation --window --old-bundle --new-bundle --backup")
	}
	smokePassed := false
	if smoke != "" {
		if smoke != "passed" {
			return fmt.Errorf("--old-bundle-smoke accepts only \"passed\" (asserted by the deployment operator)")
		}
		smokePassed = true
	}
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	registry, err := wireRegistry(db)
	if err != nil {
		return err
	}
	store := deployment.NewPGStore(db)
	orchestrator := deployment.NewOrchestrator(registry, store)
	plan := deployment.Plan{
		OperationID:          operationID,
		WindowID:             windowID,
		OldBundleIdentity:    oldBundle,
		NewBundleIdentity:    newBundle,
		BackupIdentity:       backup,
		SchemaIdentity:       "verify:db/migrations",
		BuildIdentity:        "bansoko-recovery",
		OldBundleSmokePassed: smokePassed,
	}
	result, err := orchestrator.Run(context.Background(), plan)
	if err != nil {
		return err
	}
	output, err := resultJSON(result)
	if err != nil {
		return err
	}
	fmt.Println(string(output))
	if !result.Gate.Passed {
		return fmt.Errorf("automation resume gate failed: %d failure(s)", len(result.Gate.Failures))
	}
	return nil
}

func runStatus(operationID string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	store := deployment.NewPGStore(db)
	var run *deployment.Run
	if operationID != "" {
		run, err = store.Load(context.Background(), operationID)
		if err != nil {
			return err
		}
	} else {
		run, err = store.Latest(context.Background())
		if err != nil {
			return err
		}
	}
	if run == nil {
		fmt.Println(`{"state":"no_run"}`)
		return nil
	}
	output, err := json.MarshalIndent(map[string]any{
		"state":              run.State,
		"operation":          run.ID,
		"window":             run.WindowID,
		"old_bundle":         run.OldBundleIdentity,
		"new_bundle":         run.NewBundleIdentity,
		"backup":             run.BackupIdentity,
		"schema_identity":    run.SchemaIdentity,
		"build_identity":     run.BuildIdentity,
		"registry_inventory": run.RegistryInventory,
		"checkpoint":         run.Checkpoint,
		"updated_at":         run.UpdatedAt.Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(output))
	return nil
}

// wireRegistry composes the typed reconciler registry with the three core
// owner handlers, mirroring the server composition root.
func wireRegistry(db *sql.DB) (*deployment.Registry, error) {
	workStore, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		return nil, err
	}
	// The deployment key is the explicit environment override, else the key the
	// bootstrap generated into the config directory.
	stored, err := bootsecrets.Load(bootsecrets.ConfigDir(os.Getenv), os.Getenv)
	if err != nil {
		return nil, err
	}
	encryptionKey := stored.SecretEncryptionKey
	if encryptionKey == "" {
		return nil, fmt.Errorf("the deployment encryption key (BANSOKO_SECRET_ENCRYPTION_KEY or the config directory secrets file) is required to resolve the typed qB connection")
	}
	secrets, err := configurationapplication.NewDatabaseSecretStore(db, []byte(encryptionKey), configurationpersistence.NewRepository(db))
	if err != nil {
		return nil, err
	}
	configService := configurationapplication.NewService(db, secrets)
	httpConfigService := configurationapplication.NewHTTPService(configService)

	qbClient := qbittorrent.ConfiguredClient{Source: httpConfigService}
	downloadService, err := downloadapplication.NewService(db, workStore, qbClient, httpConfigService)
	if err != nil {
		return nil, err
	}
	artifactService, err := artifactapplication.NewService(db, artifactadapter.OSArtifactInspector{})
	if err != nil {
		return nil, err
	}
	fileService, err := fileoperationapplication.NewService(db, filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	if err != nil {
		return nil, err
	}
	// A resumed execute claim is the primary restart path; this typed reconciler
	// additionally covers abandoned attempts when claims are not reclaimed.
	alignmentOperations := filemaintenance.NewAlignmentService(db)
	alignmentOperations.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	alignmentRegistration := deployment.Registration{
		Key:     "alignment_operation",
		Effect:  deployment.EffectIdentity{Owner: "alignment_operation", Effect: "filesystem_unknown_effect"},
		Version: 1,
		Handler: alignmentOperations,
		Enabled: func() bool { return true },
	}
	replacementDeletions := filemaintenance.NewCleanupService(db)
	// User file operations have no root layer: the cleanup anchor is the
	// deployment's own boundary, injected here as the constant it is.
	replacementDeletions.SetFileExecutor(filesystem.NewOSCleanupExecutor(filemaintenance.CleanupAnchor))
	replacementDeletions.SetWorkItems(workStore)
	cleanupRegistration := deployment.Registration{
		Key:     "replacement_deletion",
		Effect:  deployment.EffectIdentity{Owner: "replacement_deletion", Effect: "filesystem_unknown_effect"},
		Version: 1,
		Handler: replacementDeletions,
		Enabled: func() bool { return true },
	}
	// Offset migration operations own a typed reconcile for the uncertain
	// Offset migration attempts also converge through the typed effect reconciler.
	offsetMigrationExecute := filemaintenance.NewOffsetMigrationExecuteService(db)
	offsetMigrationExecute.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	offsetMigrationRegistration := deployment.Registration{
		Key:     "offset_migration_operation",
		Effect:  deployment.EffectIdentity{Owner: "offset_migration_operation", Effect: "filesystem_unknown_effect"},
		Version: 1,
		Handler: offsetMigrationExecute,
		Enabled: func() bool { return true },
	}
	// File manager user operations: a running operation's stage is
	// settled from its exact invocation receipt, resumed by its work item when
	// it provably never started, or blocks the gate as an unknown effect.
	fileManagerStore, err := filemanagerpersistence.NewStore(db)
	if err != nil {
		return nil, err
	}
	fileManager, err := filemanager.NewService(fileManagerStore, filemanager.NewDenialZone(config.ApplicationDataDirs(os.Getenv)...))
	if err != nil {
		return nil, err
	}
	userOperations := filemaintenance.NewUserOperationService(db, workStore,
		filesystem.OSFileExecutor{}, filesystem.OSFileObserver{},
		func() filemaintenance.CleanupFileExecutor {
			return filesystem.NewOSCleanupExecutor(filemaintenance.CleanupAnchor)
		},
		filemanagerpersistence.UserOperationGuard{Service: fileManager})
	userOperationRegistration := deployment.Registration{
		Key:     "file_manager_operation",
		Effect:  deployment.EffectIdentity{Owner: "file_manager_operation", Effect: "filesystem_unknown_effect"},
		Version: 1,
		Handler: userOperations,
		Enabled: func() bool { return true },
	}
	return deployment.NewRegistry(deployment.DefaultRequiredKeys,
		deployment.Registration{
			Key:     "qb_download_task",
			Effect:  deployment.EffectIdentity{Owner: "download_task", Effect: "qb_unknown_effect"},
			Version: 1,
			Handler: downloadService,
		},
		deployment.Registration{
			Key:     "artifact",
			Effect:  deployment.EffectIdentity{Owner: "artifact", Effect: "inspect_unknown_effect"},
			Version: 1,
			Handler: artifactService,
		},
		deployment.Registration{
			Key:     "file_operation",
			Effect:  deployment.EffectIdentity{Owner: "file_operation", Effect: "filesystem_unknown_effect"},
			Version: 1,
			Handler: fileService,
		},
		alignmentRegistration,
		cleanupRegistration,
		offsetMigrationRegistration,
		userOperationRegistration,
	)
}

// resultJSON renders the full machine-readable run outcome.
func resultJSON(result deployment.Result) ([]byte, error) {
	gate := result.Gate
	return json.MarshalIndent(map[string]any{
		"protocol_version":   deployment.CheckpointProtocolVersion,
		"operation":          result.Run.ID,
		"window":             result.Run.WindowID,
		"old_bundle":         result.Run.OldBundleIdentity,
		"new_bundle":         result.Run.NewBundleIdentity,
		"backup":             result.Run.BackupIdentity,
		"schema_identity":    result.Run.SchemaIdentity,
		"build_identity":     result.Run.BuildIdentity,
		"registry_inventory": result.Run.RegistryInventory,
		"checkpoint":         result.Run.Checkpoint,
		"gate":               gate,
	}, "", "  ")
}
