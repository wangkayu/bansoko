package application

import (
	"context"
	"encoding/json"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
	identitymetadataadapter "bansoko/internal/identitymetadata/adapter"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
)

func TestOneShotNotificationFreezesCatalogCoverIdentity(t *testing.T) {
	for _, tt := range []struct {
		name, provider, entry, source, url string
	}{
		{"mikan", "mikan", "123", "mikan", "https://mikanani.me/images/123.jpg?width=600"},
		{"anibt", "anibt", "123", "anibt", "https://r2.anibt.net/covers/123.jpg"},
		{"search", "jackett", "search-scope:fixture", "bgm", "https://lain.bgm.tv/pic/cover/l/789.jpg"},
		{"custom", "custom", "custom-source:fixture", "bgm", "https://lain.bgm.tv/pic/cover/l/789.jpg"},
		{"catalog-entry-missing", "mikan", "456", "bgm", "https://lain.bgm.tv/pic/cover/l/789.jpg"},
		{"untrusted-cover-url", "anibt", "456", "bgm", "https://lain.bgm.tv/pic/cover/l/789.jpg"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			db := openTestDB(t)
			owner := disposabledb.Owner(t)
			svc := newTestService(t, db)
			input := validCreateInput(t)
			input.Enabled, input.Secret = true, secretSetInput(t)
			input.TriggerKinds = []TriggerKind{TriggerDownloadSubmitted, TriggerDownloadFailed, TriggerFileOperationCompleted, TriggerFileOperationFailed, TriggerUnknownFilesImported}
			if _, err := svc.CreateTarget(ctx, input, "catalog-cover"); err != nil {
				t.Fatal(err)
			}
			animetest.SeedReleaseOwnerChain(t, owner, "cover-feed")
			animetest.SeedCompletedFileOpWithChannel(t, "cover-feed", 1, "completed", tt.provider)
			if _, err := owner.ExecContext(ctx, `
UPDATE subscriptions SET origin='oneshot', state='stopped' WHERE managed_anime_id='anime-trace';
INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at)
VALUES ('image-mikan','mikan','fixture','fresh',now(),'fixture',now()),
       ('image-anibt','anibt','fixture','fresh',now(),'fixture',now());
INSERT INTO provider_catalog_entries(id, snapshot_id, provider, provider_entry_ref, title, source_hint, state, observed_at)
VALUES ('mikan-123','image-mikan','mikan','123','Fixture','{"cover":"https://mikanani.me/images/123.jpg?width=400&format=webp"}','present',now()),
       ('anibt-123','image-anibt','anibt','123','Fixture','{"cover":"https://r2.anibt.net/covers/123.jpg"}','present',now()),
       ('anibt-456','image-anibt','anibt','456','Fixture','{"cover":"https://not-the-provider.example.test/456.jpg"}','present',now());
INSERT INTO media_poster_cache(bgm_id, image_bytes, content_type, image_url, expires_at)
VALUES ('789',decode('ff','hex'),'image/jpeg','https://lain.bgm.tv/pic/cover/l/789.jpg',now()+interval '1 hour');`); err != nil {
				t.Fatal(err)
			}
			if _, err := owner.ExecContext(ctx, `
INSERT INTO one_shot_acquisitions(id, managed_anime_id, subscription_id, rss_source_id, release_id,
    download_task_id, correlation_id, request_idempotency_key, request_hash, result_snapshot, state, input_snapshot)
VALUES ('cover-marker','anime-trace','subscription-trace','cover-feed','rel-ep-1','task-rel-ep-1',
    'fixture-correlation','fixture-request','sha256:'||repeat('a',64),'{}','running',
    jsonb_build_object('selected_seed',jsonb_build_object('provider',$1::text,'provider_entry_ref',$2::text)))`, tt.provider, tt.entry); err != nil {
				t.Fatal(err)
			}
			events := []struct{ trigger, ownerType, id string }{
				{string(TriggerDownloadSubmitted), plugintargetport.OwnerDownloadTask, "task-rel-ep-1"},
				{string(TriggerDownloadFailed), plugintargetport.OwnerDownloadTask, "task-rel-ep-1"},
				{string(TriggerFileOperationCompleted), plugintargetport.OwnerFileOperation, "op-rel-ep-1"},
				{string(TriggerFileOperationFailed), plugintargetport.OwnerFileOperation, "op-rel-ep-1"},
				{string(TriggerUnknownFilesImported), plugintargetport.OwnerAcquisition, "acq-task-rel-ep-1"},
			}
			for _, ev := range events {
				appendPendingAdmission(t, ctx, db, svc, plugintargetport.BusinessDeliveryRequest{
					SourceOwnerType: ev.ownerType, SourceOwnerID: ev.id, SourceOwnerVersion: 2,
					TriggerKind: ev.trigger, ResultKey: "cover-" + ev.trigger,
					Payload: json.RawMessage(`{"anime.title":"Fixture","file.name":"video.mkv","bgm.id":"789"}`),
				})
			}
			// The sender must not depend on the temporary mount's marker still existing.
			if _, err := owner.ExecContext(ctx, `DELETE FROM one_shot_acquisitions WHERE id='cover-marker'`); err != nil {
				t.Fatal(err)
			}
			if count := resolveWorkflowAdmissions(t, svc, string(TriggerFileOperationCompleted)); count != 1 {
				t.Fatalf("import deliveries=%d", count)
			}
			handler := NewPluginDeliverHandler(db, nil, nil, nil, nil)
			handler.SetNotificationImageResolver(identitymetadataadapter.NewNotificationImageResolver(db, notificationCoverConnections{}, nil, nil))
			for _, ev := range events {
				trigger := ev.trigger
				var id string
				if err := db.QueryRowContext(ctx, `SELECT id FROM plugin_deliveries WHERE trigger_kind=$1 AND managed_anime_id IS NULL`, trigger).Scan(&id); err != nil {
					t.Fatal(err)
				}
				row, err := handler.loadDelivery(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				msg, err := handler.resolveOutboundMessage(ctx, row, row.renderedTitle.String)
				if err != nil || msg.Image == nil || msg.Image.Source != tt.source || msg.Image.URL != tt.url {
					t.Fatalf("%s: image=%+v err=%v", trigger, msg.Image, err)
				}
				_, mode, err := handler.buildTelegramMessageRequest(ctx, row, msg, `{"bot_token":"123:abc"}`)
				if err != nil || mode.String() != "sendPhoto:url" || mode.Source != tt.source {
					t.Fatalf("%s: Telegram mode=%+v err=%v", trigger, mode, err)
				}
			}
		})
	}
}

type notificationCoverConnections struct{}

func (notificationCoverConnections) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return nil, nil
}
