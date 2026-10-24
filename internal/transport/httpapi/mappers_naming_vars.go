package httpapi

import (
	"bansoko/internal/namingvars"
	"bansoko/internal/namingvars/editor"
	"bansoko/internal/transport/httpapi/generated"
)

func mapNamingVarsVersion(info namingvars.VersionInfo) generated.NamingVarsVersion {
	return generated.NamingVarsVersion{
		Revision: info.Revision, Digest: info.Digest, Runtime: generated.NamingVarsVersionRuntime(namingvars.ProfileBase),
		Origin: generated.NamingVarsVersionOrigin(info.Origin), Note: info.Note, SourceBytes: info.SourceBytes,
		CreatedAt: info.CreatedAt, ActivatedAt: info.ActivatedAt, Active: info.Active,
	}
}

func mapNamingVarsProgram(version namingvars.Version) generated.NamingVarsProgram {
	return generated.NamingVarsProgram{Version: mapNamingVarsVersion(version.VersionInfo), Source: version.Source}
}

func mapNamingVarsState(state editor.State) generated.NamingVarsState {
	out := generated.NamingVarsState{
		Active:     mapNamingVarsProgram(state.Active),
		Versions:   make([]generated.NamingVarsVersion, 0, len(state.Versions)),
		InputFacts: make([]generated.NamingVarsInputFact, 0, len(state.Facts)),
		Samples:    make([]generated.NamingVarsSample, 0, len(state.Samples)),
		Limits: generated.NamingVarsLimits{
			MaxSourceBytes: state.Limits.MaxSourceBytes, MaxOutputBytes: state.Limits.MaxOutputBytes, MaxCollection: state.Limits.MaxCollection,
			TimeoutMs: state.Limits.TimeoutMillis, MaxNoteLength: state.Limits.MaxNoteLength, MaxTargets: state.Limits.MaxTargets,
		},
	}
	for _, version := range state.Versions {
		out.Versions = append(out.Versions, mapNamingVarsVersion(version))
	}
	for _, fact := range state.Facts {
		out.InputFacts = append(out.InputFacts, generated.NamingVarsInputFact{
			Path: fact.Path, Type: generated.NamingVarsInputFactType(fact.Type), Nullable: fact.Nullable,
			SampleValue: fact.Sample, LabelKey: fact.LabelKey, DescriptionKey: fact.DescriptionKey,
		})
	}
	for _, sample := range state.Samples {
		out.Samples = append(out.Samples, generated.NamingVarsSample{Ref: sample.Ref, ReleaseTitle: sample.ReleaseTitle, FileCount: sample.FileCount})
	}
	return out
}

func mapNamingVarsDiagnostic(diagnostic namingvars.Diagnostic) generated.NamingVarsDiagnostic {
	out := generated.NamingVarsDiagnostic{Code: generated.NamingVarsDiagnosticCode(diagnostic.Code), Message: diagnostic.Message}
	if diagnostic.Start >= 0 && diagnostic.End >= diagnostic.Start {
		start, end := diagnostic.Start, diagnostic.End
		out.Start, out.End = &start, &end
	}
	return out
}

func mapNamingVarsDryRun(result editor.DryRunResult) generated.NamingVarsDryRunResult {
	out := generated.NamingVarsDryRunResult{
		Diagnostics: make([]generated.NamingVarsDiagnostic, 0, len(result.Diagnostics)),
		Targets:     make([]generated.NamingVarsDryRunTargetResult, 0, len(result.Targets)),
	}
	if result.Digest != "" {
		digest := result.Digest
		out.Digest = &digest
	}
	for _, diagnostic := range result.Diagnostics {
		out.Diagnostics = append(out.Diagnostics, mapNamingVarsDiagnostic(diagnostic))
	}
	for _, target := range result.Targets {
		item := generated.NamingVarsDryRunTargetResult{
			Kind: generated.NamingVarsDryRunTargetResultKind(target.Kind), Ref: target.Ref, Title: target.Title,
			FileCount: target.FileCount, Status: generated.NamingVarsDryRunTargetResultStatus(target.Status),
			Output: map[string]map[string]string{}, Diagnostics: []generated.NamingVarsDiagnostic{},
		}
		for group, values := range target.Output {
			item.Output[group] = map[string]string(values)
		}
		if target.Diagnostic != nil {
			item.Diagnostics = append(item.Diagnostics, mapNamingVarsDiagnostic(*target.Diagnostic))
		}
		if target.Reason != "" {
			reason := generated.NamingVarsDryRunTargetResultReason(target.Reason)
			item.Reason = &reason
		}
		out.Targets = append(out.Targets, item)
	}
	return out
}

func mapNamingVarsDocument(document editor.Document) generated.NamingVarsDocument {
	return generated.NamingVarsDocument{
		Format: generated.NamingVarsDocumentFormat(document.Format), FormatVersion: document.FormatVersion, Runtime: document.Runtime,
		Digest: document.Digest, Revision: document.Revision, Note: document.Note, ExportedAt: document.ExportedAt, Source: document.Source,
	}
}
