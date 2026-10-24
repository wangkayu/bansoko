package animeworkspace

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/transport/httpapi/generated"
)

func TestMapAnimeWorkspacePreservesNilAndEmptyCollections(t *testing.T) {
	nilWorkspace, err := MapAnimeWorkspace(minimalWorkspace())
	if err != nil {
		t.Fatalf("map nil workspace: %v", err)
	}
	if nilWorkspace.AcquisitionTargets != nil || nilWorkspace.TargetObservations != nil {
		t.Fatal("nil top-level pointer-to-slice fields became non-nil")
	}
	if nilWorkspace.Artifacts != nil || nilWorkspace.DownloadTasks != nil || nilWorkspace.FileOperations != nil || nilWorkspace.Logs != nil || nilWorkspace.Releases != nil || nilWorkspace.RssSourceRuntimes != nil || nilWorkspace.RssSources != nil {
		t.Fatal("nil top-level slices became non-nil")
	}

	emptyWorkspace := minimalWorkspace()
	emptyTargets := []readmodelapplication.AcquisitionTarget{}
	emptyObservations := []readmodelapplication.TargetObservation{}
	emptyWorkspace.AcquisitionTargets = &emptyTargets
	emptyWorkspace.TargetObservations = &emptyObservations
	emptyWorkspace.Artifacts = []readmodelapplication.Artifact{}
	emptyWorkspace.DownloadTasks = []readmodelapplication.WorkspaceDownloadTask{}
	emptyWorkspace.FileOperations = []readmodelapplication.FileOperation{}
	emptyWorkspace.Logs = []readmodelapplication.SubscriptionLogEntry{}
	emptyWorkspace.Releases = []readmodelapplication.Release{}
	emptyWorkspace.RSSSourceRuntimes = []animeapplication.RSSSourceRuntime{}
	emptyWorkspace.RSSSources = []animeapplication.RSSSource{}

	mapped, err := MapAnimeWorkspace(emptyWorkspace)
	if err != nil {
		t.Fatalf("map empty workspace: %v", err)
	}
	if mapped.AcquisitionTargets == nil || len(*mapped.AcquisitionTargets) != 0 || mapped.TargetObservations == nil || len(*mapped.TargetObservations) != 0 {
		t.Fatal("non-nil empty top-level pointer-to-slice fields were not preserved")
	}
	if mapped.Artifacts == nil || mapped.DownloadTasks == nil || mapped.FileOperations == nil || mapped.Logs == nil || mapped.Releases == nil || mapped.RssSourceRuntimes == nil || mapped.RssSources == nil {
		t.Fatal("non-nil empty top-level slices were not preserved")
	}

	parsed := &readmodelapplication.ReleaseParsedFacts{Languages: &[]string{}}
	if got := mapWorkspaceParsedFacts(parsed); got == nil || got.Languages == nil || len(*got.Languages) != 0 {
		t.Fatal("non-nil empty parsed-facts languages were not preserved")
	}
	if got := mapWorkspaceParsedFacts(&readmodelapplication.ReleaseParsedFacts{}); got == nil || got.Languages != nil {
		t.Fatal("nil parsed-facts languages became non-nil")
	}

	emptyFiles := []readmodelapplication.ReleaseFile{}
	if got := mapWorkspaceReleaseFiles(&emptyFiles); got == nil || len(*got) != 0 {
		t.Fatal("non-nil empty release files were not preserved")
	}
	if got := mapWorkspaceReleaseFiles(nil); got != nil {
		t.Fatal("nil release files became non-nil")
	}

	emptyRules := []ownershipapplication.NameKeywordRule{}
	if got := mapWorkspaceNameKeywordRules(&emptyRules); got == nil || len(*got) != 0 {
		t.Fatal("non-nil empty name-keyword rules were not preserved")
	}
	if got := mapWorkspaceNameKeywordRules(nil); got != nil {
		t.Fatal("nil name-keyword rules became non-nil")
	}

	emptyNested := readmodelapplication.Release{
		AcquisitionTargets: &[]readmodelapplication.AcquisitionTarget{},
		ReleaseFiles:       &[]readmodelapplication.ReleaseFile{},
		TargetObservations: &[]readmodelapplication.TargetObservation{},
	}
	mappedNested := mapWorkspaceReleases([]readmodelapplication.Release{emptyNested})
	if mappedNested[0].AcquisitionTargets == nil || len(*mappedNested[0].AcquisitionTargets) != 0 || mappedNested[0].ReleaseFiles == nil || len(*mappedNested[0].ReleaseFiles) != 0 || mappedNested[0].TargetObservations == nil || len(*mappedNested[0].TargetObservations) != 0 {
		t.Fatal("non-nil empty release pointer-to-slice fields were not preserved")
	}
	mappedNilNested := mapWorkspaceReleases([]readmodelapplication.Release{{}})
	if mappedNilNested[0].AcquisitionTargets != nil || mappedNilNested[0].ReleaseFiles != nil || mappedNilNested[0].TargetObservations != nil {
		t.Fatal("nil release pointer-to-slice fields became non-nil")
	}
}

