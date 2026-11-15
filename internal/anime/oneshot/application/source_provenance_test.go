package application

import (
	"context"
	"encoding/json"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animeapplication "bansoko/internal/anime/application"
	rssapplication "bansoko/internal/rss/application"
)

func TestSeedSourceProvenanceFreezesWithoutChangingRequestIdentity(t *testing.T) {
	snapshot := matchingSeedPreviewSnapshot(boundOneShotSeed())
	fact := &snapshot.Seeds[0]
	fact.SourceGUID = "original-guid"
	fact.DetailsURL = "https://tracker.example.test/details/7"
	fact.SourceSite = &acquisitiondomain.SeedSourceSite{ID: "tracker", Name: "Sample Tracker", Type: "private"}
	service := &Service{seedPreviews: &fakeSeedPreviewSource{snapshot: snapshot}}
	resolved, err := service.resolveSeedReference(context.Background(), seedReferenceOf(boundOneShotSeed()))
	if err != nil {
		t.Fatal(err)
	}
	request := AcquireSeedInput{CandidateID: "c-1", Revision: 2, EffectiveDigest: "sha256:e", Seed: canonicalSelectedSeed(boundOneShotSeed())}
	before, err := canonicalOneShotRequestHash(request, "key-0001")
	if err != nil {
		t.Fatal(err)
	}
	request.Seed = resolved
	after, err := canonicalOneShotRequestHash(request, "key-0001")
	if err != nil || before != after {
		t.Fatalf("request hash changed: %v", err)
	}
	raw, err := encodeOneShotInputSnapshot(OneShotInputSnapshot{SelectedSeed: resolved})
	if err != nil {
		t.Fatal(err)
	}
	var frozen OneShotInputSnapshot
	if err := json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	fact.SourceSite.Name = "Changed Tracker"
	item := buildOneShotFeedItem(animeapplication.TorrentPayload{}, frozen.SelectedSeed)
	if item.GUID != "original-guid" || item.DetailsURL != fact.DetailsURL || item.SourceSite == nil || item.SourceSite.Name != "Sample Tracker" {
		t.Fatalf("source provenance lost: %+v", item)
	}
	internal, err := rssapplication.EncodeInternalFeedFacts(item)
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		GUID       string                            `json:"guid"`
		DetailsURL string                            `json:"details_url"`
		SourceSite *acquisitiondomain.SeedSourceSite `json:"source_site"`
	}
	if err := json.Unmarshal(internal, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.GUID != item.GUID || stored.DetailsURL != item.DetailsURL || stored.SourceSite == nil || *stored.SourceSite != *item.SourceSite {
		t.Fatalf("release lost source provenance: %+v", stored)
	}
}
