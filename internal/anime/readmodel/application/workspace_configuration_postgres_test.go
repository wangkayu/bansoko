package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
)

// referenceWorkspaceSummary follows ownerChain.ts directly over the full read,
// independently of the narrow SQL's existential/aggregate representation.
func referenceWorkspaceSummary(workspace AnimeWorkspace) AnimeWorkspaceReleaseSummary {
	result := AnimeWorkspaceReleaseSummary{Count: int64(len(workspace.Releases)), TagObservations: []AnimeWorkspaceTagObservation{}}
	for _, release := range workspace.Releases {
		if release.RawFacts.PublishedAt != nil {
			value := release.RawFacts.PublishedAt.Format(time.RFC3339Nano)
			if result.LatestPublishedAt == nil || value > *result.LatestPublishedAt {
				result.LatestPublishedAt = &value
			}
		}
		observation := AnimeWorkspaceTagObservation{AniBTTags: []string{}, TitleTags: []string{}}
		if release.SourceFacts.AniBT != nil {
			observation.AniBTTags = release.SourceFacts.AniBT.Tags
		}
		if release.SourceFacts.TitleTags != nil {
			observation.TitleTags = release.SourceFacts.TitleTags.Tags
		}
		if len(observation.AniBTTags) > 0 || len(observation.TitleTags) > 0 {
			result.TagObservations = append(result.TagObservations, observation)
		}
	}
	active := 0
	result.OwnerChainStable = true
	for _, source := range workspace.RSSSources {
		selected := source.SourceKind == "custom_rss"
		for _, group := range workspace.Subgroups.EffectiveSelection {
			p := source.ProviderProvenance
			if p != nil && group.EffectiveSelected && group.Provider == string(p.Provider) && group.GroupRef == p.ProviderGroupRef {
				selected = true
			}
		}
		if !selected {
			continue
		}
		active++
		releases := 0
		for _, release := range workspace.Releases {
			if release.RSSSourceID != source.ID {
				continue
			}
			releases++
			var task *WorkspaceDownloadTask
			for i := range workspace.DownloadTasks {
				if release.DownloadTaskID != nil && workspace.DownloadTasks[i].ID == *release.DownloadTaskID {
					task = &workspace.DownloadTasks[i]
				}
			}
			artifacts := 0
			if task != nil {
				for _, artifact := range workspace.Artifacts {
					if artifact.DownloadTaskID != task.ID {
						continue
					}
					artifacts++
					operations := 0
					for _, operation := range workspace.FileOperations {
						if operation.ArtifactID != artifact.ID {
							continue
						}
						operations++
						if operation.State != "completed" && operation.State != "failed" {
							result.OwnerChainStable = false
						}
					}
					if artifact.State != "failed" && !(artifact.State == "verified" && operations > 0) {
						result.OwnerChainStable = false
					}
				}
				if artifacts == 0 && task.State != "failed" {
					result.OwnerChainStable = false
				}
			} else {
				switch release.Verdict {
				case "waiting_metadata", "subscription_stopped", "source_unavailable", "parse_failed", "filtered", "adopted_existing", "acquisition_failed", "resume_baseline", "start_baseline":
				default:
					result.OwnerChainStable = false
				}
			}
		}
		if releases == 0 {
			known := false
			for _, runtime := range workspace.RSSSourceRuntimes {
				if runtime.RSSSourceID == source.ID {
					known = runtime.Health != "unknown"
				}
			}
			if !known {
				result.OwnerChainStable = false
			}
		}
	}
	result.OwnerChainStable = result.OwnerChainStable && active > 0
	return result
}

func assertWorkspaceConfigurationParity(t *testing.T, service *Service, animeID string) (AnimeWorkspaceConfiguration, AnimeWorkspace) {
	t.Helper()
	ctx := context.Background()
	full, err := service.GetAnimeWorkspaceApplication(ctx, animeID)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := service.GetAnimeWorkspaceConfigurationApplication(ctx, animeID)
	if err != nil {
		t.Fatal(err)
	}
	want := AnimeWorkspaceConfiguration{
		ManagedAnime: full.ManagedAnime, Subscription: full.Subscription, Metadata: full.Metadata, MetadataHold: full.MetadataHold,
		Subgroups: full.Subgroups, Rules: full.Rules, RSSSources: full.RSSSources, RSSSourceRuntimes: full.RSSSourceRuntimes,
		EpisodeProgress: full.EpisodeProgress, ConfigWaitingFiles: full.ConfigWaitingFiles, ReleaseSummary: referenceWorkspaceSummary(full),
	}
	if !reflect.DeepEqual(configuration, want) {
		t.Fatalf("configuration differs from full projection:\ngot %+v\nwant %+v", configuration, want)
	}
	return configuration, full
}

