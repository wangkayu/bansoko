// Package acquisitionrecord maps the execution-record read model onto the
// generated contract types. HTTP and MCP share it so both transports return
// the same scope and the same redacted values: redaction and visibility are
// decided by the read model, and this package only converts types.
package acquisitionrecord

import (
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/transport/httpapi/generated"
)

// MapPage maps one record list page.
func MapPage(page readmodelapplication.AcquisitionRecordPage) generated.AcquisitionRecordPage {
	items := make([]generated.AcquisitionRecordSummary, len(page.Items))
	for i, item := range page.Items {
		items[i] = MapSummary(item)
	}
	return generated.AcquisitionRecordPage{AsOf: page.AsOf, Items: items, NextCursor: page.NextCursor,
		OriginCounts: generated.AcquisitionRecordOriginCounts{Subscription: page.OriginCounts.Subscription, OneShot: page.OriginCounts.OneShot}}
}

// MapFilePage maps one record file page.
func MapFilePage(page readmodelapplication.AcquisitionFilePage) generated.AcquisitionFilePage {
	items := make([]generated.AcquisitionFile, len(page.Items))
	for i, item := range page.Items {
		items[i] = MapFile(item)
	}
	return generated.AcquisitionFilePage{AcquisitionId: generated.ID(page.AcquisitionID), AsOf: page.AsOf, Items: items, NextCursor: page.NextCursor}
}

func mapDisplay(value readmodelapplication.AcquisitionDisplayValue) generated.AcquisitionDisplayValue {
	return generated.AcquisitionDisplayValue{State: generated.AcquisitionDisplayValueState(value.State), Value: value.Value}
}

func mapCounts(value *readmodelapplication.AcquisitionCounts) *generated.AcquisitionCounts {
	if value == nil {
		return nil
	}
	return &generated.AcquisitionCounts{Total: value.Total, Required: value.Required, Excluded: value.Excluded, Succeeded: value.Succeeded, Failed: value.Failed}
}

func mapAnime(value *readmodelapplication.AcquisitionAnimeLink) *generated.AcquisitionAnimeLink {
	if value == nil {
		return nil
	}
	return &generated.AcquisitionAnimeLink{ManagedAnimeId: generated.ID(value.ManagedAnimeID), Title: value.Title}
}

func mapStageName(value *string) *generated.AcquisitionStageName {
	if value == nil {
		return nil
	}
	name := generated.AcquisitionStageName(*value)
	return &name
}

func mapStageState(value *string) *generated.AcquisitionStageState {
	if value == nil {
		return nil
	}
	state := generated.AcquisitionStageState(*value)
	return &state
}

// mapID converts an optional opaque id; a missing handle stays null.
func mapID(value *string) *generated.ID {
	if value == nil {
		return nil
	}
	id := generated.ID(*value)
	return &id
}

func mapRelease(value *readmodelapplication.AcquisitionReleaseLink) *generated.AcquisitionReleaseLink {
	if value == nil {
		return nil
	}
	return &generated.AcquisitionReleaseLink{ReleaseId: generated.ID(value.ReleaseID), Title: mapDisplay(value.Title)}
}

// mapEpisodes keeps an absent list absent; the contract has no empty list.
func mapEpisodes(value []int) *generated.AcquisitionEpisodes {
	if len(value) == 0 {
		return nil
	}
	episodes := generated.AcquisitionEpisodes(append([]int(nil), value...))
	return &episodes
}

func mapTransfer(value *readmodelapplication.AcquisitionTransfer) *generated.AcquisitionTransfer {
	if value == nil {
		return nil
	}
	return &generated.AcquisitionTransfer{CompletedBytes: value.CompletedBytes, TotalBytes: value.TotalBytes,
		DownloadSpeed: value.DownloadSpeed, ObservedAt: value.ObservedAt}
}

// MapSummary maps one record list item.
func MapSummary(item readmodelapplication.AcquisitionRecordSummary) generated.AcquisitionRecordSummary {
	return generated.AcquisitionRecordSummary{
		AcquisitionId: generated.ID(item.AcquisitionID), EntryKind: generated.AcquisitionEntryKind(item.EntryKind),
		State: generated.AcquisitionRecordState(item.State), RecordDetail: generated.AcquisitionRecordSummaryRecordDetail(item.RecordDetail),
		AcceptedAt: item.AcceptedAt, EndedAt: item.TerminalAt, SourceTitle: mapDisplay(item.SourceTitle),
		Anime: mapAnime(item.Anime), Stage: mapStageName(item.Stage), StageState: mapStageState(item.StageState),
		ReasonCode: item.ReasonCode, Counts: mapCounts(item.Counts), OneShotId: mapID(item.OneShotID),
		Release: mapRelease(item.Release), Episodes: mapEpisodes(item.Episodes), Transfer: mapTransfer(item.Transfer),
	}
}

