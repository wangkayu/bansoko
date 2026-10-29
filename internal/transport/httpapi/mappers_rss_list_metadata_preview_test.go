package httpapi

import (
	"errors"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/transport/httpapi/generated"
)

// owner_draft carries the complete unsaved metadata value on its own (no
// rules patch needed), mapped field for field; every other mode refuses it.
func TestMapRSSListReadInputCarriesMetadataPreview(t *testing.T) {
	id := generated.ID("anime-1")
	year := 2031
	kind := generated.AnimeMetadataPreviewMediaKindMovie
	title := "Edited Title"
	preview := generated.AnimeMetadataPreview{
		Bgm:           &generated.NullableBGMBinding{WorkId: "bgm-9"},
		Tmdb:          &generated.NullableTMDBBinding{WorkId: "tmdb-9", MediaKind: generated.TMDBMediaKindTv, Season: 3},
		Year:          &year,
		MediaKind:     &kind,
		TitleOverride: &title,
	}
	mapped, err := mapRSSListReadInput(generated.RSSListViewInput{InputSource: "owner_draft", ManagedAnimeId: &id, MetadataPreview: &preview})
	if err != nil {
		t.Fatalf("owner_draft with metadata_preview only: %v", err)
	}
	got := mapped.MetadataPreview
	if mapped.RulesPreview != nil || got == nil {
		t.Fatalf("mapped previews rules=%v metadata=%v", mapped.RulesPreview, got)
	}
	if got.Year == nil || *got.Year != 2031 || got.TMDB == nil || got.TMDB.Season != 3 || got.TMDB.WorkID != "tmdb-9" || got.TMDB.MediaKind != "tv" ||
		got.BGM == nil || got.BGM.WorkID != "bgm-9" || got.MediaKind == nil || *got.MediaKind != "movie" || got.TitleOverride == nil || *got.TitleOverride != title {
		t.Fatalf("mapped metadata preview = %+v", got)
	}

	if _, err := mapRSSListReadInput(generated.RSSListViewInput{InputSource: "owner_draft", ManagedAnimeId: &id}); !isInputSourceInvalid(err) {
		t.Fatalf("owner_draft without any preview: %v", err)
	}
	if _, err := mapRSSListReadInput(generated.RSSListViewInput{InputSource: "owner", ManagedAnimeId: &id, MetadataPreview: &preview}); !isInputSourceInvalid(err) {
		t.Fatalf("owner with metadata_preview: %v", err)
	}
	draft := generated.AnimeDraft{}
	if _, err := mapRSSListReadInput(generated.RSSListViewInput{InputSource: "current_input", Draft: &draft, MetadataPreview: &preview}); !isInputSourceInvalid(err) {
		t.Fatalf("current_input with metadata_preview: %v", err)
	}
}

func isInputSourceInvalid(err error) bool {
	var business *apperrors.Business
	return errors.As(err, &business) && business.Code == "INPUT_SOURCE_INVALID"
}
