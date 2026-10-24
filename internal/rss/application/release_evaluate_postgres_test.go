package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

func TestReleaseEvaluateTerminalAcquisitionVerdictsAreNotReevaluated(t *testing.T) {
	for _, verdict := range []string{"adopted_existing", "acquisition_failed"} {
		t.Run(verdict, func(t *testing.T) {
			db := openRSSDatabase(t)
			sourceID, _ := seedRSSOwner(t, db, false, true)
			releaseID := "release-" + verdict
			insertReleaseForEvaluation(t, db, sourceID, releaseID, "[Group] Test Show - 01 1080p", "https://downloads.example.test/show-01.torrent")
			if _, err := db.ExecContext(context.Background(), `
UPDATE releases
SET verdict = $2, reason_code = $2
WHERE id = $1`, releaseID, verdict); err != nil {
				t.Fatal(err)
			}

			store, err := workitems.NewStore(db, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
			if err != nil {
				t.Fatal(err)
			}
			claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: releaseID}, workitems.KindReleaseEvaluate, 1)
			finishReleaseClaim(t, store, service, claim)

			assertReleaseState(t, db, releaseID, verdict, verdict, false)
			var version int64
			if err := db.QueryRowContext(context.Background(), `SELECT version FROM releases WHERE id = $1`, releaseID).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if version != 1 {
				t.Fatalf("terminal release version=%d, want unchanged version 1", version)
			}
		})
	}
}

func TestReleaseEvaluateStoppedSubscriptionPersistsTerminalOwnerState(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, true, true)
	insertReleaseForEvaluation(t, db, sourceID, "release-stopped", "[Group] Test Show - 01 1080p", "https://downloads.example.test/show-01.torrent")

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-stopped"}, workitems.KindReleaseEvaluate, 1)
	finishReleaseClaim(t, store, service, claim)

	assertReleaseState(t, db, "release-stopped", "subscription_stopped", "subscription_stopped", false)
}

func TestReleaseEvaluateMetadataHoldDoesNotCreateDownloadTask(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, false)
	insertReleaseForEvaluation(t, db, sourceID, "release-held", "[Group] Test Show - 01 1080p", "https://downloads.example.test/show-01.torrent")

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-held"}, workitems.KindReleaseEvaluate, 1)
	finishReleaseClaim(t, store, service, claim)

	assertReleaseState(t, db, "release-held", "waiting_metadata", "metadata_incomplete", false)
}

