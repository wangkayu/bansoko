package httpapi

import (
	"encoding/json"
	"reflect"
	"testing"

	ownershipapplication "bansoko/internal/anime/ownership/application"
	"bansoko/internal/transport/httpapi/generated"
)

func TestMapDiscoverSubgroupsInputAndResultPreserveContractShape(t *testing.T) {
	input := mapDiscoverSubgroupsInput(generated.DiscoverSubgroupsInput{ExpectedVersion: 7})
	if input.ExpectedVersion != 7 {
		t.Fatalf("expected version = %d, want 7", input.ExpectedVersion)
	}

	manual := true
	mode := "fill_missing"
	offset := 2
	connectionRef := "conn-1"
	routeRef := "route-1"
	keywords := []ownershipapplication.NameKeywordRule{{Keyword: "WEB", Score: 12}}
	// Mikan group defaults hold Mikan numeric ids, never display names.
	defaults := []string{"58"}
	reason := "Discovered 1 group(s)."
	// The discovery read shares the owner projection, so it must carry the same
	// decision reason rather than dropping it at the transport hop.
	selectionReason := "policy_always"
	value := ownershipapplication.DiscoverSubgroupsResult{
		ManagedAnimeID: "anime-1",
		Outcome:        ownershipapplication.DiscoverSubgroupsOutcomeDiscovered,
		Reason:         &reason,
		Subgroups: ownershipapplication.DiscoverSubgroupsOwnerResult{
			ManagedAnimeID: "anime-1",
			Subgroups: ownershipapplication.SubgroupsSection{
				Entries: []ownershipapplication.SubgroupScoreEntry{{
					CountInLimit:      true,
					DisplayName:       "Group A",
					EffectiveSelected: true,
					GroupRef:          "group-a",
					ManualSelected:    &manual,
					Provider:          "anibt",
					Score:             88,
					SelectionMode:     "manual",
				}},
				MaxRss: 3,
				NameKeywords: &ownershipapplication.ProviderNameKeywordRules{
					Anibt: &keywords,
				},
				ProviderDefaults: &ownershipapplication.ProviderGroupDefaults{
					Mikan: &ownershipapplication.ProviderGroupDefault{Groups: defaults, Mode: "whitelist", Score: 40},
				},
			},
			EffectiveSelection: []ownershipapplication.ProviderGroupSelection{{
				CountInLimit:      true,
				DisplayName:       "Group A",
				EffectiveScore:    88,
				EffectiveSelected: true,
				GroupRef:          "group-a",
				ID:                "selection-1",
				ManualSelected:    &manual,
				Provider:          "anibt",
				ReasonCode:        &selectionReason,
				SelectionMode:     "manual",
				SubscriptionID:    "subscription-1",
				Version:           4,
			}},
			RSSources: []ownershipapplication.DiscoverRSSSource{{
				AnibtAuxMode:          &mode,
				GroupName:             "Group A",
				HttpFeedConnectionRef: &connectionRef,
				ID:                    "source-1",
				LocatorDisplay:        "https://example.test/feed.xml",
				NetworkRouteRef:       &routeRef,
				EpisodeOffset:         offset,
				ProviderProvenance: &ownershipapplication.DiscoverProviderProvenance{
					DiscoverySnapshotVersion: "snapshot-3",
					Provider:                 "anibt",
					ProviderEntryRef:         "entry-1",
					ProviderGroupRef:         "group-a",
				},
				SourceKind:     "provider_group",
				SubscriptionID: "subscription-1",
				Title:          "Group A RSS",
				Version:        5,
			}},
			Version: 6,
		},
	}

	mapped, err := mapDiscoverSubgroupsResult(value)
	if err != nil {
		t.Fatalf("map discovery result: %v", err)
	}
	if mapped.ManagedAnimeId != "anime-1" || mapped.Outcome != generated.DiscoverSubgroupsOutcomeDiscovered || mapped.Reason == nil || *mapped.Reason != reason {
		t.Fatalf("mapped result header = %+v", mapped)
	}
	if mapped.Subgroups.Subgroups.MaxRss != 3 || mapped.Subgroups.Subgroups.Entries[0].Provider != generated.ProviderKindAnibt {
		t.Fatalf("mapped subgroup section = %+v", mapped.Subgroups.Subgroups)
	}
	if mapped.Subgroups.EffectiveSelection[0].SelectionMode != generated.SelectionModeManual || mapped.Subgroups.EffectiveSelection[0].Version != 4 {
		t.Fatalf("mapped effective selection = %+v", mapped.Subgroups.EffectiveSelection)
	}
	if got := mapped.Subgroups.EffectiveSelection[0].ReasonCode; got == nil || *got != selectionReason {
		t.Fatalf("mapped selection reason = %v, want %q", got, selectionReason)
	}
	source := mapped.Subgroups.RssSources[0]
	if source.SourceKind != generated.SourceKindProviderGroup || source.AnibtAuxMode == nil || *source.AnibtAuxMode != generated.RSSSourceAnibtAuxModeFillMissing || source.ProviderProvenance == nil {
		t.Fatalf("mapped RSS source = %+v", source)
	}

	actualJSON, err := json.Marshal(mapped)
	if err != nil {
		t.Fatalf("marshal mapped result: %v", err)
	}
	var actual any
	if err := json.Unmarshal(actualJSON, &actual); err != nil {
		t.Fatalf("decode mapped JSON: %v", err)
	}
	var expected any
	if err := json.Unmarshal([]byte(`{"managed_anime_id":"anime-1","outcome":"discovered","reason":"Discovered 1 group(s).","subgroups":{"effective_selection":[{"count_in_limit":true,"display_name":"Group A","effective_score":88,"effective_selected":true,"group_ref":"group-a","id":"selection-1","manual_selected":true,"provider":"anibt","reason_code":"policy_always","score_breakdown":null,"selection_mode":"manual","subscription_id":"subscription-1","version":4}],"managed_anime_id":"anime-1","rss_sources":[{"anibt_aux_mode":"fill_missing","group_name":"Group A","http_feed_connection_ref":"conn-1","id":"source-1","locator_display":"https://example.test/feed.xml","network_route_ref":"route-1","episode_offset":2,"provider_provenance":{"discovery_snapshot_version":"snapshot-3","provider":"anibt","provider_entry_ref":"entry-1","provider_group_ref":"group-a"},"source_kind":"provider_group","subscription_id":"subscription-1","title":"Group A RSS","version":5}],"subgroups":{"entries":[{"count_in_limit":true,"display_name":"Group A","effective_selected":true,"group_ref":"group-a","manual_selected":true,"provider":"anibt","score":88,"selection_mode":"manual"}],"max_rss":3,"name_keywords":{"anibt":[{"keyword":"WEB","score":12}]},"provider_defaults":{"mikan":{"groups":["58"],"mode":"whitelist","score":40}}},"version":6}}`), &expected); err != nil {
		t.Fatalf("decode expected JSON: %v", err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("mapped JSON = %s, want %s", actualJSON, mustMarshalJSON(t, expected))
	}
}

func TestMapDiscoverSubgroupsOutcomesPreserveEnumStrings(t *testing.T) {
	for _, want := range []ownershipapplication.DiscoverSubgroupsOutcome{
		ownershipapplication.DiscoverSubgroupsOutcomeConfigError,
		ownershipapplication.DiscoverSubgroupsOutcomeDiscovered,
		ownershipapplication.DiscoverSubgroupsOutcomeNoGroups,
		ownershipapplication.DiscoverSubgroupsOutcomeProviderError,
	} {
		t.Run(string(want), func(t *testing.T) {
			mapped, err := mapDiscoverSubgroupsResult(ownershipapplication.DiscoverSubgroupsResult{Outcome: want})
			if err != nil {
				t.Fatalf("map discovery result: %v", err)
			}
			if string(mapped.Outcome) != string(want) {
				t.Fatalf("outcome = %q, want %q", mapped.Outcome, want)
			}
		})
	}
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	result, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return result
}
