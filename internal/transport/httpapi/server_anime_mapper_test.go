package httpapi

import (
	"bansoko/internal/testutil/presetdraft"
	"encoding/json"
	"strings"
	"testing"
	"time"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/transport/httpapi/generated"
)

func TestMapMetadataHoldKeepsRequiredMissingArray(t *testing.T) {
	// Regression: the mapper turned an empty hold into a null array, which the
	// workspace/RSS UI dereferences with hold.missing.join(...).
	hold := mapMetadataHold(animedomain.MetadataHold{})
	if hold.Missing == nil {
		t.Fatal("empty hold mapped to nil; schema requires a non-nullable array")
	}
	body, err := json.Marshal(hold)
	if err != nil {
		t.Fatalf("marshal mapped metadata hold: %v", err)
	}
	if string(body) != `{"held":false,"missing":[]}` {
		t.Fatalf("metadata hold wire shape = %s, want an empty array", body)
	}

	populated := mapMetadataHold(animedomain.MetadataHold{Held: true, Missing: []animedomain.MetadataHoldMissing{animedomain.MetadataHoldMissing("tmdb")}})
	if len(populated.Missing) != 1 || populated.Missing[0] != generated.MetadataHoldMissingTmdb {
		t.Fatalf("missing = %#v, want tmdb", populated.Missing)
	}
}

func TestMapSubscriptionDeleteInput(t *testing.T) {
	got := mapSubscriptionDeleteInput(generated.SubscriptionDelete{ExpectedVersion: 42})
	if got.ExpectedVersion != 42 {
		t.Fatalf("expected version = %d, want 42", got.ExpectedVersion)
	}
}

func TestMapRSSRefreshAccepted(t *testing.T) {
	got := mapRSSRefreshAccepted(ownershipapplication.RSSRefreshAccepted{Accepted: true, WorkItemID: "work-42"})
	if !got.Accepted || got.WorkItemId != "work-42" {
		t.Fatalf("mapped refresh result = %+v", got)
	}
}

func TestMapAnimeConfigCandidate(t *testing.T) {
	createdAt := time.Unix(10, 0).UTC()
	updatedAt := time.Unix(20, 0).UTC()
	got, err := mapAnimeConfigCandidate(draftapplication.AnimeConfigCandidate{
		ActivatedManagedAnimeId: candidateStringPointer("anime-1"),
		CreatedAt:               createdAt,
		Draft: animedomain.AnimeDraft{
			Title:  "Show",
			Origin: animedomain.SubscriptionOriginMikan,
		},
		Id:             "candidate-1",
		InstanceLabel:  candidateStringPointer("instance"),
		Origin:         animedomain.SubscriptionOriginMikan,
		OriginEntryRef: candidateStringPointer("entry-1"),
		Title:          "Show",
		TitleOverride:  candidateStringPointer("Override"),
		UpdatedAt:      updatedAt,
		Version:        3,
	})
	if err != nil {
		t.Fatalf("map candidate: %v", err)
	}
	if got.Id != "candidate-1" || got.Version != 3 || got.Draft.Title != "Show" ||
		got.ActivatedManagedAnimeId == nil || *got.ActivatedManagedAnimeId != "anime-1" ||
		got.CreatedAt != createdAt || got.UpdatedAt != updatedAt {
		t.Fatalf("mapped candidate = %+v", got)
	}
}

func candidateStringPointer(value string) *string { return &value }

