package domain

import (
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

func TestFeedSnapshotHashCoversSourceProvenance(t *testing.T) {
	seed := ProviderFeedSnapshotSeed{StableSeedKey: "seed", Locator: "https://download.example.test/item.torrent", LocatorDisplay: "download.example.test", Title: "Sample", TorrentState: TorrentAcquisitionNotApplicable,
		SourceGUID: "source-1", DetailsURL: "https://tracker.example.test/details/1", SourceSite: &acquisitiondomain.SeedSourceSite{ID: "tracker", Name: "Sample Tracker", Type: "public"}}
	original, err := CanonicalFeedSnapshotContentHash([]ProviderFeedSnapshotSeed{seed})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ProviderFeedSnapshotSeed){
		"guid": func(s *ProviderFeedSnapshotSeed) { s.SourceGUID = "source-2" },
		"page": func(s *ProviderFeedSnapshotSeed) { s.DetailsURL = "https://tracker.example.test/details/2" },
		"site": func(s *ProviderFeedSnapshotSeed) {
			site := *s.SourceSite
			site.Name = "Other Tracker"
			s.SourceSite = &site
		},
		"missing site": func(s *ProviderFeedSnapshotSeed) { s.SourceSite = nil },
	} {
		t.Run(name, func(t *testing.T) {
			changed := seed
			mutate(&changed)
			got, err := CanonicalFeedSnapshotContentHash([]ProviderFeedSnapshotSeed{changed})
			if err != nil {
				t.Fatal(err)
			}
			if got == original {
				t.Fatal("source evidence drift did not change snapshot identity")
			}
		})
	}
}
