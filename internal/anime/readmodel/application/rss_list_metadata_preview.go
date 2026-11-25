package application

import (
	"strings"

	acquisitiontarget "bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/postprocessing/naming"
)

// MetadataPreviewInput is the complete unsaved metadata value an owner_draft
// read previews with. It has the metadata PATCH's complete-value semantics
// (nil clears; the title is the exception: it is the anime's fixed title, so
// nil keeps the stored one) and replaces the persisted metadata for that one
// read; it is never written.
//
// The read consumes title_override, year, season, the BGM and TMDB bindings
// and media_kind: they are the naming facts (anime.title_override,
// anime.year, anime.season, bgm.*, tmdb.*), the metadata hold, and the media kind and
// season episode parsing binds. TotalEpisodes is validated with the rest but
// no preview reads it.
type MetadataPreviewInput struct {
	BGM           *BGMBinding
	TMDB          *TMDBBinding
	TitleOverride *string
	MediaKind     *string
	TotalEpisodes *int
	Year          *int
	Season        *int
}

// validateMetadataPreview applies the metadata PATCH validation. A violation
// fails the read (422) with the field under metadata_preview; nothing is
// silently dropped.
func validateMetadataPreview(preview MetadataPreviewInput) error {
	bgm, tmdb := metadataPatchBindingsForDomain(preview.BGM, preview.TMDB)
	violations := animedomain.ValidateMetadataPatch(bgm, tmdb, preview.TitleOverride, preview.MediaKind, preview.TotalEpisodes, preview.Year)
	violations = append(violations, animedomain.ValidateAnimeSeason("season", preview.Season)...)
	if len(violations) == 0 {
		return nil
	}
	for index := range violations {
		violations[index].Field = "metadata_preview." + violations[index].Field
	}
	return draftInvalidError(violations)
}

// overlayWorkspaceMetadata replaces the read's copy of the persisted metadata
// (and the title override, which the metadata PATCH also owns) with the
// unsaved value. The workspace is the read's own value; nothing persisted is
// touched.
func overlayWorkspaceMetadata(workspace *AnimeWorkspace, preview MetadataPreviewInput) {
	metadata := workspace.Metadata
	metadata.BGM, metadata.TMDB = nil, nil
	if preview.BGM != nil {
		bgm := *preview.BGM
		metadata.BGM = &bgm
	}
	if preview.TMDB != nil {
		tmdb := *preview.TMDB
		metadata.TMDB = &tmdb
	}
	if preview.TitleOverride != nil {
		metadata.TitleOverride = cloneOptionalString(preview.TitleOverride)
		workspace.ManagedAnime.TitleOverride = cloneOptionalString(preview.TitleOverride)
	}
	metadata.MediaKind = cloneOptionalString(preview.MediaKind)
	metadata.TotalEpisodes = cloneOptionalInt(preview.TotalEpisodes)
	metadata.Year = cloneOptionalInt(preview.Year)
	metadata.Season = cloneOptionalInt(preview.Season)
	workspace.Metadata = metadata
}

// overlayNamingAnimeFacts applies the unsaved binding before loading its titles.
func overlayNamingAnimeFacts(evidence *naming.AnimeNamingFacts, preview MetadataPreviewInput) {
	// The title is fixed: an absent preview title keeps the stored one.
	if preview.TitleOverride != nil {
		evidence.TitleOverride = strings.TrimSpace(*preview.TitleOverride)
	}
	evidence.Year = cloneOptionalInt(preview.Year)
	evidence.Season = cloneOptionalInt(preview.Season)
	evidence.BGM = naming.BGMNamingFacts{}
	if preview.BGM != nil {
		evidence.BGM.WorkID = strings.TrimSpace(preview.BGM.WorkID)
	}
	evidence.TMDB = naming.TMDBNamingFacts{}
	if preview.TMDB != nil {
		evidence.TMDB = naming.TMDBNamingFacts{WorkID: preview.TMDB.WorkID, MediaKind: preview.TMDB.MediaKind, Season: preview.TMDB.Season, SeasonPresent: true}
	}
}

// overlayParserSelection rebinds the media kind and season a persisted
// source's parser selection froze from the saved metadata to the unsaved
// value, through the same projection (and kind backfill) the save path uses.
func overlayParserSelection(selection acquisitiontarget.ParserSelectionInput, preview MetadataPreviewInput) acquisitiontarget.ParserSelectionInput {
	_, tmdb := metadataPatchBindingsForDomain(nil, preview.TMDB)
	selection.MediaKind, selection.Season, selection.SeasonBound = animedomain.FrozenMediaBinding(tmdb, animedomain.BackfilledMediaKind(preview.MediaKind, tmdb))
	return selection
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneOptionalInt(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
