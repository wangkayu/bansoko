package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	animecatalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func rssListViewFixture() readmodelapplication.RSSListView {
	published := time.Date(2026, 7, 29, 16, 30, 0, 0, time.UTC)
	size := int64(1261)
	manifest := &animecatalogdomain.TorrentManifest{
		PayloadDigest: "sha256:" + repeatHex("a", 64), ParserVersion: "torrent-parser-v1",
		TorrentVersion: "v1", TorrentName: "Preview Show S1", InfoHashV1: repeatHex("b", 40),
		TotalSizeBytes: 1254, ProjectionDigest: "sha256:" + repeatHex("c", 64),
		Files: []animecatalogdomain.TorrentFile{
			{FileRef: "sha256:" + repeatHex("1", 64), Ordinal: 0, SourcePath: "Preview Show S1/ep1.mkv", Filename: "ep1.mkv", SizeBytes: 1234, MediaKind: "video"},
			{FileRef: "sha256:" + repeatHex("2", 64), Ordinal: 1, SourcePath: "Preview Show S1/Subs/ep1.zh-Hans.ass", Filename: "ep1.zh-Hans.ass", SizeBytes: 20, MediaKind: "subtitle", Language: "zh-Hans"},
			{FileRef: "sha256:" + repeatHex("3", 64), Ordinal: 2, SourcePath: "Preview Show S1/cover.jpg", Filename: "cover.jpg", SizeBytes: 0, MediaKind: "other"},
		},
	}
	videoSize, subtitleSize := int64(1234), int64(20)
	videoKind, subtitleKind := "video", "subtitle"
	return readmodelapplication.RSSListView{
		Mode: "candidate_revision", ConfigIdentity: "cand-1#2",
		MetadataHold: animedomain.MetadataHold{Held: false, Missing: []animedomain.MetadataHoldMissing{}},
		Versions: readmodelapplication.RSSListViewVersions{
			Parser: "parser-v1", Evaluator: "evaluator-v1", Renderer: "1", Safety: "path-safety-v1", TorrentParser: "torrent-parser-v1",
		},
		GeneratedAt: time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC),
		Groups: []readmodelapplication.RSSListGroup{{
			Scope:       readmodelapplication.RSSListGroupScope{Provider: "anibt", ProviderEntryRef: "1001", ProviderGroupRef: "12"},
			DisplayName: "Group 12", Score: 80,
			Selection: readmodelapplication.RSSListGroupSelection{EffectiveSelected: true, SelectionMode: "auto", CountInLimit: true},
			Offset:    readmodelapplication.RSSListOffset{Value: 1, Editable: true, Source: readmodelapplication.RSSListOffsetSourceLink},
			Feed: readmodelapplication.RSSListFeedStatus{
				State: readmodelapplication.RSSListFeedFresh, SnapshotID: "snap-12", SnapshotVersion: 2,
				ContentHash: "sha256:" + repeatHex("d", 64),
			},
			Items: []readmodelapplication.RSSListItem{{
				SnapshotID: "snap-12", StableSeedKey: "seed-01",
				Seed: readmodelapplication.RSSListSeedFacts{
					Title: "[Group] Preview Show - 01", PublishedAt: &published, SizeBytes: &size,
					InfoHash: repeatHex("b", 40), LocatorDisplay: "https://provider.example.test/show-01.torrent",
				},
				Torrent: readmodelapplication.RSSListTorrentState{
					State: animecatalogdomain.TorrentAcquisitionAvailable, Manifest: manifest,
				},
				Evaluation: readmodelapplication.RSSListEvaluation{
					State: readmodelapplication.RSSListEvaluationEvaluated, Verdict: "accepted_pending_download",
					ReasonCode: "accepted", ReasonDetail: "matched the release filter",
				},
				Files: []readmodelapplication.RSSListFile{
					{
						FileRef: "sha256:" + repeatHex("e", 64), Ordinal: 0,
						SourcePath: "Preview Show S1/ep1.mkv", Filename: "ep1.mkv", SizeBytes: &videoSize,
						MediaKind: &videoKind, Included: true, Safety: "valid", RenderedTarget: "Preview Show/Season 01/Preview Show - E01.mkv",
					},
					{
						FileRef: "sha256:" + repeatHex("f", 64), Ordinal: 1,
						SourcePath: "Preview Show S1/Subs/ep1.zh-Hans.ass", Filename: "ep1.zh-Hans.ass",
						SizeBytes: &subtitleSize, MediaKind: &subtitleKind, Language: "zh-Hans",
						Included: false, ReasonCode: "file_extension_not_included", Safety: "valid",
					},
					{
						FileRef: "sha256:" + repeatHex("9", 64), Ordinal: 2,
						SourcePath: "Preview Show S1/unknown.bin", Filename: "unknown.bin",
						Included: false, ReasonCode: "ignored_kind", Safety: "valid",
					},
				},
				DownloadAction: readmodelapplication.RSSListDownloadAction{Allowed: true, Kind: readmodelapplication.RSSListDownloadOneShot},
				LibraryOverlap: readmodelapplication.LibraryOverlap{
					Verdict: readmodelapplication.RSSListLibraryOverlapAbsent, PresentFiles: libraryOverlapCount(0), PendingFiles: libraryOverlapCount(1),
				},
			}},
		}},
	}
}

