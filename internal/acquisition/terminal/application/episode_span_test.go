package application

import (
	"context"
	"testing"

	protocol "bansoko/internal/acquisition/terminal/domain"
	anime "bansoko/internal/anime/domain"
	catalog "bansoko/internal/catalog/domain"
)

func TestAcceptPrepareFreezesCombinedEpisodeSpan(t *testing.T) {
	m := qualificationFixture(t, "subscription_direct")
	m.programs.varsSource = "vars.actual = {title: input.release_title};"
	m.catalog.feed.Seeds[0].Title = "[Group] Show - 02 [1080p]"
	m.bgm = &anime.BGMBinding{WorkId: "123"}
	m.tmdb = &anime.TMDBBinding{WorkId: "456", MediaKind: "tv", Season: 1}
	var err error
	m.catalog.feed.ContentHash, err = catalog.CanonicalFeedSnapshotContentHash(m.catalog.feed.Seeds)
	if err != nil {
		t.Fatal(err)
	}
	c, err := constructQualification(m)
	if err != nil {
		t.Fatal(err)
	}
	c.material.release.span = 2
	result, err := evaluateAdmission(context.Background(), c, serviceIntentFixture(t, c), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.Validate("AcquisitionInputV1", result.input); err != nil {
		t.Fatal(err)
	}
	selection := result.input.Object["config"].Object["parser"].Object["selection"].Object["value"]
	if got := selection.Object["span"].Int; got != 2 {
		t.Fatalf("selection span = %d, want 2", got)
	}
	provenance := result.input.Object["config"].Object["parser"].Object["provenance"].Object["value"].Object["selection"]
	if got := provenance.Object["span"].Int; got != 2 {
		t.Fatalf("provenance span = %d, want 2", got)
	}
}