func TestMapAnimeWorkspaceProjectsRepresentativeOwners(t *testing.T) {
	titleOverride := "Custom title"
	totalEpisodes := 12
	bgmWorkID := "bgm-42"
	tmdbWorkID := "tmdb-42"
	metadata := animeapplication.AnimeMetadata{
		ManagedAnimeID: "anime-42",
		BGM:            &animeapplication.BGMBinding{WorkID: bgmWorkID},
		TMDB:           &animeapplication.TMDBBinding{WorkID: tmdbWorkID, MediaKind: "tv", Season: 2},
		TitleOverride:  &titleOverride,
		TotalEpisodes:  &totalEpisodes,
		Version:        4,
	}
	manualSelected := true
	provider := "mikan"
	provenance := &animedomain.ProviderProvenance{
		DiscoverySnapshotVersion: "snapshot-7",
		Provider:                 animedomain.ProviderKind(provider),
		ProviderEntryRef:         "entry-42",
		ProviderGroupRef:         "group-42",
	}
	groupLabel := "Group 42"
	offset := 1
	source := animeapplication.RSSSource{
		GroupName:          groupLabel,
		ID:                 "source-42",
		LocatorDisplay:     "https://feed.example/42",
		EpisodeOffset:      offset,
		ProviderProvenance: provenance,
		SourceKind:         animedomain.SourceKindProviderGroup,
		SubscriptionID:     "subscription-42",
		Title:              "Feed 42",
		Version:            3,
	}
	keywords := []ownershipapplication.NameKeywordRule{{Keyword: "中字", Score: 10}}
	defaultsGroups := []string{"Group 42"}
	nameKeywords := &ownershipapplication.ProviderNameKeywordRules{Mikan: &keywords}
	providerDefaults := &ownershipapplication.ProviderGroupDefaults{Mikan: &ownershipapplication.ProviderGroupDefault{Groups: defaultsGroups, Mode: "ranked", Score: 50}}
	selection := ownershipapplication.ProviderGroupSelection{
		CountInLimit: true, DisplayName: groupLabel, EffectiveScore: 88, EffectiveSelected: true,
		GroupRef: "group-42", ID: "selection-42", ManualSelected: &manualSelected,
		Provider: provider, SelectionMode: "manual", SubscriptionID: "subscription-42", Version: 5,
	}
	effectiveSubgroup := ownershipapplication.SubgroupScoreEntry{
		CountInLimit: true, DisplayName: groupLabel, EffectiveSelected: true, GroupRef: "group-42",
		ManualSelected: &manualSelected, Provider: provider, Score: 88, SelectionMode: "manual",
	}

	rules := validRulesOwner()
	rules.Effective.FileFilter.NotIn = "bad"
	rules.Effective.FileFilter.MustContain = `\.mkv$`
	rules.Effective.Parser.EpisodePatterns = []string{"E(?P<episode>\\d+)"}
	rules.Effective.ReleaseFilter.MustContain = "ja|zh"
	rules.Sections.Parser = json.RawMessage(`{"episode_patterns":["E(?P<episode>\\d+)"]}`)
	rules.Sections.ReleaseFilter = json.RawMessage(`{"must_contain":"ja|zh","not_in":"bad"}`)
	rules.Sections.FileFilter = json.RawMessage(`{"must_contain":"[.]mkv$","not_in":"bad"}`)
	rules.Sections.Naming = json.RawMessage(`{"language":"js-naming-1","movie":{"directory_template":"movie","video_filename_template":"movie-file"},"preset":"anime_default","subtitle_suffix_template":".zh","tv":{"directory_template":"tv","video_filename_template":"tv-file"}}`)
	rules.Sections.FileEffect = json.RawMessage(`{"collision_policy":"fail","operation":"copy"}`)

	episode := float32(3)
	parsedGroup := "[Group]"
	quality := "1080p"
	special := false
	languages := []string{"ja", "zh"}
	releaseDetail := "waiting for metadata"
	releaseFiles := []readmodelapplication.ReleaseFile{{
		Episode: &parsedGroup, Filename: "show.mkv", Language: &languages[1], MediaKind: "video",
		Path: "show.mkv", Resolution: &quality, Revision: &quality, SizeBytes: int64Pointer(1234),
	}}
	observedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	observedSize := int64(1234)
	target := readmodelapplication.AcquisitionTarget{CollisionPolicy: "fail", ExpectedSizeBytes: 1234, ID: "target-42", Intent: "primary", MediaKind: "video", ReleaseID: "release-42", SourcePath: "show.mkv", SpecDigest: "spec-42", TargetPath: "library/show.mkv", TargetRef: "target-ref-42", Version: 2}
	targetObservation := readmodelapplication.TargetObservation{ObservedAt: observedAt, ObservedSizeBytes: &observedSize, ReasonCode: stringPointer("MATCHED"), State: "present_matched", TargetID: target.ID, Version: 3}
	parsedFacts := &readmodelapplication.ReleaseParsedFacts{Episode: &episode, GroupName: &parsedGroup, Languages: &languages, Quality: &quality, Special: &special}
	release := readmodelapplication.Release{
		AcquisitionTargets: &[]readmodelapplication.AcquisitionTarget{target}, DownloadTaskID: stringPointer("download-42"),
		EvaluationVersion: stringPointer("eval-42"), ID: "release-42", ParsedFacts: parsedFacts,
		RawFacts:   readmodelapplication.RawReleaseFacts{Title: "[Group] Show - 03", EnclosureLength: int64Pointer(1234), EnclosureType: stringPointer("application/x-bittorrent"), PublishedAt: &observedAt},
		ReasonCode: "WAITING_METADATA", ReasonDetail: &releaseDetail, ReleaseFiles: &releaseFiles, RSSSourceID: source.ID,
		SourceFacts:   readmodelapplication.SourceFacts{GroupName: &groupLabel, IdentityKind: "provider_entry", IdentityValue: "entry-42"},
		StableItemKey: "stable-42", TargetObservations: &[]readmodelapplication.TargetObservation{targetObservation}, Verdict: "waiting_metadata", Version: 6,
	}
	cursor := &animeapplication.RSSCursor{Strategy: "published_at", Value: "cursor-42"}
	feedTitle := "Feed title"
	runtime := animeapplication.RSSSourceRuntime{Cursor: cursor, FailedWorkItemID: stringPointer("work-42"), Health: "ready", LastGoodFacts: &animeapplication.RSSLastGoodFacts{FeedTitle: &feedTitle, ItemCount: 4, NewestStableItemKey: "stable-41"}, LastRefreshAt: &observedAt, LastSuccessAt: &observedAt, ReasonCode: stringPointer("OK"), ReasonDetail: stringPointer("healthy"), RSSSourceID: source.ID, Version: 7}
	correlation := &readmodelapplication.WorkspaceProviderCorrelation{ExternalID: stringPointer("external-42"), InfoHash: stringPointer("hash-42"), StableTag: stringPointer("tag-42")}
	downloadTask := readmodelapplication.WorkspaceDownloadTask{FailedWorkItemID: stringPointer("work-43"), FailureCode: stringPointer("QB_TIMEOUT"), FailureReason: stringPointer("provider timeout"), FrozenInput: animeapplication.FrozenDownloadInput{ReleaseStableItemKey: "stable-42", SelectedURLDigest: stringPointer("url-digest-42"), SourceLocatorDisplay: source.LocatorDisplay}, FrozenPolicy: animeapplication.FrozenPolicySnapshot{EvaluatorVersion: stringPointer("eval-policy-42"), MetadataVersion: 8, RulesVersion: 9}, ID: "download-42", IdempotencyKey: "effect-42", ProviderCorrelation: correlation, ReleaseID: release.ID, State: "queued", Version: 10}
	subtitleBoundRef := "video-42"
	confidence := float32(0.95)
	method := "basename_match"
	diagnosticPath := "subtitle.ass"
	artifact := readmodelapplication.Artifact{DownloadTaskID: downloadTask.ID, FailedWorkItemID: stringPointer("work-44"), FailureCode: stringPointer("INSPECT_FAILED"), FailureReason: stringPointer("bad file"), Files: []readmodelapplication.ArtifactFile{{Fingerprint: readmodelapplication.Fingerprint{Algorithm: "sha256", SizeBytes: 1234, Value: "source-fp"}, MediaKind: "subtitle", Path: "subtitle.ass", Ref: "subtitle-42", SubtitleBinding: &readmodelapplication.ArtifactFileSubtitleBinding{BoundVideoFileRef: &subtitleBoundRef, Confidence: &confidence, Diagnostics: []readmodelapplication.NamingDiagnostic{{Code: "MATCH", Message: "matched", Path: &diagnosticPath}}, Method: &method, State: "bound"}}}, Fingerprints: []readmodelapplication.Fingerprint{{Algorithm: "sha256", SizeBytes: 1234, Value: "artifact-fp"}}, ID: "artifact-42", State: "verified", Version: 11}
	intent := "replace"
	targetFingerprint := readmodelapplication.Fingerprint{Algorithm: "sha256", SizeBytes: 1234, Value: "target-fp"}
	fileOperation := readmodelapplication.FileOperation{ArtifactFileRef: "video-42", ArtifactID: artifact.ID, CollisionPolicy: "fail", ExpectedSizeBytes: int64Pointer(1234), FailedWorkItemID: stringPointer("work-45"), FailureCode: stringPointer("COPY_FAILED"), FailureReason: stringPointer("permission denied"), FrozenConfigSnapshot: readmodelapplication.FrozenFileConfigSnapshot{RulesVersion: 9, SafetyVersion: "safe-42"}, FrozenRenderSnapshot: readmodelapplication.FrozenRenderSnapshot{Directory: "Show", Filename: "Show - 03.mkv", RendererVersion: "renderer-42"}, ID: "operation-42", IdempotencyKey: "file-effect-42", Intent: &intent, ObservedAt: &observedAt, ObservedTargetSizeBytes: int64Pointer(1234), Operation: "copy", SourceFingerprint: readmodelapplication.Fingerprint{Algorithm: "sha256", SizeBytes: 1234, Value: "source-fp"}, SourcePath: "downloads/show.mkv", State: "completed", TargetFingerprint: &targetFingerprint, TargetPath: "library/Show/Show - 03.mkv", Version: 12}
	logDetails := &readmodelapplication.SubscriptionLogDetails{ErrorCount: intPointer(1), ItemCount: intPointer(4), ReasonCode: stringPointer("REFRESHED"), ReasonDetail: stringPointer("details")}
	log := readmodelapplication.SubscriptionLogEntry{DedupeKey: "log-42", Details: logDetails, GroupRef: stringPointer("group-42"), ID: "log-entry-42", Kind: "rss_refresh", ManagedAnimeID: "anime-42", OccurredAt: observedAt, Outcome: "success", ReleaseID: stringPointer(release.ID), RetentionUntil: observedAt.Add(24 * time.Hour), RSSSourceID: stringPointer(source.ID), SubscriptionID: "subscription-42", Summary: "refresh complete", Title: "Show"}

	workspace := readmodelapplication.AnimeWorkspace{
		AcquisitionTargets: &[]readmodelapplication.AcquisitionTarget{target}, Artifacts: []readmodelapplication.Artifact{artifact}, DownloadTasks: []readmodelapplication.WorkspaceDownloadTask{downloadTask},
		EpisodeProgress: &readmodelapplication.EpisodeProgress{Done: false, Episodes: []int{1, 2, 3}, Total: &totalEpisodes}, FileOperations: []readmodelapplication.FileOperation{fileOperation}, Logs: []readmodelapplication.SubscriptionLogEntry{log},
		ManagedAnime: animeapplication.ManagedAnime{ID: "anime-42", Title: "Original title", TitleOverride: &titleOverride, Version: 2}, Metadata: metadata, MetadataHold: animeapplication.MetadataHold{Held: false, Missing: []string{"tmdb"}}, Releases: []readmodelapplication.Release{release},
		RSSSourceRuntimes: []animeapplication.RSSSourceRuntime{runtime}, RSSSources: []animeapplication.RSSSource{source}, Rules: rules,
		Subgroups:    ownershipapplication.SubgroupsOwnerResult{EffectiveSelection: []ownershipapplication.ProviderGroupSelection{selection}, ManagedAnimeID: "anime-42", RSSSources: []animeapplication.RSSSource{source}, Subgroups: ownershipapplication.SubgroupsSection{Entries: []ownershipapplication.SubgroupScoreEntry{effectiveSubgroup}, MaxRss: 3, NameKeywords: nameKeywords, ProviderDefaults: providerDefaults}, Version: 13},
		Subscription: animeapplication.Subscription{ID: "subscription-42", ManagedAnimeID: "anime-42", Origin: provider, OriginEntryRef: stringPointer("entry-42"), InstanceLabel: stringPointer("instance-42"), State: "active", Version: 14}, TargetObservations: &[]readmodelapplication.TargetObservation{targetObservation},
	}

	mapped, err := MapAnimeWorkspace(workspace)
	if err != nil {
		t.Fatalf("map representative workspace: %v", err)
	}
	if mapped.ManagedAnime.TitleOverride == nil || *mapped.ManagedAnime.TitleOverride != titleOverride || mapped.Metadata.Bgm == nil || mapped.Metadata.Bgm.WorkId != bgmWorkID || mapped.Metadata.Tmdb == nil || mapped.Metadata.Tmdb.WorkId != tmdbWorkID || mapped.Metadata.Tmdb.MediaKind != generated.TMDBMediaKind("tv") {
		t.Fatalf("managed anime/metadata projection = %#v / %#v", mapped.ManagedAnime, mapped.Metadata)
	}
	if len(mapped.Releases) != 1 || mapped.Releases[0].ParsedFacts == nil || mapped.Releases[0].ParsedFacts.Languages == nil || mapped.Releases[0].ReleaseFiles == nil || len(*mapped.Releases[0].ReleaseFiles) != 1 || mapped.Releases[0].TargetObservations == nil || len(*mapped.Releases[0].TargetObservations) != 1 {
		t.Fatalf("release nested projection = %#v", mapped.Releases)
	}
	if mapped.RssSourceRuntimes[0].Cursor == nil || mapped.RssSourceRuntimes[0].Cursor.Strategy != generated.RSSCursorStrategy("published_at") || mapped.RssSourceRuntimes[0].LastGoodFacts == nil || mapped.RssSourceRuntimes[0].LastGoodFacts.FeedTitle == nil || mapped.RssSources[0].EpisodeOffset != offset || mapped.RssSources[0].ProviderProvenance == nil || mapped.RssSources[0].ProviderProvenance.Provider != generated.ProviderKind("mikan") || mapped.RssSources[0].ProviderProvenance.ProviderGroupRef != "group-42" {
		t.Fatalf("runtime/source projection = %#v / %#v", mapped.RssSourceRuntimes, mapped.RssSources)
	}
	if mapped.Artifacts[0].Files[0].SubtitleBinding == nil || mapped.Artifacts[0].Files[0].SubtitleBinding.State != generated.ArtifactFileSubtitleBindingState("bound") || mapped.Artifacts[0].Files[0].SubtitleBinding.Diagnostics[0].Path == nil || mapped.Artifacts[0].Fingerprints[0].Value != "artifact-fp" || mapped.FileOperations[0].TargetFingerprint == nil || mapped.FileOperations[0].TargetFingerprint.Algorithm != generated.FingerprintAlgorithm("sha256") || mapped.FileOperations[0].SourceFingerprint.Value != "source-fp" || mapped.Logs[0].Details == nil {
		t.Fatal("artifact/file-operation/log nested projection missing")
	}
	if mapped.Rules.Effective.FileFilter.NotIn != "bad" {
		t.Fatalf("rules effective projection = %#v", mapped.Rules.Effective)
	}
	sectionsJSON, err := json.Marshal(mapped.Rules.Sections)
	if err != nil {
		t.Fatalf("marshal mapped rules sections: %v", err)
	}
	if string(sectionsJSON) == "" || !strings.Contains(string(sectionsJSON), `"episode_patterns"`) {
		t.Fatalf("rules sections projection = %s", sectionsJSON)
	}
	if len(mapped.Subgroups.EffectiveSelection) != 1 || mapped.Subgroups.Subgroups.NameKeywords == nil || mapped.Subgroups.Subgroups.NameKeywords.Mikan == nil || mapped.Subgroups.Subgroups.ProviderDefaults == nil || mapped.Subgroups.Subgroups.ProviderDefaults.Mikan == nil || mapped.Subgroups.Subgroups.ProviderDefaults.Mikan.Groups[0] != groupLabel {
		t.Fatalf("subgroups projection = %#v", mapped.Subgroups)
	}
	if mapped.DownloadTasks[0].ProviderCorrelation == nil || mapped.DownloadTasks[0].ProviderCorrelation.InfoHash == nil || *mapped.DownloadTasks[0].ProviderCorrelation.InfoHash != "hash-42" {
		t.Fatalf("download provider correlation = %#v", mapped.DownloadTasks[0])
	}
	if mapped.Releases[0].AcquisitionTargets == nil || (*mapped.Releases[0].AcquisitionTargets)[0].TargetPath != "library/show.mkv" || mapped.Releases[0].TargetObservations == nil || (*mapped.Releases[0].TargetObservations)[0].ObservedSizeBytes == nil || *(*mapped.Releases[0].TargetObservations)[0].ObservedSizeBytes != 1234 {
		t.Fatalf("release targets/observations = %#v", mapped.Releases[0])
	}
}

