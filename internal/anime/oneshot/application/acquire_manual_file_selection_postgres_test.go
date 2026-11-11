package application

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/batchplan"
)

// oneShotFixtureTorrentFile is the one file of the shared single-file test
// torrent (animetest.ValidTestTorrentBytes).
const oneShotFixtureTorrentFile = "Show 01 [1080p].mkv"

// A manual file selection is frozen with the admission: the input snapshot
// records it, the release's batch plan carries it as selected_paths, and the
// replay identity includes it, so the same key with the default files is a
// different request.
func TestAcquireSeedOnceFreezesTheManualFileSelection(t *testing.T) {
	service, _, _, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()
	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	_, input := currentInputForSeed(t, service, seed, nil)
	input.Seed = bindSeedIdentity(seed, snapshot)
	input.FileSelection = []string{oneShotFixtureTorrentFile}
	const key = "one-shot-file-selection-freeze-1"

	result, err := service.AcquireSeedOnce(ctx, input, key)
	if err != nil {
		t.Fatalf("admission with a manual selection: %v", err)
	}
	view, err := service.ReadOneShotAcquisition(ctx, result.OneShotID)
	if err != nil {
		t.Fatalf("read one-shot view: %v", err)
	}
	if view.InputSnapshot == nil || !reflect.DeepEqual(view.InputSnapshot.FileSelection, []string{oneShotFixtureTorrentFile}) {
		t.Fatalf("input snapshot file selection = %+v", view.InputSnapshot)
	}
	var raw []byte
	if err := service.db.QueryRowContext(ctx, `
SELECT release_facts->'batch_plan'->'selected_paths' FROM media_fact_snapshots WHERE release_id = $1`, result.ReleaseID).Scan(&raw); err != nil {
		t.Fatalf("read the frozen batch plan: %v", err)
	}
	var selected []string
	if err := json.Unmarshal(raw, &selected); err != nil || !reflect.DeepEqual(selected, []string{oneShotFixtureTorrentFile}) {
		t.Fatalf("frozen selected_paths = %s (%v)", raw, err)
	}

	replay, err := service.AcquireSeedOnce(ctx, input, key)
	if err != nil || replay.OneShotID != result.OneShotID {
		t.Fatalf("replay = %v, %v; want the original handle", replay.OneShotID, err)
	}
	input.FileSelection = nil
	if _, err := service.AcquireSeedOnce(ctx, input, key); !isBusinessCode(err, 409, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("the default files under the same key err = %v, want 409 IDEMPOTENCY_KEY_REUSED", err)
	}
}

func TestAcquireSeedOnceFreezesFinalFileOverrides(t *testing.T) {
	service, _, _, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()
	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	_, input := currentInputForSeed(t, service, seed, nil)
	input.Seed = bindSeedIdentity(seed, snapshot)
	input.EpisodeOffset = 10
	zero := 0
	input.Overrides = []acquisitiontarget.FileEpisodeOverride{{Path: oneShotFixtureTorrentFile, Season: &zero, Episode: 7}}
	const key = "one-shot-file-selection-freeze-1-overrides"

	result, err := service.AcquireSeedOnce(ctx, input, key)
	if err != nil {
		t.Fatalf("admission with file overrides: %v", err)
	}
	view, err := service.ReadOneShotAcquisition(ctx, result.OneShotID)
	if err != nil || view.InputSnapshot == nil || !reflect.DeepEqual(view.InputSnapshot.Overrides, input.Overrides) {
		t.Fatalf("frozen overrides = %+v, %v", view.InputSnapshot, err)
	}
	var raw []byte
	if err := service.db.QueryRowContext(ctx, `SELECT release_facts->'batch_plan' FROM media_fact_snapshots WHERE release_id = $1`, result.ReleaseID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plan batchplan.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	entry, ok := plan.File(oneShotFixtureTorrentFile)
	if !ok || entry.Identity == nil || entry.Identity.Episode == nil || *entry.Identity.Episode != "7" || entry.Identity.Offset != 0 || entry.Season == nil || *entry.Season != 0 {
		t.Fatalf("frozen plan override = %+v", entry)
	}
	replay, err := service.AcquireSeedOnce(ctx, input, key)
	if err != nil || replay.OneShotID != result.OneShotID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	input.Overrides[0].Episode = 8
	if _, err := service.AcquireSeedOnce(ctx, input, key); !isBusinessCode(err, 409, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("changed overrides err = %v, want idempotency conflict", err)
	}
}

// A selection the product would not place fails closed before any write.
func TestAcquireSeedOnceInvalidFileSelectionRejectsWithoutWrites(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()
	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	_, input := currentInputForSeed(t, service, seed, nil)
	input.Seed = bindSeedIdentity(seed, snapshot)

	for index, selection := range [][]string{{"Other 02 [1080p].mkv"}, {}} {
		input.FileSelection = selection
		_, err := service.AcquireSeedOnce(ctx, input, "one-shot-file-selection-invalid-"+string(rune('a'+index)))
		if !isBusinessCode(err, 422, "FILE_SELECTION_INVALID") {
			t.Fatalf("selection %v err = %v, want 422 FILE_SELECTION_INVALID", selection, err)
		}
		assertNoOneShotRows(t, db)
	}
}
