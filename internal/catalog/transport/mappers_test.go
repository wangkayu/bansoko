package transport

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func assertJSONEqual(t *testing.T, left, right any) {
	t.Helper()
	leftJSON, err := json.Marshal(left)
	if err != nil {
		t.Fatal(err)
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		t.Fatal(err)
	}
	var leftValue, rightValue any
	if err := json.Unmarshal(leftJSON, &leftValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rightJSON, &rightValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(leftValue, rightValue) {
		t.Fatalf("JSON differs:\nleft:  %s\nright: %s", leftJSON, rightJSON)
	}
}

func TestProviderGroupDiscoveryResultPreservesHTTPShape(t *testing.T) {
	observedAt := time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC)
	value := catalogdomain.ProviderGroupDiscoveryResult{
		Groups: []catalogdomain.ProviderGroupDiscoveryGroup{{
			Provider:    "mikan",
			GroupRef:    "678",
			DisplayName: "Example",
			FeedURL:     "https://mikan.example/RSS/Bangumi?bangumiId=123&subgroupid=678",
			ObservedAt:  observedAt,
		}},
		ObservedAt:       observedAt,
		Provider:         "mikan",
		ProviderEntryRef: "123",
		SchemaVersion:    "groups-v1",
		SnapshotVersion:  4,
		SourceVersion:    "source-4",
	}
	want := generated.ProviderGroupDiscoveryResult{
		Groups: []generated.ProviderGroupDiscoveryGroup{{
			Provider:    generated.ProviderKindMikan,
			GroupRef:    "678",
			DisplayName: "Example",
			FeedUrl:     "https://mikan.example/RSS/Bangumi?bangumiId=123&subgroupid=678",
			ObservedAt:  observedAt,
		}},
		ObservedAt:       observedAt,
		Provider:         generated.ProviderKindMikan,
		ProviderEntryRef: "123",
		SchemaVersion:    "groups-v1",
		SnapshotVersion:  4,
		SourceVersion:    "source-4",
	}
	assertJSONEqual(t, want, ProviderGroupDiscoveryResult(value))
}

func TestProviderRSSPreviewResultPreservesHTTPShape(t *testing.T) {
	publishedAt := time.Date(2026, 9, 3, 2, 3, 4, 0, time.UTC)
	value := catalogdomain.ProviderRSSPreviewResult{
		GroupRef:         "678",
		ObservedAt:       publishedAt,
		Provider:         "anibt",
		ProviderEntryRef: "123",
		Seeds: []catalogdomain.ProviderRSSPreviewSeed{{
			DownloadURL: "https://anibt.example/torrent/1",
			InfoHash:    stringPtr("abc123"),
			PublishedAt: publishedAt,
			SizeBytes:   int64Ptr(1024),
			Title:       "Example 01",
		}},
	}
	want := generated.ProviderRSSPreviewResult{
		GroupRef:         "678",
		ObservedAt:       &publishedAt,
		Provider:         generated.ProviderRSSPreviewResultProviderAnibt,
		ProviderEntryRef: "123",
		Seeds: []generated.ProviderRSSPreviewSeed{{
			DownloadUrl:   "https://anibt.example/torrent/1",
			InfoHash:      stringPtr("abc123"),
			PublishedAt:   &publishedAt,
			SizeBytes:     int64Ptr(1024),
			StableSeedKey: "",
			Title:         "Example 01",
		}},
	}
	assertJSONEqual(t, want, ProviderRSSPreviewResult(value))
}

func stringPtr(value string) *string { return &value }
func int64Ptr(value int64) *int64    { return &value }
