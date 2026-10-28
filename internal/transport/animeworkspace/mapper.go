package animeworkspace

import (
	"fmt"
	"time"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	animetransport "bansoko/internal/anime/transport"
	"bansoko/internal/transport/httpapi/generated"
)

// mapAnimeWorkspace projects the application-owned workspace into the generated
// HTTP contract. The projection is intentionally explicit: transport DTOs are
// not allowed to leak into the application read model, and nested pointers and
// slices must not alias application-owned values.
func MapAnimeWorkspace(value readmodelapplication.AnimeWorkspace) (generated.AnimeWorkspace, error) {
	rules, err := mapRulesOwner(value.Rules)
	if err != nil {
		return generated.AnimeWorkspace{}, err
	}
	return generated.AnimeWorkspace{
		AcquisitionTargets: mapWorkspaceTargets(value.AcquisitionTargets),
		Artifacts:          mapWorkspaceArtifacts(value.Artifacts),
		DownloadTasks:      mapWorkspaceDownloadTasks(value.DownloadTasks),
		EpisodeProgress:    mapWorkspaceEpisodeProgress(value.EpisodeProgress),
		FileOperations:     mapWorkspaceFileOperations(value.FileOperations),
		PlacementHandoffs:  mapWorkspacePlacementHandoffs(value.PlacementHandoffs),
		Logs:               mapWorkspaceLogs(value.Logs),
		ManagedAnime:       mapWorkspaceManagedAnime(value.ManagedAnime),
		Metadata:           mapWorkspaceMetadata(value.Metadata),
		MetadataHold:       mapWorkspaceMetadataHold(value.MetadataHold),
		Releases:           mapWorkspaceReleases(value.Releases),
		RssSourceRuntimes:  mapWorkspaceRuntimes(value.RSSSourceRuntimes),
		RssSources:         mapWorkspaceSources(value.RSSSources),
		Rules:              rules,
		Subgroups:          mapWorkspaceSubgroups(value.Subgroups),
		Subscription:       mapSubscription(value.Subscription),
		TargetObservations: mapWorkspaceObservations(value.TargetObservations),
	}, nil
}

func mapWorkspaceManagedAnime(value animeapplication.ManagedAnime) generated.ManagedAnime {
	return generated.ManagedAnime{
		Id:            generated.ID(value.ID),
		Title:         value.Title,
		TitleOverride: cloneWorkspaceString(value.TitleOverride),
		Version:       generated.Version(value.Version),
	}
}

func mapWorkspaceMetadata(value animeapplication.AnimeMetadata) generated.AnimeMetadata {
	result := generated.AnimeMetadata{
		ManagedAnimeId:    generated.ID(value.ManagedAnimeID),
		TitleOverride:     cloneWorkspaceString(value.TitleOverride),
		TotalEpisodes:     cloneWorkspaceInt(value.TotalEpisodes),
		MediaKindOverride: (*generated.AnimeMetadataMediaKindOverride)(cloneWorkspaceString(value.MediaKindOverride)),
		Year:              cloneWorkspaceInt(value.Year),
		Season:            cloneWorkspaceInt(value.Season),
		Version:           generated.Version(value.Version),
	}
	if value.BGM != nil {
		result.Bgm = &generated.NullableBGMBinding{WorkId: value.BGM.WorkID}
	}
	if value.TMDB != nil {
		result.Tmdb = &generated.NullableTMDBBinding{
			WorkId:    value.TMDB.WorkID,
			MediaKind: generated.TMDBMediaKind(value.TMDB.MediaKind),
			Season:    value.TMDB.Season,
		}
	}
	result.TitleSuggestions = MapTitleSuggestions(value.TitleSuggestions)
	return result
}

// MapTitleSuggestions maps title candidates; nil when there are none, so the
// field is omitted rather than sent as an empty list.
func MapTitleSuggestions(value []animeapplication.TitleSuggestion) *[]generated.TitleSuggestion {
	if len(value) == 0 {
		return nil
	}
	result := make([]generated.TitleSuggestion, 0, len(value))
	for _, item := range value {
		result = append(result, generated.TitleSuggestion{Source: generated.TitleSuggestionSource(item.Source), Text: item.Text})
	}
	return &result
}

func mapWorkspaceMetadataHold(value animeapplication.MetadataHold) generated.MetadataHold {
	return generated.MetadataHold{
		Held:    value.Held,
		Missing: mapWorkspaceMetadataMissing(value.Missing),
	}
}

