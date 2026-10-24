package application

// MetadataPatchInput is the complete metadata owner value submitted with CAS.
type MetadataPatchInput struct {
	BGM             *BGMBinding
	ExpectedVersion int64
	TitleOverride   *string
	TMDB            *TMDBBinding
	TotalEpisodes   *int
	// MediaKindOverride is the complete owner value of the media-kind pin: nil
	// clears it and a value (tv | movie) replaces it. It is independent of the
	// TMDB tuple and never rewrites it.
	MediaKindOverride *string
	// Year is the complete owner value of the naming year: nil clears it.
	Year *int
	// Season is the complete owner value of the naming season (0 = specials):
	// nil clears it. It never rewrites the TMDB tuple and the tuple never
	// rewrites it.
	Season *int
}
