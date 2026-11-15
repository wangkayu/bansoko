package application

import (
	"context"
	"testing"

	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	plugintarget "bansoko/internal/plugintarget/application"
	"bansoko/internal/workitems"
)

// Exercise the real submit producer and terminal-failure SQL trigger, not the
// fact helper. The owner entry is frozen at acceptance by the shared fixture.
func TestDownloadNotificationsPreserveAcceptedOrigin(t *testing.T) {
	for _, entry := range []string{"automatic", "subscription_add", "one_shot"} {
		t.Run(entry, func(t *testing.T) {
			db := openDownloadDB(t)
			taskID := "task-origin"
			seedDownloadTaskWithEntry(t, db, taskID, "queued", "null", true, entry)
			provider := &fakeProvider{lookupFound: true, lookupTask: domain.ProviderTask{ID: "tag:qb:release-qb-test", Hash: "abc", State: domain.TaskDownloading, Progress: 0.4}}
			service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}})
			notices, err := plugintarget.NewService(db, nil, store)
			if err != nil {
				t.Fatal(err)
			}
			service.SetBusinessDeliveryEnqueuer(notices)
			claim := createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSubmit)
			outcome := handleSync(t, service, claim)
			if outcome.OwnerResult.State != "downloading" || provider.lookupCalls != 1 {
				t.Fatalf("submit outcome=%+v lookups=%d", outcome, provider.lookupCalls)
			}
			if _, err := ownerTestDB(t).ExecContext(context.Background(), `UPDATE download_tasks SET state='failed',failure_code='DOWNLOAD_STALLED',version=version+1 WHERE id=$1`, taskID); err != nil {
				t.Fatal(err)
			}
			if _, err := ownerTestDB(t).ExecContext(context.Background(), `UPDATE download_tasks SET failure_reason='updated',version=version+1 WHERE id=$1`, taskID); err != nil {
				t.Fatal(err)
			}
			want := "subscription"
			if entry == "one_shot" {
				want = "one_shot"
			}
			for _, trigger := range []string{"download_submitted", "download_failed"} {
				var count int
				var origin, source, evidence string
				if err := db.QueryRowContext(context.Background(), `SELECT count(*),coalesce(min(payload->>'task.origin'),''),coalesce(min(payload->>'release.src'),''),coalesce(min(source_evidence->>'provider'),'') FROM notification_admissions WHERE source_owner_id=$1 AND trigger_kind=$2`, taskID, trigger).Scan(&count, &origin, &source, &evidence); err != nil {
					t.Fatal(err)
				}
				if count != 1 || origin != want || source != "custom" || evidence != "custom" {
					t.Fatalf("%s: count=%d origin=%q source=%q evidence=%q", trigger, count, origin, source, evidence)
				}
			}
			if entry == "one_shot" {
				var retryable bool
				if err := db.QueryRow(`SELECT (payload->>'result.retryable')::boolean FROM notification_admissions WHERE trigger_kind='download_failed'`).Scan(&retryable); err != nil {
					t.Fatal(err)
				}
				if retryable {
					t.Fatal("one-shot failure advertised RSS retry")
				}
			}
		})
	}
}
