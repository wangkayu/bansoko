package httpapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	animeapplication "bansoko/internal/anime/application"
	oneshotapplication "bansoko/internal/anime/oneshot/application"
	"bansoko/internal/transport/httpapi/generated"
)

func TestMapOneShotAcquisitionViewProjectsNestedOwners(t *testing.T) {
	digest := "sha256:abc"
	evaluator := "eval-1"
	externalID := "external-1"
	infoHash := "hash-1"
	stableTag := "tag-1"
	failedTask := "work-task"
	failedArtifact := "work-artifact"
	failedOperation := "work-operation"
	failureCode := "FAILED"
	failureReason := "safe reason"
	boundRef := "video-1"
	method := "basename_match"
	confidence := float32(0.95)
	path := "subtitle.ass"
	observedAt := time.Unix(20, 0).UTC()
	expectedSize := int64(42)
	observedSize := int64(42)
	intent := "replace"
	downloadTaskID := "task-1"

	got, err := mapOneShotAcquisitionView(oneshotapplication.OneShotAcquisitionView{
		Artifact: &oneshotapplication.OneShotArtifact{
			DownloadTaskID:   "task-1",
			FailedWorkItemID: &failedArtifact,
			FailureCode:      &failureCode,
			FailureReason:    &failureReason,
			Files: []oneshotapplication.OneShotArtifactFile{{
				Fingerprint: oneshotapplication.OneShotFingerprint{Algorithm: "metadata", SizeBytes: 42, Value: "file-fingerprint"},
				MediaKind:   "video",
				Path:        "Show/video.mkv",
				Ref:         "file-1",
				SubtitleBinding: &oneshotapplication.OneShotSubtitleBinding{
					BoundVideoFileRef: &boundRef,
					Confidence:        &confidence,
					Diagnostics: []oneshotapplication.OneShotNamingDiagnostic{{
						Code: "WARN", Message: "diagnostic", Path: &path,
					}},
					Method: &method,
					State:  "bound",
				},
			}},
			Fingerprints: []oneshotapplication.OneShotFingerprint{{Algorithm: "metadata", SizeBytes: 42, Value: "artifact-fingerprint"}},
			ID:           "artifact-1",
			State:        "verified",
			Version:      3,
		},
		CreatedAt:      time.Unix(10, 0).UTC(),
		DownloadTaskID: &downloadTaskID,
		DownloadTask: &oneshotapplication.OneShotDownloadTask{
			FailedWorkItemID: &failedTask,
			FailureCode:      &failureCode,
			FailureReason:    &failureReason,
			FrozenInput: oneshotapplication.OneShotFrozenDownloadInput{
				ReleaseStableItemKey: "release-key",
				SelectedURLDigest:    &digest,
				SourceLocatorDisplay: "https://example.test/seed.torrent",
			},
			FrozenPolicy: oneshotapplication.OneShotFrozenPolicySnapshot{
				EvaluatorVersion: &evaluator,
				MetadataVersion:  2,
				RulesVersion:     3,
			},
			ID: "task-1", IdempotencyKey: "effect-key", ReleaseID: "release-1", State: "completed", Version: 4,
			ProviderCorrelation: &oneshotapplication.OneShotProviderCorrelation{
				ExternalID: &externalID, InfoHash: &infoHash, StableTag: &stableTag,
			},
		},
		FileOperation: &oneshotapplication.OneShotFileOperation{
			ArtifactFileRef: "file-1", ArtifactID: "artifact-1", CollisionPolicy: "versioned",
			ExpectedSizeBytes: &expectedSize, FailedWorkItemID: &failedOperation, FailureCode: &failureCode,
			FailureReason:        &failureReason,
			FrozenConfigSnapshot: oneshotapplication.OneShotFrozenFileConfigSnapshot{RulesVersion: 5, SafetyVersion: "safety-1"},
			FrozenRenderSnapshot: oneshotapplication.OneShotFrozenRenderSnapshot{Directory: "Show", Filename: "video.mkv", RendererVersion: "render-1"},
			ID:                   "operation-1", IdempotencyKey: "file-effect", Intent: &intent, ObservedAt: &observedAt,
			ObservedTargetSizeBytes: &observedSize, Operation: "copy",
			SourceFingerprint: oneshotapplication.OneShotFingerprint{Algorithm: "metadata", SizeBytes: 42, Value: "source"},
			SourcePath:        "download/video.mkv", State: "completed",
			TargetFingerprint: &oneshotapplication.OneShotFingerprint{Algorithm: "metadata", SizeBytes: 42, Value: "target"},
			TargetPath:        "library/video.mkv", Version: 6,
		},
		ManagedAnimeID: "anime-1", OneShotID: "one-shot-1", ReleaseID: "release-1", RSSSourceID: "source-1",
		State: oneshotapplication.OneShotAcquisitionStateCleanupBlocked, SubscriptionID: "subscription-1",
		TerminalOutcome: func() *oneshotapplication.OneShotTerminalOutcome {
			value := oneshotapplication.OneShotTerminalOutcome("failed")
			return &value
		}(),
		TerminalReason: &failureReason, TerminalReasonCode: &failureCode, UpdatedAt: time.Unix(30, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("map view: %v", err)
	}

	if got.OneShotId != "one-shot-1" || got.State != generated.OneShotAcquisitionStateCleanupBlocked || got.TerminalOutcome == nil || *got.TerminalOutcome != generated.OneShotTerminalOutcome("failed") {
		t.Fatalf("marker fields were not projected: %+v", got)
	}
	if got.DownloadTask == nil || got.DownloadTask.ProviderCorrelation == nil || *got.DownloadTask.ProviderCorrelation.InfoHash != infoHash || got.DownloadTask.FrozenInput.SelectedUrlDigest == nil || *got.DownloadTask.FrozenInput.SelectedUrlDigest != digest {
		t.Fatalf("download task fields were not projected: %+v", got.DownloadTask)
	}
	if got.Artifact == nil || len(got.Artifact.Files) != 1 || got.Artifact.Files[0].SubtitleBinding == nil || got.Artifact.Files[0].SubtitleBinding.Confidence == nil || *got.Artifact.Files[0].SubtitleBinding.Confidence != confidence {
		t.Fatalf("artifact fields were not projected: %+v", got.Artifact)
	}
	if got.FileOperation == nil || got.FileOperation.TargetFingerprint == nil || got.FileOperation.ExpectedSizeBytes == nil || *got.FileOperation.ExpectedSizeBytes != expectedSize || got.FileOperation.FrozenRenderSnapshot.Filename != "video.mkv" {
		t.Fatalf("file operation fields were not projected: %+v", got.FileOperation)
	}
}

func TestMapOneShotAcquisitionViewProjectsInputSnapshot(t *testing.T) {
	locator := "https://torrent.example.test/seed.torrent"
	candidateID := "candidate-1"
	revision := int64(4)
	digest := "sha256:" + strings.Repeat("a", 64)
	got, err := mapOneShotAcquisitionView(oneshotapplication.OneShotAcquisitionView{
		InputSnapshot: &oneshotapplication.OneShotInputSnapshot{
			CandidateID:       candidateID,
			Revision:          revision,
			SelectedSeed:      oneshotapplication.SelectedSeed{Locator: locator, Title: "submitted seed"},
			EffectiveSections: json.RawMessage(`{"rules":{"naming":"frozen"}}`),
			SectionVersions:   map[string]int64{"rules.naming": 2},
			SectionSchemas:    map[string]string{"rules.naming": "v2"},
			EffectiveDigest:   digest,
			ConfigIdentity:    animeapplication.ConfigRevisionIdentity{CandidateID: &candidateID, Revision: &revision, EffectiveDigest: digest, IdentityKind: "candidate_revision"},
		},
	})
	if err != nil {
		t.Fatalf("map view: %v", err)
	}
	mappedCandidateID := generated.ID(candidateID)
	if got.InputSnapshot == nil || got.InputSnapshot.CandidateId == nil || *got.InputSnapshot.CandidateId != mappedCandidateID || got.InputSnapshot.Revision == nil || *got.InputSnapshot.Revision != revision || got.InputSnapshot.SelectedSeed.Locator != locator || got.InputSnapshot.SelectedSeed.Title != "submitted seed" || got.InputSnapshot.EffectiveDigest != digest || got.InputSnapshot.ConfigIdentity.IdentityKind != "candidate_revision" {
		t.Fatalf("input snapshot was not projected: %+v", got.InputSnapshot)
	}
	if got.InputSnapshot.EffectiveSections["rules"] == nil || got.InputSnapshot.SectionVersions["rules.naming"] != 2 || got.InputSnapshot.SectionSchemas["rules.naming"] != "v2" {
		t.Fatalf("frozen config snapshot was not projected: %+v", got.InputSnapshot)
	}
}

func TestMapOneShotAcquisitionViewPreservesNullableAndCleanedShape(t *testing.T) {
	got, err := mapOneShotAcquisitionView(oneshotapplication.OneShotAcquisitionView{
		OneShotID: "one-shot-1", State: oneshotapplication.OneShotAcquisitionStateCleaned,
		TerminalOutcome: nil, TerminalReason: nil, TerminalReasonCode: nil,
	})
	if err != nil {
		t.Fatalf("map view: %v", err)
	}
	if got.DownloadTask != nil || got.Artifact != nil || got.FileOperation != nil || got.TerminalOutcome != nil || got.TerminalReason != nil || got.TerminalReasonCode != nil {
		t.Fatalf("nullable/cleaned fields were not preserved: %+v", got)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal mapped view: %v", err)
	}
	var shape map[string]any
	if err := json.Unmarshal(body, &shape); err != nil {
		t.Fatalf("decode mapped JSON: %v", err)
	}
	for _, key := range []string{"download_task", "artifact", "file_operation", "terminal_outcome", "terminal_reason", "terminal_reason_code"} {
		if value, ok := shape[key]; !ok || value != nil {
			t.Fatalf("%s = %#v, want explicit null", key, value)
		}
	}
}

// The read surface must carry the current marker version: the manual recovery
// commands require If-Match to equal it exactly, so a null or missing version
// would make every recovery a deterministic 409.
func TestMapOneShotAcquisitionViewProjectsMarkerVersion(t *testing.T) {
	got, err := mapOneShotAcquisitionView(oneshotapplication.OneShotAcquisitionView{
		OneShotID: "one-shot-1", State: oneshotapplication.OneShotAcquisitionStateReclaimPending, Version: 7,
	})
	if err != nil {
		t.Fatalf("map view: %v", err)
	}
	if got.Version != generated.Version(7) {
		t.Fatalf("version = %d, want 7", got.Version)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal mapped view: %v", err)
	}
	var shape map[string]any
	if err := json.Unmarshal(body, &shape); err != nil {
		t.Fatalf("decode mapped JSON: %v", err)
	}
	value, ok := shape["version"]
	if !ok {
		t.Fatal("mapped view omits version")
	}
	if number, isNumber := value.(float64); !isNumber || number != 7 {
		t.Fatalf("version = %#v, want the number 7", value)
	}
}

// An adopted acquisition has no download task; the admission handle must carry
// an explicit null rather than a fabricated placeholder id, while the ordinary
// download handle stays a plain id.
func TestMapOneShotAdmissionResultKeepsAdoptedDownloadTaskNull(t *testing.T) {
	adopted := mapOneShotAdmissionResult(oneshotapplication.OneShotAdmissionResult{
		OneShotID: "one-shot-adopted", ManagedAnimeID: "anime-1", SubscriptionID: "sub-1",
		RSSSourceID: "src-1", ReleaseID: "rel-1",
		State: string(oneshotapplication.OneShotAcquisitionStateReclaimPending),
	})
	body, err := json.Marshal(adopted)
	if err != nil {
		t.Fatalf("marshal adopted handle: %v", err)
	}
	var shape map[string]any
	if err := json.Unmarshal(body, &shape); err != nil {
		t.Fatalf("decode adopted handle: %v", err)
	}
	value, present := shape["download_task_id"]
	if !present || value != nil {
		t.Fatalf("adopted download_task_id = %#v (present=%t), want explicit null", value, present)
	}
	if adopted.DownloadTaskId != nil {
		t.Fatalf("adopted handle carries a download task id: %q", string(*adopted.DownloadTaskId))
	}

	taskID := "task-1"
	downloaded := mapOneShotAdmissionResult(oneshotapplication.OneShotAdmissionResult{
		OneShotID: "one-shot-downloaded", ManagedAnimeID: "anime-1", SubscriptionID: "sub-1",
		RSSSourceID: "src-1", ReleaseID: "rel-1", DownloadTaskID: &taskID,
		State: string(oneshotapplication.OneShotAcquisitionStateRunning),
	})
	if downloaded.DownloadTaskId == nil || string(*downloaded.DownloadTaskId) != taskID {
		t.Fatalf("downloaded handle lost its task id: %+v", downloaded.DownloadTaskId)
	}
}

// Manual confirmation: the admission body forwards the confirmed
// torrent file set and the business-filter override, and keeps the
// present/absent distinction a legacy body depends on.
func TestAcquireContractHasNoManualFilterInputs(t *testing.T) {
	var body generated.AcquireSeedOnceJSONRequestBody
	typ := reflect.TypeOf(body)
	for _, name := range []string{"SelectedFileRefs", "OverrideFilter"} {
		if _, exists := typ.FieldByName(name); exists {
			t.Fatalf("retired input %s remains", name)
		}
	}
}
