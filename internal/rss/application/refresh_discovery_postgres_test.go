package application

import (
	"encoding/json"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/workitems"
)

// TestDiscoveryEntryCountsFeedOutcomes proves the discovery entry says what
// became of its batch at feed level: one accepted item handed to evaluation,
// one filtered item with the filter reason that rejected it.
func TestDiscoveryEntryCountsFeedOutcomes(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, subscriptionID := seedRSSOwner(t, db, false, true)
	// Reject titles carrying Banner, so one of the two feed items is filtered
	// at the feed pass and the other is handed to evaluation.
	if _, err := db.Exec(`
UPDATE anime_config_overrides SET value = $1::jsonb, updated_at = NOW()
WHERE managed_anime_id = 'anime-rss-test' AND section_name = 'release_rules'`,
		`{"must_contain":"","not_in":"Banner"}`); err != nil {
		t.Fatal(err)
	}
	accepted := discoveryFeedItem(t, 1)
	blocked := discoveryFeedItem(t, 2)
	blocked.Title = "[Group] Test Show - 02 Banner 1080p"
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{accepted, blocked}})
	if err != nil {
		t.Fatal(err)
	}

	runDiscoveryRefresh(t, db, store, service, sourceID, "discovery")

	var raw string
	if err := db.QueryRow(`
SELECT details::text FROM subscription_log_entries
WHERE subscription_id = $1 AND kind = 'rss_new_content'`, subscriptionID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var details struct {
		ItemCount      int            `json:"item_count"`
		OutcomeCounts  map[string]int `json:"outcome_counts"`
		FilteredReason map[string]int `json:"filtered_reasons"`
	}
	if err := json.Unmarshal([]byte(raw), &details); err != nil {
		t.Fatalf("decode details %s: %v", raw, err)
	}
	if details.ItemCount != 2 {
		t.Fatalf("item_count = %d, want 2 (details %s)", details.ItemCount, raw)
	}
	wantCounts := map[string]int{"accepted_pending_download": 1, "filtered": 1}
	if len(details.OutcomeCounts) != len(wantCounts) {
		t.Fatalf("outcome_counts = %v, want %v (details %s)", details.OutcomeCounts, wantCounts, raw)
	}
	for verdict, want := range wantCounts {
		if details.OutcomeCounts[verdict] != want {
			t.Fatalf("outcome_counts[%s] = %d, want %d (details %s)", verdict, details.OutcomeCounts[verdict], want, raw)
		}
	}
	if details.FilteredReason["filtered_by_rules"] != 1 {
		t.Fatalf("filtered_reasons = %v, want filtered_by_rules: 1 (details %s)", details.FilteredReason, raw)
	}
}