func mapWorkspaceMetadataMissing(value []string) []generated.MetadataHoldMissing {
	// missing is a required, non-nullable array in the schema: an empty hold
	// must serialize as [] rather than null.
	result := make([]generated.MetadataHoldMissing, 0, len(value))
	for _, item := range value {
		result = append(result, generated.MetadataHoldMissing(item))
	}
	return result
}

func mapWorkspaceEpisodeProgress(value *readmodelapplication.EpisodeProgress) *struct {
	Done     bool  `json:"done"`
	Episodes []int `json:"episodes"`
	Total    *int  `json:"total,omitempty"`
} {
	if value == nil {
		return nil
	}
	return &struct {
		Done     bool  `json:"done"`
		Episodes []int `json:"episodes"`
		Total    *int  `json:"total,omitempty"`
	}{
		Done:     value.Done,
		Episodes: cloneWorkspaceInts(value.Episodes),
		Total:    cloneWorkspaceInt(value.Total),
	}
}

func mapWorkspaceSources(value []animeapplication.RSSSource) []generated.RSSSource {
	if value == nil {
		return nil
	}
	result := make([]generated.RSSSource, len(value))
	for i, item := range value {
		result[i] = mapRSSSource(item)
	}
	return result
}

func mapWorkspaceRuntimes(value []animeapplication.RSSSourceRuntime) []generated.RSSSourceRuntime {
	if value == nil {
		return nil
	}
	result := make([]generated.RSSSourceRuntime, len(value))
	for i, item := range value {
		result[i] = generated.RSSSourceRuntime{
			Cursor:           mapWorkspaceCursor(item.Cursor),
			FailedWorkItemId: cloneWorkspaceID(item.FailedWorkItemID),
			Health:           generated.RSSSourceRuntimeHealth(item.Health),
			LastGoodFacts:    mapWorkspaceLastGoodFacts(item.LastGoodFacts),
			LastRefreshAt:    cloneWorkspaceTime(item.LastRefreshAt),
			LastSuccessAt:    cloneWorkspaceTime(item.LastSuccessAt),
			ReasonCode:       cloneWorkspaceString(item.ReasonCode),
			ReasonDetail:     cloneWorkspaceString(item.ReasonDetail),
			RssSourceId:      generated.ID(item.RSSSourceID),
			Version:          generated.Version(item.Version),
		}
	}
	return result
}

func mapWorkspaceCursor(value *animeapplication.RSSCursor) *generated.RSSCursor {
	if value == nil {
		return nil
	}
	return &generated.RSSCursor{
		Strategy: generated.RSSCursorStrategy(value.Strategy),
		Value:    value.Value,
	}
}

func mapWorkspaceLastGoodFacts(value *animeapplication.RSSLastGoodFacts) *generated.RSSLastGoodFacts {
	if value == nil {
		return nil
	}
	return &generated.RSSLastGoodFacts{
		FeedTitle:           cloneWorkspaceString(value.FeedTitle),
		ItemCount:           value.ItemCount,
		NewestStableItemKey: value.NewestStableItemKey,
	}
}

func mapWorkspaceReleases(value []readmodelapplication.Release) []generated.Release {
	if value == nil {
		return nil
	}
	result := make([]generated.Release, len(value))
	for i, item := range value {
		result[i] = generated.Release{
			AcquisitionId:      cloneWorkspaceID(item.AcquisitionID),
			AcquisitionTargets: mapWorkspaceTargets(item.AcquisitionTargets),
			DownloadTaskId:     cloneWorkspaceID(item.DownloadTaskID),
			EvaluationVersion:  cloneWorkspaceString(item.EvaluationVersion),
			Id:                 generated.ID(item.ID),
			ParsedFacts:        mapWorkspaceParsedFacts(item.ParsedFacts),
			RawFacts:           mapWorkspaceRawFacts(item.RawFacts),
			ReasonCode:         item.ReasonCode,
			ReasonDetail:       cloneWorkspaceString(item.ReasonDetail),
			ReleaseFiles:       mapWorkspaceReleaseFiles(item.ReleaseFiles),
			RssSourceId:        generated.ID(item.RSSSourceID),
			SourceFacts:        mapWorkspaceSourceFacts(item.SourceFacts),
			StableItemKey:      item.StableItemKey,
			TargetObservations: mapWorkspaceObservations(item.TargetObservations),
			Verdict:            generated.ReleaseVerdict(item.Verdict),
			Version:            generated.Version(item.Version),
		}
	}
	return result
}

