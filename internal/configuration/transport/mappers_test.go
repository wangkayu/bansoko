package transport

import (
	"encoding/json"
	"reflect"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func TestGlobalSectionInputMapsToDomainWithoutChangingJSONValues(t *testing.T) {
	value := generated.GlobalSectionInput{
		SchemaVersion: "parser-section-v1",
		Value: map[string]interface{}{
			"episode_patterns": []interface{}{"S(\\d+)E(\\d+)"},
			"sample_limit":     1,
		},
	}

	mapped := GlobalSectionInput(value)
	want := configurationdomain.GlobalSectionInput{
		SchemaVersion: "parser-section-v1",
		Value: map[string]interface{}{
			"episode_patterns": []interface{}{"S(\\d+)E(\\d+)"},
			"sample_limit":     1,
		},
	}
	if !reflect.DeepEqual(mapped, want) {
		t.Fatalf("mapped input = %#v, want %#v", mapped, want)
	}
	if mapped.Value == nil {
		t.Fatal("mapped input value must not be nil")
	}
	value.Value["sample_limit"] = 2
	if mapped.Value["sample_limit"] != 1 {
		t.Fatal("input mapper must not alias the HTTP value map")
	}
}

func TestGlobalSectionViewPreservesHTTPShapeAndNilValue(t *testing.T) {
	value := configurationdomain.GlobalSectionView{
		SectionName:   "parser",
		SchemaVersion: "parser-section-v1",
		Value:         map[string]interface{}{"sample_limit": 2},
		Version:       7,
	}
	mapped := GlobalSectionView(value)
	want := generated.GlobalSectionView{
		SectionName:   generated.GlobalSectionName("parser"),
		SchemaVersion: "parser-section-v1",
		Value:         map[string]interface{}{"sample_limit": 2},
		Version:       7,
	}
	assertJSONEqual(t, mapped, want)
	value.Value["sample_limit"] = 3
	if mapped.Value["sample_limit"] != 2 {
		t.Fatal("view mapper must not alias the domain value map")
	}

	nilValue := GlobalSectionView(configurationdomain.GlobalSectionView{
		SectionName:   "file_effect",
		SchemaVersion: "file-effect-v1",
		Version:       1,
	})
	if nilValue.Value != nil {
		t.Fatalf("nil domain value mapped to %#v, want nil", nilValue.Value)
	}
	assertJSONEqual(t, nilValue, generated.GlobalSectionView{
		SectionName:   generated.GlobalSectionName("file_effect"),
		SchemaVersion: "file-effect-v1",
		Value:         nil,
		Version:       1,
	})
}

func TestGlobalSectionViewsPreservesOrderAndVersions(t *testing.T) {
	mapped := GlobalSectionViews([]configurationdomain.GlobalSectionView{
		{SectionName: "parser", SchemaVersion: "parser-section-v1", Value: map[string]interface{}{}, Version: 4},
		{SectionName: "naming_rules", SchemaVersion: "naming-rules-v1", Value: nil, Version: 9},
	})
	if len(mapped) != 2 {
		t.Fatalf("mapped views=%d, want 2", len(mapped))
	}
	if mapped[0].SectionName != generated.GlobalSectionName("parser") || mapped[0].Version != 4 {
		t.Fatalf("first mapped view=%+v", mapped[0])
	}
	if mapped[1].SectionName != generated.GlobalSectionName("naming_rules") || mapped[1].Version != 9 || mapped[1].Value != nil {
		t.Fatalf("second mapped view=%+v", mapped[1])
	}
}

func TestQBittorrentMappersPreserveOperationalHTTPShape(t *testing.T) {
	torrent := configurationdomain.QBittorrentTorrentView{
		AddedOn:       11,
		Category:      "anime",
		DownloadSpeed: 12,
		Downloaded:    13,
		Eta:           14,
		Hash:          "abc123",
		Name:          "Show EP01",
		Progress:      0.75,
		SavePath:      "/downloads/show",
		Size:          15,
		State:         "downloading",
		Tags:          "bansoko-show",
		Uploaded:      16,
		UploadSpeed:   17,
	}
	assertJSONEqual(t, QBittorrentTorrents([]configurationdomain.QBittorrentTorrentView{torrent}), []generated.QBittorrentTorrent{{
		AddedOn:    int64Ptr(11),
		Category:   stringPtr("anime"),
		Dlspeed:    int64Ptr(12),
		Downloaded: int64Ptr(13),
		Eta:        int64Ptr(14),
		Hash:       "abc123",
		Name:       "Show EP01",
		Progress:   0.75,
		SavePath:   stringPtr("/downloads/show"),
		Size:       15,
		State:      "downloading",
		Tags:       stringPtr("bansoko-show"),
		Uploaded:   int64Ptr(16),
		Upspeed:    int64Ptr(17),
	}})

	file := configurationdomain.QBittorrentTorrentFileView{Name: "Show/Show.mkv", Size: 99, Progress: 0.5, Priority: 1}
	assertJSONEqual(t, QBittorrentTorrentFiles([]configurationdomain.QBittorrentTorrentFileView{file}), []generated.QBittorrentTorrentFile{{
		Name:     "Show/Show.mkv",
		Size:     99,
		Progress: 0.5,
		Priority: intPtr(1),
	}})

	status := configurationdomain.QBittorrentServerStatusView{
		Version:          "v4.6.0",
		ConnectionStatus: "connected",
		DownloadSpeed:    21,
		UploadSpeed:      22,
		FreeSpace:        23,
		TotalSize:        24,
	}
	assertJSONEqual(t, QBittorrentServerStatus(status), generated.QBittorrentServerStatus{
		Version:          "v4.6.0",
		ConnectionStatus: "connected",
		DlSpeed:          int64Ptr(21),
		UpSpeed:          int64Ptr(22),
		FreeSpace:        int64Ptr(23),
		TotalSize:        int64Ptr(24),
	})
}

func stringPtr(value string) *string { return &value }
func int64Ptr(value int64) *int64    { return &value }
func intPtr(value int) *int          { return &value }

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
