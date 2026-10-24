package application

import (
	"bansoko/internal/testutil/animetest"
	"reflect"
	"testing"
	"time"
)

func fileSelectionCanonicalSeed() SelectedSeed {
	return SelectedSeed{Provider: "mikan", ProviderEntryRef: "entry-1", ProviderGroupRef: "group-1", Locator: "https://torrent.example.test/one-shot.torrent", Title: "[Group] Example Anime - 01 [1080p]", PublishedAt: time.Unix(1000, 0).UTC(), InfoHash: animetest.StringPtr("0123456789abcdef0123456789abcdef01234567")}
}

func TestOneShotHasNoManualFilterInputs(t *testing.T) {
	for _, value := range []any{AcquireSeedInput{}, SnapshotSeedDownloadInput{}, OneShotInputSnapshot{}} {
		typ := reflect.TypeOf(value)
		for _, name := range []string{"SelectedFileRefs", "OverrideFilter", "ResolvedSelection"} {
			if _, exists := typ.FieldByName(name); exists {
				t.Fatalf("%s still accepts %s", typ, name)
			}
		}
	}
}