func repeatHex(value string, count int) string {
	result := make([]byte, 0, count)
	for i := 0; i < count; i++ {
		result = append(result, value...)
	}
	return string(result)
}

func TestMapRSSListReadInputRejectsMixedInputSources(t *testing.T) {
	candidate := generated.ID("cand-1")
	revision := generated.Version(2)
	currentInput := generated.RSSListViewInputInputSource("current_input")
	candidateRevision := generated.RSSListViewInputInputSource("candidate_revision")

	cases := []struct {
		name  string
		input generated.RSSListViewInput
		ok    bool
	}{
		{name: "current input without draft", input: generated.RSSListViewInput{InputSource: currentInput}},
		{name: "current input with candidate id but no revision", input: generated.RSSListViewInput{InputSource: currentInput, Draft: &generated.AnimeDraft{}, CandidateId: &candidate}},
		{name: "candidate without revision", input: generated.RSSListViewInput{InputSource: candidateRevision, CandidateId: &candidate}},
		{name: "candidate without candidate id", input: generated.RSSListViewInput{InputSource: candidateRevision, Revision: &revision}},
		{name: "candidate with draft", input: generated.RSSListViewInput{InputSource: candidateRevision, CandidateId: &candidate, Revision: &revision, Draft: &generated.AnimeDraft{}}},
		{name: "unknown source", input: generated.RSSListViewInput{InputSource: generated.RSSListViewInputInputSource("latest")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := mapRSSListReadInput(tc.input); err == nil {
				t.Fatalf("input %s was accepted", tc.name)
			}
		})
	}
}

func TestMapRSSListViewSerializesTheCompleteHierarchy(t *testing.T) {
	mapped, err := mapRSSListView(rssListViewFixture())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(mapped)
	if err != nil {
		t.Fatal(err)
	}
	// The wire body must round-trip into the generated type: additionalProperties
	// is false, so an extra or missing field is a contract break.
	var decoded generated.RSSListView
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("mapped view does not round-trip: %v", err)
	}
	if decoded.ConfigIdentity != "cand-1#2" || decoded.Mode != generated.RSSListViewModeCandidateRevision {
		t.Fatalf("view identity drifted: %+v", decoded)
	}
	if len(decoded.Groups) != 1 || len(decoded.Groups[0].Items) != 1 {
		t.Fatalf("hierarchy did not survive the wire: %+v", decoded.Groups)
	}
	group := decoded.Groups[0]
	if group.Scope.Provider != "anibt" || group.Scope.ProviderGroupRef != "12" {
		t.Fatalf("group scope drifted: %+v", group.Scope)
	}
	if group.Score == nil || *group.Score != 80 {
		t.Fatalf("group score drifted: %+v", group.Score)
	}
	// The read model's offset editability is carried verbatim; a hard-coded
	// value here is what kept the owner's offset button disabled.
	if group.Offset.Value != 1 || !group.Offset.Editable {
		t.Fatalf("offset editability drifted: %+v", group.Offset)
	}
	// The offset provenance reaches the wire, so a reader can tell a stored
	// offset from a preview-only shift.
	if group.Offset.Source != generated.RSSListOffsetSourceLink {
		t.Fatalf("offset provenance drifted: %+v", group.Offset)
	}
	item := group.Items[0]
	if item.Torrent.Manifest == nil || item.Torrent.Manifest.FileCount != 3 {
		t.Fatalf("manifest did not survive the wire: %+v", item.Torrent.Manifest)
	}
	if len(item.Files) != 3 {
		t.Fatalf("files did not survive the wire: %+v", item.Files)
	}
	if item.Files[0].RenderedTarget == nil || *item.Files[0].RenderedTarget == "" {
		t.Fatalf("rendered target was dropped: %+v", item.Files[0])
	}
	if item.Files[0].MediaKind == nil || *item.Files[0].MediaKind != generated.RSSListFileMediaKindVideo {
		t.Fatalf("a manifest file's media kind was dropped: %+v", item.Files[0])
	}
	if item.Files[0].SizeBytes == nil || *item.Files[0].SizeBytes != 1234 {
		t.Fatalf("a manifest file's size was dropped: %+v", item.Files[0])
	}
	if item.Files[1].ReasonCode == nil || *item.Files[1].ReasonCode != "file_extension_not_included" {
		t.Fatalf("excluded file lost its reason: %+v", item.Files[1])
	}
	// The file the manifest never declared is unknown, and the wire says so by
	// withholding the keys: neither a fake 0 nor an empty enum value is sent.
	for index := 2; index < len(item.Files); index++ {
		if item.Files[index].SizeBytes != nil || item.Files[index].MediaKind != nil {
			t.Fatalf("an unknown file must withhold size and media kind: %+v", item.Files[index])
		}
	}
	if !item.DownloadAction.Allowed || item.DownloadAction.Kind != generated.RSSListDownloadActionKindOneShot {
		t.Fatalf("download action drifted: %+v", item.DownloadAction)
	}
	// The redacted locator display is what the wire carries; the raw locator is
	// not part of this shape at all.
	if item.Seed.LocatorDisplay == "" {
		t.Fatal("locator display is missing")
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if _, leaked := raw["download_url"]; leaked {
		t.Fatal("the RSS list view must not expose a raw download locator")
	}
	// An unknown file's size_bytes and media_kind keys are absent from the body
	// itself, not merely nil after decoding.
	unknownFile := raw["groups"].([]any)[0].(map[string]any)["items"].([]any)[0].(map[string]any)["files"].([]any)[2].(map[string]any)
	for _, key := range []string{"size_bytes", "media_kind"} {
		if _, present := unknownFile[key]; present {
			t.Fatalf("an unknown file must not carry a %s key: %#v", key, unknownFile)
		}
	}
}