func mapWorkspaceRawFacts(value readmodelapplication.RawReleaseFacts) generated.RawReleaseFacts {
	return generated.RawReleaseFacts{
		EnclosureLength: cloneWorkspaceInt64(value.EnclosureLength),
		EnclosureType:   cloneWorkspaceString(value.EnclosureType),
		PublishedAt:     cloneWorkspaceTime(value.PublishedAt),
		Title:           value.Title,
	}
}

func mapWorkspaceParsedFacts(value *readmodelapplication.ReleaseParsedFacts) *generated.ParsedReleaseFacts {
	if value == nil {
		return nil
	}
	mapped := &generated.ParsedReleaseFacts{
		Episode:   cloneWorkspaceFloat32(value.Episode),
		GroupName: cloneWorkspaceString(value.GroupName),
		Languages: cloneWorkspaceStrings(value.Languages),
		Quality:   cloneWorkspaceString(value.Quality),
		Special:   cloneWorkspaceBool(value.Special),
	}
	if value.Coverage != nil {
		mapped.Coverage = &generated.ReleaseCoverage{Episodes: append([]int(nil), value.Coverage.Episodes...)}
	}
	return mapped
}

func mapWorkspaceSourceFacts(value readmodelapplication.SourceFacts) generated.SourceFacts {
	result := generated.SourceFacts{
		GroupName:     cloneWorkspaceString(value.GroupName),
		IdentityKind:  generated.SourceFactsIdentityKind(value.IdentityKind),
		IdentityValue: value.IdentityValue,
	}
	if value.AniBT != nil {
		facts := generated.AniBTTagFacts{Tags: append([]string{}, value.AniBT.Tags...)}
		if value.AniBT.Schema != "" {
			schema := value.AniBT.Schema
			facts.Schema = &schema
		}
		if value.AniBT.Diagnostic != "" {
			diagnostic := value.AniBT.Diagnostic
			facts.Diagnostic = &diagnostic
		}
		result.Anibt = &facts
	}
	if value.TitleTags != nil {
		facts := generated.TitleTagFacts{Schema: value.TitleTags.Schema, Implementation: value.TitleTags.Implementation, Tags: append([]string{}, value.TitleTags.Tags...)}
		if value.TitleTags.ConfigDigest != "" {
			digest := value.TitleTags.ConfigDigest
			facts.ConfigDigest = &digest
		}
		if value.TitleTags.Diagnostic != "" {
			diagnostic := value.TitleTags.Diagnostic
			facts.Diagnostic = &diagnostic
		}
		result.TitleTags = &facts
	}
	return result
}

func mapWorkspaceReleaseFiles(value *[]readmodelapplication.ReleaseFile) *[]generated.ReleaseFile {
	if value == nil {
		return nil
	}
	result := make([]generated.ReleaseFile, len(*value))
	for i, item := range *value {
		result[i] = generated.ReleaseFile{
			Episode:          cloneWorkspaceString(item.Episode),
			Filename:         item.Filename,
			Language:         cloneWorkspaceString(item.Language),
			MediaKind:        generated.ReleaseFileMediaKind(item.MediaKind),
			Path:             item.Path,
			Resolution:       cloneWorkspaceString(item.Resolution),
			Revision:         cloneWorkspaceString(item.Revision),
			SizeBytes:        cloneWorkspaceInt64(item.SizeBytes),
			TargetPath:       cloneWorkspaceString(item.TargetPath),
			TargetDiagnostic: mapWorkspaceTargetDiagnostic(item.TargetDiagnostic),
			OutcomeReason:    cloneWorkspaceString(item.OutcomeReason),
		}
		if item.Outcome != nil {
			outcome := generated.ReleaseFileOutcome(*item.Outcome)
			result[i].Outcome = &outcome
		}
	}
	return &result
}

func mapWorkspaceTargets(value *[]readmodelapplication.AcquisitionTarget) *[]generated.AcquisitionTarget {
	if value == nil {
		return nil
	}
	result := make([]generated.AcquisitionTarget, len(*value))
	for i, item := range *value {
		result[i] = generated.AcquisitionTarget{
			CollisionPolicy:   generated.AcquisitionTargetCollisionPolicy(item.CollisionPolicy),
			ExpectedSizeBytes: item.ExpectedSizeBytes,
			Id:                generated.ID(item.ID),
			Intent:            generated.AcquisitionTargetIntent(item.Intent),
			MediaKind:         generated.AcquisitionTargetMediaKind(item.MediaKind),
			ReleaseId:         generated.ID(item.ReleaseID),
			SourcePath:        item.SourcePath,
			SpecDigest:        item.SpecDigest,
			TargetPath:        item.TargetPath,
			TargetRef:         item.TargetRef,
			Version:           generated.Version(item.Version),
		}
	}
	return &result
}

