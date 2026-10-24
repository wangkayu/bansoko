package application

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"bansoko/internal/catalog/domain"
)

func TestCatalogSnapshotHealthMapsToCardSummary(t *testing.T) {
	now := time.Now().UTC()
	active := catalogSnapshotHealth(domain.Snapshot{Status: domain.StatusFresh, HealthState: domain.HealthReady, ObservedAt: now})
	if active.State != "active" || active.LastSuccessAt == nil {
		t.Fatalf("ready snapshot health was lost: %+v", active)
	}
	degraded := catalogSnapshotHealth(domain.Snapshot{Status: domain.StatusOutage, HealthState: domain.HealthFailed, ObservedAt: now})
	if degraded.State != "degraded" || degraded.LastSuccessAt != nil {
		t.Fatalf("outage fabricated a successful observation: %+v", degraded)
	}
}

func TestBroadcastDayKeyMapsProviderScheduleFacts(t *testing.T) {
	cases := []struct {
		provider string
		hint     map[string]any
		want     string
	}{
		// Mikan data-dayofweek: 0=周日 … 6=周六 (pinned against the provider
		// contract); 7 (movie) and 8 (OVA) are formats, not days, and read as none.
		{ProviderMikan, map[string]any{"weekday": "0"}, "sun"},
		{ProviderMikan, map[string]any{"weekday": "1"}, "mon"},
		{ProviderMikan, map[string]any{"weekday": "3"}, "wed"},
		{ProviderMikan, map[string]any{"weekday": "6"}, "sat"},
		{ProviderMikan, map[string]any{"weekday": "7"}, "none"},
		{ProviderMikan, map[string]any{"weekday": "x"}, "none"},
		{ProviderMikan, map[string]any{"weekday": "8"}, "none"},
		{ProviderMikan, nil, "none"},
		// AniBT ships the Chinese weekdayLabel; 未定 is tbd and a missing label none.
		{ProviderAniBT, map[string]any{"weekdayLabel": "周一"}, "mon"},
		{ProviderAniBT, map[string]any{"weekdayLabel": "周日"}, "sun"},
		{ProviderAniBT, map[string]any{"weekdayLabel": "未定"}, "tbd"},
		{ProviderAniBT, map[string]any{"weekdayLabel": "星期"}, "none"},
		{ProviderAniBT, nil, "none"},
		// AniBT also ships a numeric weekday; only the label is read.
		{ProviderAniBT, map[string]any{"weekday": "3"}, "none"},
		// A custom program carries no provider schedule fact at all.
		{"custom", nil, "none"},
	}
	for _, tc := range cases {
		if got := BroadcastDayKey(tc.provider, tc.hint); got != tc.want {
			t.Errorf("%s %v = %q, want %q", tc.provider, tc.hint, got, tc.want)
		}
	}
}

func TestReleaseFormatKeyMapsProviderCatalogFacts(t *testing.T) {
	cases := []struct {
		provider string
		hint     map[string]any
		want     string
	}{
		// Mikan homepage sections: 0-6 weekly TV, 7 movie, 8 OVA.
		{ProviderMikan, map[string]any{"weekday": "0"}, "tv"},
		{ProviderMikan, map[string]any{"weekday": "6"}, "tv"},
		{ProviderMikan, map[string]any{"weekday": "7"}, "movie"},
		{ProviderMikan, map[string]any{"weekday": "8"}, "ova"},
		{ProviderMikan, map[string]any{"weekday": "9"}, ""},
		{ProviderMikan, nil, ""},
		// AniBT format field.
		{ProviderAniBT, map[string]any{"format": "TV"}, "tv"},
		{ProviderAniBT, map[string]any{"format": "MOVIE"}, "movie"},
		{ProviderAniBT, map[string]any{"format": "OVA"}, "ova"},
		{ProviderAniBT, map[string]any{"format": "ONA"}, "ona"},
		{ProviderAniBT, map[string]any{"format": ""}, ""},
		{ProviderAniBT, map[string]any{"format": "SPECIAL"}, ""},
		{ProviderAniBT, map[string]any{"kind": "MOVIE"}, ""},
		{"custom", nil, ""},
	}
	for _, tc := range cases {
		if got := ReleaseFormatKey(tc.provider, tc.hint); got != tc.want {
			t.Errorf("%s %v = %q, want %q", tc.provider, tc.hint, got, tc.want)
		}
	}
}

