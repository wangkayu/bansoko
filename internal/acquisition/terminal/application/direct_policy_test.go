package application

import (
	anime "bansoko/internal/anime/domain"
	catalog "bansoko/internal/catalog/domain"
	"context"
	"encoding/json"
	"testing"
)

// The manual retry entry re-checks the release filter exactly like the
// automatic entry: a filtered release is rejected by both.
func TestDirectReleaseFilterEntryPolicy(t *testing.T) {
	m := qualificationFixture(t, "subscription_direct")
	m.programs.varsSource = "vars.actual = {title: input.release_title};"
	m.bgm = &anime.BGMBinding{WorkId: "123"}
	m.tmdb = &anime.TMDBBinding{WorkId: "456", MediaKind: "tv", Season: 1}
	m.config.business.Sections[anime.SectionReleaseRules] = json.RawMessage(`{"must_contain":"NEVER_MATCH_THIS","not_in":""}`)
	var err error
	m.config.digest, err = m.config.business.Digest()
	if err != nil {
		t.Fatal(err)
	}
	m.catalog.feed.Seeds[0].Title = "[Group] Show - 01 [1080p]"
	m.catalog.feed.ContentHash, err = catalog.CanonicalFeedSnapshotContentHash(m.catalog.feed.Seeds)
	if err != nil {
		t.Fatal(err)
	}
	c, err := constructQualification(m)
	if err != nil {
		t.Fatal(err)
	}
	intent := serviceIntentFixture(t, c)
	intent.entryMode = "subscription_direct"
	_, err = evaluateAdmission(context.Background(), c, intent)
	assertAcceptanceClass(t, err, 422, "ADMISSION_REJECTED")
	intent.entryMode = "automatic_source"
	if _, err := evaluateAdmission(context.Background(), c, intent); err == nil {
		t.Fatal("automatic filter bypassed")
	}
}
