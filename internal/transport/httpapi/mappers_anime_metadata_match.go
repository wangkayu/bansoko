package httpapi

import (
	"fmt"

	ownershipapplication "bansoko/internal/anime/ownership/application"
	"bansoko/internal/transport/httpapi/generated"
)

func mapMetadataMatchInput(input generated.MetadataMatchInput) ownershipapplication.MetadataMatchInput {
	return ownershipapplication.MetadataMatchInput{ExpectedVersion: input.ExpectedVersion}
}

func mapMetadataMatchResult(result ownershipapplication.MetadataMatchResult) (generated.MetadataMatchResult, error) {
	if result.ManagedAnimeID == "" {
		return generated.MetadataMatchResult{}, fmt.Errorf("metadata match result has empty managed anime id")
	}
	outcome, err := mapMetadataMatchOutcome(result.Outcome)
	if err != nil {
		return generated.MetadataMatchResult{}, err
	}
	metadata, err := mapMetadataMatchMetadata(result.Metadata)
	if err != nil {
		return generated.MetadataMatchResult{}, err
	}
	mapped := generated.MetadataMatchResult{
		ManagedAnimeId: generated.ID(result.ManagedAnimeID),
		Metadata:       metadata,
		Outcome:        outcome,
	}
	if result.Match != nil {
		match, err := mapMetadataMatch(result.Match)
		if err != nil {
			return generated.MetadataMatchResult{}, err
		}
		mapped.Match = &match
	}
	if result.Reason != nil {
		reason := *result.Reason
		mapped.Reason = &reason
	}
	return mapped, nil
}

func mapMetadataMatchOutcome(outcome ownershipapplication.MetadataMatchOutcome) (generated.MetadataMatchOutcome, error) {
	switch outcome {
	case ownershipapplication.MetadataMatchOutcomeComplete:
		return generated.MetadataMatchOutcomeComplete, nil
	case ownershipapplication.MetadataMatchOutcomeConfigError:
		return generated.MetadataMatchOutcomeConfigError, nil
	case ownershipapplication.MetadataMatchOutcomeNoResult:
		return generated.MetadataMatchOutcomeNoResult, nil
	case ownershipapplication.MetadataMatchOutcomePartial:
		return generated.MetadataMatchOutcomePartial, nil
	case ownershipapplication.MetadataMatchOutcomeProviderError:
		return generated.MetadataMatchOutcomeProviderError, nil
	default:
		return "", fmt.Errorf("metadata match result has unknown outcome %q", outcome)
	}
}

func mapMetadataMatchMetadata(metadata ownershipapplication.MetadataMatchMetadata) (generated.AnimeMetadata, error) {
	if metadata.ManagedAnimeID == "" {
		return generated.AnimeMetadata{}, fmt.Errorf("metadata match result has empty metadata managed anime id")
	}
	mapped := generated.AnimeMetadata{
		ManagedAnimeId:    generated.ID(metadata.ManagedAnimeID),
		TitleOverride:     cloneMetadataMatchString(metadata.TitleOverride),
		TotalEpisodes:     cloneMetadataMatchInt(metadata.TotalEpisodes),
		MediaKindOverride: (*generated.AnimeMetadataMediaKindOverride)(cloneMetadataMatchString(metadata.MediaKindOverride)),
		Year:              cloneMetadataMatchInt(metadata.Year),
		Season:            cloneMetadataMatchInt(metadata.Season),
		Version:           generated.Version(metadata.Version),
	}
	if metadata.BGM != nil {
		mapped.Bgm = &generated.NullableBGMBinding{WorkId: metadata.BGM.WorkID}
	}
	if metadata.TMDB != nil {
		mediaKind, err := mapMetadataTMDBMediaKind(metadata.TMDB.MediaKind)
		if err != nil {
			return generated.AnimeMetadata{}, err
		}
		mapped.Tmdb = &generated.NullableTMDBBinding{
			WorkId:    metadata.TMDB.WorkID,
			MediaKind: mediaKind,
			Season:    metadata.TMDB.Season,
		}
	}
	return mapped, nil
}

func mapMetadataMatch(match *ownershipapplication.MetadataMatch) (generated.MetadataMatch, error) {
	if match == nil {
		return generated.MetadataMatch{}, fmt.Errorf("metadata match result has nil match")
	}
	mapped := generated.MetadataMatch{}
	if match.BGM != nil {
		mapped.Bgm = &generated.NullableBGMBinding{WorkId: match.BGM.WorkID}
	}
	if match.TMDB != nil {
		mediaKind, err := mapMetadataTMDBMediaKind(match.TMDB.MediaKind)
		if err != nil {
			return generated.MetadataMatch{}, err
		}
		mapped.Tmdb = &generated.NullableTMDBBinding{
			WorkId:    match.TMDB.WorkID,
			MediaKind: mediaKind,
			Season:    match.TMDB.Season,
		}
	}
	return mapped, nil
}

func mapMetadataTMDBMediaKind(mediaKind string) (generated.TMDBMediaKind, error) {
	switch mediaKind {
	case string(generated.TMDBMediaKindTv):
		return generated.TMDBMediaKindTv, nil
	case string(generated.TMDBMediaKindMovie):
		return generated.TMDBMediaKindMovie, nil
	default:
		return "", fmt.Errorf("metadata match result has unknown TMDB media kind %q", mediaKind)
	}
}

func cloneMetadataMatchString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneMetadataMatchInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