// A release whose exact torrent file list was never read projects no file row,
// and the wire carries an empty array rather than null or a fabricated name.
func TestMapRSSListViewSerializesAnEmptyFileListAsAnArray(t *testing.T) {
	view := rssListViewFixture()
	view.Groups[0].Items[0].Files = []readmodelapplication.RSSListFile{}
	mapped, err := mapRSSListView(view)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(mapped)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	files := raw["groups"].([]any)[0].(map[string]any)["items"].([]any)[0].(map[string]any)["files"]
	if list, ok := files.([]any); !ok || len(list) != 0 {
		t.Fatalf("an item without a file row must send an empty array, got %#v", files)
	}
}

func TestMapRSSListViewEmitsCustomOwnerScopeIdentity(t *testing.T) {
	view := rssListViewFixture()
	view.Mode = "owner"
	view.ConfigIdentity = "owner:anime-1@2"
	view.Groups[0].Scope = readmodelapplication.RSSListGroupScope{Provider: "custom", ProviderEntryRef: "", ProviderGroupRef: "Custom", RSSSourceID: "rss-source-1"}
	mapped, err := mapRSSListView(view)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(mapped)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	group := body["groups"].([]any)[0].(map[string]any)
	scope := group["scope"].(map[string]any)
	if scope["provider"] != "custom" || scope["provider_entry_ref"] != "" || scope["rss_source_id"] != "rss-source-1" {
		t.Fatalf("owner scope wire identity = %#v", scope)
	}
}
func TestMapRSSListViewCarriesBlockedReason(t *testing.T) {
	view := rssListViewFixture()
	view.Groups[0].Items[0].DownloadAction = readmodelapplication.RSSListDownloadAction{
		Allowed: false, Kind: readmodelapplication.RSSListDownloadNone,
		ReasonCode: readmodelapplication.RSSListReasonSnapshotRefreshReq, Reason: "请先刷新种子。",
	}
	mapped, err := mapRSSListView(view)
	if err != nil {
		t.Fatal(err)
	}
	action := mapped.Groups[0].Items[0].DownloadAction
	if action.Allowed {
		t.Fatal("a blocked action must not be allowed")
	}
	if action.ReasonCode == nil || *action.ReasonCode != readmodelapplication.RSSListReasonSnapshotRefreshReq {
		t.Fatalf("blocked reason code drifted: %+v", action.ReasonCode)
	}
	if action.Reason == nil || *action.Reason == "" {
		t.Fatalf("blocked reason text is missing: %+v", action.Reason)
	}
}

func libraryOverlapCount(value int) *int {
	return &value
}

