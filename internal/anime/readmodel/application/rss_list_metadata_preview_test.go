package application

import (
	"context"
	"errors"
	"testing"

	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/apperrors"
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
	kind := "reltitle"
	if _, err := (&Service{}).ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: "anime-1", ParserKindPreview: &kind}); err == nil {
		t.Fatal("owner mode accepted parser_kind_preview")
	}
	if _, err := (&Service{}).ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{InputSource: "owner_draft", ManagedAnimeID: "anime-1"}); err == nil {
		t.Fatal("owner_draft accepted no preview at all")
	}
}

func TestOwnerDraftParserKindPreviewValidatesAndChangesDigest(t *testing.T) {
	invalid := "not-a-parser"
	_, err := (&Service{}).ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{
		InputSource:       "owner_draft",
		ManagedAnimeID:    "anime-1",
		ParserKindPreview: &invalid,
	})
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 422 || business.Code != "POLICY_INVALID" {
		t.Fatalf("invalid parser_kind_preview err = %v, want 422 POLICY_INVALID", err)
	}
	regexKind, reltitleKind := "regex", "reltitle"
	d1, err := ownerDraftDigest(nil, nil, &regexKind)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := ownerDraftDigest(nil, nil, &reltitleKind)
	if err != nil {
		t.Fatal(err)
	}
	if d1 == d2 {
		t.Fatalf("ownerDraftDigest did not change across parser_kind_preview: %s", d1)
	}
}

// The unsaved year, owner season and TMDB binding are projected directly into
// the typed naming facts, replacing the persisted values.
func TestMetadataPreviewOverlaysNamingEvidence(t *testing.T) {
	savedYear, savedSeason := 2020, 1
	evidence := naming.AnimeNamingFacts{ID: "anime-1", Title: "Show", TitleOverride: "Saved", Year: &savedYear, Season: &savedSeason,
		BGM: naming.BGMNamingFacts{WorkID: "bgm-1"}, TMDB: naming.TMDBNamingFacts{WorkID: "tmdb-1", MediaKind: "tv", Season: 1, SeasonPresent: true}}
	overlayNamingAnimeFacts(&evidence, metadataPreviewFixture())
	if evidence.Year == nil || *evidence.Year != 2031 || evidence.Season != nil || !evidence.TMDB.SeasonPresent || evidence.TMDB.Season != 3 || evidence.TMDB.WorkID != "tmdb-9" || evidence.BGM.WorkID != "bgm-9" || evidence.TitleOverride != "Edited Title" {
		t.Fatalf("overlaid evidence = %+v", evidence)
	}

	// A cleared value clears: the persisted year does not leak back in. The
	// fixed title is never cleared: an absent preview title keeps it.
	overlayNamingAnimeFacts(&evidence, MetadataPreviewInput{})
	if evidence.Year != nil || evidence.Season != nil || evidence.TMDB.SeasonPresent || evidence.TMDB.WorkID != "" || evidence.BGM.WorkID != "" || evidence.TitleOverride != "Edited Title" {
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