func mapWorkspaceObservations(value *[]readmodelapplication.TargetObservation) *[]generated.TargetObservation {
	if value == nil {
		return nil
	}
	result := make([]generated.TargetObservation, len(*value))
	for i, item := range *value {
		result[i] = generated.TargetObservation{
			ObservedAt:        item.ObservedAt,
			ObservedSizeBytes: cloneWorkspaceInt64(item.ObservedSizeBytes),
			ReasonCode:        cloneWorkspaceString(item.ReasonCode),
			State:             generated.TargetObservationState(item.State),
			TargetId:          generated.ID(item.TargetID),
			Version:           generated.Version(item.Version),
		}
	}
	return &result
}

func mapWorkspaceDownloadTasks(value []readmodelapplication.WorkspaceDownloadTask) []generated.DownloadTask {
	if value == nil {
		return nil
	}
	result := make([]generated.DownloadTask, len(value))
	for i, item := range value {
		result[i] = generated.DownloadTask{
			FailedWorkItemId:    cloneWorkspaceID(item.FailedWorkItemID),
			FailureCode:         cloneWorkspaceString(item.FailureCode),
			FailureReason:       cloneWorkspaceString(item.FailureReason),
			FrozenInput:         mapWorkspaceFrozenInput(item.FrozenInput),
			FrozenPolicy:        mapWorkspaceFrozenPolicy(item.FrozenPolicy),
			Id:                  generated.ID(item.ID),
			IdempotencyKey:      item.IdempotencyKey,
			ProviderCorrelation: mapWorkspaceProviderCorrelation(item.ProviderCorrelation),
			ReleaseId:           generated.ID(item.ReleaseID),
			State:               generated.DownloadTaskState(item.State),
			Version:             generated.Version(item.Version),
		}
	}
	return result
}

func mapWorkspaceFrozenInput(value animeapplication.FrozenDownloadInput) generated.FrozenDownloadInput {
	return generated.FrozenDownloadInput{
		ReleaseStableItemKey: value.ReleaseStableItemKey,
		SelectedUrlDigest:    cloneWorkspaceString(value.SelectedURLDigest),
		SourceLocatorDisplay: value.SourceLocatorDisplay,
	}
}

func mapWorkspaceFrozenPolicy(value animeapplication.FrozenPolicySnapshot) generated.FrozenPolicySnapshot {
	return generated.FrozenPolicySnapshot{
		EvaluatorVersion: cloneWorkspaceString(value.EvaluatorVersion),
		MetadataVersion:  generated.Version(value.MetadataVersion),
		RulesVersion:     generated.Version(value.RulesVersion),
	}
}

func mapWorkspaceProviderCorrelation(value *readmodelapplication.WorkspaceProviderCorrelation) *generated.ProviderCorrelation {
	if value == nil {
		return nil
	}
	return &generated.ProviderCorrelation{
		ExternalId: cloneWorkspaceString(value.ExternalID),
		InfoHash:   cloneWorkspaceString(value.InfoHash),
		StableTag:  cloneWorkspaceString(value.StableTag),
	}
}

func mapWorkspaceArtifacts(value []readmodelapplication.Artifact) []generated.Artifact {
	if value == nil {
		return nil
	}
	result := make([]generated.Artifact, len(value))
	for i, item := range value {
		result[i] = generated.Artifact{
			DownloadTaskId:   generated.ID(item.DownloadTaskID),
			FailedWorkItemId: cloneWorkspaceID(item.FailedWorkItemID),
			FailureCode:      cloneWorkspaceString(item.FailureCode),
			FailureReason:    cloneWorkspaceString(item.FailureReason),
			Files:            mapWorkspaceArtifactFiles(item.Files),
			Fingerprints:     mapWorkspaceFingerprints(item.Fingerprints),
			Id:               generated.ID(item.ID),
			State:            generated.ArtifactState(item.State),
			Version:          generated.Version(item.Version),
		}
	}
	return result
}

