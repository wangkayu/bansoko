package main

import (
	"errors"
	"fmt"

	draftapplication "bansoko/internal/anime/draft/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	"bansoko/internal/events"
	observabilityapplication "bansoko/internal/observability/application"
	plugintargetapplication "bansoko/internal/plugintarget/application"
	plugintargetport "bansoko/internal/plugintarget/port"
)

type businessDeliveryPublisher interface {
	SetBusinessDeliveryEnqueuer(plugintargetport.BusinessDeliveryEnqueuer)
	BusinessDeliveryReady() bool
}

// wireProductionBusinessPublishers connects the active owner publishers before
// any producer or worker can handle a request.
func wireProductionBusinessPublishers(core *coreApplicationGraph, maintenance *fileMaintenanceGraph, draft *draftapplication.Service, ownership *ownershipapplication.Service, target *plugintargetapplication.Service, hub *events.Hub, observability *observabilityapplication.Service) error {
	if core == nil || maintenance == nil || target == nil || hub == nil || observability == nil {
		return errors.New("missing business publisher dependencies")
	}
	publishers := []struct {
		name    string
		service businessDeliveryPublisher
	}{
		{"RSS", core.rssService},
		{"Download", core.downloadService},
		{"Draft", draft},
		{"Ownership", ownership},
		{"Cleanup", maintenance.cleanupService},
		{"Replacement", maintenance.replacementDecisions},
		{"Placement", maintenance.placementService},
		{"Alignment", maintenance.alignmentOperations},
	}
	for _, publisher := range publishers {
		publisher.service.SetBusinessDeliveryEnqueuer(target)
		if !publisher.service.BusinessDeliveryReady() {
			return fmt.Errorf("%s business delivery is not wired", publisher.name)
		}
	}
	core.downloadService.SetEventHub(hub)
	draft.SetEventHub(hub)
	draft.SetObservabilityRecorder(observability)
	if !core.downloadService.EventHubReady() || !draft.EventHubReady() || !draft.ObservabilityRecorderReady() {
		return errors.New("Download or Draft event hub or Draft recorder is not wired")
	}
	return nil
}