func TestMapAnimeWorkspaceDoesNotAliasNestedApplicationValues(t *testing.T) {
	workspace := representativeWorkspaceForIsolation()
	mapped, err := MapAnimeWorkspace(workspace)
	if err != nil {
		t.Fatalf("map workspace: %v", err)
	}

	if mapped.ManagedAnime.TitleOverride == workspace.ManagedAnime.TitleOverride || mapped.Metadata.TitleOverride == workspace.Metadata.TitleOverride || mapped.Metadata.Bgm == nil || mapped.Metadata.Tmdb == nil || pointerAddress(mapped.Metadata.Bgm) == pointerAddress(workspace.Metadata.BGM) || pointerAddress(mapped.Metadata.Tmdb) == pointerAddress(workspace.Metadata.TMDB) {
		t.Fatal("managed anime or metadata pointers alias application values")
	}
	if mapped.EpisodeProgress == nil || mapped.EpisodeProgress.Total == workspace.EpisodeProgress.Total || mapped.EpisodeProgress.Episodes == nil || &mapped.EpisodeProgress.Episodes[0] == &workspace.EpisodeProgress.Episodes[0] {
		t.Fatal("episode progress aliases application values")
	}
	if mapped.Releases[0].ParsedFacts == nil || pointerAddress(mapped.Releases[0].ParsedFacts) == pointerAddress(workspace.Releases[0].ParsedFacts) || pointerAddress(mapped.Releases[0].ParsedFacts.Languages) == pointerAddress(workspace.Releases[0].ParsedFacts.Languages) || pointerAddress(mapped.Releases[0].ReleaseFiles) == pointerAddress(workspace.Releases[0].ReleaseFiles) || pointerAddress(mapped.Releases[0].AcquisitionTargets) == pointerAddress(workspace.Releases[0].AcquisitionTargets) || pointerAddress(mapped.Releases[0].TargetObservations) == pointerAddress(workspace.Releases[0].TargetObservations) {
		t.Fatal("release nested pointers alias application values")
	}
	if pointerAddress(mapped.RssSourceRuntimes[0].Cursor) == pointerAddress(workspace.RSSSourceRuntimes[0].Cursor) || pointerAddress(mapped.RssSourceRuntimes[0].LastGoodFacts) == pointerAddress(workspace.RSSSourceRuntimes[0].LastGoodFacts) || pointerAddress(mapped.RssSourceRuntimes[0].LastGoodFacts.FeedTitle) == pointerAddress(workspace.RSSSourceRuntimes[0].LastGoodFacts.FeedTitle) {
		t.Fatal("runtime nested pointers alias application values")
	}
	if pointersAlias(mapped.Artifacts[0].Files[0].SubtitleBinding, workspace.Artifacts[0].Files[0].SubtitleBinding) || pointersAlias(mapped.Artifacts[0].Files[0].SubtitleBinding.BoundVideoFileRef, workspace.Artifacts[0].Files[0].SubtitleBinding.BoundVideoFileRef) || pointersAlias(mapped.Artifacts[0].Files[0].SubtitleBinding.Confidence, workspace.Artifacts[0].Files[0].SubtitleBinding.Confidence) || pointersAlias(mapped.Artifacts[0].Files[0].SubtitleBinding.Method, workspace.Artifacts[0].Files[0].SubtitleBinding.Method) || pointersAlias(mapped.Artifacts[0].Files[0].SubtitleBinding.Diagnostics, workspace.Artifacts[0].Files[0].SubtitleBinding.Diagnostics) || pointersAlias(mapped.Artifacts[0].Files[0].SubtitleBinding.Diagnostics[0].Path, workspace.Artifacts[0].Files[0].SubtitleBinding.Diagnostics[0].Path) {
		t.Fatal("artifact subtitle binding aliases application values")
	}
	if pointerAddress(mapped.FileOperations[0].TargetFingerprint) == pointerAddress(workspace.FileOperations[0].TargetFingerprint) || pointerAddress(mapped.FileOperations[0].ExpectedSizeBytes) == pointerAddress(workspace.FileOperations[0].ExpectedSizeBytes) || pointerAddress(mapped.FileOperations[0].ObservedAt) == pointerAddress(workspace.FileOperations[0].ObservedAt) || pointerAddress(mapped.Logs[0].Details) == pointerAddress(workspace.Logs[0].Details) || pointerAddress(mapped.Logs[0].Details.ErrorCount) == pointerAddress(workspace.Logs[0].Details.ErrorCount) {
		t.Fatal("file-operation or log pointers alias application values")
	}

	*workspace.ManagedAnime.TitleOverride = "changed"
	*workspace.Metadata.TitleOverride = "changed"
	workspace.Metadata.BGM.WorkID = "changed"
	workspace.Metadata.TMDB.WorkID = "changed"
	workspace.EpisodeProgress.Episodes[0] = 99
	*workspace.Releases[0].ParsedFacts.Languages = []string{"changed"}
	(*workspace.Releases[0].ReleaseFiles)[0].Filename = "changed"
	(*workspace.Releases[0].AcquisitionTargets)[0].TargetPath = "changed"
	workspace.RSSSourceRuntimes[0].Cursor.Value = "changed"
	*workspace.RSSSourceRuntimes[0].LastGoodFacts.FeedTitle = "changed"
	workspace.Artifacts[0].Files[0].SubtitleBinding.Diagnostics[0].Path = stringPointer("changed")
	*workspace.Artifacts[0].Files[0].SubtitleBinding.BoundVideoFileRef = "changed"
	*workspace.Artifacts[0].Files[0].SubtitleBinding.Confidence = 0
	*workspace.Artifacts[0].Files[0].SubtitleBinding.Method = "changed"
	*workspace.FileOperations[0].TargetFingerprint = readmodelapplication.Fingerprint{Value: "changed"}
	*workspace.Logs[0].Details.ErrorCount = 99
	*workspace.Logs[0].Details.ItemCount = 99
	*workspace.Logs[0].Details.ReasonCode = "changed"
	*workspace.Logs[0].Details.ReasonDetail = "changed"

	if *mapped.ManagedAnime.TitleOverride != "Custom title" || *mapped.Metadata.TitleOverride != "Custom title" || mapped.Metadata.Bgm.WorkId != "bgm" || mapped.Metadata.Tmdb.WorkId != "tmdb" || mapped.EpisodeProgress.Episodes[0] != 1 || (*mapped.Releases[0].ParsedFacts.Languages)[0] != "ja" || (*mapped.Releases[0].ReleaseFiles)[0].Filename != "show.mkv" || (*mapped.Releases[0].AcquisitionTargets)[0].TargetPath != "library/show.mkv" || mapped.RssSourceRuntimes[0].Cursor.Value != "cursor-42" || *mapped.RssSourceRuntimes[0].LastGoodFacts.FeedTitle != "Feed title" || *mapped.Artifacts[0].Files[0].SubtitleBinding.Diagnostics[0].Path != "subtitle.ass" || *mapped.Artifacts[0].Files[0].SubtitleBinding.BoundVideoFileRef != "video-42" || *mapped.Artifacts[0].Files[0].SubtitleBinding.Confidence != 0.95 || *mapped.Artifacts[0].Files[0].SubtitleBinding.Method != "basename_match" || mapped.FileOperations[0].TargetFingerprint.Value != "target-fp" || *mapped.Logs[0].Details.ErrorCount != 1 || *mapped.Logs[0].Details.ItemCount != 4 || *mapped.Logs[0].Details.ReasonCode != "REFRESHED" || *mapped.Logs[0].Details.ReasonDetail != "details" {
		t.Fatal("mapped workspace changed after application nested values were mutated")
	}
}

