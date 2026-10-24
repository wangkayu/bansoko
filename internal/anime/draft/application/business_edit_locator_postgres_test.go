package application

import (
	"context"
	"strings"
	"testing"

	"bansoko/internal/testutil/animetest"
)

// The instance edit baseline shows each source's stored display through the
// same origin-only rule as every other source read. A provider source's
// display column can hold its full feed URL, and a legacy custom row a
// path-keyed one (UNIT3D /rss/<id>.<key>; synthetic key). The stored locator is
// untouched: the app needs it to fetch.
func TestBusinessEditSanitizesStoredLocatorDisplay(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	service := draftNewTestService(t, db)
	draft, err := domainDraft(draftTestDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	b := frozenBusinessFixture(t)
	draft.Business = b.Business
	draft.BusinessDigest = b.BusinessDigest
	seedBusinessPrograms(t, db, &draft)
	original, err := service.SaveAnimeConfigCandidate(ctx, CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	activated, err := service.ActivateAnimeConfigCandidate(ctx, original.Id, original.Version, "business-edit-locator-0001", CandidateSubscribeInput{Revision: *original.CurrentRevision, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	const key = "0123456789abcdef0123456789abcdef"
	legacy := "https://t.example.test/rss/12." + key + "?rsskey=" + key
	if _, err := db.ExecContext(ctx, `UPDATE rss_sources SET locator_display=$2 WHERE subscription_id=$1`, activated.Subscription.ID, legacy); err != nil {
		t.Fatal(err)
	}
	baseline, err := service.GetAnimeBusinessEdit(ctx, activated.ManagedAnime.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Draft.RssSources) != 1 || baseline.Draft.RssSources[0].LocatorDisplay == nil {
		t.Fatalf("baseline sources: %+v", baseline.Draft.RssSources)
	}
	if display := *baseline.Draft.RssSources[0].LocatorDisplay; strings.Contains(display, key) || display != "https://t.example.test" {
		t.Fatalf("locator_display = %q, want the origin only", display)
	}
	var stored string
	if err := db.QueryRowContext(ctx, `SELECT locator_display FROM rss_sources WHERE subscription_id=$1`, activated.Subscription.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != legacy {
		t.Fatal("a read never rewrites the stored row")
	}
}