// A custom preset keeps its poster without a linked subscription: the poster
// path comes from the draft's own Bangumi binding.
func TestMapAnimeConfigCandidatePosterFromDraftBinding(t *testing.T) {
	bound, err := mapAnimeConfigCandidate(draftapplication.AnimeConfigCandidate{
		Draft: animedomain.AnimeDraft{
			Title:    "Custom",
			Origin:   animedomain.SubscriptionOriginCustom,
			Metadata: animedomain.AnimeMetadataDraft{Bgm: &animedomain.BGMBinding{WorkId: " 434144 "}},
		},
		Id:     "candidate-custom",
		Origin: animedomain.SubscriptionOriginCustom,
		Title:  "Custom",
	})
	if err != nil {
		t.Fatalf("map candidate: %v", err)
	}
	if bound.PosterUrl == nil || *bound.PosterUrl != "/api/v1/images/poster/434144" {
		t.Fatalf("poster_url = %v, want /api/v1/images/poster/434144", bound.PosterUrl)
	}
	for _, bgm := range []*animedomain.BGMBinding{nil, {WorkId: "  "}} {
		unbound, err := mapAnimeConfigCandidate(draftapplication.AnimeConfigCandidate{
			Draft: animedomain.AnimeDraft{
				Title:    "Custom",
				Origin:   animedomain.SubscriptionOriginCustom,
				Metadata: animedomain.AnimeMetadataDraft{Bgm: bgm},
			},
			Id:     "candidate-custom",
			Origin: animedomain.SubscriptionOriginCustom,
			Title:  "Custom",
		})
		if err != nil {
			t.Fatalf("map candidate: %v", err)
		}
		if unbound.PosterUrl != nil {
			t.Fatalf("poster_url = %q, want nil without a binding", *unbound.PosterUrl)
		}
	}
}