func mapWorkspaceArtifactFiles(value []readmodelapplication.ArtifactFile) []generated.ArtifactFile {
	if value == nil {
		return nil
	}
	result := make([]generated.ArtifactFile, len(value))
	for i, item := range value {
		result[i] = generated.ArtifactFile{
			Fingerprint:     mapWorkspaceFingerprint(item.Fingerprint),
			MediaKind:       generated.ArtifactFileMediaKind(item.MediaKind),
			Path:            item.Path,
			Ref:             item.Ref,
			SubtitleBinding: mapWorkspaceSubtitleBinding(item.SubtitleBinding),
		}
	}
	return result
}

func mapWorkspaceSubtitleBinding(value *readmodelapplication.ArtifactFileSubtitleBinding) *generated.ArtifactFileSubtitleBinding {
	if value == nil {
		return nil
	}
	return &generated.ArtifactFileSubtitleBinding{
		BoundVideoFileRef: cloneWorkspaceString(value.BoundVideoFileRef),
		Confidence:        cloneWorkspaceFloat32(value.Confidence),
		Diagnostics:       mapWorkspaceDiagnostics(value.Diagnostics),
		Method:            cloneWorkspaceString(value.Method),
		State:             generated.ArtifactFileSubtitleBindingState(value.State),
	}
}

func mapWorkspaceDiagnostics(value []readmodelapplication.NamingDiagnostic) []generated.NamingDiagnostic {
	if value == nil {
		return nil
	}
	result := make([]generated.NamingDiagnostic, len(value))
	for i, item := range value {
		result[i] = generated.NamingDiagnostic{
			Code:    item.Code,
			Message: item.Message,
			Path:    cloneWorkspaceString(item.Path),
		}
	}
	return result
}

func mapWorkspaceFingerprints(value []readmodelapplication.Fingerprint) []generated.Fingerprint {
	if value == nil {
		return nil
	}
	result := make([]generated.Fingerprint, len(value))
	for i, item := range value {
		result[i] = mapWorkspaceFingerprint(item)
	}
	return result
}

func mapWorkspaceFingerprint(value readmodelapplication.Fingerprint) generated.Fingerprint {
	return generated.Fingerprint{
		Algorithm: generated.FingerprintAlgorithm(value.Algorithm),
		SizeBytes: value.SizeBytes,
		Value:     value.Value,
	}
}

func mapWorkspaceFileOperations(value []readmodelapplication.FileOperation) []generated.FileOperation {
	if value == nil {
		return nil
	}
	result := make([]generated.FileOperation, len(value))
	for i, item := range value {
		result[i] = generated.FileOperation{
			ArtifactFileRef:         item.ArtifactFileRef,
			ArtifactId:              generated.ID(item.ArtifactID),
			CollisionPolicy:         generated.CollisionPolicy(item.CollisionPolicy),
			ExpectedSizeBytes:       cloneWorkspaceInt64(item.ExpectedSizeBytes),
			FailedWorkItemId:        cloneWorkspaceID(item.FailedWorkItemID),
			FailureCode:             cloneWorkspaceString(item.FailureCode),
			FailureReason:           cloneWorkspaceString(item.FailureReason),
			FrozenConfigSnapshot:    mapWorkspaceFrozenFileConfig(item.FrozenConfigSnapshot),
			FrozenRenderSnapshot:    mapWorkspaceFrozenRender(item.FrozenRenderSnapshot),
			Id:                      generated.ID(item.ID),
			IdempotencyKey:          item.IdempotencyKey,
			Intent:                  mapWorkspaceIntent(item.Intent),
			ObservedAt:              cloneWorkspaceTime(item.ObservedAt),
			ObservedTargetSizeBytes: cloneWorkspaceInt64(item.ObservedTargetSizeBytes),
			Operation:               generated.FileOperationOperation(item.Operation),
			SourceFingerprint:       mapWorkspaceFingerprint(item.SourceFingerprint),
			SourcePath:              item.SourcePath,
			State:                   generated.FileOperationState(item.State),
			TargetFingerprint:       mapWorkspaceFingerprintPointer(item.TargetFingerprint),
			TargetPath:              item.TargetPath,
			Version:                 generated.Version(item.Version),
		}
	}
	return result
}

