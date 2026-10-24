package main

import (
	"context"
	"testing"

	acquisitiontarget "bansoko/internal/acquisition/target"
	animeapplication "bansoko/internal/anime/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	configurationapplication "bansoko/internal/configuration/application"
	"bansoko/internal/filemanager"
	"bansoko/internal/events"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// TestProductionCompositionWiresTargetAcquisition builds the real production
// composition graph (not a hand-assembled service fixture) and proves the RSS
// and finite one-shot admission paths carry the actual filesystem probe and
// persistence-backed target owner. A graph that silently omitted either would
// fall back to an admission that skips its target checks.
func TestProductionCompositionWiresTargetAcquisition(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	logger := observabilityport.NopRuntimeLogger{}

	workStore, err := workitems.NewStore(db, workitems.DefaultRetentionDays, logger)
	if err != nil {
		t.Fatal(err)
	}
	configService := configurationapplication.NewService(db, nil)
	httpConfigService := configurationapplication.NewHTTPService(configService)
	eventHub := events.NewHub()
	animeService, err := animeapplication.NewService(db, workStore, animeapplication.HTTPFeedFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	ownershipService, err := ownershipapplication.NewService(db, ownershipapplication.Dependencies{WorkItems: workStore})
	if err != nil {
		t.Fatal(err)
	}
	draftService, err := provideDraftService(db, workStore)
	if err != nil {
		t.Fatal(err)
	}
	fileMaintenanceGraph, err := provideFileMaintenanceGraph(db, workStore, draftService, ownershipService, filemanager.NewDenialZone(t.TempDir()), logger)
	if err != nil {
		t.Fatal(err)
	}

	coreGraph, err := provideCoreApplicationGraph(
		db, workStore, httpConfigService, configService,
		nil, nil, eventHub, logger, animeService, fileMaintenanceGraph,
	)
	if err != nil {
		t.Fatalf("production composition must build with the real target boundary: %v", err)
	}
	if !coreGraph.rssService.TargetAcquisitionReady() {
		t.Fatal("production RSS target acquisition (filesystem probe, target store) is not wired")
	}
	if !coreGraph.artifactService.MediaProberReady() {
		t.Fatal("production artifact inspection has no media prober: file.resolution, file.video_codec, file.audio_codec and file.container would always be empty")
	}
	applicationGraph, err := provideApplicationGraph(
		db, workStore, nil, configService, eventHub, logger, nil, nil,
		animeService, ownershipService, draftService, coreGraph.rssService, coreGraph.downloadService, coreGraph.artifactService,
		coreGraph.fileOperationService, fileMaintenanceGraph.replacementDecisions,
		fileMaintenanceGraph.cleanupService, fileMaintenanceGraph.placementService,
		fileMaintenanceGraph.replacementTrigger, fileMaintenanceGraph, coreGraph.targetStore,
	)
	if err != nil {
		t.Fatalf("production application graph must build with target acquisition: %v", err)
	}
	if !applicationGraph.oneShotService.TargetAcquisitionReady() {
		t.Fatal("production one-shot target acquisition (filesystem probe, target store) is not wired")
	}
	var targetCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM acquisition_targets`).Scan(&targetCount); err != nil {
		t.Fatalf("target store is not backed by the real schema: %v", err)
	}
}

// TestProductionCompositionRejectsUnwiredTargetAcquisition proves the startup
// guard fails closed: a composition that omits the target boundary is rejected
// instead of booting an admission path that would silently skip target checks.
func TestProductionCompositionRejectsUnwiredTargetAcquisition(t *testing.T) {
	if err := requireProductionTargetAcquisition(nil, nil); err == nil {
		t.Fatal("an empty composition must be rejected")
	}
	// A service that never received SetTargetAcquisition reports not ready even
	// though it is a usable receiver, so the guard catches a dropped setter.
	unwired := &rssTargetReadinessProbe{ready: false}
	if err := requireProductionTargetAcquisition(unwired, unwired); err == nil {
		t.Fatal("a service without the target boundary must be rejected")
	}
	wired := &rssTargetReadinessProbe{ready: true}
	if err := requireProductionTargetAcquisition(wired, wired); err != nil {
		t.Fatalf("fully wired target acquisition must pass the guard: %v", err)
	}
}

type rssTargetReadinessProbe struct{ ready bool }

func (p *rssTargetReadinessProbe) TargetAcquisitionReady() bool { return p.ready }

type noopTargetRepository struct{}

func (noopTargetRepository) ReplaceSet(context.Context, txscope.Tx, acquisitiontarget.Set) error {
	return nil
}
func (noopTargetRepository) UpsertObservation(context.Context, txscope.Tx, acquisitiontarget.Observation) error {
	return nil
}
func (noopTargetRepository) ReadTargets(context.Context, string) ([]acquisitiontarget.TargetRow, bool, error) {
	return nil, false, nil
}
func (noopTargetRepository) PersistObservations(context.Context, []acquisitiontarget.Observation) error {
	return nil
}
func (noopTargetRepository) ReadObservationVersions(context.Context, []string) (map[string]int64, error) {
	return nil, nil
}

// TestTargetStoreConfiguredReportsWiring pins the store readiness signal the
// startup guard relies on: an empty facade must never be reported ready.
func TestTargetStoreConfiguredReportsWiring(t *testing.T) {
	if (acquisitiontarget.Store{}).Configured() {
		t.Fatal("an empty target store must not report configured")
	}
	if !acquisitiontarget.NewStore(noopTargetRepository{}).Configured() {
		t.Fatal("a repository-backed target store must report configured")
	}
}