func minimalWorkspace() readmodelapplication.AnimeWorkspace {
	return readmodelapplication.AnimeWorkspace{ManagedAnime: animeapplication.ManagedAnime{ID: "anime-1"}, Rules: validRulesOwner()}
}

func validRulesOwner() ownershipapplication.RulesOwner {
	return ownershipapplication.RulesOwner{
		ManagedAnimeID: "anime-1",
		Sections:       animeDomainRulesSections(),
		Effective: animedomain.EffectiveRules{
			FileEffect:    animedomain.FileEffectRules{Operation: "copy", CollisionPolicy: "fail"},
			FileFilter:    animedomain.FileFilterRules{},
			Naming:        animedomain.NamingRules{Language: "js-naming-1", Movie: animedomain.NamingVariant{}, Tv: animedomain.NamingVariant{}},
			Parser:        animedomain.ParserRules{EpisodePatterns: []string{}},
			ReleaseFilter: animedomain.ReleaseFilterRules{},
		},
		Version: 1,
	}
}

func animeDomainRulesSections() animedomain.RulesSections {
	return animedomain.RulesSections{
		FileEffect:    json.RawMessage(`{"collision_policy":"fail","operation":"copy"}`),
		FileFilter:    json.RawMessage(`{"must_contain":"","not_in":""}`),
		Naming:        json.RawMessage(`{"language":"js-naming-1","movie":{"directory_template":"","video_filename_template":""},"subtitle_suffix_template":"","tv":{"directory_template":"","video_filename_template":""}}`),
		Parser:        json.RawMessage(`{"episode_patterns":[]}`),
		ReleaseFilter: json.RawMessage(`{"must_contain":"","not_in":""}`),
	}
}