func TestApplyReleaseAvailabilityKeepsUnknownAbsent(t *testing.T) {
	long := strings.Repeat("长", 65)
	cases := []struct {
		name      string
		provider  string
		hint      map[string]any
		state     string
		count     *int
		updateTxt string
	}{
		{"anibt has", ProviderAniBT, map[string]any{"hasRelease": true, "rssReleaseCount": float64(3)}, domain.ReleaseStateHasReleases, intPtr(3), ""},
		{"anibt none", ProviderAniBT, map[string]any{"hasRelease": false, "rssReleaseCount": float64(0)}, domain.ReleaseStateNoReleases, intPtr(0), ""},
		{"anibt search hit has no facts", ProviderAniBT, map[string]any{"provider": "anibt", "cover": "x"}, "", nil, ""},
		{"anibt mistyped facts", ProviderAniBT, map[string]any{"hasRelease": "yes", "rssReleaseCount": float64(-1)}, "", nil, ""},
		{"anibt fractional count", ProviderAniBT, map[string]any{"rssReleaseCount": 1.5}, "", nil, ""},
		{"mikan greyout", ProviderMikan, map[string]any{"greyout": true, "update_text": ""}, domain.ReleaseStateNoReleases, nil, ""},
		{"mikan works", ProviderMikan, map[string]any{"greyout": false, "update_text": "2026/10/18"}, domain.ReleaseStateHasReleases, nil, "2026/10/18"},
		{"mikan old snapshot", ProviderMikan, map[string]any{"update_text": "2026/10/18"}, "", nil, "2026/10/18"},
		{"mikan over-long text", ProviderMikan, map[string]any{"update_text": long}, "", nil, ""},
		{"mikan ignores anibt keys", ProviderMikan, map[string]any{"hasRelease": true}, "", nil, ""},
		{"custom", "custom", map[string]any{"hasRelease": true, "greyout": true}, "", nil, ""},
		{"nil hint", ProviderAniBT, nil, "", nil, ""},
	}
	for _, tc := range cases {
		item := domain.Item{Provider: tc.provider, SourceHint: tc.hint}
		ApplyReleaseAvailability(&item)
		if item.ReleaseState != tc.state || item.UpdateText != tc.updateTxt || !reflect.DeepEqual(item.ReleaseCount, tc.count) {
			t.Errorf("%s: got state=%q count=%v text=%q", tc.name, item.ReleaseState, item.ReleaseCount, item.UpdateText)
		}
	}
}

func intPtr(v int) *int { return &v }

// TestApplyInstanceOverlayAssociatesDistinctOwnersForOneProviderEntry pins the
// no-collapse rule without a database: one external provider entry carrying
// several already-created owners exposes all associations (with each owner's
// subscription version) and leaves the singular compatibility summary empty,
// while a single association still fills that summary. This is the DB-free half
// of the PostgreSQL case in provider_service_postgres_test.go.
func TestApplyInstanceOverlayAssociatesDistinctOwnersForOneProviderEntry(t *testing.T) {
	ownerAssociations := domain.Item{AllowedActions: []string{"prepare_subscribe"}}
	applyInstanceOverlay(&ownerAssociations, []domain.InstanceLinkage{
		{ManagedAnimeRef: "anime-a", SubscriptionRef: "sub-a", SubscriptionState: "active", SubscriptionVersion: 7},
		{ManagedAnimeRef: "anime-b", SubscriptionRef: "sub-b", SubscriptionState: "stopped", SubscriptionVersion: 2},
	})
	if len(ownerAssociations.Instances) != 2 {
		t.Fatalf("expected both owner associations, got %+v", ownerAssociations.Instances)
	}
	if ownerAssociations.Instances[0].ManagedAnimeRef != "anime-a" || ownerAssociations.Instances[0].SubscriptionVersion != 7 {
		t.Fatalf("first owner association is wrong: %+v", ownerAssociations.Instances[0])
	}
	if ownerAssociations.Instances[1].ManagedAnimeRef != "anime-b" || ownerAssociations.Instances[1].SubscriptionVersion != 2 {
		t.Fatalf("second owner association is wrong: %+v", ownerAssociations.Instances[1])
	}
	if ownerAssociations.Instances[0].ManagedAnimeRef == ownerAssociations.Instances[1].ManagedAnimeRef {
		t.Fatalf("distinct associations must carry distinct managed owner IDs: %+v", ownerAssociations.Instances)
	}
	if ownerAssociations.Instances[0].SubscriptionRef == ownerAssociations.Instances[1].SubscriptionRef {
		t.Fatalf("distinct associations must carry distinct subscription IDs: %+v", ownerAssociations.Instances)
	}
	if ownerAssociations.ManagedAnimeRef != "" || ownerAssociations.SubscriptionRef != "" || ownerAssociations.SubscriptionState != "" {
		t.Fatalf("multi-owner entry leaked an arbitrary singular summary: %+v", ownerAssociations)
	}
	if !reflect.DeepEqual(ownerAssociations.AllowedActions, []string{"prepare_subscribe"}) {
		t.Fatalf("multiple-owner entry actions drifted: %v", ownerAssociations.AllowedActions)
	}

	single := domain.Item{AllowedActions: []string{"prepare_subscribe"}}
	applyInstanceOverlay(&single, []domain.InstanceLinkage{
		{ManagedAnimeRef: "anime-a", SubscriptionRef: "sub-a", SubscriptionState: "stopped", SubscriptionVersion: 4},
	})
	if single.ManagedAnimeRef != "anime-a" || single.SubscriptionRef != "sub-a" || single.SubscriptionState != "stopped" {
		t.Fatalf("single owner association did not fill the compatibility summary: %+v", single)
	}
	if !reflect.DeepEqual(single.AllowedActions, []string{"open_workbench", "start"}) {
		t.Fatalf("single owner association actions drifted: %v", single.AllowedActions)
	}

	// An entry without any owner association stays a browse entry: nothing is invented.
	unmanaged := domain.Item{AllowedActions: []string{"prepare_subscribe"}}
	applyInstanceOverlay(&unmanaged, nil)
	if unmanaged.ManagedAnimeRef != "" || len(unmanaged.Instances) != 0 {
		t.Fatalf("unmanaged entry fabricated owner association: %+v", unmanaged)
	}
}