func TestWorkspaceConfigurationAllHistoryParity(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	service, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	subscribe := readmodelSubscribeTestDraft(t, root, "configuration-history")
	animeID, sourceID := subscribe.ManagedAnime.Id, subscribe.RssSources[0].Id
	initial, _ := assertWorkspaceConfigurationParity(t, service, animeID)
	if initial.ReleaseSummary.OwnerChainStable || initial.ReleaseSummary.LatestPublishedAt != nil {
		t.Fatalf("initial summary = %+v", initial.ReleaseSummary)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('configuration-source-2',$1,'custom_rss','https://feed.example.test/second','https://feed.example.test/second','Second','Example')`, subscribe.Subscription.Id); err != nil {
		t.Fatal(err)
	}
	// More than either existing 100/500 history window, with an old unfinished
	// release on the first source and another source's still-running history.
	if _, err := db.ExecContext(ctx, `
INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,internal_facts,verdict,reason_code,discovered_at)
SELECT 'configuration-release-'||n,$1,'configuration-key-'||n,
       CASE WHEN n=1 THEN '{"published_at":"2026-01-01T00:00:00Z"}'::jsonb
            WHEN n=2 THEN '{"published_at":"2026-11-10T09:00:00+08:00"}'::jsonb
            WHEN n=3 THEN '{"published_at":"2026-11-10T02:00:00Z"}'::jsonb ELSE '{}'::jsonb END,
       CASE WHEN n IN (1,2,3,511) THEN '{"anibt":{"tags":["tag:HEVC","tag:hevc","tag:HEVC"]},"title_tags":{"tags":["Codec:H265","codec:h265","Codec:H265"]}}'::jsonb ELSE '{}'::jsonb END,
       '{"release_files":[{"source_path":"Example.mkv","filename":"Example.mkv","media_kind":"video","size_bytes":1024}]}'::jsonb,
       CASE WHEN n=1 THEN 'discovered' ELSE 'filtered' END,'fixture',
       '2026-11-10T00:00:00Z'::timestamptz + n*interval '1 second'
FROM generate_series(1,511) n`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,verdict,reason_code,discovered_at)
VALUES ('configuration-second','configuration-source-2','second','{}','{"title_tags":{"tags":["Language:CHS"]}}','discovered','fixture','2030-01-01')`); err != nil {
		t.Fatal(err)
	}
	configuration, full := assertWorkspaceConfigurationParity(t, service, animeID)
	if configuration.ReleaseSummary.Count != 512 || configuration.ReleaseSummary.OwnerChainStable || len(configuration.ReleaseSummary.TagObservations) != 5 {
		t.Fatalf("all-history summary = %+v", configuration.ReleaseSummary)
	}
	if configuration.ReleaseSummary.LatestPublishedAt == nil || *configuration.ReleaseSummary.LatestPublishedAt != "2026-11-10T09:00:00+08:00" {
		t.Fatalf("lexical publication max = %+v", configuration.ReleaseSummary)
	}
	fullJSON, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	configurationJSON, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("same 512-release sample, application JSON bytes (not latency): fullBytes=%d configBytes=%d", len(fullJSON), len(configurationJSON))
	if len(configurationJSON) >= len(fullJSON) {
		t.Fatal("configuration must omit the full history payload")
	}
	if _, err := db.ExecContext(ctx, `UPDATE releases SET verdict='filtered' WHERE id='configuration-second'`); err != nil {
		t.Fatal(err)
	}
	configuration, _ = assertWorkspaceConfigurationParity(t, service, animeID)
	if configuration.ReleaseSummary.OwnerChainStable {
		t.Fatal("old unstable history must still hold polling")
	}
	if _, err := db.ExecContext(ctx, `UPDATE releases SET verdict='filtered' WHERE id='configuration-release-1'`); err != nil {
		t.Fatal(err)
	}
	configuration, _ = assertWorkspaceConfigurationParity(t, service, animeID)
	if !configuration.ReleaseSummary.OwnerChainStable {
		t.Fatal("all selected sources are now stable")
	}
	// Read retirement and independent CAS versions through the shared owner
	// reader, without hiding retired owners or replacing their versions.
	if _, err := disposabledb.Owner(t).ExecContext(ctx, `UPDATE managed_anime SET lifecycle_state='retired', retired_at=CURRENT_TIMESTAMP, fence_epoch=fence_epoch+1,
retired_subscription_version=(SELECT version FROM subscriptions WHERE managed_anime_id=$1), lifecycle_version=lifecycle_version+1, version=version+1 WHERE id=$1`, animeID); err != nil {
		t.Fatal(err)
	}
	configuration, _ = assertWorkspaceConfigurationParity(t, service, animeID)
	if configuration.Subscription.LifecycleState != "retired" || configuration.Subscription.RetiredAt == nil {
		t.Fatal("retired owner must remain readable")
	}
	_, err = service.GetAnimeWorkspaceConfigurationApplication(ctx, "missing-configuration-anime")
	var notFound *apperrors.NotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("missing owner error = %v", err)
	}
}

