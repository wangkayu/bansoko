package application

import (
	"context"

	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/transport/httpapi/generated"
)

// PatchAnimeMetadata is a compatibility facade for existing in-process callers.
// HTTP transport uses PatchAnimeMetadataApplication so the core remains
// independent of generated transport DTOs.
func (s *Service) PatchAnimeMetadata(ctx context.Context, managedAnimeID string, patch generated.AnimeMetadataPatch) (generated.AnimeMetadata, error) {
	result, err := s.PatchAnimeMetadataApplication(ctx, managedAnimeID, metadataPatchInputFromHTTP(patch))
	if err != nil {
		return generated.AnimeMetadata{}, err
	}
	return metadataPatchResultForHTTP(result), nil
}

func metadataPatchInputFromHTTP(patch generated.AnimeMetadataPatch) MetadataPatchInput {
	input := MetadataPatchInput{
		ExpectedVersion: int64(patch.ExpectedVersion),
		TitleOverride:   animeapplication.CloneString(patch.TitleOverride),
		TotalEpisodes:   animeapplication.CloneInt(patch.TotalEpisodes),
		MediaKind:       animeapplication.CloneString((*string)(patch.MediaKind)),
		Year:            animeapplication.CloneInt(patch.Year),
		Season:          animeapplication.CloneInt(patch.Season),
	}
	if patch.Bgm != nil {
		input.BGM = &BGMBinding{WorkID: patch.Bgm.WorkId}
	}
	if patch.Tmdb != nil {
		input.TMDB = &TMDBBinding{
			WorkID:    patch.Tmdb.WorkId,
			MediaKind: string(patch.Tmdb.MediaKind),
			Season:    patch.Tmdb.Season,
		}
	}
	return input
}

func metadataPatchResultForHTTP(value AnimeMetadata) generated.AnimeMetadata {
	result := generated.AnimeMetadata{
		ManagedAnimeId: generated.ID(value.ManagedAnimeID),
		TitleOverride:  animeapplication.CloneString(value.TitleOverride),
		TotalEpisodes:  animeapplication.CloneInt(value.TotalEpisodes),
		MediaKind:      (*generated.AnimeMetadataMediaKind)(animeapplication.CloneString(value.MediaKind)),
		Year:           animeapplication.CloneInt(value.Year),
		Season:         animeapplication.CloneInt(value.Season),
		Version:        generated.Version(value.Version),
	}
	if value.BGM != nil {
		result.Bgm = &generated.NullableBGMBinding{WorkId: value.BGM.WorkID}
	}
	if value.TMDB != nil {
		result.Tmdb = &generated.NullableTMDBBinding{
			WorkId:    value.TMDB.WorkID,
			MediaKind: generated.TMDBMediaKind(value.TMDB.MediaKind),
			Season:    value.TMDB.Season,
		}
	}
	return result
}
