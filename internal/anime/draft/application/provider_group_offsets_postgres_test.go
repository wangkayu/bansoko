package application

import (
	"context"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/presetdraft"
	"bansoko/internal/transport/httpapi/generated"
)

// draftNoConnections lists no provider connection, so the provider reader
// endpoint falls back to the built-in default.
type draftNoConnections struct{}

func (draftNoConnections) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return nil, nil
}

// Subscribe turns a provider draft's non-zero group offsets into link rows in
// its own transaction: no network read and no work. Zero entries and groups the
// draft does not list create nothing.
func TestSubscribeCreatesLinkRowsForProviderGroupOffsets(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	draft := providerDraft(t, "3100", "Offset Draft Show")
	entry := func(ref string) generated.SubgroupScoreEntry {
		return generated.SubgroupScoreEntry{Provider: generated.ProviderKindMikan, GroupRef: ref, DisplayName: "Group " + ref, SelectionMode: generated.SelectionModeAuto, EffectiveSelected: true, Score: 50}
	}
	draft.Subgroups.Entries = []generated.SubgroupScoreEntry{entry("301"), entry("302")}
	offsets := []generated.ProviderGroupOffsetDraft{
		{ProviderGroupRef: "301", EpisodeOffset: -48},
		{ProviderGroupRef: "302", EpisodeOffset: 0},
		{ProviderGroupRef: "999", EpisodeOffset: 7},
	}
	draft.ProviderGroupOffsets = &offsets

	result, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, draft)}, "group-offsets-subscribe-0001")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	rows, err := db.QueryContext(ctx, `SELECT provider_group_ref, episode_offset, discovery_snapshot_version FROM rss_sources WHERE subscription_id = $1 ORDER BY provider_group_ref`, result.Subscription.Id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type link struct {
		offset   int
		snapshot string
	}
	got := map[string]link{}
	for rows.Next() {
		var ref string
		var l link
		if err := rows.Scan(&ref, &l.offset, &l.snapshot); err != nil {
			t.Fatal(err)
		}
		got[ref] = l
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["301"].offset != -48 || got["301"].snapshot != "pending" {
		t.Fatalf("link rows = %+v, want only group 301 at -48 with the pending snapshot", got)
	}
	var work int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items WHERE kind = 'rss_refresh'`).Scan(&work); err != nil {
		t.Fatal(err)
	}
	if work != 0 {
		t.Fatalf("subscribe scheduled %d refreshes for pending links", work)
	}
}
