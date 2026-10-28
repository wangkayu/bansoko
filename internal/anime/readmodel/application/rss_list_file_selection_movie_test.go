package application

import (
	"context"
	"testing"

	acquisitiontarget "bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/batchplan"
	animecatalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/filekinds"
)

func movieManifestRecognitions(t *testing.T, selection *filekinds.Selection) map[string]string {
	t.Helper()
	evidence, err := acquisitiontarget.ResolveParserEvidence(context.Background(), acquisitiontarget.ParserSelectionInput{Kind: "regex", MediaKind: "movie"}, "Preview Movie 2024")
	if err != nil {
		t.Fatal(err)
	}
	manifest := &animecatalogdomain.TorrentManifest{Files: []animecatalogdomain.TorrentFile{
		{SourcePath: "Pack/Preview Movie.mkv", Filename: "Preview Movie.mkv", SizeBytes: 9000, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
		{SourcePath: "Pack/Trailer.mkv", Filename: "Trailer.mkv", SizeBytes: 100, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
		{SourcePath: "Pack/Extras/Making.mkv", Filename: "Making.mkv", SizeBytes: 5000, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
	}}
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	rows, err := oneShotManifestFiles(context.Background(), manifest, evidence, effective, batchplan.RouteSkip, selection)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, row := range rows {
		out[row.SourcePath] = row.Recognition
	}
	return out
}

// For a movie binding only the read's own plan carries recognitions: exactly
// one video is the main video, and a video outside the chosen files reads as
// nothing at all (never a second main video).
func TestOneShotMovieManifestMarksOnlyTheChosenMainVideo(t *testing.T) {
	defaults := movieManifestRecognitions(t, nil)
	if defaults["Pack/Preview Movie.mkv"] != RSSListRecognitionMovie || defaults["Pack/Extras/Making.mkv"] != "" {
		t.Fatalf("default recognitions = %v", defaults)
	}
	chosen := movieManifestRecognitions(t, filekinds.NewSelection([]string{"Pack/Extras/Making.mkv"}))
	if chosen["Pack/Extras/Making.mkv"] != RSSListRecognitionMovie || chosen["Pack/Preview Movie.mkv"] != "" || chosen["Pack/Trailer.mkv"] != "" {
		t.Fatalf("chosen recognitions = %v", chosen)
	}
	for name, recognitions := range map[string]map[string]string{"defaults": defaults, "chosen": chosen} {
		movies := 0
		for _, recognition := range recognitions {
			if recognition == RSSListRecognitionMovie {
				movies++
			}
		}
		if movies != 1 {
			t.Fatalf("%s: %d main videos, want 1: %v", name, movies, recognitions)
		}
	}
}