// A decidable overlap reaches the wire as counts plus an explicit per-file
// boolean; the read-only fact never carries a placement or a plan.
func TestMapRSSListViewCarriesADecidableLibraryOverlap(t *testing.T) {
	view := rssListViewFixture()
	held, pending := true, false
	view.Groups[0].Items[0].Files[0].InLibrary = &held
	view.Groups[0].Items[0].Files[1].InLibrary = &pending
	mapped, err := mapRSSListView(view)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(mapped)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	item := raw["groups"].([]any)[0].(map[string]any)["items"].([]any)[0].(map[string]any)
	overlap, ok := item["library_overlap"].(map[string]any)
	if !ok {
		t.Fatalf("library_overlap is missing from the body: %#v", item["library_overlap"])
	}
	if overlap["verdict"] != "absent" {
		t.Fatalf("verdict = %#v, want absent", overlap["verdict"])
	}
	if overlap["present_files"] != float64(0) || overlap["pending_files"] != float64(1) {
		t.Fatalf("counts = %#v/%#v, want 0/1", overlap["present_files"], overlap["pending_files"])
	}
	if value, present := overlap["reason_code"]; !present || value != nil {
		t.Fatalf("a decidable verdict must carry a null reason, got %#v (present=%v)", value, present)
	}
	files := item["files"].([]any)
	if files[0].(map[string]any)["in_library"] != true || files[1].(map[string]any)["in_library"] != false {
		t.Fatalf("per-file facts = %#v/%#v, want true/false", files[0].(map[string]any)["in_library"], files[1].(map[string]any)["in_library"])
	}
}

// "Cannot decide" must not reach the wire as "nothing is held": an undetermined
// overlap withholds both counts and every per-file boolean, because a body that
// carried 0/false would be read as an absent verdict.
func TestMapRSSListViewWithholdsAnUndeterminedLibraryOverlap(t *testing.T) {
	view := rssListViewFixture()
	reason := readmodelapplication.RSSListLibraryOverlapReasonNoTarget
	view.Groups[0].Items[0].LibraryOverlap = readmodelapplication.LibraryOverlap{
		Verdict: readmodelapplication.RSSListLibraryOverlapUndetermined, ReasonCode: &reason,
	}
	// The read model leaves every file fact null for an undecidable seed; the
	// fixture repeats that so the assertion below is about the mapper.
	mapped, err := mapRSSListView(view)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(mapped)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	item := raw["groups"].([]any)[0].(map[string]any)["items"].([]any)[0].(map[string]any)
	overlap := item["library_overlap"].(map[string]any)
	if overlap["verdict"] != "undetermined" {
		t.Fatalf("verdict = %#v, want undetermined", overlap["verdict"])
	}
	for _, key := range []string{"present_files", "pending_files"} {
		if value, present := overlap[key]; present && value != nil {
			t.Fatalf("an undecided overlap must withhold %s, got %#v", key, value)
		}
	}
	if overlap["reason_code"] != "no_rendered_targets" {
		t.Fatalf("reason_code = %#v, want no_rendered_targets", overlap["reason_code"])
	}
	for index, file := range item["files"].([]any) {
		if value, present := file.(map[string]any)["in_library"]; present && value != nil {
			t.Fatalf("file %d in_library = %#v, want null for an undecidable seed", index, value)
		}
	}
}

// Only the read model decides a verdict, and a missing one would serialize as an
// unknown enum member: the mapping refuses it instead of sending it.
func TestMapRSSListViewRefusesAnItemWithoutALibraryOverlapVerdict(t *testing.T) {
	view := rssListViewFixture()
	view.Groups[0].Items[0].LibraryOverlap = readmodelapplication.LibraryOverlap{}
	if _, err := mapRSSListView(view); err == nil {
		t.Fatal("an item without a library overlap verdict must not be mapped")
	}
}

// The preview's own offset is an input, and an omitted one is the preview's 0,
// exactly like a one-shot request that carries no offset.
func TestMapRSSListReadInputCarriesThePreviewOffset(t *testing.T) {
	source := generated.RSSListViewInputInputSource("candidate_revision")
	candidate := generated.ID("cand-1")
	revision := generated.Version(1)
	offset := 3
	input, err := mapRSSListReadInput(generated.RSSListViewInput{InputSource: source, CandidateId: &candidate, Revision: &revision, EpisodeOffset: &offset})
	if err != nil {
		t.Fatal(err)
	}
	if input.PreviewEpisodeOffset != 3 {
		t.Fatalf("preview offset = %d, want 3", input.PreviewEpisodeOffset)
	}
	input, err = mapRSSListReadInput(generated.RSSListViewInput{InputSource: source, CandidateId: &candidate, Revision: &revision})
	if err != nil {
		t.Fatal(err)
	}
	if input.PreviewEpisodeOffset != 0 {
		t.Fatalf("an omitted preview offset = %d, want 0", input.PreviewEpisodeOffset)
	}
}

func TestMapRSSListReadInputOneShotIsCurrentInputOnly(t *testing.T) {
	candidate := generated.ID("cand-1")
	revision := generated.Version(1)
	yes := true
	if _, err := mapRSSListReadInput(generated.RSSListViewInput{InputSource: generated.RSSListViewInputInputSource("candidate_revision"), CandidateId: &candidate, Revision: &revision, OneShot: &yes}); err == nil {
		t.Fatal("one_shot accepted with candidate_revision")
	}
}