// mapWorkspacePlacementHandoffs projects the durable placement handoff stage
// facts. Only the public stage state, the stable reason, the attempt budget and
// the live retry due time cross the boundary; no work item identity does.
func mapWorkspacePlacementHandoffs(value []readmodelapplication.PlacementHandoff) []generated.PlacementHandoffView {
	if value == nil {
		return nil
	}
	result := make([]generated.PlacementHandoffView, len(value))
	for i, item := range value {
		result[i] = generated.PlacementHandoffView{
			FileOperationId: generated.ID(item.FileOperationID),
			State:           generated.PlacementHandoffViewState(item.State),
			ReasonCode:      cloneWorkspaceString(item.ReasonCode),
			Attempts:        int(item.Attempts),
			AttemptBudget:   int(item.AttemptBudget),
			NextRetryAt:     cloneWorkspaceTime(item.NextRetryAt),
			UpdatedAt:       item.UpdatedAt,
		}
	}
	return result
}
func mapWorkspaceFrozenFileConfig(value readmodelapplication.FrozenFileConfigSnapshot) generated.FrozenFileConfigSnapshot {
	return generated.FrozenFileConfigSnapshot{
		RulesVersion:  generated.Version(value.RulesVersion),
		SafetyVersion: value.SafetyVersion,
	}
}
func mapWorkspaceFrozenRender(value readmodelapplication.FrozenRenderSnapshot) generated.FrozenRenderSnapshot {
	return generated.FrozenRenderSnapshot{
		Directory:       value.Directory,
		Filename:        value.Filename,
		RendererVersion: value.RendererVersion,
	}
}

func mapWorkspaceIntent(value *string) *generated.FileOperationIntent {
	if value == nil {
		return nil
	}
	result := generated.FileOperationIntent(*value)
	return &result
}

func mapWorkspaceFingerprintPointer(value *readmodelapplication.Fingerprint) *generated.Fingerprint {
	if value == nil {
		return nil
	}
	result := mapWorkspaceFingerprint(*value)
	return &result
}

func mapWorkspaceLogs(value []readmodelapplication.SubscriptionLogEntry) []generated.SubscriptionLogEntry {
	if value == nil {
		return nil
	}
	result := make([]generated.SubscriptionLogEntry, len(value))
	for i, item := range value {
		result[i] = generated.SubscriptionLogEntry{
			CausationId:    cloneWorkspaceString(item.CausationID),
			CausationType:  cloneWorkspaceString(item.CausationType),
			CorrelationId:  cloneWorkspaceString(item.CorrelationID),
			DedupeKey:      item.DedupeKey,
			Details:        mapWorkspaceLogDetails(item.Details),
			GroupRef:       cloneWorkspaceString(item.GroupRef),
			Id:             generated.ID(item.ID),
			Kind:           generated.SubscriptionLogKind(item.Kind),
			ManagedAnimeId: generated.ID(item.ManagedAnimeID),
			OccurredAt:     item.OccurredAt,
			Outcome:        generated.SubscriptionLogOutcome(item.Outcome),
			ReasonCode:     cloneWorkspaceString(item.ReasonCode),
			ReleaseId:      cloneWorkspaceID(item.ReleaseID),
			RetentionUntil: item.RetentionUntil,
			RssSourceId:    cloneWorkspaceID(item.RSSSourceID),
			SubjectId:      cloneWorkspaceString(item.SubjectID),
			SubjectType:    cloneWorkspaceString(item.SubjectType),
			SubjectVersion: cloneWorkspaceInt64(item.SubjectVersion),
			SubscriptionId: generated.ID(item.SubscriptionID),
			AnimeTitle:     item.AnimeTitle,
			Source:         generated.SubscriptionLogSource(item.Source),
			MessageCode:    generated.SubscriptionLogMessageCode(item.MessageCode),
			MessageParams: generated.SubscriptionLogMessageParams{
				Count:     cloneWorkspaceInt64(item.MessageParams.Count),
				FileName:  cloneWorkspaceString(item.MessageParams.FileName),
				GroupName: cloneWorkspaceString(item.MessageParams.GroupName),
				Name:      cloneWorkspaceString(item.MessageParams.Name),
			},
		}
	}
	return result
}

func mapWorkspaceLogDetails(value *readmodelapplication.SubscriptionLogDetails) *generated.SubscriptionLogDetails {
	if value == nil {
		return nil
	}
	return &generated.SubscriptionLogDetails{
		ErrorCount:   cloneWorkspaceInt(value.ErrorCount),
		ItemCount:    cloneWorkspaceInt(value.ItemCount),
		ReasonCode:   cloneWorkspaceString(value.ReasonCode),
		ReasonDetail: cloneWorkspaceString(value.ReasonDetail),
	}
}

