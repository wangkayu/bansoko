package httpapi

import (
	filemaintenance "bansoko/internal/filemaintenance"
	filemainttransport "bansoko/internal/filemaintenance/transport"
	reconciliationapplication "bansoko/internal/reconciliation/application"
	"bansoko/internal/transport/httpapi/generated"
)

func mapFileMaintenanceOverviewView(value filemaintenance.OverviewView) (generated.FileMaintenanceOverviewView, error) {
	mapped := generated.FileMaintenanceOverviewView{ManagedAnimeId: generated.ID(value.ManagedAnimeID)}
	if value.Scan != nil {
		scan := mapOverviewScanView(*value.Scan)
		mapped.Scan = &scan
	}
	if value.Consumers != nil {
		consumers := make([]generated.ReconciliationConsumerStateView, len(*value.Consumers))
		for index, consumer := range *value.Consumers {
			consumers[index] = mapOverviewConsumerView(consumer)
		}
		mapped.Consumers = &consumers
	}
	if value.Placements != nil {
		placements := mapPlacementViews(*value.Placements)
		mapped.Placements = &placements
	}
	if value.Decisions != nil {
		decisions := mapReplacementDecisionViews(*value.Decisions)
		mapped.Decisions = &decisions
	}
	if value.Deletions != nil {
		deletions := make([]generated.ReplacementDeletionView, len(*value.Deletions))
		for index, cleanup := range *value.Deletions {
			deletions[index] = mapOverviewCleanupView(cleanup)
		}
		mapped.Deletions = &deletions
	}
	if value.Alignments != nil {
		alignments := make([]generated.AlignmentOperationView, len(*value.Alignments))
		for index, alignment := range *value.Alignments {
			alignments[index] = mapOverviewAlignmentView(alignment)
		}
		mapped.Alignments = &alignments
	}
	if value.PlacementIssues != nil {
		issues := filemainttransport.PlacementIssueViews(*value.PlacementIssues)
		mapped.PlacementIssues = &issues
	}
	if value.CompanionIssues != nil {
		issues := filemainttransport.CompanionIssueViews(*value.CompanionIssues)
		mapped.CompanionIssues = &issues
	}
	if value.InstancePolicy != nil {
		instance, err := filemainttransport.InstanceReplacementPolicyView(*value.InstancePolicy)
		if err != nil {
			return generated.FileMaintenanceOverviewView{}, err
		}
		mapped.InstancePolicy = &instance
	}
	if value.GlobalDefault != nil {
		global, err := filemainttransport.ReplacementPolicyDefaultView(*value.GlobalDefault)
		if err != nil {
			return generated.FileMaintenanceOverviewView{}, err
		}
		mapped.GlobalDefault = &global
	}
	return mapped, nil
}

func mapOverviewScanView(value reconciliationapplication.ScanView) generated.ReconciliationScanView {
	return generated.ReconciliationScanView{
		CapacityStats:    cloneJSONMap(value.CapacityStats),
		CompletedAt:      cloneTime(value.CompletedAt),
		FailureCode:      cloneString(value.FailureCode),
		FailureReason:    cloneString(value.FailureReason),
		Generation:       int(value.Generation),
		InputFingerprint: value.InputFingerprint,
		ManagedAnimeId:   generated.ID(value.ManagedAnimeID),
		Progress:         cloneJSONMap(value.Progress),
		StartedAt:        cloneTime(value.StartedAt),
		State:            generated.ReconciliationScanViewState(value.State),
		Version:          int(value.Version),
	}
}

func mapOverviewConsumerView(value filemaintenance.ReconciliationConsumerStateView) generated.ReconciliationConsumerStateView {
	return generated.ReconciliationConsumerStateView{
		Consumer:       generated.ReconciliationConsumerStateViewConsumer(value.Consumer),
		Generation:     value.Generation,
		ManagedAnimeId: generated.ID(value.ManagedAnimeID),
		Reason:         cloneStringPtr(value.Reason),
		ReasonCode:     cloneStringPtr(value.ReasonCode),
		State:          generated.ReconciliationConsumerStateViewState(value.State),
		UpdatedAt:      value.UpdatedAt,
		Version:        value.Version,
	}
}

func mapOverviewCleanupView(value filemaintenance.ReplacementDeletionView) generated.ReplacementDeletionView {
	return generated.ReplacementDeletionView{
		CreatedAt:         value.CreatedAt,
		FailureCode:       cloneStringPtr(value.FailureCode),
		Id:                value.ID,
		ManagedAnimeId:    value.ManagedAnimeID,
		PlacementId:       value.PlacementID,
		Reason:            cloneStringPtr(value.Reason),
		ReplacementRef:    value.ReplacementRef,
		AdmissionMode:     generated.ReplacementDeletionViewAdmissionMode(value.AdmissionMode),
		SourceFingerprint: value.SourceFingerprint,
		SourcePath:        value.SourcePath,
		State:             generated.ReplacementDeletionViewState(value.State),
		UpdatedAt:         cloneTimePointer(value.UpdatedAt),
		Version:           value.Version,
	}
}

func mapOverviewAlignmentView(value filemaintenance.AlignmentOperationView) generated.AlignmentOperationView {
	return generated.AlignmentOperationView{
		CompletedAt:       cloneTimePointer(value.CompletedAt),
		CreatedAt:         value.CreatedAt,
		FailureCode:       cloneStringPtr(value.FailureCode),
		Id:                value.ID,
		ManagedAnimeId:    value.ManagedAnimeID,
		ObservationRef:    value.ObservationRef,
		PolicyVersion:     cloneIntPtr(value.PolicyVersion),
		Reason:            cloneStringPtr(value.Reason),
		ScanGeneration:    value.ScanGeneration,
		SourceFingerprint: value.SourceFingerprint,
		SourcePath:        value.SourcePath,
		State:             generated.AlignmentOperationViewState(value.State),
		TargetPath:        value.TargetPath,
		UpdatedAt:         cloneTimePointer(value.UpdatedAt),
		Version:           value.Version,
	}
}
