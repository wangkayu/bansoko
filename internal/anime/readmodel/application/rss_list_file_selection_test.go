package application

import (
	"context"
	"reflect"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

func pickerSnapshot(t *testing.T) SeedPreviewSnapshot {
	t.Helper()
	return manifestPreviewSnapshotWithFiles(t, "12", "Pack", []animecatalogdomain.TorrentFile{
		{SourcePath: "Pack/Preview Show - 01.mkv", Filename: "Preview Show - 01.mkv", SizeBytes: 1234, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
		{SourcePath: "Pack/Preview Show - 01.zh-Hans.ass", Filename: "Preview Show - 01.zh-Hans.ass", SizeBytes: 20, MediaKind: animecatalogdomain.TorrentFileMediaSubtitle, Language: "zh-Hans"},
		{SourcePath: "Pack/NCOP.mkv", Filename: "NCOP.mkv", SizeBytes: 99, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
		{SourcePath: "Pack/SPs/Preview Show - 02.mkv", Filename: "Preview Show - 02.mkv", SizeBytes: 1234, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
		{SourcePath: "Pack/SPs/Preview Show SP.mkv", Filename: "Preview Show SP.mkv", SizeBytes: 1234, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
		{SourcePath: "Pack/SPs/Preview Show - 02.zh-Hans.ass", Filename: "Preview Show - 02.zh-Hans.ass", SizeBytes: 20, MediaKind: animecatalogdomain.TorrentFileMediaSubtitle, Language: "zh-Hans"},
		{SourcePath: "Pack/cover.jpg", Filename: "cover.jpg", SizeBytes: 7, MediaKind: animecatalogdomain.TorrentFileMediaOther},
	})
}

func pickerRead(t *testing.T, selections rssListFileSelections) RSSListItem {
	t.Helper()
	draft := previewBoundDraft()
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	effective.Rules.FileFilter = animedomain.FileFilterRules{NotIn: "NCOP"}
	service := &Service{seedPreviews: &readmodelPreviewSource{snapshot: pickerSnapshot(t)}}
	seedPreviewBusiness(t, service, &draft)
	setDraftOwnerYear(&draft)
	view, err := service.composeRSSListViewFor(context.Background(), "current_input", "draft:x", draft, oneShotBrowseSelection(draft, effective), previewTestSelection(), []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, nil, nil, 0, true, selections)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Items) != 1 {
		t.Fatalf("groups/items = %+v", view.Groups)
	}
	return view.Groups[0].Items[0]
}

func manifestRows(item RSSListItem) map[string]RSSListManifestFile {
	out := map[string]RSSListManifestFile{}
	for _, file := range item.ManifestFiles {
		out[file.SourcePath] = file
	}
	return out
}

// A one-shot read lists every manifest file for the picker: the file rules and
// the kind gate decide whether it may be chosen, the default plan decides
// whether it starts checked, and a selectable video carries its own filename
// recognition.
func TestOneShotReadListsEveryManifestFileForThePicker(t *testing.T) {
	item := pickerRead(t, nil)
	rows := manifestRows(item)
	if len(rows) != 7 {
		t.Fatalf("manifest files = %d, want every file of the torrent", len(rows))
	}
	type want struct {
		selectable, defaults bool
		reason, recognition  string
		episodes             []int
		bound                string
	}
	for path, expected := range map[string]want{
		"Pack/Preview Show - 01.mkv":             {true, true, "", RSSListRecognitionEpisode, []int{1}, ""},
		"Pack/Preview Show - 01.zh-Hans.ass":     {true, true, "", "", nil, "Pack/Preview Show - 01.mkv"},
		"Pack/NCOP.mkv":                          {false, false, "file_not_in_matched", "", nil, ""},
		"Pack/SPs/Preview Show - 02.mkv":         {true, false, "", RSSListRecognitionEpisode, []int{2}, ""},
		"Pack/SPs/Preview Show SP.mkv":           {true, false, "", RSSListRecognitionUnrecognized, nil, ""},
		"Pack/SPs/Preview Show - 02.zh-Hans.ass": {true, false, "", "", nil, "Pack/SPs/Preview Show - 02.mkv"},
		"Pack/cover.jpg":                         {false, false, "ignored_kind", "", nil, ""},
	} {
		row := rows[path]
		got := want{row.Selectable, row.DefaultSelected, row.ReasonCode, row.Recognition, row.Episodes, row.BoundVideoSourcePath}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("%s = %+v, want %+v", path, got, expected)
		}
	}
	for _, file := range item.Files {
		if file.SourcePath == "Pack/SPs/Preview Show - 02.mkv" {
			t.Fatalf("the default preview planned a nested file: %+v", file)
		}
	}
}

// A preview selection renders the chosen files at any depth: the deep video
// is planned with a target and the default first-level video becomes
// ignored_unselected.
func TestOneShotReadRendersThePreviewSelection(t *testing.T) {
	selections := newRSSListFileSelections([]RSSListFileSelectionInput{{
		SnapshotID: "snap-12", StableSeedKey: "seed-01",
		SourcePaths: []string{"Pack/SPs/Preview Show - 02.mkv", "Pack/SPs/Preview Show - 02.zh-Hans.ass"},
	}})
	item := pickerRead(t, selections)
	files := map[string]RSSListFile{}
	for _, file := range item.Files {
		files[file.SourcePath] = file
	}
	deep, ok := files["Pack/SPs/Preview Show - 02.mkv"]
	if !ok || !deep.Included || deep.RenderedTarget == "" {
		t.Fatalf("the selected deep video = %+v (listed %v)", deep, ok)
	}
	if subtitle := files["Pack/SPs/Preview Show - 02.zh-Hans.ass"]; !subtitle.Included || subtitle.RenderedTarget == "" {
		t.Fatalf("the selected deep subtitle = %+v", subtitle)
	}
	if first, listed := files["Pack/Preview Show - 01.mkv"]; listed && first.Included {
		t.Fatalf("an unselected default video is still planned: %+v", first)
	}
	if rows := manifestRows(item); !rows["Pack/Preview Show - 01.mkv"].DefaultSelected {
		t.Fatal("default_selected follows the default plan, not the selection")
	}
}