func representativeWorkspaceForIsolation() readmodelapplication.AnimeWorkspace {
	workspace := minimalWorkspace()
	title := "Custom title"
	workspace.ManagedAnime = animeapplication.ManagedAnime{ID: "anime-42", Title: "Original", TitleOverride: &title}
	workspace.Metadata = animeapplication.AnimeMetadata{ManagedAnimeID: "anime-42", TitleOverride: &title, BGM: &animeapplication.BGMBinding{WorkID: "bgm"}, TMDB: &animeapplication.TMDBBinding{WorkID: "tmdb", MediaKind: "tv"}}
	total := 12
	workspace.EpisodeProgress = &readmodelapplication.EpisodeProgress{Episodes: []int{1, 2}, Total: &total}
	languages := []string{"ja"}
	files := []readmodelapplication.ReleaseFile{{Filename: "show.mkv", Language: stringPointer("ja")}}
	targets := []readmodelapplication.AcquisitionTarget{{TargetPath: "library/show.mkv"}}
	observations := []readmodelapplication.TargetObservation{{ObservedSizeBytes: int64Pointer(1)}}
	workspace.Releases = []readmodelapplication.Release{{ParsedFacts: &readmodelapplication.ReleaseParsedFacts{Languages: &languages}, ReleaseFiles: &files, AcquisitionTargets: &targets, TargetObservations: &observations}}
	feedTitle := "Feed title"
	workspace.RSSSourceRuntimes = []animeapplication.RSSSourceRuntime{{Cursor: &animeapplication.RSSCursor{Value: "cursor-42"}, LastGoodFacts: &animeapplication.RSSLastGoodFacts{FeedTitle: &feedTitle}}}
	path := "subtitle.ass"
	boundVideoRef := "video-42"
	confidence := float32(0.95)
	method := "basename_match"
	workspace.Artifacts = []readmodelapplication.Artifact{{Files: []readmodelapplication.ArtifactFile{{SubtitleBinding: &readmodelapplication.ArtifactFileSubtitleBinding{BoundVideoFileRef: &boundVideoRef, Confidence: &confidence, Diagnostics: []readmodelapplication.NamingDiagnostic{{Path: &path}}, Method: &method, State: "bound"}}}}}
	targetFingerprint := readmodelapplication.Fingerprint{Value: "target-fp"}
	workspace.FileOperations = []readmodelapplication.FileOperation{{ExpectedSizeBytes: int64Pointer(1), ObservedAt: timePointer(time.Unix(1, 0)), TargetFingerprint: &targetFingerprint}}
	errorCount := 1
	itemCount := 4
	reasonCode := "REFRESHED"
	reasonDetail := "details"
	workspace.Logs = []readmodelapplication.SubscriptionLogEntry{{Details: &readmodelapplication.SubscriptionLogDetails{ErrorCount: &errorCount, ItemCount: &itemCount, ReasonCode: &reasonCode, ReasonDetail: &reasonDetail}}}
	return workspace
}

