package main

import (
	"context"
	"database/sql"

	"bansoko/internal/events"
	filemaintenancepolicy "bansoko/internal/filemaintenance/policy"
	"bansoko/internal/filemanager"
	filemanagerpersistence "bansoko/internal/filemanager/persistence"
	observabilityport "bansoko/internal/observability/port"
	reconciliationapplication "bansoko/internal/reconciliation/application"
	"bansoko/internal/workitems"
)

// fileManagerAlignmentPolicy reads the effective file maintenance policy the
// alignment consumer is gated by (B13 P1 align).
type fileManagerAlignmentPolicy struct {
	policies *filemaintenancepolicy.PolicyService
}

func (p fileManagerAlignmentPolicy) AlignmentEnabled(ctx context.Context, managedAnimeID string) (bool, error) {
	effective, err := p.policies.GetEffective(ctx, managedAnimeID)
	if err != nil {
		return false, err
	}
	return effective.AlignmentEnabled != nil && *effective.AlignmentEnabled, nil
}

// scanSubmitter is the reconciliation scan admission an align enqueues.
type scanSubmitter interface {
	SubmitScan(ctx context.Context, input reconciliationapplication.ScanSubmitInput) (reconciliationapplication.ScanView, error)
}

// fileManagerAligner admits a user align: a manual scan of the anime, whose
// calibrate and alignment consumer realign every file of the anime through
// the existing chain.
type fileManagerAligner struct {
	scans scanSubmitter
}

func (a fileManagerAligner) Align(ctx context.Context, admission filemanager.AlignAdmission) (filemanager.AlignOutcome, error) {
	requestID := admission.IdempotencyKey
	trigger := reconciliationapplication.ScanSubmitTriggerManual
	if _, err := a.scans.SubmitScan(ctx, reconciliationapplication.ScanSubmitInput{
		ManagedAnimeID: admission.ManagedAnimeID, RequestID: &requestID, Trigger: &trigger,
	}); err != nil {
		return filemanager.AlignOutcome{}, err
	}
	return filemanager.AlignOutcome{ManagedAnimeID: admission.ManagedAnimeID}, nil
}

// wireFileManagerCommands attaches the P1 write ports once the activity
// writer, event hub, scan admission and the season catalog exist.
func wireFileManagerCommands(
	graph *fileMaintenanceGraph,
	db *sql.DB,
	workStore *workitems.Store,
	activity observabilityport.ActivityWriter,
	recorder observabilityport.Recorder,
	hub *events.Hub,
	scans scanSubmitter,
	seasons filemanager.SeasonCatalog,
) error {
	commands, err := filemanagerpersistence.NewCommands(db, workStore, activity, hub)
	if err != nil {
		return err
	}
	reader, err := filemanagerpersistence.NewStore(db)
	if err != nil {
		return err
	}
	graph.fileManager.SetCommands(commands, fileManagerAligner{scans: scans})
	// B13 P2: a match renders through the same naming renderer; the season
	// lists share the lookup and its cache.
	graph.fileManager.SetMatch(graph.namingRenderer, reader, seasons)
	graph.userOperations.SetRecorder(recorder)
	graph.userOperations.SetEventHub(hub)
	return nil
}