func mapWorkspaceSubgroups(value ownershipapplication.SubgroupsOwnerResult) generated.SubgroupsOwnerResult {
	result := generated.SubgroupsOwnerResult{
		EffectiveSelection: mapWorkspaceSelections(value.EffectiveSelection),
		ManagedAnimeId:     generated.ID(value.ManagedAnimeID),
		RssSources:         mapWorkspaceSources(value.RSSSources),
		Subgroups:          mapWorkspaceSubgroupSection(value.Subgroups),
		Version:            generated.Version(value.Version),
	}
	return result
}

func mapWorkspaceSelections(value []ownershipapplication.ProviderGroupSelection) []generated.ProviderGroupSelection {
	if value == nil {
		return nil
	}
	result := make([]generated.ProviderGroupSelection, len(value))
	for i, item := range value {
		result[i] = generated.ProviderGroupSelection{
			DisplayName:       item.DisplayName,
			EffectiveScore:    item.EffectiveScore,
			EffectiveSelected: item.EffectiveSelected,
			GroupRef:          item.GroupRef,
			Id:                generated.ID(item.ID),
			ManualSelected:    cloneWorkspaceBool(item.ManualSelected),
			Provider:          generated.ProviderKind(item.Provider),
			ReasonCode:        cloneWorkspaceString(item.ReasonCode),
			ScoreBreakdown:    ownershipapplication.SubgroupScoreBreakdownToGenerated(item.ScoreBreakdown),
			SelectionMode:     generated.SelectionMode(item.SelectionMode),
			SubscriptionId:    generated.ID(item.SubscriptionID),
			Version:           generated.Version(item.Version),
		}
	}
	return result
}
func mapWorkspaceSubgroupSection(value ownershipapplication.SubgroupsSection) generated.SubgroupsSection {
	result := generated.SubgroupsSection{
		Entries: make([]generated.SubgroupScoreEntry, 0, len(value.Entries)),
		MaxRss:  value.MaxRss,
	}
	if value.Entries == nil {
		result.Entries = nil
	}
	for _, item := range value.Entries {
		result.Entries = append(result.Entries, generated.SubgroupScoreEntry{
			DisplayName:       item.DisplayName,
			EffectiveSelected: item.EffectiveSelected,
			GroupRef:          item.GroupRef,
			ManualSelected:    cloneWorkspaceBool(item.ManualSelected),
			Provider:          generated.ProviderKind(item.Provider),
			Score:             item.Score,
			SelectionMode:     generated.SelectionMode(item.SelectionMode),
		})
	}
	if value.NameKeywords != nil {
		result.NameKeywords = &generated.ProviderNameKeywordRules{
			Anibt: mapWorkspaceNameKeywordRules(value.NameKeywords.Anibt),
			Mikan: mapWorkspaceNameKeywordRules(value.NameKeywords.Mikan),
		}
	}
	if value.ProviderDefaults != nil {
		result.ProviderDefaults = &generated.ProviderGroupDefaults{
			Anibt: mapWorkspaceProviderDefault(value.ProviderDefaults.Anibt),
			Mikan: mapWorkspaceProviderDefault(value.ProviderDefaults.Mikan),
		}
	}
	return result
}
func mapWorkspaceProviderDefault(value *ownershipapplication.ProviderGroupDefault) *generated.ProviderGroupDefault {
	if value == nil {
		return nil
	}
	return &generated.ProviderGroupDefault{
		Groups:       cloneWorkspaceStringsSlice(value.Groups),
		Mode:         generated.ProviderGroupDefaultMode(value.Mode),
		Score:        value.Score,
		PinnedScores: ownershipapplication.PinnedGroupScoresToGenerated(value.PinnedScores),
	}
}

func mapWorkspaceNameKeywordRules(value *[]ownershipapplication.NameKeywordRule) *[]generated.NameKeywordRule {
	if value == nil {
		return nil
	}
	result := make([]generated.NameKeywordRule, 0, len(*value))
	for _, rule := range *value {
		result = append(result, generated.NameKeywordRule{Keyword: rule.Keyword, Score: rule.Score})
	}
	return &result
}

