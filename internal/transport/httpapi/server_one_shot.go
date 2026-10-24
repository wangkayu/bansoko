package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	animedomain "bansoko/internal/anime/domain"
	oneshotapplication "bansoko/internal/anime/oneshot/application"
	"bansoko/internal/apperrors"
	"bansoko/internal/transport/httpapi/generated"
)

// Finite one-shot seed acquisition transport (operationIds acquireSeedOnce /
// getOneShotAcquisition). Every handler delegates to the same typed finite
// acquisition service the MCP tools use — internal/anime/oneshot/application,
// injected as Dependencies.OneShot and never via the root anime service — so
// there is no second execution path and no second owner.
//
// Error mapping (application sentinel -> HTTP), per the frozen contract:
//
//	SEED_IDENTITY_REQUIRED        -> 422 (no strong content identity supplied)
//	SEED_PREVIEW_STALE            -> 422 (authorized preview snapshot replaced)
//	SEED_IDENTITY_MISMATCH        -> 422 (fetched torrent contradicts identity)
//	SEED_LOCATOR_UNSAFE           -> 422 (locator fails transport safety)
//	TORRENT_FETCH_FAILED          -> 422 (fetch/bencode validation failed)
//	DRAFT_INVALID                 -> 422 (typed draft validation, with details)
//	FILE_SELECTION_INVALID        -> 422 (empty/duplicate/blank/out-of-scope
//	                                 selected_file_refs; zero writes)
//	FILTER_OVERRIDE_REQUIRED      -> 422 (the file filter excluded every entry
//	                                 and no explicit confirmation was given)
//	ONE_SHOT_CALLER_INVALID       -> 422 (caller/draft/seed binding refused)
//	SEED_PREVIEW_STALE            -> 422 (a custom seed outside the draft's own
//	                                 frozen custom feed; zero writes)
//	IDEMPOTENCY_KEY_REUSED        -> 409 (same key, different canonical body)
//	SEED_PREVIEW_UNAVAILABLE      -> 503 (provider preview fail-closed)
//	TARGET_PROBE_UNAVAILABLE      -> 503 (target admission fail-closed)
//	TARGET_ACQUISITION_FAILED     -> 422 (render/path/entry refusal)
//	TARGET_COLLISION_BLOCKED      -> 409 (frozen collision policy blocks the target)
//	adopted exact target          -> 201 with a null download_task_id (success; no
//	                                 DownloadTask/qB work/Artifact/FileOperation)
//	unknown one_shot_id           -> 404
//
// These codes arrive as typed apperrors.Business / apperrors.Validation /
// apperrors.BadRequest / apperrors.NotFound values and are mapped by the
// shared writeError boundary; this file adds no parallel mapping.
func (s *Server) oneShotService(w http.ResponseWriter, r *http.Request) OneShotService {
	return s.deps.OneShot
}

// AcquireSeedOnce admits one finite one-shot seed acquisition from an exact
// saved candidate revision. The HTTP body is exactly
// {candidate_id, revision, selected_seed}; there is no caller field in the
// contract, so every HTTP caller is the human catalog path bound to the
// current authorized provider preview context.
func (s *Server) AcquireSeedOnce(w http.ResponseWriter, r *http.Request, params generated.AcquireSeedOnceParams) {
	service := s.oneShotService(w, r)
	if service == nil {
		return
	}
	var body generated.AcquireSeedOnceJSONRequestBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	input, err := mapAcquireSeedInput(body)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := service.AcquireSeedOnce(r.Context(), input, string(params.IdempotencyKey))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusCreated, mapOneShotAdmissionResult(result))
}

// DownloadSnapshotSeed admits one finite download for a seed that already
// exists in an exact persisted provider feed snapshot. The body carries
// identities only; the locator, title, published time and strong identity are
// read from that snapshot server-side.
func (s *Server) DownloadSnapshotSeed(w http.ResponseWriter, r *http.Request, snapshotId generated.ID, stableSeedKey string, params generated.DownloadSnapshotSeedParams) {
	service := s.oneShotService(w, r)
	if service == nil {
		return
	}
	var body generated.SnapshotSeedDownloadInput
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	input, err := mapSnapshotSeedDownloadInput(string(snapshotId), stableSeedKey, body)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := service.DownloadSnapshotSeed(r.Context(), input, string(params.IdempotencyKey))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusCreated, mapOneShotAdmissionResult(result))
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func int64Value(value *generated.Version) int64 {
	if value == nil {
		return 0
	}
	return int64(*value)
}

