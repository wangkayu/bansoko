package httpapi

import (
	"testing"

	ownershipapplication "bansoko/internal/anime/ownership/application"
	"bansoko/internal/transport/httpapi/generated"
)

func TestMapMetadataMatchInput(t *testing.T) {
	mapped := mapMetadataMatchInput(generated.MetadataMatchInput{ExpectedVersion: 7})
	if mapped.ExpectedVersion != 7 {
		t.Fatalf("expected version = %d, want 7", mapped.ExpectedVersion)
	}
}

func TestMapMetadataMatchResult(t *testing.T) {
	titleOverride := "renamed"
	episodes := 12
	reason := "Metadata matched successfully."
	result := ownershipapplication.MetadataMatchResult{
		ManagedAnimeID: "anime-1",
		Outcome:        ownershipapplication.MetadataMatchOutcomeComplete,
		Reason:         &reason,
		Metadata: ownershipapplication.MetadataMatchMetadata{
			ManagedAnimeID: "anime-1",
			TitleOverride:  &titleOverride,
			TotalEpisodes:  &episodes,
			Version:        3,
			BGM:            &ownershipapplication.MetadataMatchBGM{WorkID: "bgm-1"},
			TMDB:           &ownershipapplication.MetadataMatchTMDB{WorkID: "tmdb-1", MediaKind: "tv", Season: 2},
		},
		Match: &ownershipapplication.MetadataMatch{
			BGM:  &ownershipapplication.MetadataMatchBGM{WorkID: "bgm-1"},
			TMDB: &ownershipapplication.MetadataMatchTMDB{WorkID: "tmdb-1", MediaKind: "tv", Season: 2},
		},
	}

	mapped, err := mapMetadataMatchResult(result)
	if err != nil {
		t.Fatalf("map metadata match result: %v", err)
	}
	if mapped.ManagedAnimeId != "anime-1" || mapped.Outcome != generated.MetadataMatchOutcomeComplete {
		t.Fatalf("unexpected result identity/outcome: %+v", mapped)
	}
	if mapped.Metadata.ManagedAnimeId != "anime-1" || mapped.Metadata.Version != 3 ||
		mapped.Metadata.TitleOverride == nil || *mapped.Metadata.TitleOverride != titleOverride ||
		mapped.Metadata.TotalEpisodes == nil || *mapped.Metadata.TotalEpisodes != episodes {
		t.Fatalf("metadata fields were not projected: %+v", mapped.Metadata)
	}
	if mapped.Metadata.Bgm == nil || mapped.Metadata.Bgm.WorkId != "bgm-1" ||
		mapped.Metadata.Tmdb == nil || mapped.Metadata.Tmdb.WorkId != "tmdb-1" ||
		mapped.Metadata.Tmdb.MediaKind != generated.TMDBMediaKindTv || mapped.Metadata.Tmdb.Season != 2 {
		t.Fatalf("metadata bindings were not projected: %+v", mapped.Metadata)
	}
	if mapped.Match == nil || mapped.Match.Bgm == nil || mapped.Match.Bgm.WorkId != "bgm-1" ||
		mapped.Match.Tmdb == nil || mapped.Match.Tmdb.WorkId != "tmdb-1" {
		t.Fatalf("match bindings were not projected: %+v", mapped.Match)
	}
	if mapped.Match.Tmdb.MediaKind != generated.TMDBMediaKindTv || mapped.Match.Tmdb.Season != 2 {
		t.Fatalf("match TMDB fields were not projected: %+v", mapped.Match.Tmdb)
	}
	if mapped.Reason == nil || *mapped.Reason != reason {
		t.Fatalf("reason was not projected: %+v", mapped.Reason)
	}

	// The mapper copies pointer values rather than retaining application-owned
	// mutable pointers.
	*result.Metadata.TitleOverride = "changed"
	*result.Metadata.TotalEpisodes = 99
	*result.Reason = "changed"
	if *mapped.Metadata.TitleOverride != "renamed" || *mapped.Metadata.TotalEpisodes != 12 || *mapped.Reason != "Metadata matched successfully." {
		t.Fatal("mapped optional values alias application input")
	}
}

func TestMapMetadataMatchResultRejectsUnknownValues(t *testing.T) {
	base := ownershipapplication.MetadataMatchResult{
		ManagedAnimeID: "anime-1",
		Outcome:        ownershipapplication.MetadataMatchOutcome("unknown"),
		Metadata: ownershipapplication.MetadataMatchMetadata{
			ManagedAnimeID: "anime-1",
		},
	}
	if _, err := mapMetadataMatchResult(base); err == nil {
		t.Fatal("unknown outcome must fail closed")
	}

	base.Outcome = ownershipapplication.MetadataMatchOutcomeNoResult
	base.Metadata.TMDB = &ownershipapplication.MetadataMatchTMDB{WorkID: "tmdb-1", MediaKind: "unknown"}
	if _, err := mapMetadataMatchResult(base); err == nil {
		t.Fatal("unknown TMDB media kind must fail closed")
	}
}
