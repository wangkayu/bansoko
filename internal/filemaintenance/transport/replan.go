package transport

import (
	filemaintenance "bansoko/internal/filemaintenance"
	planner "bansoko/internal/filemaintenance/planner"
	"bansoko/internal/transport/httpapi/generated"
)

func ReplanInput(value generated.FileMaintenanceReplanInput) filemaintenance.ReplanInput {
	return filemaintenance.ReplanInput{
		ManagedAnimeID: string(value.ManagedAnimeId),
		Generation:     int64(value.Generation),
	}
}

func ReplanResult(value planner.PlanResult) generated.FileMaintenanceReplanResult {
	return generated.FileMaintenanceReplanResult{
		AnimeId:          value.AnimeID,
		Generation:       int(value.Generation),
		PolicyVersion:    int(value.PolicyVersion),
		InputFingerprint: value.InputFingerprint,
		Candidates:       replanCandidates(value.Candidates),
	}
}

func replanCandidates(values []planner.PlanCandidate) []generated.FileMaintenancePlanCandidate {
	if values == nil {
		return nil
	}
	mapped := make([]generated.FileMaintenancePlanCandidate, len(values))
	for index, value := range values {
		mapped[index] = replanCandidate(value)
	}
	return mapped
}

func replanCandidate(value planner.PlanCandidate) generated.FileMaintenancePlanCandidate {
	mapped := generated.FileMaintenancePlanCandidate{
		Coverage:       cloneStrings(value.Coverage),
		Fingerprint:    value.Fingerprint,
		ObservationRef: value.ObservationRef,
		SizeBytes:      int(value.SizeBytes),
		SourcePath:     value.SourcePath,
		Excluded:       cloneBoolValue(value.Excluded),
	}
	if value.TargetPath != "" {
		targetPath := value.TargetPath
		mapped.TargetPath = &targetPath
	}
	if value.ExcludeReason != "" {
		reason := value.ExcludeReason
		mapped.ExcludeReason = &reason
	}
	if value.ExcludeReasonCode != "" {
		reasonCode := value.ExcludeReasonCode
		mapped.ExcludeReasonCode = &reasonCode
	}
	return mapped
}

func cloneBoolValue(value bool) *bool {
	cloned := value
	return &cloned
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}
