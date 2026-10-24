package main

import (
	"context"
	"errors"
	"fmt"

	"bansoko/internal/filemaintenance"
	fileoperationapplication "bansoko/internal/fileoperation/application"
)

// placementSinkAdapter implements the fileoperation.PlacementSink port in
// the composition root: it delegates verified file-operation admission to the
// placement owner and schedules replacement evaluation after the owner commits.
// Coverage is resolved here from the Release media fact snapshot's frozen
// per-file EpisodeIdentity, so the placement owner never re-parses a title and
// never reads the candidate-facts projection as pool identity.
//
// Every failure returns a typed PlacementSinkError carrying a stable reason
// code. The caller owns the durable placement obligation and its bounded retry
// continuation; the sink never swallows a stage failure and never rolls back
// the completed file effect. Missing or unreadable frozen evidence is typed
// and retryable: it must never be recorded as an unknown file.
type placementSinkAdapter struct {
	evidence   filemaintenance.FileOperationEvidenceSource
	mediaFacts filemaintenance.MediaFileFactsReader
	placements *filemaintenance.PlacementService
	triggers   *filemaintenance.ReplacementTriggerService
}

func placementSinkError(code string, err error) error {
	return &fileoperationapplication.PlacementSinkError{Code: code, Err: err}
}

func (a *placementSinkAdapter) AdmitFromFileOperation(ctx context.Context, fileOperationID string) error {
	if a == nil || a.placements == nil || a.evidence == nil {
		return placementSinkError("placement_sink_unwired", errors.New("placement sink is not fully wired"))
	}
	evidence, found, err := a.evidence.ReadVerifiedEvidence(ctx, fileOperationID)
	if err != nil {
		return placementSinkError("placement_evidence_read_failed", err)
	}
	if !found || evidence == nil {
		// The obligation handoff only runs for completed file operations, so no
		// resolvable evidence row means the verified chain evidence is missing.
		// This is typed and retryable: never a silent nil, and never a
		// permanent unknown-file write.
		return placementSinkError("placement_evidence_missing", fmt.Errorf("verified evidence for file operation %s is missing", fileOperationID))
	}
	if evidence.FileKind == "subtitle" {
		// A subtitle carries no episode placement of its own: it becomes a
		// companion of its video's placement (B12), or waits for the video's
		// admission to register it.
		if err := a.placements.AdmitSidecarFromFileOperation(ctx, fileOperationID); err != nil {
			return placementSinkError("companion_registration_failed", err)
		}
		return nil
	}
	if evidence.FileKind != "" && evidence.FileKind != "video" {
		return nil
	}
	if evidence.UnknownFile {
		return a.admitUnknown(ctx, fileOperationID)
	}
	if a.mediaFacts == nil {
		return placementSinkError("placement_media_facts_unwired", errors.New("placement sink media facts reader is not wired"))
	}
	coverage, factsVersion, ok, coverageErr := filemaintenance.FrozenFileCoverage(ctx, a.mediaFacts, evidence.ReleaseID, evidence.FileRef)
	if coverageErr != nil {
		if errors.Is(coverageErr, filemaintenance.ErrPlacementEvidenceMissing) {
			return placementSinkError("placement_evidence_missing", coverageErr)
		}
		return placementSinkError("placement_evidence_read_failed", coverageErr)
	}
	if !ok {
		// The frozen identity is genuinely unresolved: the file is an unknown
		// file, released at admission. This never invents an episode.
		return a.admitUnknown(ctx, fileOperationID)
	}
	_, admitted, err := a.placements.AdmitFromFileOperationResult(ctx, fileOperationID, coverage, factsVersion)
	if err != nil {
		if errors.Is(err, filemaintenance.ErrCompanionRegistration) {
			return placementSinkError("companion_registration_failed", err)
		}
		return placementSinkError("placement_admission_failed", err)
	}
	if !admitted || a.triggers == nil {
		return nil
	}
	// The placement owner has already committed the verified owner truth. This
	// post-commit trigger only schedules replacement evaluation; it never owns
	// admission policy and never rolls back placement. A trigger failure is
	// returned so the caller records it as an independent visible outcome
	// instead of silently dropping the scheduled reevaluation.
	if err := a.triggers.TriggerPlacementCommitted(ctx, evidence.ManagedAnimeID, evidence.ReleaseID); err != nil {
		return placementSinkError("replacement_trigger_failed", err)
	}
	return nil
}

// admitUnknown records an unknown file (design §6a): its placement is written
// released in one transaction with its subtitles, so it never takes part in
// coverage, replacement or cleanup and schedules no replacement evaluation.
func (a *placementSinkAdapter) admitUnknown(ctx context.Context, fileOperationID string) error {
	if _, err := a.placements.AdmitUnknownFromFileOperation(ctx, fileOperationID); err != nil {
		if errors.Is(err, filemaintenance.ErrCompanionRegistration) {
			return placementSinkError("companion_registration_failed", err)
		}
		return placementSinkError("placement_unknown_admission_failed", err)
	}
	return nil
}
