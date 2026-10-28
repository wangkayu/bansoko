package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/presetdraft"
	"bansoko/internal/transport/httpapi/generated"
)

// seedSubscriptionDownload seeds one running automatic download of the
// anime's subscription, completed and verified. With a hold its import waits for
// configuration (the real wait owner function parked the plan) and the plan's
// work item is returned; without one no plan exists yet.
func seedSubscriptionDownload(t *testing.T, db *sql.DB, animeID, key, hold string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
SELECT 'source-'||$2, id, 'custom_rss', 'https://feed.example.test/'||$2||'.xml', 'https://feed.example.test/'||$2||'.xml', 'Group', 'Wait Show'
FROM subscriptions WHERE managed_anime_id = $1`, animeID, key); err != nil {
		t.Fatal(err)
	}
	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ('release-'||$1, 'source-'||$1, 'guid:'||$1, '{"title":"[Group] Wait Show - 01"}', '{}', '{}', 'accepted_pending_download', 'accepted')`, key)
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "root-"+key, "task-"+key, "release-"+key, "download:"+key, `{}`, `{}`, "completed", `null`,
		`[{"file_ref":"primary","ordinal":0,"relative_path":"Wait Show - 01.mkv","exact_size":7,"media_kind":"video","identity_evidence":{}}]`)
	// The shared fixture accepts a one-shot root; the root is re-labelled as
	// the automatic acquisition the subscription's RSS admission accepts.
	exec(`UPDATE acquisitions SET entry_kind = 'automatic', request_namespace = 'auto_acquire_v1' WHERE id = 'root-'||$1`, key)
	exec(`UPDATE releases SET download_task_id = 'task-'||$1 WHERE id = 'release-'||$1`, key)
	exec(`INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ('artifact-'||$1, 'task-'||$1, 'root-'||$1, '{}', '{"fingerprints":[]}', 'verified', CURRENT_TIMESTAMP)`, key)
	exec(`INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, subtitle_binding_state)
VALUES ('file-'||$1, 'artifact-'||$1, 'root-'||$1, 'primary', 'Wait Show - 01.mkv', '/downloads/'||$1||'.mkv', 'video', 7, '{}', 'not_applicable')`, key)
	work := ""
	if hold != "" {
		_, work = acquisitionfixture.ParkWaitingPlan(t, tx, "artifact-"+key, hold, 1)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return work
}

// parked reports whether the work item is still parked (not due now).
func parked(t *testing.T, db *sql.DB, work string) bool {
	t.Helper()
	var later bool
	if err := db.QueryRowContext(context.Background(), `SELECT available_at > clock_timestamp() FROM work_items WHERE id = $1 AND state = 'available'`, work).Scan(&later); err != nil {
		t.Fatal(err)
	}
	return later
}

// A metadata write and a naming rules write of the anime make its import that
// waits for configuration due in their own transaction; the next claim checks
// the configuration again (here it parks again in between).
func TestConfigurationWritesWakeWaitingImports(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribed, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: ownershipTestDraft(t), Start: new(true)}, "config-wait-wake-0001")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	animeID := subscribed.ManagedAnime.Id

	work := seedSubscriptionDownload(t, db, animeID, "wake", `[{"item":"binding_missing:tmdb","files":1}]`)
	if !parked(t, db, work) {
		t.Fatal("a waiting import must be parked until something wakes it")
	}
	if _, err := service.PatchAnimeMetadataApplication(ctx, animeID, MetadataPatchInput{
		ExpectedVersion: 1,
		BGM:             &BGMBinding{WorkID: "bgm-123"},
		TMDB:            &TMDBBinding{WorkID: "tmdb-456", MediaKind: "tv", Season: 1},
	}); err != nil {
		t.Fatalf("metadata patch: %v", err)
	}
	if parked(t, db, work) {
		t.Fatal("the metadata write must wake the waiting import")
	}

	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	acquisitionfixture.ParkDuePlan(t, tx, "fixture-plan-artifact-wake", `[{"item":"template_variable_undefined:anibt.language","files":1}]`, 1)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !parked(t, db, work) {
		t.Fatal("the import must park again")
	}
	rules := RulesPatchInput{ExpectedVersion: 1, Sections: presetdraft.DefaultDomainRules(t)}
	rules.Sections.TagFilters = nil
	if _, err := service.PatchAnimeRules(ctx, animeID, rules); err != nil {
		t.Fatalf("rules patch: %v", err)
	}
	if parked(t, db, work) {
		t.Fatal("the rules write must wake the waiting import")
	}
}

// The media kind changes only while nothing holds the identity: every
// acquisition that has not settled counts, a download whose import has not
// planned yet and an import that waits for configuration alike.
func TestMediaKindChangeBlockedWhileAcquisitionsHaveNotSettled(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribed, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: ownershipTestDraft(t), Start: new(true)}, "config-wait-kind-0001")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	animeID := subscribed.ManagedAnime.Id
	movie := "movie"
	changeKind := func() map[string]int64 {
		t.Helper()
		_, err := service.PatchAnimeMetadataApplication(ctx, animeID, MetadataPatchInput{
			ExpectedVersion:   1,
			BGM:               &BGMBinding{WorkID: "bgm-123"},
			TMDB:              &TMDBBinding{WorkID: "tmdb-456", MediaKind: "tv", Season: 1},
			MediaKindOverride: &movie,
		})
		animetest.AssertBusinessCode(t, err, 409, animeapplication.IdentityKindChangeBlockedCode)
		var business *apperrors.Business
		if !errors.As(err, &business) {
			t.Fatal(err)
		}
		counts := map[string]int64{}
		for _, detail := range business.Details {
			if detail.Count != nil {
				counts[detail.Code] = *detail.Count
			}
		}
		return counts
	}

	seedSubscriptionDownload(t, db, animeID, "kind-download", "")
	if counts := changeKind(); counts["pending_acquisitions"] != 1 || counts["active_placements"] != 0 {
		t.Fatalf("with a download not yet planned: %+v", counts)
	}
	seedSubscriptionDownload(t, db, animeID, "kind-waiting", `[{"item":"binding_missing:bgm","files":1}]`)
	if counts := changeKind(); counts["pending_acquisitions"] != 2 || counts["active_placements"] != 0 {
		t.Fatalf("with an import waiting for configuration: %+v", counts)
	}
}