func mapAcquireSeedInput(value generated.AcquireSeedOnceJSONRequestBody) (oneshotapplication.AcquireSeedInput, error) {
	seed := oneshotapplication.SelectedSeed{
		ExpectedPayloadDigest: value.SelectedSeed.ExpectedPayloadDigest,
		InfoHash:              value.SelectedSeed.InfoHash,
		Locator:               stringValue(value.SelectedSeed.Locator),
		Provider:              animedomain.ProviderKind(value.SelectedSeed.Provider),
		ProviderEntryRef:      value.SelectedSeed.ProviderEntryRef,
		ProviderGroupRef:      value.SelectedSeed.ProviderGroupRef,
		SizeBytes:             value.SelectedSeed.SizeBytes,
		SnapshotContentHash:   stringValue(value.SelectedSeed.SnapshotContentHash),
		SnapshotID:            stringValue(value.SelectedSeed.SnapshotId),
		SnapshotVersion:       int64Value(value.SelectedSeed.SnapshotVersion),
		StableSeedKey:         stringValue(value.SelectedSeed.StableSeedKey),
		Title:                 stringValue(value.SelectedSeed.Title),
	}
	// An absent locator selects the snapshot reference form; the application
	// resolves it from the persisted snapshot before anything is hashed or
	// frozen. An absent published_at stays the zero time (required, and
	// validated, only in the locator form).
	if value.SelectedSeed.PublishedAt != nil {
		seed.PublishedAt = *value.SelectedSeed.PublishedAt
	}
	// The two input variants are mutually exclusive. A legacy body (no
	// discriminator) keeps the saved-revision parsing; a current_input body
	// carries the complete unsaved draft. Exclusivity is re-validated in the
	// application layer so any transport path fails with the same typed 400.
	// The manual-confirmation fields are common to every variant; an absent
	// field stays nil so a legacy body's canonical replay identity is byte-
	// identical to before.
	input, err := mapAcquireSeedInputSource(value, seed)
	if err != nil {
		return oneshotapplication.AcquireSeedInput{}, err
	}
	// An omitted episode_offset is 0, the one-shot's default offset.
	if value.EpisodeOffset != nil {
		input.EpisodeOffset = *value.EpisodeOffset
	}
	return input, nil
}

func mapAcquireSeedInputSource(value generated.AcquireSeedOnceJSONRequestBody, seed oneshotapplication.SelectedSeed) (oneshotapplication.AcquireSeedInput, error) {
	switch stringValue3(value.InputSource) {
	case "":
		if value.Draft != nil {
			return oneshotapplication.AcquireSeedInput{}, inputSourceInvalid()
		}
		if value.Revision == nil || *value.Revision < 1 {
			return oneshotapplication.AcquireSeedInput{}, &apperrors.Business{Status: 409, Code: "CANDIDATE_REVISION_REQUIRED", Message: "候选尚无不可变配置版本，请先保存该候选，再执行一次性下载。"}
		}
		return oneshotapplication.AcquireSeedInput{
			CandidateID: stringValue2(value.CandidateId),
			Revision:    *value.Revision,
			Seed:        seed,
			Caller:      oneshotapplication.AcquireSeedCaller{Kind: oneshotapplication.AcquireSeedCallerHuman},
		}, nil
	case generated.AcquireSeedOnceInputInputSourceSavedRevision:
		if value.Draft != nil {
			return oneshotapplication.AcquireSeedInput{}, inputSourceInvalid()
		}
		if value.Revision == nil || *value.Revision < 1 || value.CandidateId == nil || strings.TrimSpace(string(*value.CandidateId)) == "" {
			return oneshotapplication.AcquireSeedInput{}, inputSourceInvalid()
		}
		return oneshotapplication.AcquireSeedInput{
			InputSource: oneshotapplication.AcquireSeedInputSourceSavedRevision,
			CandidateID: string(*value.CandidateId),
			Revision:    *value.Revision,
			Seed:        seed,
			Caller:      oneshotapplication.AcquireSeedCaller{Kind: oneshotapplication.AcquireSeedCallerHuman},
		}, nil
	case generated.AcquireSeedOnceInputInputSourceCurrentInput:
		if value.Draft != nil && (value.Revision != nil || value.CandidateId != nil) {
			return oneshotapplication.AcquireSeedInput{}, inputSourceInvalid()
		}
		if value.Draft == nil {
			return oneshotapplication.AcquireSeedInput{}, inputSourceInvalid()
		}
		draft, err := mapAnimeDraft(*value.Draft)
		if err != nil {
			return oneshotapplication.AcquireSeedInput{}, &apperrors.Business{Status: 422, Code: "DRAFT_INVALID", Message: fmt.Sprintf("draft transport mapping failed: %v", err)}
		}
		return oneshotapplication.AcquireSeedInput{
			InputSource: oneshotapplication.AcquireSeedInputSourceCurrentInput,
			Draft:       &draft,
			Seed:        seed,
			Caller:      oneshotapplication.AcquireSeedCaller{Kind: oneshotapplication.AcquireSeedCallerHuman},
		}, nil
	default:
		return oneshotapplication.AcquireSeedInput{}, inputSourceInvalid()
	}
}

