package application

import (
	"context"
	"testing"

	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// The ignore-multi-video toggle settles undecided releases in its own
// transaction: turning it on writes filtered/multi_video_files directly for
// awaiting releases whose stored manifest carries more than two video files;
// turning it off re-enqueues only the releases the option filtered. Releases
// with another verdict stay untouched, and a no-op call keeps the version.
func TestIgnoreMultiVideoToggleSweepsUndecidedReleases(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	sub := subscribeCustomDraft(t, service, "ignore-multi-video-key", "https://feeds.example.com/multivideo.xml", "multivideo")
	sourceID := sub.RssSources[0].Id
	animeID := sub.ManagedAnime.Id
	seedRelease := func(id, verdict, reason, files string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, internal_facts, verdict, reason_code)
VALUES ($1, $2, $3, '{"title":"[Group] Show - 01"}', '{}', jsonb_build_object('release_files', $4::jsonb), $5, $6)`,
			id, sourceID, id, files, verdict, reason); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	threeVideos := `[{"filename":"Show - 01.mkv","media_kind":"video"},{"filename":"Show - 02.mkv","media_kind":"video"},{"filename":"Extras/Show - 03.mkv","media_kind":"video"}]`
	twoVideos := `[{"filename":"Show - 01.mkv","media_kind":"video"},{"filename":"Show - 02.mkv","media_kind":"video"}]`
	seedRelease("mv-awaiting-pack", "awaiting_competition", "awaiting_competition", threeVideos)
	seedRelease("mv-awaiting-single", "awaiting_competition", "awaiting_competition", twoVideos)
	seedRelease("mv-filtered-tag", "filtered", "title_tag", threeVideos)

	releaseState := func(id string) (verdict, reason, detail string, version int64) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT verdict, reason_code, COALESCE(reason_detail,''), version FROM releases WHERE id = $1`, id).Scan(&verdict, &reason, &detail, &version); err != nil {
			t.Fatal(err)
		}
		return
	}

	version := sub.Subscription.Version
	on, err := service.SetIgnoreMultiVideoReleases(ctx, animeID, IgnoreMultiVideoInput{ExpectedVersion: version, Enabled: true})
	if err != nil {
		t.Fatalf("turn on: %v", err)
	}
	if !on.IgnoreMultiVideoReleases || on.Version != version+1 {
		t.Fatalf("on result = %+v", on)
	}
	if verdict, reason, detail, _ := releaseState("mv-awaiting-pack"); verdict != "filtered" || reason != "multi_video_files" || detail != "video files: 3" {
		t.Fatalf("the multi-video pack must be swept: %s/%s/%s", verdict, reason, detail)
	}
	if verdict, _, _, version := releaseState("mv-awaiting-single"); verdict != "awaiting_competition" || version != 1 {
		t.Fatalf("a two-video release must stay awaiting: %s v%d", verdict, version)
	}
	if verdict, reason, _, _ := releaseState("mv-filtered-tag"); verdict != "filtered" || reason != "title_tag" {
		t.Fatalf("another filter reason must stay: %s/%s", verdict, reason)
	}

	// A repeat call with the same value is a no-op: no version bump, no sweep.
	same, err := service.SetIgnoreMultiVideoReleases(ctx, animeID, IgnoreMultiVideoInput{ExpectedVersion: on.Version, Enabled: true})
	if err != nil {
		t.Fatalf("repeat on: %v", err)
	}
	if same.Version != on.Version {
		t.Fatalf("repeat call bumped the version: %d -> %d", on.Version, same.Version)
	}

	off, err := service.SetIgnoreMultiVideoReleases(ctx, animeID, IgnoreMultiVideoInput{ExpectedVersion: on.Version, Enabled: false})
	if err != nil {
		t.Fatalf("turn off: %v", err)
	}
	if off.IgnoreMultiVideoReleases || off.Version != on.Version+1 {
		t.Fatalf("off result = %+v", off)
	}
	var scheduled int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_items
WHERE owner_id = 'mv-awaiting-pack' AND kind = 'release_evaluate'
  AND input_snapshot ->> 'trigger' = 'ignore_multi_video_off'`).Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if scheduled != 1 {
		t.Fatalf("turning off must re-enqueue only the swept release, got %d", scheduled)
	}
	var untouched int
	if err := db.QueryRowContext(ctx, `
SELECT (SELECT COUNT(*) FROM work_items WHERE owner_id IN ('mv-awaiting-single','mv-filtered-tag'))`).Scan(&untouched); err != nil || untouched != 0 {
		t.Fatalf("other releases must not be re-enqueued: %d %v", untouched, err)
	}
}

// The draft carries the option until subscribe (off by default), and a started
// subscription can still change it: unlike new-releases-only it is never
// frozen, which is the owner decision the option rides on.
func TestIgnoreMultiVideoCarriedByDraftAndChangeableAfterStart(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	on := true
	draft := ownershipTestDraft(t)
	draft.IgnoreMultiVideoReleases = &on
	subscribed, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: draft}, "ignore-multi-video-draft-key")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if !subscribed.Subscription.IgnoreMultiVideoReleases {
		t.Fatal("subscribe must carry the draft option")
	}

	started, err := service.PatchAnimeSubscription(ctx, subscribed.ManagedAnime.Id, SubscriptionPatchInput{ExpectedVersion: subscribed.Subscription.Version, State: "active"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	off, err := service.SetIgnoreMultiVideoReleases(ctx, subscribed.ManagedAnime.Id, IgnoreMultiVideoInput{ExpectedVersion: started.Version, Enabled: false})
	if err != nil {
		t.Fatalf("change after start: %v", err)
	}
	if off.IgnoreMultiVideoReleases {
		t.Fatalf("the change after start must hold: %+v", off)
	}
	var stored bool
	if err := db.QueryRowContext(ctx, `SELECT ignore_multi_video_releases FROM subscriptions WHERE id = $1`, subscribed.Subscription.Id).Scan(&stored); err != nil || stored {
		t.Fatalf("stored option = %v (%v), want false", stored, err)
	}
}