func mapRulesOwner(value ownershipapplication.RulesOwner) (generated.AnimeRules, error) {
	if value.ManagedAnimeID == "" {
		return generated.AnimeRules{}, fmt.Errorf("rules result has empty managed anime id")
	}
	sections, err := animetransport.RulesSectionsToHTTP(value.Sections)
	if err != nil {
		return generated.AnimeRules{}, fmt.Errorf("map rules sections: %w", err)
	}
	effective, err := animetransport.EffectiveRules(value.Effective)
	if err != nil {
		return generated.AnimeRules{}, fmt.Errorf("map effective rules: %w", err)
	}
	return generated.AnimeRules{
		ManagedAnimeId:      generated.ID(value.ManagedAnimeID),
		Sections:            sections,
		Effective:           effective,
		EffectiveTagFilters: animetransport.TagFilterRulesToHTTP(value.EffectiveTagFilters),
		HasAnibtSource:      value.HasAniBTSource,
		Version:             generated.Version(value.Version),
	}, nil
}

func mapRSSSource(value animeapplication.RSSSource) generated.RSSSource {
	result := generated.RSSSource{
		AnibtAuxMode:          mapRSSSourceAniBTMode(value.AnibtAuxMode),
		GroupName:             value.GroupName,
		HttpFeedConnectionRef: cloneWorkspaceString(value.HttpFeedConnectionRef),
		Id:                    generated.ID(value.ID),
		LocatorDisplay:        value.LocatorDisplay,
		NetworkRouteRef:       cloneWorkspaceString(value.NetworkRouteRef),
		EpisodeOffset:         value.EpisodeOffset,
		ProviderProvenance:    mapProviderProvenance(value.ProviderProvenance),
		SourceKind:            generated.SourceKind(value.SourceKind),
		SubscriptionId:        generated.ID(value.SubscriptionID),
		Title:                 value.Title,
		Version:               generated.Version(value.Version),
	}
	return result
}

func mapRSSSourceAniBTMode(value *animedomain.RSSSourceDraftAnibtAuxMode) *generated.RSSSourceAnibtAuxMode {
	if value == nil {
		return nil
	}
	mapped := generated.RSSSourceAnibtAuxMode(*value)
	return &mapped
}

func mapProviderProvenance(value *animedomain.ProviderProvenance) *generated.ProviderProvenance {
	if value == nil {
		return nil
	}
	return &generated.ProviderProvenance{
		DiscoverySnapshotVersion: value.DiscoverySnapshotVersion,
		Provider:                 generated.ProviderKind(value.Provider),
		ProviderEntryRef:         value.ProviderEntryRef,
		ProviderGroupRef:         value.ProviderGroupRef,
	}
}

func mapSubscription(value animeapplication.Subscription) generated.Subscription {
	result := generated.Subscription{
		Id:              generated.ID(value.ID),
		ManagedAnimeId:  generated.ID(value.ManagedAnimeID),
		Origin:          generated.SubscriptionOrigin(value.Origin),
		State:           generated.SubscriptionState(value.State),
		Version:         generated.Version(value.Version),
		LifecycleState:  generated.SubscriptionLifecycleState(value.LifecycleState),
		LifecycleEpoch:  value.LifecycleEpoch,
		RetiredAt:       cloneWorkspaceTime(value.RetiredAt),
		NewReleasesOnly: value.NewReleasesOnly,
		StartBaselineAt: cloneWorkspaceTime(value.StartBaselineAt),
	}
	result.OriginEntryRef = cloneWorkspaceString(value.OriginEntryRef)
	result.InstanceLabel = cloneWorkspaceString(value.InstanceLabel)
	return result
}
func mapWorkspaceTargetDiagnostic(value *readmodelapplication.TargetDiagnostic) *generated.TargetDiagnostic {
	if value == nil {
		return nil
	}
	return &generated.TargetDiagnostic{
		Code:       value.Code,
		Message:    value.Message,
		SourcePath: cloneWorkspaceString(value.SourcePath),
	}
}

func cloneWorkspaceID(value *string) *generated.ID {
	if value == nil {
		return nil
	}
	result := generated.ID(*value)
	return &result
}

func cloneWorkspaceString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneWorkspaceInt(value *int) *int {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneWorkspaceInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneWorkspaceFloat32(value *float32) *float32 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneWorkspaceBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneWorkspaceTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneWorkspaceInts(value []int) []int {
	if value == nil {
		return nil
	}
	result := make([]int, len(value))
	copy(result, value)
	return result
}

func cloneWorkspaceStrings(value *[]string) *[]string {
	if value == nil {
		return nil
	}
	result := cloneWorkspaceStringsSlice(*value)
	return &result
}

func cloneWorkspaceStringsSlice(value []string) []string {
	if value == nil {
		return nil
	}
	result := make([]string, len(value))
	copy(result, value)
	return result
}
