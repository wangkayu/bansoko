package main

import (
	"database/sql"
	"testing"

	draftapplication "bansoko/internal/anime/draft/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	downloadapplication "bansoko/internal/download/application"
	namingfacts "bansoko/internal/episodetitle/namingfacts"
	"bansoko/internal/events"
	"bansoko/internal/filemaintenance"
	fileoperationapplication "bansoko/internal/fileoperation/application"
	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	rssapplication "bansoko/internal/rss/application"
	"bansoko/internal/workitems"
)

func TestProductionCompositionWiresBusinessPublishers(t *testing.T) {
	// The production wiring only stores collaborators. No driver, network, or
	// schema should be needed to verify those connections.
	db := new(sql.DB)
	logger := observabilityport.NopRuntimeLogger{}
	store, err := workitems.NewStore(db, workitems.DefaultRetentionDays, logger)
	if err != nil {
		t.Fatal(err)
	}
	observability, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	core := &coreApplicationGraph{
		rssService: new(rssapplication.Service), downloadService: new(downloadapplication.Service),
		fileOperationService: new(fileoperationapplication.Service),
	}
	maintenance := &fileMaintenanceGraph{
		cleanupService: new(filemaintenance.CleanupService), replacementDecisions: new(filemaintenance.ReplacementDecisionService),
		placementService: new(filemaintenance.PlacementService), alignmentOperations: new(filemaintenance.AlignmentService),
	}
	draft := new(draftapplication.Service)
	ownership := new(ownershipapplication.Service)
	hub := events.NewHub()
	plugin, err := initializePluginRuntime(db, nil, nil, store, logger, core.downloadService, core.fileOperationService, core.rssService, observability, nil, nil, nil, new(namingfacts.Querier))
	if err != nil {
		t.Fatal(err)
	}
	if err := wireProductionBusinessPublishers(core, maintenance, draft, ownership, plugin.targetService, hub, observability); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name  string
		ready bool
	}{
		{"RSS delivery", core.rssService.BusinessDeliveryReady()},
		{"Download delivery", core.downloadService.BusinessDeliveryReady()},
		{"Draft delivery", draft.BusinessDeliveryReady()},
		{"Ownership delivery", ownership.BusinessDeliveryReady()},
		{"Cleanup delivery", maintenance.cleanupService.BusinessDeliveryReady()},
		{"Replacement delivery", maintenance.replacementDecisions.BusinessDeliveryReady()},
		{"Placement delivery", maintenance.placementService.BusinessDeliveryReady()},
		{"Alignment delivery", maintenance.alignmentOperations.BusinessDeliveryReady()},
		{"Download event hub", core.downloadService.EventHubReady()},
		{"Draft event hub", draft.EventHubReady()},
		{"Draft recorder", draft.ObservabilityRecorderReady()},
		{"FileOperation diagnostics", core.fileOperationService.DiagnosticRecorderReady()},
	} {
		if !check.ready {
			t.Errorf("%s is not wired", check.name)
		}
	}
	if err := wireProductionBusinessPublishers(core, &fileMaintenanceGraph{}, draft, ownership, plugin.targetService, hub, observability); err == nil {
		t.Fatal("production wiring must reject a missing publisher")
	}
}
