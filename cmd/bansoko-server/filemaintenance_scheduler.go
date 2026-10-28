package main

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/filemaintenance"
	filemaintenancepolicy "bansoko/internal/filemaintenance/policy"
	observabilityport "bansoko/internal/observability/port"
	reconciliationapplication "bansoko/internal/reconciliation/application"
	"bansoko/internal/transport/httpapi"
)

// scanSubmitterAdapter adapts the reconciliation SubmitScan to the
// scheduler port. Every due tick passes its own request identity (periodic
// trigger) so an independent trigger always starts a newer generation; a
// replay of the same request identity reuses the completed generation. The
// scheduler is subscription-neutral: active and stopped instances both scan.
type scanSubmitterAdapter struct {
	reconciliation *reconciliationapplication.Service
}

func (a scanSubmitterAdapter) SubmitScan(ctx context.Context, animeID, treeTop, requestID string) error {
	trigger := reconciliationapplication.ScanSubmitTriggerPeriodic
	_, err := a.reconciliation.SubmitScan(ctx, reconciliationapplication.ScanSubmitInput{
		ManagedAnimeID: animeID,
		TreeTop:        &treeTop,
		Trigger:        &trigger,
		RequestID:      &requestID,
	})
	return err
}

// animeListerAdapter adapts the anime read service to the scheduler port.
type animeListerAdapter struct {
	readmodel httpapi.ReadmodelService
}

func (a animeListerAdapter) ListAnimeIDs(ctx context.Context) ([]string, error) {
	items, err := a.readmodel.ListAnimeApplication(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ManagedAnime.ID)
	}
	return ids, nil
}

// currentScanAdapter adapts the reconciliation GetScan to the overview's
// nullable scan owner port (absent scan -> nil, never an error).
type currentScanAdapter struct {
	reconciliation *reconciliationapplication.Service
}

func (a currentScanAdapter) CurrentScan(ctx context.Context, animeID string) (*reconciliationapplication.ScanView, error) {
	view, err := a.reconciliation.GetScan(ctx, animeID)
	if err != nil {
		var notFound *apperrors.NotFound
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, err
	}
	return &view, nil
}

// startFileMaintenanceScheduler runs the periodic scan loop when any policy
// enables it, and the alignment request dispatch on every tick. The loop never
// blocks the core chain.
func startFileMaintenanceScheduler(ctx context.Context, db *sql.DB, policies *filemaintenancepolicy.PolicyService, reconciliation *reconciliationapplication.Service, readmodel httpapi.ReadmodelService, retention filemaintenance.AlignmentRetention, activity observabilityport.ActivityWriter, logger observabilityport.RuntimeLogger) {
	scheduler := filemaintenance.NewScanScheduler(
		db,
		policies,
		scanSubmitterAdapter{reconciliation: reconciliation},
		animeListerAdapter{readmodel: readmodel},
		logger,
	)
	scheduler.SetAlignmentRequests(retention, activity)
	go scheduler.Run(ctx, time.Minute)
}