func TestMapAnimeWorkspacePreservesOwnerProjectionAndIsolation(t *testing.T) {
	titleOverride := "Override"
	originRef := "origin-1"
	instance := "instance-1"
	totalEpisodes := 12
	episode := float32(3)
	quality := "1080p"
	special := false
	language := "jpn"
	filePath := "file.mkv"
	fileRef := "video-1"
	confidence := float32(0.9)
	method := "naming"
	diagnosticPath := "$.name"
	observedSize := int64(100)
	reason := "missing"
	evalVersion := "eval-1"
	selectedURLDigest := "sha256:abc"
	evaluatorVersion := "rules-1"
	feedTitle := "Feed"
	workItem := "work-1"
	occurred := time.Unix(100, 0).UTC()
	retention := time.Unix(200, 0).UTC()
	sections := presetdraft.DefaultDomainRules(t)

	targets := &[]readmodelapplication.AcquisitionTarget{{ID: "target-1", ReleaseID: "release-1", TargetRef: "video", SourcePath: filePath, TargetPath: "library/file.mkv", MediaKind: "video", Intent: "ensure_present", CollisionPolicy: "overwrite", ExpectedSizeBytes: observedSize, SpecDigest: "digest-1234", Version: 2}}
	workspace := readmodelapplication.AnimeWorkspace{
		AcquisitionTargets: targets,
		Artifacts:          []readmodelapplication.Artifact{{ID: "artifact-1", DownloadTaskID: "task-1", State: "verified", FailedWorkItemID: &workItem, Files: []readmodelapplication.ArtifactFile{{Ref: fileRef, Path: filePath, MediaKind: "video", Fingerprint: readmodelapplication.Fingerprint{Algorithm: "metadata", SizeBytes: observedSize, Value: "hash"}, SubtitleBinding: &readmodelapplication.ArtifactFileSubtitleBinding{State: "bound", BoundVideoFileRef: &fileRef, Confidence: &confidence, Method: &method, Diagnostics: []readmodelapplication.NamingDiagnostic{{Code: "ok", Message: "matched", Path: &diagnosticPath}}}}}, Fingerprints: []readmodelapplication.Fingerprint{{Algorithm: "metadata", SizeBytes: observedSize, Value: "hash"}}, Version: 4}},
		DownloadTasks:      []readmodelapplication.WorkspaceDownloadTask{{ID: "task-1", ReleaseID: "release-1", State: "completed", IdempotencyKey: "idem-1", FailedWorkItemID: &workItem, FrozenInput: animeapplication.FrozenDownloadInput{ReleaseStableItemKey: "stable-1", SelectedURLDigest: &selectedURLDigest, SourceLocatorDisplay: "feed"}, FrozenPolicy: animeapplication.FrozenPolicySnapshot{EvaluatorVersion: &evaluatorVersion, MetadataVersion: 2, RulesVersion: 3}, ProviderCorrelation: &readmodelapplication.WorkspaceProviderCorrelation{ExternalID: &workItem}}},
		EpisodeProgress:    &readmodelapplication.EpisodeProgress{Done: false, Episodes: []int{1, 2, 3}, Total: &totalEpisodes},
		FileOperations:     []readmodelapplication.FileOperation{{ID: "op-1", ArtifactID: "artifact-1", ArtifactFileRef: fileRef, Operation: "copy", SourcePath: filePath, TargetPath: "library/file.mkv", State: "completed", CollisionPolicy: "overwrite", Intent: &reason, ExpectedSizeBytes: &observedSize, ObservedTargetSizeBytes: &observedSize, SourceFingerprint: readmodelapplication.Fingerprint{Algorithm: "metadata", SizeBytes: observedSize, Value: "hash"}, TargetFingerprint: &readmodelapplication.Fingerprint{Algorithm: "metadata", SizeBytes: observedSize, Value: "hash"}, FrozenConfigSnapshot: readmodelapplication.FrozenFileConfigSnapshot{RulesVersion: 3, SafetyVersion: "safe-1"}, FrozenRenderSnapshot: readmodelapplication.FrozenRenderSnapshot{Directory: "library", Filename: "file.mkv", RendererVersion: "render-1"}, IdempotencyKey: "op-idem", Version: 5}},
		Logs:               []readmodelapplication.SubscriptionLogEntry{{ID: "log-1", SubscriptionID: "subscription-1", ManagedAnimeID: "anime-1", Kind: "release_processed", Outcome: "success", OccurredAt: occurred, RetentionUntil: retention, AnimeTitle: "Title", Source: "one_shot", MessageCode: "release_accepted", RSSSourceID: &workItem, ReleaseID: &evalVersion, GroupRef: &reason}},
		ManagedAnime:       animeapplication.ManagedAnime{ID: "anime-1", Title: "Title", TitleOverride: &titleOverride, Version: 7},
		Metadata:           animeapplication.AnimeMetadata{ManagedAnimeID: "anime-1", BGM: &animeapplication.BGMBinding{WorkID: "bgm-1"}, TMDB: &animeapplication.TMDBBinding{WorkID: "tmdb-1", MediaKind: "tv", Season: 1}, TitleOverride: &titleOverride, TotalEpisodes: &totalEpisodes, Version: 8},
		MetadataHold:       animeapplication.MetadataHold{Held: true, Missing: []string{"tmdb"}},
		Releases:           []readmodelapplication.Release{{ID: "release-1", RSSSourceID: "source-1", StableItemKey: "stable-1", ReasonCode: "accepted", ReasonDetail: &reason, EvaluationVersion: &evalVersion, DownloadTaskID: &workItem, Verdict: "submitted", Version: 9, RawFacts: readmodelapplication.RawReleaseFacts{Title: "Episode", EnclosureLength: &observedSize}, SourceFacts: readmodelapplication.SourceFacts{IdentityKind: "guid", IdentityValue: "guid-1"}, ParsedFacts: &readmodelapplication.ReleaseParsedFacts{Episode: &episode, GroupName: &reason, Languages: &[]string{language}, Quality: &quality, Special: &special}, ReleaseFiles: &[]readmodelapplication.ReleaseFile{{Filename: "Episode.mkv", Path: filePath, MediaKind: "video", Episode: &reason, Language: &language, Resolution: &quality, SizeBytes: &observedSize}}, AcquisitionTargets: targets, TargetObservations: &[]readmodelapplication.TargetObservation{{TargetID: "target-1", State: "present_matched", ObservedAt: occurred, ObservedSizeBytes: &observedSize, ReasonCode: &reason, Version: 2}}}},
		RSSSourceRuntimes:  []animeapplication.RSSSourceRuntime{{RSSSourceID: "source-1", Health: "ready", Cursor: &animeapplication.RSSCursor{Strategy: "etag", Value: "etag-1"}, LastGoodFacts: &animeapplication.RSSLastGoodFacts{FeedTitle: &feedTitle, ItemCount: 1, NewestStableItemKey: "stable-1"}, FailedWorkItemID: &workItem, LastRefreshAt: &occurred, LastSuccessAt: &occurred, ReasonCode: &reason, ReasonDetail: &quality, Version: 3}},
		RSSSources:         []animeapplication.RSSSource{{ID: "source-1", SubscriptionID: "subscription-1", SourceKind: animedomain.SourceKindProviderGroup, LocatorDisplay: "feed", GroupName: "Group", Title: "Feed", Version: 2}},
		Rules:              ownershipapplication.RulesOwner{ManagedAnimeID: "anime-1", Sections: sections, Effective: animedomain.EffectiveRules{}, Version: 3},
		Subscription:       animeapplication.Subscription{ID: "subscription-1", ManagedAnimeID: "anime-1", Origin: "mikan", OriginEntryRef: &originRef, InstanceLabel: &instance, State: "active", Version: 4},
		TargetObservations: &[]readmodelapplication.TargetObservation{{TargetID: "target-1", State: "present_matched", ObservedAt: occurred, ObservedSizeBytes: &observedSize, ReasonCode: &reason, Version: 2}},
	}

	got, err := mapAnimeWorkspace(workspace)
	if err != nil {
		t.Fatalf("map workspace: %v", err)
	}
	if got.ManagedAnime.Id != "anime-1" || got.Metadata.Bgm == nil || got.Metadata.Tmdb == nil || got.Subscription.Id != "subscription-1" || got.EpisodeProgress == nil || len(got.Releases) != 1 || len(got.Artifacts) != 1 || len(got.FileOperations) != 1 || len(got.Logs) != 1 {
		t.Fatalf("top-level workspace projection incomplete: %+v", got)
	}
	if got.Releases[0].ParsedFacts == nil || got.Releases[0].ParsedFacts.Languages == nil || got.Releases[0].ReleaseFiles == nil || got.Releases[0].AcquisitionTargets == nil || got.Releases[0].TargetObservations == nil {
		t.Fatal("nested release projection incomplete")
	}
	if got.Artifacts[0].Files[0].SubtitleBinding == nil || got.Artifacts[0].Files[0].SubtitleBinding.Diagnostics[0].Path == nil || got.DownloadTasks[0].ProviderCorrelation == nil || got.FileOperations[0].TargetFingerprint == nil {
		t.Fatal("nested owner projection incomplete")
	}
	if got.AcquisitionTargets == nil || got.TargetObservations == nil || got.Rules.ManagedAnimeId != "anime-1" {
		t.Fatal("pointer or rules projection incomplete")
	}
	workspace.ManagedAnime.TitleOverride = nil
	workspace.Releases[0].ParsedFacts.Languages = &[]string{"mutated"}
	workspace.Artifacts[0].Files[0].SubtitleBinding.Diagnostics[0].Path = nil
	if got.ManagedAnime.TitleOverride == nil || got.Releases[0].ParsedFacts.Languages == nil || len(*got.Releases[0].ParsedFacts.Languages) != 1 || (*got.Releases[0].ParsedFacts.Languages)[0] != language || got.Artifacts[0].Files[0].SubtitleBinding.Diagnostics[0].Path == nil {
		t.Fatal("mapped nested values alias application input")
	}
	for _, key := range []string{"managed_anime", "title_override", "download_task_id", "source_fingerprint", "target_fingerprint", "rss_source_runtimes", "acquisition_targets"} {
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal workspace: %v", err)
		}
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("workspace JSON missing snake_case key %q: %s", key, raw)
		}
	}
}