func pointersAlias(left, right any) bool {
	leftValue := reflect.ValueOf(left)
	rightValue := reflect.ValueOf(right)
	if !leftValue.IsValid() || !rightValue.IsValid() {
		return false
	}
	if leftValue.Kind() == reflect.Slice {
		if rightValue.Kind() != reflect.Slice || leftValue.IsNil() || rightValue.IsNil() || leftValue.Len() == 0 || rightValue.Len() == 0 {
			return false
		}
	} else if leftValue.Kind() == reflect.Ptr {
		if rightValue.Kind() != reflect.Ptr || leftValue.IsNil() || rightValue.IsNil() {
			return false
		}
	} else {
		return false
	}
	return leftValue.Pointer() == rightValue.Pointer()
}

func pointerAddress(value any) uintptr {
	return reflect.ValueOf(value).Pointer()
}

// Activity lineage persisted with a record is owner-first truth: the read
// projection must carry reason/subject/correlation/causation through to the
// generated contract instead of dropping them. Nothing here may be derived.
func TestMapWorkspaceLogsPreservesActivityLineage(t *testing.T) {
	reasonCode := "RELEASE_FILTERED"
	subjectType, subjectID := "release", "release-7"
	subjectVersion := int64(3)
	correlationID := "corr-1"
	causationType, causationID := "rss_refresh", "work-9"
	occurredAt := time.Unix(1_700_000_000, 0).UTC()
	logs := mapWorkspaceLogs([]readmodelapplication.SubscriptionLogEntry{{
		CausationID:    &causationID,
		CausationType:  &causationType,
		CorrelationID:  &correlationID,
		DedupeKey:      "dedupe-1",
		ID:             "log-1",
		Kind:           "release_processed",
		ManagedAnimeID: "anime-1",
		OccurredAt:     occurredAt,
		Outcome:        "success",
		ReasonCode:     &reasonCode,
		RetentionUntil: occurredAt.Add(24 * time.Hour),
		SubjectID:      &subjectID,
		SubjectType:    &subjectType,
		SubjectVersion: &subjectVersion,
		SubscriptionID: "sub-1",
		Summary:        "processed",
		Title:          "Show",
	}})
	if len(logs) != 1 {
		t.Fatalf("expected one mapped log, got %d", len(logs))
	}
	got := logs[0]
	checks := []struct {
		name string
		got  *string
		want string
	}{
		{"reason_code", got.ReasonCode, reasonCode},
		{"subject_type", got.SubjectType, subjectType},
		{"subject_id", got.SubjectId, subjectID},
		{"correlation_id", got.CorrelationId, correlationID},
		{"causation_type", got.CausationType, causationType},
		{"causation_id", got.CausationId, causationID},
	}
	for _, check := range checks {
		if check.got == nil || *check.got != check.want {
			t.Fatalf("%s lineage was dropped or altered: %v", check.name, check.got)
		}
	}
	if got.SubjectVersion == nil || *got.SubjectVersion != subjectVersion {
		t.Fatalf("subject_version lineage was dropped or altered: %v", got.SubjectVersion)
	}
}

