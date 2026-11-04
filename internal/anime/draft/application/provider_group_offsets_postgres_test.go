package application

import (
	"context"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/subscription/groupsource"
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
// its own transaction: no network read and no work. Zero entries create nothing;
// an observed group need not also be listed in the subgroup settings.
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
	if len(got) != 2 || got["301"].offset != -48 || got["301"].snapshot != "pending" || got["999"].offset != 7 || got["999"].snapshot != "pending" {
		t.Fatalf("link rows = %+v, want groups 301 at -48 and 999 at 7, both with the pending snapshot", got)
	}
	var work int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items WHERE kind = 'rss_refresh'`).Scan(&work); err != nil {
		t.Fatal(err)
	}
	if work != 0 {
		t.Fatalf("subscribe scheduled %d refreshes for pending links", work)
	}
}

func TestCandidateActivationKeepsResavedOffsetsForObservedGroups(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()

	draft := providerDraft(t, "3101", "Resaved Offset Show")
	draft.Subgroups.Entries = []generated.SubgroupScoreEntry{{
		Provider: generated.ProviderKindMikan, GroupRef: "301", DisplayName: "Group 301",
		SelectionMode: generated.SelectionModeAuto, EffectiveSelected: true, Score: 50,
	}}
	draft.ProviderGroupOffsets = &[]generated.ProviderGroupOffsetDraft{
		{ProviderGroupRef: "301", EpisodeOffset: -48},
		{ProviderGroupRef: "999", EpisodeOffset: 7},
	}
	candidate := saveCandidate(t, service, draft)

	// The RSS workspace can observe group 999 without adding it to the subgroup
	// settings. A second save must still be the revision activation materializes.
	draft.ProviderGroupOffsets = &[]generated.ProviderGroupOffsetDraft{
		{ProviderGroupRef: "301", EpisodeOffset: 24},
		{ProviderGroupRef: "999", EpisodeOffset: -6},
	}
	patched, err := service.PatchAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, candidateInput(t, service.db, draft))
	if err != nil {
		t.Fatalf("resave offsets: %v", err)
	}
	result, err := service.SubscribeAnimeConfigCandidate(ctx, patched.Id, patched.Version, "candidate-resaved-offsets", generated.CandidateSubscribeInput{})
	if err != nil {
		t.Fatalf("activate candidate: %v", err)
	}

	var name, snapshot string
	var offset int
	if err := db.QueryRowContext(ctx, `SELECT group_name, episode_offset, discovery_snapshot_version FROM rss_sources WHERE subscription_id = $1 AND provider_group_ref = '999'`, result.Subscription.Id).Scan(&name, &offset, &snapshot); err != nil {
		t.Fatalf("read observed group link: %v", err)
	}
	if offset != -6 || snapshot != groupsource.PendingSnapshotVersion || name == "" {
		t.Fatalf("observed group link name=%q offset=%d snapshot=%q, want a display name, -6 and pending", name, offset, snapshot)
	}
	if err := db.QueryRowContext(ctx, `SELECT episode_offset FROM rss_sources WHERE subscription_id = $1 AND provider_group_ref = '301'`, result.Subscription.Id).Scan(&offset); err != nil {
		t.Fatal(err)
	}
	if offset != 24 {
		t.Fatalf("resaved listed group offset = %d, want 24", offset)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, _, err := groupsource.UpsertTx(ctx, tx, groupsource.Source{
		SubscriptionID: result.Subscription.Id, Provider: "mikan", CatalogEntryRef: "3101", GroupExternalID: "999",
		DisplayName: "Observed Group 999", AnimeTitle: "Resaved Offset Show",
		RSSURL: "https://mikanani.me/RSS/Bangumi?bangumiId=3101&subgroupid=999", SnapshotVersion: "v9",
	}); err != nil {
		t.Fatalf("discovery upsert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT group_name, episode_offset, discovery_snapshot_version FROM rss_sources WHERE subscription_id = $1 AND provider_group_ref = '999'`, result.Subscription.Id).Scan(&name, &offset, &snapshot); err != nil {
		t.Fatal(err)
	}
	if name != "Observed Group 999" || offset != -6 || snapshot != "v9" {
		t.Fatalf("after discovery name=%q offset=%d snapshot=%q, want the observed name, kept offset -6 and v9", name, offset, snapshot)
	}
}
