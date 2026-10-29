package application

import (
	"context"
	"errors"
	"testing"

	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/apperrors"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
)

func metadataPreviewFixture() MetadataPreviewInput {
	year := 2031
	title := "Edited Title"
	return MetadataPreviewInput{
		BGM:           &BGMBinding{WorkID: "bgm-9"},
		TMDB:          &TMDBBinding{WorkID: "tmdb-9", MediaKind: "tv", Season: 3},
		Year:          &year,
		TitleOverride: &title,
	}
}

// An invalid unsaved value fails the read with the PATCH's field errors under
// metadata_preview; it is never silently dropped. Validation runs before any
// owner state is read.
func TestOwnerDraftMetadataPreviewValidatesLikeThePatch(t *testing.T) {
	year, blank, kind := 1800, " ", "anime"
	preview := MetadataPreviewInput{TMDB: &TMDBBinding{WorkID: "", MediaKind: "tv", Season: 1}, Year: &year, TitleOverride: &blank, MediaKind: &kind}
	_, err := (&Service{}).ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{InputSource: "owner_draft", ManagedAnimeID: "anime-1", MetadataPreview: &preview})
	var validation *apperrors.Validation
	if !errors.As(err, &validation) {
		t.Fatalf("invalid metadata_preview err = %v, want a validation error", err)
	}
	fields := map[string]bool{}
	for _, detail := range validation.Details {
		if detail.Field != nil {
			fields[*detail.Field] = true
		}
	}
	for _, want := range []string{"metadata_preview.tmdb", "metadata_preview.year", "metadata_preview.title_override", "metadata_preview.media_kind"} {
		if !fields[want] {
			t.Fatalf("violations %v miss %s", fields, want)
		}
	}
}

func TestOwnerReadModesRefuseMisplacedPreviews(t *testing.T) {
	preview := metadataPreviewFixture()
	if _, err := (&Service{}).ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: "anime-1", MetadataPreview: &preview}); err == nil {
		t.Fatal("owner mode accepted metadata_preview")
	}
	if _, err := (&Service{}).ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{InputSource: "owner_draft", ManagedAnimeID: "anime-1"}); err == nil {
		t.Fatal("owner_draft accepted no preview at all")
	}
}

// The unsaved year and TMDB season are what the naming context renders: the
// overlaid evidence freezes them into the release fact object the renderer
// reads (anime.year, tmdb.season), replacing the persisted values.
func TestMetadataPreviewOverlaysNamingEvidence(t *testing.T) {
	savedYear, savedSeason := int64(2020), int64(1)
	evidence := namingvars.AnimeEvidence{AnimeID: "anime-1", AnimeTitle: "Show", AnimeTitleOverride: "Saved", AnimeYear: &savedYear, BgmID: "bgm-1", TmdbID: "tmdb-1", TmdbKind: "tv", TmdbSeason: &savedSeason}
	overlayVarsAnimeEvidence(&evidence, metadataPreviewFixture())
	if evidence.AnimeYear == nil || *evidence.AnimeYear != 2031 || evidence.TmdbSeason == nil || *evidence.TmdbSeason != 3 || evidence.TmdbID != "tmdb-9" || evidence.BgmID != "bgm-9" || evidence.AnimeTitleOverride != "Edited Title" {
		t.Fatalf("overlaid evidence = %+v", evidence)
	}
	facts := namingvars.BuildInput(namingvars.ReleaseEvidence{AnimeEvidence: evidence}).Facts
	if year := naming.FrozenAnimeYear(facts); year == nil || *year != 2031 {
		t.Fatalf("frozen anime.year = %v, want 2031 (facts %v)", year, facts)
	}
	tmdb, _ := facts["tmdb"].(map[string]any)
	if season, ok := tmdb["season"]; !ok || season == nil {
		t.Fatalf("frozen tmdb.season missing: %v", facts)
	}

	// A cleared value clears: the persisted year does not leak back in. The
	// fixed title is never cleared: an absent preview title keeps it.
	overlayVarsAnimeEvidence(&evidence, MetadataPreviewInput{})
	if evidence.AnimeYear != nil || evidence.TmdbSeason != nil || evidence.TmdbID != "" || evidence.BgmID != "" || evidence.AnimeTitleOverride != "Edited Title" {
		t.Fatalf("cleared evidence = %+v", evidence)
	}
}

// The persisted source's parser selection rebinds the unsaved season and kind;
// the workspace copy carries the unsaved value and keeps its version.
func TestMetadataPreviewOverlaysParserSelectionAndWorkspace(t *testing.T) {
	selection := overlayParserSelection(acquisitiontarget.ParserSelectionInput{Kind: "regex", MediaKind: "tv", Season: 1}, metadataPreviewFixture())
	if selection.Kind != "regex" || selection.MediaKind != "tv" || selection.Season != 3 {
		t.Fatalf("overlaid selection = %+v", selection)
	}
	movie := "movie"
	preview := metadataPreviewFixture()
	preview.MediaKind = &movie
	if selection := overlayParserSelection(acquisitiontarget.ParserSelectionInput{MediaKind: "tv"}, preview); selection.MediaKind != "movie" {
		t.Fatalf("media_kind not applied: %+v", selection)
	}

	savedYear := 2020
	workspace := AnimeWorkspace{Metadata: AnimeMetadata{ManagedAnimeID: "anime-1", Year: &savedYear, Version: 7}}
	overlayWorkspaceMetadata(&workspace, metadataPreviewFixture())
	if workspace.Metadata.Version != 7 || workspace.Metadata.ManagedAnimeID != "anime-1" || workspace.Metadata.Year == nil || *workspace.Metadata.Year != 2031 ||
		workspace.Metadata.TMDB == nil || workspace.Metadata.TMDB.Season != 3 || workspace.ManagedAnime.TitleOverride == nil || *workspace.ManagedAnime.TitleOverride != "Edited Title" {
		t.Fatalf("overlaid workspace = %+v / %+v", workspace.Metadata, workspace.ManagedAnime)
	}
	if savedYear != 2020 {
		t.Fatal("the overlay wrote through the persisted value")
	}
	draft := pendingBenchDraft(workspace, nil)
	if draft.Metadata.Year == nil || *draft.Metadata.Year != 2031 || draft.Metadata.Tmdb == nil || draft.Metadata.Tmdb.Season != 3 {
		t.Fatalf("bench draft metadata = %+v", draft.Metadata)
	}
}