// MapDetail maps one record detail.
func MapDetail(item readmodelapplication.AcquisitionRecordDetail) generated.AcquisitionRecordDetail {
	result := generated.AcquisitionRecordDetail{
		AcquisitionId: generated.ID(item.AcquisitionID), EntryKind: generated.AcquisitionEntryKind(item.EntryKind),
		ExecutionBranch: generated.AcquisitionRecordDetailExecutionBranch(item.ExecutionBranch),
		State:           generated.AcquisitionRecordState(item.State), RecordDetail: generated.AcquisitionRecordDetailRecordDetail(item.RecordDetail),
		AcceptedAt: item.AcceptedAt, EndedAt: item.TerminalAt, CompactedAt: item.CompactedAt,
		AsOf: item.AsOf, SourceTitle: mapDisplay(item.SourceTitle), Anime: mapAnime(item.Anime),
		Stage: mapStageName(item.Stage), StageState: mapStageState(item.StageState), ReasonCode: item.ReasonCode, OneShotId: mapID(item.OneShotID),
		Stages: make([]generated.AcquisitionStage, len(item.Stages)), FilesAvailable: item.FilesAvailable,
		Version: item.Version, ImportRetryAvailable: item.ImportRetryAvailable,
		Release: mapRelease(item.Release), Episodes: mapEpisodes(item.Episodes), Transfer: mapTransfer(item.Transfer),
	}
	if item.CompactReason != nil {
		reason := generated.AcquisitionRecordDetailCompactReason(*item.CompactReason)
		result.CompactReason = &reason
	}
	if item.Outcome != nil {
		outcome := &generated.AcquisitionOutcome{
			Outcome:            generated.AcquisitionOutcomeOutcome(item.Outcome.Outcome),
			RequirementsStatus: generated.AcquisitionOutcomeRequirementsStatus(item.Outcome.RequirementsStatus),
			Counts:             mapCounts(item.Outcome.Counts), EffectsComplete: item.Outcome.EffectsComplete,
			HasConfirmedEffect: item.Outcome.HasConfirmed, HasUnknownEffect: item.Outcome.HasUnknown, SettledAt: item.Outcome.SettledAt,
		}
		if failure := item.Outcome.PrimaryFailure; failure != nil {
			outcome.PrimaryFailure = &generated.AcquisitionFailure{Stage: generated.AcquisitionStageName(failure.Stage), ReasonCode: failure.ReasonCode, FileRef: failure.FileRef}
		}
		result.Outcome = outcome
	}
	for i, stage := range item.Stages {
		result.Stages[i] = generated.AcquisitionStage{
			Stage: generated.AcquisitionStageName(stage.Stage), Availability: generated.AcquisitionStageAvailability(stage.Availability),
			State: mapStageState(stage.State), ReasonCode: stage.ReasonCode, AttemptsStarted: stage.AttemptsStarted,
			StartedAt: stage.StartedAt, UpdatedAt: stage.UpdatedAt, Done: stage.Done, Total: stage.Total,
		}
	}
	if req := item.Requirements; req != nil {
		result.Requirements = &generated.AcquisitionRequirements{Status: generated.AcquisitionRequirementsStatus(req.Status),
			FileCount: req.FileCount, RequiredCount: req.RequiredCount, ExcludedCount: req.ExcludedCount}
	}
	return result
}

// MapFile maps one record file.
func MapFile(item readmodelapplication.AcquisitionFile) generated.AcquisitionFile {
	result := generated.AcquisitionFile{
		Ordinal: item.Ordinal, FileRef: item.FileRef, RelativePath: mapDisplay(item.RelativePath),
		SizeBytes: item.SizeBytes, MediaKind: generated.AcquisitionFileMediaKind(item.MediaKind),
		Requirement: generated.AcquisitionFileRequirement(item.Requirement), SelectionReasonCode: item.SelectionReasonCode,
	}
	if item.PlacedPath != nil {
		placed := mapDisplay(*item.PlacedPath)
		result.PlacedPath = &placed
	}
	if item.CollisionOutcome != nil {
		outcome := generated.AcquisitionFileCollisionOutcome(*item.CollisionOutcome)
		result.CollisionOutcome = &outcome
	}
	if op := item.Operation; op != nil {
		result.Operation = &generated.AcquisitionFileOperation{State: generated.AcquisitionFileOperationState(op.State), Operation: op.Operation, ReasonCode: op.ReasonCode}
	}
	if d := item.Delivery; d != nil {
		delivery := &generated.AcquisitionFileDelivery{Outcome: generated.AcquisitionFileDeliveryOutcome(d.Outcome),
			EffectClass: generated.AcquisitionFileDeliveryEffectClass(d.EffectClass), SourceCleanup: generated.AcquisitionFileDeliverySourceCleanup(d.SourceCleanup),
			ReasonCode: d.ReasonCode}
		if d.CompletionMode != nil {
			mode := generated.AcquisitionFileDeliveryCompletionMode(*d.CompletionMode)
			delivery.CompletionMode = &mode
		}
		result.Delivery = delivery
	}
	if h := item.Handoff; h != nil {
		result.Handoff = &generated.AcquisitionFileHandoff{State: generated.AcquisitionFileHandoffState(h.State), ReasonCode: h.ReasonCode}
	}
	if r := item.PlacementFailure; r != nil {
		result.PlacementFailure = &generated.AcquisitionFilePlacementFailure{Attempts: r.Attempts, ReasonCode: r.ReasonCode}
	}
	if e := item.PlacementEnd; e != nil {
		end := &generated.AcquisitionFilePlacementEnd{State: generated.AcquisitionFilePlacementEndState(e.State)}
		if e.Cause != nil {
			cause := generated.AcquisitionFilePlacementEndCause(*e.Cause)
			end.Cause = &cause
		}
		end.Successor = mapAnime(e.Successor)
		result.PlacementEnd = end
	}
	return result
}
