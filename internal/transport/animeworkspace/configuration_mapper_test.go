package animeworkspace

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	readmodelapplication "bansoko/internal/anime/readmodel/application"
)

func TestMapAnimeWorkspaceConfigurationParityAndIsolation(t *testing.T) {
	full := minimalWorkspace()
	retired := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	full.ManagedAnime.Version = 7
	full.Subscription.Version = 8
	full.Subscription.LifecycleState = "retired"
	full.Subscription.LifecycleEpoch = 9
	full.Subscription.RetiredAt = &retired
	full.Metadata.Version = 10
	full.Subgroups.Version = 11
	full.Rules.Version = 12
	full.ConfigWaitingFiles = 13
	full.EpisodeProgress = &readmodelapplication.EpisodeProgress{Episodes: []int{1, 2}, Done: false}
	latest := "2026-11-10T09:00:00+08:00"
	input := readmodelapplication.AnimeWorkspaceConfiguration{
		ManagedAnime: full.ManagedAnime, Subscription: full.Subscription,
		Metadata: full.Metadata, MetadataHold: full.MetadataHold, Subgroups: full.Subgroups, Rules: full.Rules,
		RSSSources: full.RSSSources, RSSSourceRuntimes: full.RSSSourceRuntimes,
		EpisodeProgress: full.EpisodeProgress, ConfigWaitingFiles: full.ConfigWaitingFiles,
		ReleaseSummary: readmodelapplication.AnimeWorkspaceReleaseSummary{
			Count: 501, LatestPublishedAt: &latest, OwnerChainStable: true,
			TagObservations: []readmodelapplication.AnimeWorkspaceTagObservation{{AniBTTags: []string{"tag:A", "tag:a", "tag:A"}}},
		},
	}
	mapped, err := MapAnimeWorkspaceConfiguration(input)
	if err != nil {
		t.Fatal(err)
	}
	mappedFull, err := MapAnimeWorkspace(full)
	if err != nil {
		t.Fatal(err)
	}
	asObject := func(value any) map[string]json.RawMessage {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	fullObject, configObject := asObject(mappedFull), asObject(mapped)
	for _, key := range []string{"managed_anime", "subscription", "metadata", "metadata_hold", "subgroups", "rules", "rss_sources", "rss_source_runtimes", "episode_progress", "config_waiting_files"} {
		if string(fullObject[key]) != string(configObject[key]) {
			t.Errorf("configuration %s differs from full: %s / %s", key, configObject[key], fullObject[key])
		}
	}
	for _, key := range []string{"releases", "download_tasks", "artifacts", "file_operations", "placement_handoffs", "acquisition_targets", "target_observations", "logs"} {
		if _, exists := configObject[key]; exists {
			t.Errorf("history field %s must be absent, not an empty array", key)
		}
	}
	if len(configObject) != 11 || mapped.ReleaseSummary.Count != 501 || !mapped.ReleaseSummary.OwnerChainStable {
		t.Fatalf("unexpected configuration contract: %+v", mapped)
	}
	if mapped.ReleaseSummary.TagObservations[0].TitleTags == nil {
		t.Fatal("missing tag kind must be []")
	}
	mapped.ReleaseSummary.TagObservations[0].AnibtTags[0] = "changed"
	*mapped.ReleaseSummary.LatestPublishedAt = "changed"
	mapped.EpisodeProgress.Episodes[0] = 99
	*mapped.Subscription.RetiredAt = time.Time{}
	if !reflect.DeepEqual(input.ReleaseSummary.TagObservations[0].AniBTTags, []string{"tag:A", "tag:a", "tag:A"}) || latest == "changed" || full.EpisodeProgress.Episodes[0] != 1 || full.Subscription.RetiredAt.IsZero() {
		t.Fatal("configuration mapper aliases application values")
	}
	input.ReleaseSummary = readmodelapplication.AnimeWorkspaceReleaseSummary{}
	empty, err := MapAnimeWorkspaceConfiguration(input)
	if err != nil {
		t.Fatal(err)
	}
	if empty.ReleaseSummary.TagObservations == nil || empty.ReleaseSummary.LatestPublishedAt != nil {
		t.Fatalf("empty summary shape = %+v", empty.ReleaseSummary)
	}
}
