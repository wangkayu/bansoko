package application

import (
	"context"
	"testing"

	anime "bansoko/internal/anime/domain"
	configuration "bansoko/internal/configuration/application"
	configdomain "bansoko/internal/configuration/domain"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/workitems"
)

// The Global download_timeouts setting is read at acceptance and frozen into
// the acquisition limits and the download task budget; a later save changes
// only downloads accepted after it.
func TestAcceptanceFreezesGlobalDownloadTimeouts(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	config := configuration.NewService(db, nil)

	save := func(expected int64, value anime.DownloadTimeouts) int64 {
		t.Helper()
		view, err := config.PutGlobalSection(ctx, anime.SectionDownloadTimeouts, expected, configdomain.GlobalSectionInput{
			SchemaVersion: anime.DownloadTimeoutsSchemaVersion,
			Value:         map[string]any{"stall_seconds": value.StallSeconds, "download_wait_seconds": value.DownloadWaitSeconds},
		})
		if err != nil {
			t.Fatal(err)
		}
		return view.Version
	}
	accept := func(releaseID, title, torrentID, file string) {
		t.Helper()
		insertReleaseForEvaluation(t, db, sourceID, releaseID, title, "https://downloads.example.test/"+file+".torrent")
		seedNativeTorrentFile(t, db, torrentID, releaseID, file+".mkv", 1024, file+".torrent")
		claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: releaseID}, workitems.KindReleaseEvaluate, currentReleaseVersion(t, db, releaseID))
		finishReleaseClaim(t, store, service, claim)
		assertReleaseState(t, db, releaseID, "accepted_pending_download", "accepted", true)
	}
	assertFrozen := func(releaseID string, want anime.DownloadTimeouts) {
		t.Helper()
		var stall, waitSeconds, maxPolls, limitStall, limitWait, limitPolls int64
		if err := db.QueryRowContext(ctx, `
SELECT task.stall_seconds,
       extract(epoch FROM task.wait_until - task.wait_started_at)::bigint,
       task.max_polls,
       (a.input->'limits'->>'stall_seconds')::bigint,
       (a.input->'limits'->>'download_wait_seconds')::bigint,
       (a.input->'limits'->'sync'->>'max_polls')::bigint
FROM download_tasks task
JOIN acquisitions a ON a.id = task.acquisition_id
WHERE task.release_id = $1`, releaseID).Scan(&stall, &waitSeconds, &maxPolls, &limitStall, &limitWait, &limitPolls); err != nil {
			t.Fatal(err)
		}
		wantPolls := (want.DownloadWaitSeconds+59)/60 + 1
		if stall != want.StallSeconds || waitSeconds != want.DownloadWaitSeconds || maxPolls != wantPolls ||
			limitStall != want.StallSeconds || limitWait != want.DownloadWaitSeconds || limitPolls != wantPolls {
			t.Fatalf("%s frozen stall=%d wait=%d polls=%d limits=%d/%d/%d, want %+v polls=%d",
				releaseID, stall, waitSeconds, maxPolls, limitStall, limitWait, limitPolls, want, wantPolls)
		}
	}

	first := anime.DownloadTimeouts{StallSeconds: 2 * 3600, DownloadWaitSeconds: 3 * 86400}
	version := save(0, first)
	accept("release-timeouts-1", "[Group] Test Show - 01 1080p", "torrent-timeouts-1", "show-01")
	assertFrozen("release-timeouts-1", first)

	second := anime.DownloadTimeouts{StallSeconds: 3600, DownloadWaitSeconds: 86400}
	save(version, second)
	// The accepted download keeps the values frozen at its acceptance.
	assertFrozen("release-timeouts-1", first)
	accept("release-timeouts-2", "[Group] Test Show - 02 1080p", "torrent-timeouts-2", "show-02")
	assertFrozen("release-timeouts-2", second)
}

// A saved value outside the bounds or with stall > wait is refused by the
// generic Global section API and by the table check.
func TestGlobalDownloadTimeoutsRefuseInvalidValues(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	config := configuration.NewService(db, nil)
	for _, value := range []map[string]any{
		{"stall_seconds": 60, "download_wait_seconds": 86400},
		{"stall_seconds": 2 * 86400, "download_wait_seconds": 86400},
		{"stall_seconds": 3600, "download_wait_seconds": 31 * 86400},
		{"stall_seconds": 3600},
	} {
		if _, err := config.PutGlobalSection(ctx, anime.SectionDownloadTimeouts, 0, configdomain.GlobalSectionInput{SchemaVersion: anime.DownloadTimeoutsSchemaVersion, Value: value}); err == nil {
			t.Fatalf("%v accepted", value)
		}
	}
	for _, raw := range []string{
		`{"stall_seconds":60,"download_wait_seconds":86400}`,
		`{"stall_seconds":172800,"download_wait_seconds":86400}`,
		`{"stall_seconds":3600,"download_wait_seconds":2678400}`,
		`{"stall_seconds":3600,"download_wait_seconds":86400,"extra":1}`,
		`{"stall_seconds":"3600","download_wait_seconds":86400}`,
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO global_config_sections(section_name, schema_version, value, version) VALUES ('download_timeouts', 'download-timeouts-v1', $1::jsonb, 1)`, raw); err == nil {
			t.Fatalf("table accepted %s", raw)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO global_config_sections(section_name, schema_version, value, version) VALUES ('download_timeouts', 'download-timeouts-v1', '{"stall_seconds":600,"download_wait_seconds":2592000}'::jsonb, 1)`); err != nil {
		t.Fatalf("table refused the bounds: %v", err)
	}
}