// mapSelectedFileRefs preserves the present/absent distinction: an absent field
// stays nil (legacy canonical identity), a present-but-empty array is kept as a
// non-nil empty set so the application refuses it with FILE_SELECTION_INVALID.
func mapSelectedFileRefs(value *[]string) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	refs := make([]string, len(*value))
	copy(refs, *value)
	for index, ref := range refs {
		refs[index] = strings.TrimSpace(ref)
	}
	return refs, nil
}

func inputSourceInvalid() error {
	return &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "The one-shot config input variants are mutually exclusive: supply either input_source=current_input with a draft, or a saved candidate_id+revision, never both."}
}

// stringValue3 normalizes the optional input_source discriminator.
func stringValue3(value *generated.AcquireSeedOnceInputInputSource) generated.AcquireSeedOnceInputInputSource {
	if value == nil {
		return ""
	}
	return *value
}

// stringValue2 dereferences an optional generated ID.
func stringValue2(value *generated.ID) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func mapOneShotAdmissionResult(result oneshotapplication.OneShotAdmissionResult) generated.OneShotAdmissionResult {
	return generated.OneShotAdmissionResult{
		DownloadTaskId: optionalID(result.DownloadTaskID),
		ManagedAnimeId: generated.ID(result.ManagedAnimeID),
		OneShotId:      generated.ID(result.OneShotID),
		ReleaseId:      generated.ID(result.ReleaseID),
		RssSourceId:    generated.ID(result.RSSSourceID),
		State:          generated.OneShotAcquisitionState(result.State),
		SubscriptionId: generated.ID(result.SubscriptionID),
	}
}

// optionalID maps an optional identity: a nil source is a JSON null, never a
// fabricated empty id. An adopted one-shot has no download task.
func optionalID(value *string) *generated.ID {
	if value == nil {
		return nil
	}
	mapped := generated.ID(*value)
	return &mapped
}

// GetOneShotAcquisition is the owner-first read surface for one finite
// one-shot acquisition. It stays side-effect free: no WorkItem insert, no
// provider/qB/filesystem read, no product write, and it never exposes lease
// tokens, fencing tokens, raw payloads, secrets, or private paths.
func (s *Server) GetOneShotAcquisition(w http.ResponseWriter, r *http.Request, oneShotId generated.OneShotID) {
	service := s.oneShotService(w, r)
	if service == nil {
		return
	}
	view, err := service.ReadOneShotAcquisition(r.Context(), string(oneShotId))
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := mapOneShotAcquisitionView(view)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// ListOneShotExecutionAttempts answers one sequence-DESC keyset page of the
// acquisition's execution-attempt history.
func (s *Server) ListOneShotExecutionAttempts(w http.ResponseWriter, r *http.Request, oneShotId generated.OneShotID, params generated.ListOneShotExecutionAttemptsParams) {
	service := s.oneShotService(w, r)
	if service == nil {
		return
	}
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	cursor := ""
	if params.Cursor != nil {
		cursor = string(*params.Cursor)
	}
	page, err := service.ListOneShotExecutionAttempts(r.Context(), string(oneShotId), limit, cursor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapOneShotAttemptPage(page))
}

// GetOneShotExecutionAttempt answers the full redacted detail of one attempt.
func (s *Server) GetOneShotExecutionAttempt(w http.ResponseWriter, r *http.Request, oneShotId generated.OneShotID, attemptId generated.OneShotAttemptID) {
	service := s.oneShotService(w, r)
	if service == nil {
		return
	}
	attempt, err := service.GetOneShotExecutionAttempt(r.Context(), string(oneShotId), string(attemptId))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapOneShotExecutionAttempt(attempt))
}

