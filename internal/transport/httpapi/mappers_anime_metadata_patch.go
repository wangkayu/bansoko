package httpapi

import (
	"fmt"

	animeapplication "bansoko/internal/anime/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/transport/httpapi/generated"
)

func mapMetadataPatchInput(value generated.AnimeMetadataPatch) ownershipapplication.MetadataPatchInput {
	input := ownershipapplication.MetadataPatchInput{
		ExpectedVersion: int64(value.ExpectedVersion),
		TitleOverride:   cloneRulesPreviewString(value.TitleOverride),
		TotalEpisodes:   cloneRulesPreviewInt(value.TotalEpisodes),
		MediaKind:       (*string)(cloneRulesPreviewString((*string)(value.MediaKind))),
		Year:            cloneRulesPreviewInt(value.Year),
		Season:          cloneRulesPreviewInt(value.Season),
	}
	if value.Bgm != nil {
		input.BGM = &animeapplication.BGMBinding{WorkID: value.Bgm.WorkId}
	}
	if value.Tmdb != nil {
		input.TMDB = &animeapplication.TMDBBinding{
			WorkID:    value.Tmdb.WorkId,
			MediaKind: string(value.Tmdb.MediaKind),
			Season:    value.Tmdb.Season,
		}
	}
	return input
}

// mapMetadataPreviewInput projects the unsaved metadata value an owner_draft
// RSS list read previews with. It is the PATCH body without expected_version,
// mapped field for field; the read model applies the PATCH validation.
func mapMetadataPreviewInput(value generated.AnimeMetadataPreview) readmodelapplication.MetadataPreviewInput {
	input := readmodelapplication.MetadataPreviewInput{
		TitleOverride: cloneRulesPreviewString(value.TitleOverride),
		TotalEpisodes: cloneRulesPreviewInt(value.TotalEpisodes),
		MediaKind:     cloneRulesPreviewString((*string)(value.MediaKind)),
		Year:          cloneRulesPreviewInt(value.Year),
		Season:        cloneRulesPreviewInt(value.Season),
	}
	if value.Bgm != nil {
		input.BGM = &animeapplication.BGMBinding{WorkID: value.Bgm.WorkId}
	}
	if value.Tmdb != nil {
		input.TMDB = &animeapplication.TMDBBinding{
			WorkID:    value.Tmdb.WorkId,
			MediaKind: string(value.Tmdb.MediaKind),
			Season:    value.Tmdb.Season,
		}
	}
	return input
}

func mapMetadataPatchResult(value animeapplication.AnimeMetadata) (generated.AnimeMetadata, error) {
	if value.ManagedAnimeID == "" {
		return generated.AnimeMetadata{}, fmt.Errorf("metadata patch result has empty managed anime id")
	}
	result := generated.AnimeMetadata{
		ManagedAnimeId: generated.ID(value.ManagedAnimeID),
		TitleOverride:  cloneRulesPreviewString(value.TitleOverride),
		TotalEpisodes:  cloneRulesPreviewInt(value.TotalEpisodes),
		MediaKind:      (*generated.AnimeMetadataMediaKind)(cloneRulesPreviewString(value.MediaKind)),
		Year:           cloneRulesPreviewInt(value.Year),
		Season:         cloneRulesPreviewInt(value.Season),
		Version:        generated.Version(value.Version),
	}
	result.ReevaluatedReleases = cloneRulesPreviewInt(value.ReevaluatedReleases)
	if value.BGM != nil {
		result.Bgm = &generated.NullableBGMBinding{WorkId: value.BGM.WorkID}
	}
	if value.TMDB != nil {
		mediaKind, err := mapMetadataTMDBMediaKind(value.TMDB.MediaKind)
		if err != nil {
			return generated.AnimeMetadata{}, err
		}
		result.Tmdb = &generated.NullableTMDBBinding{
			WorkId:    value.TMDB.WorkID,
			MediaKind: mediaKind,
			Season:    value.TMDB.Season,
		}
	}
	return result, nil
}
