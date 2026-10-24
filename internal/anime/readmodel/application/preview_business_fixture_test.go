package application

import (
	domain "bansoko/internal/anime/domain"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/frozenpolicy"
	"context"
	"testing"
)

// previewObservedYear is the owner-edited year a preview fixture sets. The
// shipped default names ${anime.year}, which reads only the owner value — never
// a provider year or a number read off a release title.
const previewObservedYear = 2025

// Preview uses the production exact-history loader and the manifest itself as
// evaluation input. This fixture seeds source, never evaluated sample output.
func seedPreviewBusiness(t *testing.T, s *Service, d *domain.AnimeDraft) {
	t.Helper()
	if s.db == nil {
		s.db = animetest.OpenTestDB(t)
	}
	b, err := frozenpolicy.SeedBusiness(context.Background(), s.db, `vars.actual = {title: input.release_title, count: input.files.length};`)
	if err != nil {
		t.Fatal(err)
	}
	d.Business = &b
	digest, err := b.Digest()
	if err != nil {
		t.Fatal(err)
	}
	d.BusinessDigest = &digest
}

// setDraftOwnerYear sets the owner-edited year on a preview draft. ${anime.year}
// reads only that value (never a provider observation), so a fixture whose
// target must carry a year states it here, as the owner would in the form.
// ${anime.season*} likewise reads only the owner season, so a TV-bound draft
// also gets the season the TMDB match prefills when it is unset.
func setDraftOwnerYear(d *domain.AnimeDraft) {
	year := previewObservedYear
	d.Metadata.Year = &year
	if tmdb := d.Metadata.Tmdb; tmdb != nil && tmdb.MediaKind == domain.TMDBMediaKindTv && d.Metadata.Season == nil {
		season := tmdb.Season
		d.Metadata.Season = &season
	}
}