func mapOneShotAttemptPage(page oneshotapplication.OneShotAttemptPage) generated.OneShotExecutionAttemptPage {
	mapped := generated.OneShotExecutionAttemptPage{
		Items:      make([]generated.OneShotExecutionAttemptSummary, 0, len(page.Items)),
		NextCursor: cloneStringPointer(page.NextCursor),
	}
	for _, item := range page.Items {
		summary := generated.OneShotExecutionAttemptSummary{
			Id:                generated.ID(item.ID),
			Sequence:          item.Sequence,
			PreviousAttemptId: optionalID(item.PreviousAttemptID),
			Mode:              generated.OneShotExecutionAttemptSummaryMode(item.Mode),
			State:             generated.OneShotExecutionAttemptSummaryState(item.State),
			InputDigest:       item.InputDigest,
			CreatedAt:         item.CreatedAt,
			TerminalAt:        cloneTimePointer(item.TerminalAt),
			ReasonCode:        cloneStringPointer(item.ReasonCode),
		}
		if item.ResumeStage != nil {
			stage := generated.OneShotExecutionAttemptSummaryResumeStage(*item.ResumeStage)
			summary.ResumeStage = &stage
		}
		if item.FailedStage != nil {
			stage := generated.OneShotExecutionAttemptSummaryFailedStage(*item.FailedStage)
			summary.FailedStage = &stage
		}
		mapped.Items = append(mapped.Items, summary)
	}
	return mapped
}

func mapOneShotExecutionAttempt(attempt oneshotapplication.OneShotExecutionAttempt) generated.OneShotExecutionAttempt {
	mapped := generated.OneShotExecutionAttempt{
		Id:                generated.ID(attempt.ID),
		AcquisitionId:     generated.ID(attempt.AcquisitionID),
		Sequence:          attempt.Sequence,
		PreviousAttemptId: optionalID(attempt.PreviousAttemptID),
		Mode:              generated.OneShotExecutionAttemptMode(attempt.Mode),
		State:             generated.OneShotExecutionAttemptState(attempt.State),
		InputDigest:       attempt.InputDigest,
		OutcomeCode:       cloneStringPointer(attempt.OutcomeCode),
		OutcomeReason:     cloneStringPointer(attempt.OutcomeReason),
		TerminalAt:        cloneTimePointer(attempt.TerminalAt),
		CreatedAt:         attempt.CreatedAt,
		UpdatedAt:         attempt.UpdatedAt,
	}
	if attempt.ResumeStage != nil {
		stage := generated.OneShotExecutionAttemptResumeStage(*attempt.ResumeStage)
		mapped.ResumeStage = &stage
	}
	if attempt.FailedStage != nil {
		stage := generated.OneShotExecutionAttemptFailedStage(*attempt.FailedStage)
		mapped.FailedStage = &stage
	}
	if attempt.InputSnapshot != nil {
		if raw, err := json.Marshal(attempt.InputSnapshot); err == nil {
			var snapshot generated.OneShotInputSnapshot
			if err := json.Unmarshal(raw, &snapshot); err == nil {
				mapped.InputSnapshot = &snapshot
			}
		}
	}
	return mapped
}

