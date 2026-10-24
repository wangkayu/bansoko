package transport

import (
	"time"

	filemaintenance "bansoko/internal/filemaintenance"
	"bansoko/internal/parser"
	"bansoko/internal/parserpolicy"
	"bansoko/internal/transport/httpapi/generated"
)

func PathDiffView(value filemaintenance.PathDiffView) generated.ReconciliationPathDiffView {
	var expectedPath *string
	if value.ExpectedPath != nil {
		cloned := *value.ExpectedPath
		expectedPath = &cloned
	}
	mapped := generated.ReconciliationPathDiffView{
		Differs:      value.Differs,
		Disposition:  value.Disposition,
		ExpectedPath: expectedPath,
		FilePath:     value.FilePath,
	}
	if len(value.Companions) > 0 {
		companions := make([]generated.ReconciliationCompanionPathDiffView, len(value.Companions))
		for index, companion := range value.Companions {
			companions[index] = generated.ReconciliationCompanionPathDiffView{
				Differs:      companion.Differs,
				ExpectedPath: cloneOptionalString(companion.ExpectedPath),
				FilePath:     companion.FilePath,
				MediaKind:    generated.ReconciliationCompanionPathDiffViewMediaKind(companion.MediaKind),
				State:        generated.ReconciliationCompanionPathDiffViewState(companion.State),
			}
		}
		mapped.Companions = &companions
	}
	return mapped
}

// CompanionIssueViews maps the overview's companion issues.
func CompanionIssueViews(values []filemaintenance.CompanionIssueView) []generated.CompanionIssueView {
	mapped := make([]generated.CompanionIssueView, len(values))
	for index, value := range values {
		mapped[index] = generated.CompanionIssueView{
			ExpectedPath: cloneOptionalString(value.ExpectedPath),
			FilePath:     value.FilePath,
			MediaKind:    generated.CompanionIssueViewMediaKind(value.MediaKind),
			ReasonCode:   generated.CompanionIssueViewReasonCode(value.ReasonCode),
			VideoPath:    value.VideoPath,
		}
	}
	return mapped
}

// PlacementIssueViews projects the missing or mismatched placements.
func PlacementIssueViews(values []filemaintenance.PlacementIssueView) []generated.PlacementIssueView {
	mapped := make([]generated.PlacementIssueView, len(values))
	for index, value := range values {
		mapped[index] = generated.PlacementIssueView{
			FilePath:            value.FilePath,
			PlacementId:         generated.ID(value.PlacementID),
			ReasonCode:          generated.PlacementIssueViewReasonCode(value.ReasonCode),
			RedownloadRequested: value.RedownloadRequested,
		}
	}
	return mapped
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func PathDiffViews(values []filemaintenance.PathDiffView) []generated.ReconciliationPathDiffView {
	if values == nil {
		return nil
	}
	mapped := make([]generated.ReconciliationPathDiffView, len(values))
	for index, value := range values {
		mapped[index] = PathDiffView(value)
	}
	return mapped
}

func ParserPolicyInput(value generated.ParserPolicyInput) filemaintenance.ParserPolicyInput {
	return filemaintenance.ParserPolicyInput{
		ExpectedVersion: int64(value.ExpectedVersion),
		Policy: parserpolicy.Policy{
			Kind: parser.ParserKind(value.ParserKind),
		},
	}
}

func ParserPolicyView(value parserpolicy.Instance) generated.ParserPolicyView {
	return generated.ParserPolicyView{
		ManagedAnimeId:       generated.ID(value.ManagedAnimeID),
		ParserKind:           generated.ParserPolicyViewParserKind(value.Policy.Kind),
		SourceDefaultVersion: int(value.SourceDefaultVersion),
		Version:              value.Version,
	}
}

// ParserPolicyDefaultInput maps the wire save into the creation-template
// input. The parser kind stays a plain string: registration, not this mapper,
// decides which kinds exist.
func ParserPolicyDefaultInput(value generated.ParserPolicyDefaultInput) filemaintenance.ParserPolicyDefaultInput {
	return filemaintenance.ParserPolicyDefaultInput{
		ExpectedVersion: int64(value.ExpectedVersion),
		ParserKind:      value.ParserKind,
	}
}

func ParserPolicyDefaultView(value filemaintenance.ParserPolicyDefaultView) generated.ParserPolicyDefaultView {
	return generated.ParserPolicyDefaultView{
		ParserKind: value.ParserKind,
		Version:    int(value.Version),
		UpdatedAt:  cloneOptionalTime(value.UpdatedAt),
	}
}

func cloneOptionalTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
func FileEffectPolicyDefaultInput(value generated.FileEffectPolicyDefaultInput) filemaintenance.FileEffectPolicyDefaultInput {
	return filemaintenance.FileEffectPolicyDefaultInput{
		ExpectedVersion:   int64(value.ExpectedVersion),
		UnclassifiedRoute: filemaintenance.UnclassifiedRoutePolicyValue(value.UnclassifiedRoute),
	}
}

func FileEffectPolicyDefaultView(value filemaintenance.FileEffectPolicyDefaultView) generated.FileEffectPolicyDefaultView {
	return generated.FileEffectPolicyDefaultView{
		UnclassifiedRoute: generated.UnclassifiedRoutePolicyValue(value.UnclassifiedRoute),
		Version:           int(value.Version),
	}
}

func InstanceFileEffectPolicyInput(value generated.InstanceFileEffectPolicyInput) filemaintenance.InstanceFileEffectPolicyInput {
	return filemaintenance.InstanceFileEffectPolicyInput{
		ExpectedVersion:   int64(value.ExpectedVersion),
		UnclassifiedRoute: filemaintenance.UnclassifiedRoutePolicyValue(value.UnclassifiedRoute),
	}
}

func InstanceFileEffectPolicyView(value filemaintenance.InstanceFileEffectPolicyView) generated.InstanceFileEffectPolicyView {
	return generated.InstanceFileEffectPolicyView{
		ManagedAnimeId:       generated.ID(value.ManagedAnimeID),
		SourceDefaultVersion: int(value.SourceDefaultVersion),
		UnclassifiedRoute:    generated.UnclassifiedRoutePolicyValue(value.UnclassifiedRoute),
		UpdatedAt:            value.UpdatedAt,
		Version:              int(value.Version),
	}
}