func TestWorkspaceConfigurationCurrentChainParity(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	service, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	subscribe := readmodelSubscribeTestDraft(t, root, "configuration-current-chain")
	animeID, sourceID := subscribe.ManagedAnime.Id, subscribe.RssSources[0].Id
	// Actual operation fixtures prove SQL EXISTS/terminal-state branches.
	for i, state := range []string{"completed", "failed", "pending"} {
		animetest.SeedCompletedFileOp(t, db, sourceID, i+1, false, state)
		configuration, _ := assertWorkspaceConfigurationParity(t, service, animeID)
		if configuration.ReleaseSummary.OwnerChainStable != (state != "pending") {
			t.Fatalf("operation %s summary = %+v", state, configuration.ReleaseSummary)
		}
	}
	// Each following source has one current task and optionally an artifact.
	// Aggregate facts are checked independently so an earlier unstable source
	// cannot mask a later branch of the predicate.
	for i, tc := range []struct {
		task, artifact string
		stable         bool
	}{
		{"failed", "", true}, {"completed", "", false},
		{"completed", "failed", true}, {"completed", "verified", false},
		{"completed", "pending", false}, {"completed", "waiting_retry", false},
	} {
		source, release, task, artifact := fmt.Sprintf("chain-source-%d", i), fmt.Sprintf("chain-release-%d", i), fmt.Sprintf("chain-task-%d", i), fmt.Sprintf("chain-artifact-%d", i)
		tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO rss_sources(id,subscription_id,source_kind,locator,locator_display,group_name,title) VALUES ($1,$2,'custom_rss','https://feed.example.test/'||$1,'https://feed.example.test/'||$1,'Example','Example')`, source, subscribe.Subscription.Id); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,verdict,reason_code) VALUES ($1,$2,$1,'{}','{}','accepted_pending_download','accepted')`, release, source); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		acquisitionfixture.SeedObservedDownload(t, tx, "root-"+task, task, release, "key-"+task, `{}`, `{}`, tc.task, `null`)
		if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id=$1 WHERE id=$2`, task, release); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if tc.artifact != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state,verified_at)
SELECT $1,id,acquisition_id,'{}','{}',$3,CASE WHEN $3='verified' THEN CURRENT_TIMESTAMP ELSE NULL END FROM download_tasks WHERE id=$2`, artifact, task, tc.artifact); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		_, full := assertWorkspaceConfigurationParity(t, service, animeID)
		history, err := service.readWorkspaceSourceHistory(ctx, subscribe.Subscription.Id)
		if err != nil {
			t.Fatal(err)
		}
		full.RSSSources = []RSSSource{{ID: source, SourceKind: "custom_rss"}}
		if got := workspaceSourceHistoryStable(history[source]); got != tc.stable || referenceWorkspaceSummary(full).OwnerChainStable != got {
			t.Fatalf("task=%s artifact=%s stable=%v want=%v facts=%+v", tc.task, tc.artifact, got, tc.stable, history[source])
		}
	}
	// A completed historical task/verified artifact must not override the
	// current failed task when the release is retried.
	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	acquisitionfixture.SeedObservedDownload(t, tx, "root-current-retry", "task-current-retry", "chain-release-3", "key-current-retry", `{}`, `{}`, "failed", `null`)
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id='task-current-retry' WHERE id='chain-release-3'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceConfigurationParity(t, service, animeID)
	history, err := service.readWorkspaceSourceHistory(ctx, subscribe.Subscription.Id)
	if err != nil {
		t.Fatal(err)
	}
	if !workspaceSourceHistoryStable(history["chain-source-3"]) {
		t.Fatal("historical artifact without operations must not override current failed task")
	}
}
