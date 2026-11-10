package application

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	ownershipapplication "bansoko/internal/anime/ownership/application"
)

func TestWorkspaceConfigurationSourceHistoryStability(t *testing.T) {
	for _, tc := range []struct {
		name    string
		history workspaceSourceHistory
		stable  bool
	}{
		{"failed artifacts need no operations", workspaceSourceHistory{ReleaseCount: 1}, true},
		{"verified artifacts with terminal operations", workspaceSourceHistory{ReleaseCount: 1}, true},
		{"pending or waiting retry artifact", workspaceSourceHistory{ReleaseCount: 1, UnverifiedArtifacts: true}, false},
		{"verified artifact without operations", workspaceSourceHistory{ReleaseCount: 1, VerifiedWithoutOperations: true}, false},
		{"unfinished operation even on failed artifact", workspaceSourceHistory{ReleaseCount: 1, UnfinishedOperations: true}, false},
		{"failed task without artifacts", workspaceSourceHistory{ReleaseCount: 1}, true},
		{"completed or cancelled task without artifacts", workspaceSourceHistory{ReleaseCount: 1, UnfinishedTasksWithoutArtifacts: true}, false},
		{"all stable taskless verdicts", workspaceSourceHistory{ReleaseCount: 9, TasklessVerdicts: []string{"waiting_metadata", "subscription_stopped", "source_unavailable", "parse_failed", "filtered", "adopted_existing", "acquisition_failed", "resume_baseline", "start_baseline"}}, true},
		{"old unstable release among many", workspaceSourceHistory{ReleaseCount: 501, TasklessVerdicts: []string{"filtered", "discovered"}}, false},
		{"not a terminal verdict whitelist member", workspaceSourceHistory{ReleaseCount: 1, TasklessVerdicts: []string{"accepted_pending_download"}}, false},
		{"unknown verdict fails closed", workspaceSourceHistory{ReleaseCount: 1, TasklessVerdicts: []string{"future_verdict"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := workspaceSourceHistoryStable(tc.history); got != tc.stable {
				t.Fatalf("stable = %v, want %v", got, tc.stable)
			}
		})
	}
}

func TestWorkspaceConfigurationOwnerSelectionStability(t *testing.T) {
	custom := RSSSource{ID: "custom", SourceKind: "custom_rss"}
	provider := RSSSource{ID: "provider", SourceKind: "provider_group", ProviderProvenance: &animedomain.ProviderProvenance{Provider: "mikan", ProviderGroupRef: "group"}}
	owners := AnimeWorkspace{}
	check := func(want bool, history map[string]workspaceSourceHistory) {
		t.Helper()
		if got := workspaceOwnerChainStable(owners, history); got != want {
			t.Fatalf("stable = %v, want %v; owners=%+v history=%+v", got, want, owners, history)
		}
	}
	check(false, nil)
	owners.RSSSources = []RSSSource{custom, provider}
	check(false, nil) // selected custom source has no runtime
	owners.RSSSourceRuntimes = []RSSSourceRuntime{{RSSSourceID: "custom", Health: "unknown"}}
	check(false, nil)
	for _, health := range []string{"healthy", "degraded", "failed"} {
		owners.RSSSourceRuntimes[0].Health = health
		check(true, nil) // no release, any known health; unselected provider ignored
	}
	owners.Subgroups.EffectiveSelection = []ownershipapplication.ProviderGroupSelection{{Provider: "anibt", GroupRef: "group", EffectiveSelected: true}}
	check(true, nil) // matching group ref from another provider is not selected
	owners.Subgroups.EffectiveSelection[0].Provider = "mikan"
	check(false, nil)
	owners.RSSSourceRuntimes = append(owners.RSSSourceRuntimes, RSSSourceRuntime{RSSSourceID: "provider", Health: "healthy"})
	check(true, nil)
	check(false, map[string]workspaceSourceHistory{"provider": {ReleaseCount: 501, TasklessVerdicts: []string{"discovered"}}})
	owners.RSSSourceRuntimes[1].Health = "unknown"
	check(true, map[string]workspaceSourceHistory{"provider": {ReleaseCount: 501, TasklessVerdicts: []string{"filtered"}}}) // releases trump runtime
	owners.RSSSources = []RSSSource{provider}
	owners.Subgroups.EffectiveSelection[0].EffectiveSelected = false
	check(false, nil) // zero active is never stable
	owners.Subgroups.EffectiveSelection[0].EffectiveSelected = true
	owners.RSSSources[0].ProviderProvenance = nil
	check(false, nil)
}

func TestWorkspaceConfigurationReleaseObservations(t *testing.T) {
	summary := AnimeWorkspaceReleaseSummary{TagObservations: []AnimeWorkspaceTagObservation{}}
	for _, tc := range []struct{ published, anibt, title string }{
		{`"2026-11-10T09:00:00+08:00"`, `["tag:HEVC","tag:hevc","tag:HEVC"]`, `["Codec:H265","codec:h265","Codec:H265"]`},
		{`"2026-11-10T02:00:00Z"`, `null`, `[]`}, // chronologically newer, lexically older
		{`null`, `[]`, `["Language:CHS"]`},
		{"", "", ""},
	} {
		if err := appendWorkspaceReleaseObservation(&summary, []byte(tc.published), []byte(tc.anibt), []byte(tc.title)); err != nil {
			t.Fatal(err)
		}
	}
	if summary.Count != 4 || summary.LatestPublishedAt == nil || *summary.LatestPublishedAt != "2026-11-10T09:00:00+08:00" {
		t.Fatalf("count/latest = %+v", summary)
	}
	if len(summary.TagObservations) != 2 {
		t.Fatalf("non-empty observations = %+v", summary.TagObservations)
	}
	var full SourceFacts
	if err := json.Unmarshal([]byte(`{"anibt":{"tags":["tag:HEVC","tag:hevc","tag:HEVC"]},"title_tags":{"tags":["Codec:H265","codec:h265","Codec:H265"]}}`), &full); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(summary.TagObservations[0].AniBTTags, full.AniBT.Tags) || !reflect.DeepEqual(summary.TagObservations[0].TitleTags, full.TitleTags.Tags) {
		t.Fatalf("tag order, case, duplicates or existing classification drifted: %+v", summary.TagObservations[0])
	}
	var noDate AnimeWorkspaceReleaseSummary
	if err := appendWorkspaceReleaseObservation(&noDate, nil, nil, nil); err != nil || noDate.LatestPublishedAt != nil {
		t.Fatalf("missing publication date must stay absent: %+v, %v", noDate, err)
	}
	// Match the string sent by the full workspace, including JSON's trimming of
	// fractional trailing zeros and the existing offset representation.
	for _, raw := range []string{`"2026-11-10T09:00:00.100000+08:00"`, `"2026-11-10T09:00:00+00:00"`} {
		var fullTime time.Time
		if err := json.Unmarshal([]byte(raw), &fullTime); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(fullTime)
		if err != nil {
			t.Fatal(err)
		}
		var want string
		if err := json.Unmarshal(encoded, &want); err != nil {
			t.Fatal(err)
		}
		var got AnimeWorkspaceReleaseSummary
		if err := appendWorkspaceReleaseObservation(&got, []byte(raw), nil, nil); err != nil {
			t.Fatal(err)
		}
		if got.LatestPublishedAt == nil || *got.LatestPublishedAt != want {
			t.Fatalf("latest = %v, want %q", got.LatestPublishedAt, want)
		}
	}
}