func TestMapAnimeWorkspacePreservesNilAndEmptySlices(t *testing.T) {
	sections := presetdraft.DefaultDomainRules(t)
	base := readmodelapplication.AnimeWorkspace{ManagedAnime: animeapplication.ManagedAnime{ID: "anime-1"}, Rules: ownershipapplication.RulesOwner{ManagedAnimeID: "anime-1", Sections: sections}}
	got, err := mapAnimeWorkspace(base)
	if err != nil {
		t.Fatalf("map nil workspace: %v", err)
	}
	if got.Releases != nil || got.RssSources != nil || got.AcquisitionTargets != nil || got.TargetObservations != nil {
		t.Fatal("nil slices changed during mapping")
	}
	base.Releases = []readmodelapplication.Release{}
	base.RSSSources = []animeapplication.RSSSource{}
	base.AcquisitionTargets = &[]readmodelapplication.AcquisitionTarget{}
	base.TargetObservations = &[]readmodelapplication.TargetObservation{}
	got, err = mapAnimeWorkspace(base)
	if err != nil {
		t.Fatalf("map empty workspace: %v", err)
	}
	if got.Releases == nil || got.RssSources == nil || got.AcquisitionTargets == nil || got.TargetObservations == nil || len(got.Releases) != 0 || len(*got.AcquisitionTargets) != 0 {
		t.Fatal("non-nil empty slices were not preserved")
	}
}