func TestMapAnimeWorkspaceKeepsRequiredMetadataMissingArray(t *testing.T) {
	// Regression: an empty hold reached the workspace response as "missing": null
	// and the config dialog's hold.missing.join(...) crashed the whole app.
	workspace := minimalWorkspace()
	workspace.MetadataHold = animeapplication.MetadataHold{}
	mapped, err := MapAnimeWorkspace(workspace)
	if err != nil {
		t.Fatalf("map workspace: %v", err)
	}
	if mapped.MetadataHold.Missing == nil {
		t.Fatal("empty hold mapped to nil; schema requires a non-nullable array")
	}
	body, err := json.Marshal(mapped.MetadataHold)
	if err != nil {
		t.Fatalf("marshal mapped hold: %v", err)
	}
	if string(body) != `{"held":false,"missing":[]}` {
		t.Fatalf("metadata hold wire shape = %s, want an empty array", body)
	}

	workspace.MetadataHold = animeapplication.MetadataHold{Held: true, Missing: []string{"tmdb"}}
	mapped, err = MapAnimeWorkspace(workspace)
	if err != nil {
		t.Fatalf("map populated workspace: %v", err)
	}
	if len(mapped.MetadataHold.Missing) != 1 || mapped.MetadataHold.Missing[0] != generated.MetadataHoldMissingTmdb {
		t.Fatalf("missing = %#v, want tmdb", mapped.MetadataHold.Missing)
	}
}

func stringPointer(value string) *string     { return &value }
func intPointer(value int) *int              { return &value }
func int64Pointer(value int64) *int64        { return &value }
func timePointer(value time.Time) *time.Time { return &value }

var _ generated.AnimeWorkspace