func mapOneShotAcquisitionView(view oneshotapplication.OneShotAcquisitionView) (generated.OneShotAcquisitionView, error) {
	result := generated.OneShotAcquisitionView{
		OneShotId:          generated.ID(view.OneShotID),
		State:              generated.OneShotAcquisitionState(view.State),
		TerminalOutcome:    mapOneShotTerminalOutcome(view.TerminalOutcome),
		TerminalReasonCode: cloneStringPointer(view.TerminalReasonCode),
		TerminalReason:     cloneStringPointer(view.TerminalReason),
		ManagedAnimeId:     generated.ID(view.ManagedAnimeID),
		SubscriptionId:     generated.ID(view.SubscriptionID),
		RssSourceId:        generated.ID(view.RSSSourceID),
		ReleaseId:          generated.ID(view.ReleaseID),
		DownloadTaskId:     optionalID(view.DownloadTaskID),
		CreatedAt:          view.CreatedAt,
		UpdatedAt:          view.UpdatedAt,
		Version:            generated.Version(view.Version),
	}
	if view.InputSnapshot != nil {
		raw, err := json.Marshal(view.InputSnapshot)
		if err == nil {
			var snapshot generated.OneShotInputSnapshot
			if err := json.Unmarshal(raw, &snapshot); err == nil {
				result.InputSnapshot = &snapshot
			}
		}
	}
	result.CurrentAttemptId = optionalID(view.CurrentAttemptID)
	if view.FailedStage != nil {
		stage := generated.OneShotAcquisitionViewFailedStage(*view.FailedStage)
		result.FailedStage = &stage
	}
	if view.DownloadTask != nil {
		frozenInput, err := mapFrozenDownloadInput(view.DownloadTask.FrozenInput)
		if err != nil {
			return generated.OneShotAcquisitionView{}, err
		}
		frozenPolicy, err := mapFrozenPolicySnapshot(view.DownloadTask.FrozenPolicy)
		if err != nil {
			return generated.OneShotAcquisitionView{}, err
		}
		task := generated.DownloadTask{
			FailedWorkItemId: cloneStringPointer(view.DownloadTask.FailedWorkItemID),
			FailureCode:      cloneStringPointer(view.DownloadTask.FailureCode),
			FailureReason:    cloneStringPointer(view.DownloadTask.FailureReason),
			FrozenInput:      frozenInput,
			FrozenPolicy:     frozenPolicy,
			Id:               generated.ID(view.DownloadTask.ID),
			IdempotencyKey:   view.DownloadTask.IdempotencyKey,
			ReleaseId:        generated.ID(view.DownloadTask.ReleaseID),
			State:            generated.DownloadTaskState(view.DownloadTask.State),
			Version:          generated.Version(view.DownloadTask.Version),
		}
		if view.DownloadTask.ProviderCorrelation != nil {
			task.ProviderCorrelation = &generated.ProviderCorrelation{
				ExternalId: cloneStringPointer(view.DownloadTask.ProviderCorrelation.ExternalID),
				InfoHash:   cloneStringPointer(view.DownloadTask.ProviderCorrelation.InfoHash),
				StableTag:  cloneStringPointer(view.DownloadTask.ProviderCorrelation.StableTag),
			}
		}
		result.DownloadTask = &task
	}
	if view.Artifact != nil {
		artifact := generated.Artifact{
			DownloadTaskId:   generated.ID(view.Artifact.DownloadTaskID),
			FailedWorkItemId: cloneStringPointer(view.Artifact.FailedWorkItemID),
			FailureCode:      cloneStringPointer(view.Artifact.FailureCode),
			FailureReason:    cloneStringPointer(view.Artifact.FailureReason),
			Id:               generated.ID(view.Artifact.ID),
			State:            generated.ArtifactState(view.Artifact.State),
			Version:          generated.Version(view.Artifact.Version),
			Files:            make([]generated.ArtifactFile, 0, len(view.Artifact.Files)),
			Fingerprints:     make([]generated.Fingerprint, 0, len(view.Artifact.Fingerprints)),
		}
		for _, fingerprint := range view.Artifact.Fingerprints {
			artifact.Fingerprints = append(artifact.Fingerprints, mapOneShotFingerprint(fingerprint))
		}
		for _, file := range view.Artifact.Files {
			mapped := generated.ArtifactFile{
				Fingerprint: mapOneShotFingerprint(file.Fingerprint),
				MediaKind:   generated.ArtifactFileMediaKind(file.MediaKind),
				Path:        file.Path,
				Ref:         file.Ref,
			}
			if file.SubtitleBinding != nil {
				binding := generated.ArtifactFileSubtitleBinding{
					BoundVideoFileRef: cloneStringPointer(file.SubtitleBinding.BoundVideoFileRef),
					Confidence:        cloneFloat32Pointer(file.SubtitleBinding.Confidence),
					Method:            cloneStringPointer(file.SubtitleBinding.Method),
					State:             generated.ArtifactFileSubtitleBindingState(file.SubtitleBinding.State),
					Diagnostics:       make([]generated.NamingDiagnostic, 0, len(file.SubtitleBinding.Diagnostics)),
				}
				for _, diagnostic := range file.SubtitleBinding.Diagnostics {
					binding.Diagnostics = append(binding.Diagnostics, generated.NamingDiagnostic{
						Code: diagnostic.Code, Message: diagnostic.Message, Path: cloneStringPointer(diagnostic.Path),
					})
				}
				mapped.SubtitleBinding = &binding
			}
			artifact.Files = append(artifact.Files, mapped)
		}
		result.Artifact = &artifact
	}
	if view.FileOperation != nil {
		operation := generated.FileOperation{
			ArtifactFileRef:   view.FileOperation.ArtifactFileRef,
			ArtifactId:        generated.ID(view.FileOperation.ArtifactID),
			CollisionPolicy:   generated.CollisionPolicy(view.FileOperation.CollisionPolicy),
			ExpectedSizeBytes: cloneInt64Pointer(view.FileOperation.ExpectedSizeBytes),
			FailedWorkItemId:  cloneStringPointer(view.FileOperation.FailedWorkItemID),
			FailureCode:       cloneStringPointer(view.FileOperation.FailureCode),
			FailureReason:     cloneStringPointer(view.FileOperation.FailureReason),
			FrozenConfigSnapshot: generated.FrozenFileConfigSnapshot{
				RulesVersion:  generated.Version(view.FileOperation.FrozenConfigSnapshot.RulesVersion),
				SafetyVersion: view.FileOperation.FrozenConfigSnapshot.SafetyVersion,
			},
			FrozenRenderSnapshot: generated.FrozenRenderSnapshot{
				Directory:       view.FileOperation.FrozenRenderSnapshot.Directory,
				Filename:        view.FileOperation.FrozenRenderSnapshot.Filename,
				RendererVersion: view.FileOperation.FrozenRenderSnapshot.RendererVersion,
			},
			Id:                      generated.ID(view.FileOperation.ID),
			IdempotencyKey:          view.FileOperation.IdempotencyKey,
			ObservedAt:              cloneTimePointer(view.FileOperation.ObservedAt),
			ObservedTargetSizeBytes: cloneInt64Pointer(view.FileOperation.ObservedTargetSizeBytes),
			Operation:               generated.FileOperationOperation(view.FileOperation.Operation),
			SourceFingerprint:       mapOneShotFingerprint(view.FileOperation.SourceFingerprint),
			SourcePath:              view.FileOperation.SourcePath,
			State:                   generated.FileOperationState(view.FileOperation.State),
			TargetPath:              view.FileOperation.TargetPath,
			Version:                 generated.Version(view.FileOperation.Version),
		}
		if view.FileOperation.Intent != nil {
			intent := generated.FileOperationIntent(*view.FileOperation.Intent)
			operation.Intent = &intent
		}
		if view.FileOperation.TargetFingerprint != nil {
			fingerprint := mapOneShotFingerprint(*view.FileOperation.TargetFingerprint)
			operation.TargetFingerprint = &fingerprint
		}
		result.FileOperation = &operation
	}
	return result, nil
}

func mapOneShotTerminalOutcome(value *oneshotapplication.OneShotTerminalOutcome) *generated.OneShotTerminalOutcome {
	if value == nil {
		return nil
	}
	mapped := generated.OneShotTerminalOutcome(*value)
	return &mapped
}

func mapOneShotFingerprint(value oneshotapplication.OneShotFingerprint) generated.Fingerprint {
	return generated.Fingerprint{Algorithm: generated.FingerprintAlgorithm(value.Algorithm), SizeBytes: value.SizeBytes, Value: value.Value}
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloat32Pointer(value *float32) *float32 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