func TestMapRSSSourcePatchInputsAndResult(t *testing.T) {
	offset := 4
	offsetPatch := mapRSSSourceOffsetPatch(generated.RSSSourceOffsetPatch{
		ExpectedVersion: 7,
		EpisodeOffset:   offset,
	})
	if offsetPatch.ExpectedVersion != 7 || offsetPatch.EpisodeOffset != offset {
		t.Fatalf("mapped offset patch = %+v", offsetPatch)
	}

	mode := generated.RSSSourceAniBTModePatchAnibtAuxModeOverride
	modePatch := mapRSSSourceAniBTModePatch(generated.RSSSourceAniBTModePatch{
		ExpectedVersion: 8,
		AnibtAuxMode:    &mode,
	})
	if modePatch.ExpectedVersion != 8 || modePatch.AnibtAuxMode == nil || *modePatch.AnibtAuxMode != animedomain.RSSSourceDraftAnibtAuxModeOverride {
		t.Fatalf("mapped AniBT mode patch = %+v", modePatch)
	}

	provenance := &animedomain.ProviderProvenance{
		DiscoverySnapshotVersion: "snapshot-1",
		Provider:                 animedomain.ProviderKindAnibt,
		ProviderEntryRef:         "entry-1",
		ProviderGroupRef:         "group-1",
	}
	got := mapRSSSource(animeapplication.RSSSource{
		AnibtAuxMode:          modePatch.AnibtAuxMode,
		GroupName:             "Group",
		HttpFeedConnectionRef: candidateStringPointer("connection-1"),
		ID:                    "source-1",
		LocatorDisplay:        "https://example.test",
		NetworkRouteRef:       candidateStringPointer("route-1"),
		EpisodeOffset:         offset,
		ProviderProvenance:    provenance,
		SourceKind:            animedomain.SourceKindProviderGroup,
		SubscriptionID:        "subscription-1",
		Title:                 "Title",
		Version:               9,
	})
	if got.Id != "source-1" || got.SubscriptionId != "subscription-1" || got.Version != 9 ||
		got.SourceKind != generated.SourceKindProviderGroup || got.AnibtAuxMode == nil ||
		*got.AnibtAuxMode != generated.RSSSourceAnibtAuxModeOverride || got.EpisodeOffset != offset || got.ProviderProvenance == nil ||
		got.ProviderProvenance.Provider != generated.ProviderKindAnibt {
		t.Fatalf("mapped RSS source = %+v", got)
	}
}

func candidateIntPointer(value int) *int { return &value }
