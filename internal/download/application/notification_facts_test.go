package application

import (
	"database/sql"
	"strings"
	"testing"

	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
)

func TestDownloadSubmittedFactsFillEpisodeAndCatalog(t *testing.T) {
	row := downloadNotificationRow{
		animeTitle: "示例动画", titleOverride: "自定义",
		rawFacts:        []byte(`{"title":"[组] 示例动画 13","published_at":"2026-01-02T03:04:05Z","author":"组"}`),
		inputSnapshot:   []byte(`{"source":{"provider":{"state":"known","value":"mikan"},"group":{"state":"known","value":"示例组"},"infohash_v1":{"state":"known","value":"0123456789abcdef0123456789abcdef01234567"},"infohash_v2":{"state":"absent","value":null}},"media":{"bgm":{"state":"known","value":{"ref":"42","version":1}},"tmdb":{"state":"absent","value":null}},"config":{"identity":{"kind":"submitted","managed_anime_ref":{"state":"known","value":"anime-1"}}}}`),
		clientName:      sql.NullString{String: "家里的 qB", Valid: true},
		releaseIdentity: []byte(`{"State":"resolved","Kind":"tv_episode","Shape":"single","RawEpisode":"13","Offset":-12,"Episode":"1"}`),
	}
	facts := downloadSubmittedFacts(row, "anime-1", taskSnapshot{id: "task-1", releaseID: "release-1"}, providerCorrelation{Name: "[组] 示例动画 13", ExternalID: "x", InfoHash: "h"})
	for key, want := range map[string]any{
		"ep.display": "1", "ep.number": "1", "ep.raw": "13", "download.client": "家里的 qB",
		"anime.title": "自定义", "anime.title_override": "自定义", "release.id": "release-1", "release.src": "mikan",
		"bgm.id": "42", "release.published_at": "2026-01-02T03:04:05Z", "anime.group": "示例组",
	} {
		if facts[key] != want {
			t.Errorf("%s=%v, want %v", key, facts[key], want)
		}
	}
	if offset := facts["ep.offset"]; offset == nil || offset.(interface{ String() string }).String() != "-12" {
		t.Errorf("ep.offset=%v", offset)
	}
	for _, key := range notification.PayloadKeys(plugintargetport.TriggerDownloadSubmitted) {
		if key == "event.at" {
			continue // filled by the admission resolver from the ledger row
		}
		if _, ok := facts[key]; !ok {
			t.Errorf("payload never writes %s", key)
		}
	}
	unresolved := downloadSubmittedFacts(downloadNotificationRow{releaseIdentity: []byte(`{"State":"unresolved"}`)}, "anime-1", taskSnapshot{}, providerCorrelation{})
	if unresolved["ep.display"] != nil || unresolved["download.client"] != nil {
		t.Fatalf("unresolved identity facts=%v", unresolved)
	}
}

// A batch plan has no single episode: ep.* are empty and
// release.episodes labels every planned episode; a single episode keeps its
// episode facts and labels the one episode.
func TestDownloadSubmittedFactsLabelTheBatchEpisodes(t *testing.T) {
	identity := []byte(`{"State":"resolved","Kind":"tv_episode","Shape":"single","RawEpisode":"1","Offset":0,"Episode":"1"}`)
	batch := downloadSubmittedFacts(downloadNotificationRow{releaseIdentity: identity, coverageKeys: []byte(`["tv:e1","tv:e2","tv:e3","tv:e7"]`)}, "anime-1", taskSnapshot{}, providerCorrelation{})
	if batch["release.episodes"] != "01-03, 07" {
		t.Fatalf("release.episodes = %v", batch["release.episodes"])
	}
	for _, key := range []string{"ep.display", "ep.number", "ep.raw", "ep.offset"} {
		if batch[key] != nil {
			t.Errorf("batch %s = %v, want empty", key, batch[key])
		}
	}
	single := downloadSubmittedFacts(downloadNotificationRow{releaseIdentity: identity, coverageKeys: []byte(`["tv:e1"]`)}, "anime-1", taskSnapshot{}, providerCorrelation{})
	if single["release.episodes"] != "01" || single["ep.display"] != "1" {
		t.Fatalf("single = %v / %v", single["release.episodes"], single["ep.display"])
	}
	if movie := downloadSubmittedFacts(downloadNotificationRow{coverageKeys: []byte(`["movie:0"]`)}, "anime-1", taskSnapshot{}, providerCorrelation{}); movie["release.episodes"] != nil {
		t.Fatalf("movie release.episodes = %v", movie["release.episodes"])
	}
}

func TestTorrentTreeTextRendersTheRetainedPayload(t *testing.T) {
	// A real two-file torrent: one video, one nested subtitle.
	payload := []byte("d4:infod5:filesld6:lengthi10e4:pathl7:ep1.mkveed6:lengthi20e4:pathl4:Subs7:ep1.asseee4:name7:Show S1ee")
	tree, ok := torrentTreeText(payload).(string)
	if !ok {
		t.Fatalf("tree = %v", torrentTreeText(payload))
	}
	if !strings.Contains(tree, "Subs/") || !strings.Contains(tree, "ep1.mkv") || !strings.Contains(tree, "合计: 30 B · 2 files") {
		t.Fatalf("tree =\n%s", tree)
	}
	if torrentTreeText(nil) != nil || torrentTreeText([]byte("not bencode")) != nil {
		t.Fatalf("an absent or unreadable payload must leave the fact empty")
	}
}