func TestReleaseEvaluateFilteredReleaseDoesNotCreateDownloadTask(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	filter, err := json.Marshal(map[string]any{
		"must_contain": "",
		"not_in":       "SP",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
UPDATE anime_config_overrides
SET schema_version = 'release-rules-v1', value = $1
WHERE managed_anime_id = 'anime-rss-test' AND section_name = 'release_rules'`, filter); err != nil {
		t.Fatal(err)
	}
	insertReleaseForEvaluation(t, db, sourceID, "release-filtered", "[Group] Test Show - 01 SP 1080p", "https://downloads.example.test/show-01.torrent")

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-filtered"}, workitems.KindReleaseEvaluate, 1)
	finishReleaseClaim(t, store, service, claim)

	assertReleaseState(t, db, "release-filtered", "filtered", "filtered_by_rules", false)
}

// A release whose manifest has no video with a recognizable episode places
// nothing: it is skipped (no_placeable_files) and never reaches a download.
func TestReleaseEvaluateWithoutPlaceableFileIsFilteredWithoutDownloadTask(t *testing.T) {
	initTestNamingVars(t)
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	insertReleaseForEvaluation(t, db, sourceID, "release-unplaceable", "[Group] Test Show 1080p", "https://downloads.example.test/show.torrent")
	seedNativeTorrentFile(t, db, "torrent-unplaceable", "release-unplaceable", "show.mkv", 1024, "show.torrent")

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	// The batch plan decides acceptance at the production pre-download
	// target boundary.
	service.SetTargetAcquisition(missingTargetProbe{})
	service.SetTargetStore(target.NewStore(targetpersistence.NewRepository(db)))
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-unplaceable"}, workitems.KindReleaseEvaluate, currentReleaseVersion(t, db, "release-unplaceable"))
	finishReleaseClaim(t, store, service, claim)

	assertReleaseState(t, db, "release-unplaceable", "filtered", "no_placeable_files", false)
}

func TestReleaseReevaluationUsesCurrentLinkEpisodeOffset(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, false)
	ctx := context.Background()
	seedInstanceParserPolicy(t, db, "anime-rss-test")
	// The reevaluation must read the link's current episode_offset.
	if _, err := db.ExecContext(ctx, `UPDATE rss_sources SET episode_offset = 2 WHERE id = $1`, sourceID); err != nil {
		t.Fatal(err)
	}
	insertReleaseForEvaluation(t, db, sourceID, "release-link-offset", "Test Show - 01", "https://downloads.example.test/show-01.torrent")
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-link-offset"}, workitems.KindReleaseEvaluate, 1)
	finishReleaseClaim(t, store, service, claim)
	var episode int
	if err := db.QueryRowContext(ctx, `SELECT (parsed_facts->>'episode')::int FROM releases WHERE id = 'release-link-offset'`).Scan(&episode); err != nil {
		t.Fatal(err)
	}
	if episode != 3 {
		t.Fatalf("reevaluated episode=%d, want the link offset 2 applied to episode 1", episode)
	}
}

// A metadata-only fetch leaves the manifest without program-owned payload
// bytes: the plan is accepted but the release fails at the task boundary.
func TestReleaseEvaluateAcceptedWithoutTorrentPayloadFailsAtTaskBoundary(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	insertReleaseForEvaluation(t, db, sourceID, "release-no-torrent", "[Group] Test Show - 01 1080p", "https://downloads.example.test/show-01.torrent")
	seedTorrentManifest(t, db, "release-no-torrent", "show-01.mkv", 1024, "show-01.torrent")

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-no-torrent"}, workitems.KindReleaseEvaluate, currentReleaseVersion(t, db, "release-no-torrent"))
	finishReleaseClaim(t, store, service, claim)

	assertReleaseState(t, db, "release-no-torrent", "parse_failed", "torrent_payload_missing", false)
}

// Without a torrent file list (no URL, nothing fetched) the batch plan has
// nothing to classify: the release fails visibly at evaluation.
func TestReleaseEvaluateAcceptedWithoutManifestFailsAtEvaluation(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	insertReleaseForEvaluation(t, db, sourceID, "release-no-url", "[Group] Test Show - 01 1080p", "")

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-no-url"}, workitems.KindReleaseEvaluate, 1)
	finishReleaseClaim(t, store, service, claim)

	assertReleaseState(t, db, "release-no-url", "parse_failed", "torrent_manifest_missing", false)
}

func insertReleaseForEvaluation(t *testing.T, db *sql.DB, sourceID, releaseID, title, downloadURL string) {
	t.Helper()
	internal, err := json.Marshal(map[string]any{
		"title":        title,
		"download_url": downloadURL,
		"guid":         releaseID + "-guid",
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceFacts, err := json.Marshal(map[string]string{
		"identity_kind":  "guid",
		"identity_value": releaseID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO releases(
    id, rss_source_id, stable_item_key, raw_facts, source_facts, internal_facts,
    verdict, reason_code, evaluation_version
)
VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6::jsonb, 'discovered', 'discovered', NULL)`,
		releaseID, sourceID, "guid:"+releaseID, `{"title":"placeholder"}`, sourceFacts, internal); err != nil {
		t.Fatal(err)
	}
}

func finishReleaseClaim(t *testing.T, store *workitems.Store, service *Service, claim workitems.Claim) {
	t.Helper()
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil || outcome.Kind != worker.OutcomeFinish {
		t.Fatalf("release evaluation outcome=%+v err=%v", outcome, err)
	}
	if err := store.FinishWithResult(context.Background(), claim, workitems.StateSucceeded, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
}

func assertReleaseState(t *testing.T, db *sql.DB, releaseID, verdict, reasonCode string, hasTask bool) {
	t.Helper()
	var actualVerdict, actualReason string
	var taskID sql.NullString
	if err := db.QueryRowContext(context.Background(), `
SELECT verdict, reason_code, download_task_id
FROM releases WHERE id = $1`, releaseID).Scan(&actualVerdict, &actualReason, &taskID); err != nil {
		t.Fatal(err)
	}
	if actualVerdict != verdict || actualReason != reasonCode || taskID.Valid != hasTask {
		t.Fatalf("release %s state=%q/%q task=%v, want %q/%q task=%v", releaseID, actualVerdict, actualReason, taskID.Valid, verdict, reasonCode, hasTask)
	}
	var taskCount, workItemCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM download_tasks WHERE release_id = $1`, releaseID).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*)
FROM work_item_history wi
JOIN download_tasks task ON task.id = wi.owner_id
WHERE wi.owner_type = 'download_task' AND task.release_id = $1`, releaseID).Scan(&workItemCount); err != nil {
		t.Fatal(err)
	}
	if (taskCount > 0) != hasTask || (workItemCount > 0) != hasTask {
		t.Fatalf("release %s task count=%d work item count=%d, want task=%v", releaseID, taskCount, workItemCount, hasTask)
	}
}
