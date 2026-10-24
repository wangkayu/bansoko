package application

import (
	"context"
	"testing"

	acquisitiontarget "bansoko/internal/acquisition/target"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

// A subtitle's preview episode is its bound video's own filename episode. The
// release title says episode 7; the files say 3 and 4, so a subtitle that took
// the release-level episode would be wrong.
func TestPreviewSubtitleEpisodeComesFromItsBoundVideo(t *testing.T) {
	ctx := context.Background()
	evidence, err := acquisitiontarget.ResolveParserEvidence(ctx, acquisitiontarget.ParserSelectionInput{Kind: "regex", MediaKind: "tv"}, "[Group] Preview Show - 07 [1080p]")
	if err != nil {
		t.Fatal(err)
	}
	manifest := manifestPreviewSnapshotWithFiles(t, "12", "Preview Show", []animecatalogdomain.TorrentFile{
		{SourcePath: "Preview Show/Preview Show - 03.mkv", Filename: "Preview Show - 03.mkv", SizeBytes: 10, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
		{SourcePath: "Preview Show/Preview Show - 04.mkv", Filename: "Preview Show - 04.mkv", SizeBytes: 10, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
		{SourcePath: "Preview Show/Preview Show - 04.zh-Hans.ass", Filename: "Preview Show - 04.zh-Hans.ass", SizeBytes: 5, MediaKind: animecatalogdomain.TorrentFileMediaSubtitle},
		{SourcePath: "Preview Show/notes.ass", Filename: "notes.ass", SizeBytes: 5, MediaKind: animecatalogdomain.TorrentFileMediaSubtitle},
	}).Seeds[0].Manifest

	files, err := previewManifestFiles(ctx, manifest, evidence)
	if err != nil {
		t.Fatal(err)
	}
	subtitle := files[2]
	if !subtitle.PerFileEpisode || subtitle.BoundVideo != "Preview Show/Preview Show - 04.mkv" {
		t.Fatalf("subtitle must follow the video its name starts with: %+v", subtitle)
	}
	if subtitle.FrozenEpisode == nil || subtitle.FrozenEpisode.Number != "4" {
		t.Fatalf("subtitle episode = %+v, want its video's episode 4 (title says 7)", subtitle.FrozenEpisode)
	}
	if files[1].FrozenEpisode == nil || files[1].FrozenEpisode.Number != "4" || files[0].FrozenEpisode.Number != "3" {
		t.Fatalf("video episodes come from their own filenames: %+v / %+v", files[0].FrozenEpisode, files[1].FrozenEpisode)
	}
	if orphan := files[3]; !orphan.PerFileEpisode || orphan.BoundVideo != "" || orphan.FrozenEpisode != nil {
		t.Fatalf("a subtitle matching no video is unbound and takes no episode: %+v", orphan)
	}
}
