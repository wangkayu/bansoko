package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
)

func TestFileNotificationUsesSubscriptionOriginOnlyForImage(t *testing.T) {
	db := openTestDB(t)
	owner := disposabledb.Owner(t)
	service := newTestService(t, db)
	ctx := context.Background()
	animetest.SeedReleaseOwnerChain(t, owner, "cover-feed")
	animetest.SeedSubscriptionFileOp(t, "cover-feed", 1, "completed", "automatic")
	animetest.SeedSubscriptionFileOp(t, "cover-feed", 2, "failed", "subscription_add")
	animetest.SeedCompletedFileOp(t, db, "cover-feed", 3, false, "completed") // one-shot with the same fixture origin reference

	input := validCreateInput(t)
	input.Enabled = true
	input.Secret = secretSetInput(t)
	if _, err := service.CreateTarget(ctx, input, "cover-target"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		operation string
		trigger   TriggerKind
		anime     string
	}{
		{"op-rel-ep-1", TriggerFileOperationCompleted, "anime-trace"},
		{"op-rel-ep-2", TriggerFileOperationFailed, "anime-trace"},
		{"op-rel-ep-3", TriggerFileOperationCompleted, ""},
		{"purged-operation", TriggerFileOperationCompleted, ""},
	}
	for _, tt := range tests {
		t.Run(tt.operation, func(t *testing.T) {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			req := plugintargetport.BusinessDeliveryRequest{
				SourceOwnerType: plugintargetport.OwnerFileOperation, SourceOwnerID: tt.operation,
				SourceOwnerVersion: 2, TriggerKind: string(tt.trigger), ResultKey: tt.operation,
				Payload: json.RawMessage(`{"anime.title":"Fixture","file.name":"video.mkv","bgm.id":"123"}`),
			}
			if err := service.EnqueueBusinessDelivery(ctx, txscope.Wrap(tx), req); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			resolver := &AdmissionResolver{Service: service, Logger: observabilityport.NopRuntimeLogger{}, SweepInterval: time.Second}
			if count, err := resolver.SweepOnce(ctx); err != nil || count != 1 {
				t.Fatalf("resolved=%d err=%v", count, err)
			}
			var id string
			var detached bool
			if err := db.QueryRowContext(ctx, `SELECT d.id, d.managed_anime_id IS NULL AND w.managed_anime_id IS NULL AND w.owner_lifecycle_epoch IS NULL
FROM plugin_deliveries d JOIN work_item_history w ON w.owner_type='plugin_delivery' AND w.owner_id=d.id WHERE d.source_owner_id=$1`, tt.operation).Scan(&id, &detached); err != nil || !detached {
				t.Fatalf("delivery and work must stay detached: %v, %v", detached, err)
			}
			images := &fakeNotificationImageResolver{image: plugintargetport.NotificationImage{
				URL: "https://images.example.test/cover.jpg",
			}}
			handler := NewPluginDeliverHandler(db, nil, nil, nil, nil)
			handler.SetNotificationImageResolver(images)
			row, err := handler.loadDelivery(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			_, err = handler.resolveOutboundMessage(ctx, row, row.renderedTitle.String)
			if err != nil || images.request.ManagedAnimeID != tt.anime || images.request.BGMID != "123" {
				t.Fatalf("image request=%+v err=%v", images.request, err)
			}
		})
	}
}

// Resolver consumes a self-contained ledger payload. This test does not claim
// to establish typed acquisition owner admission; that belongs to the finish.
func TestAdmissionResolverPreservesNullManagementAssociation(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()
	input := validCreateInput(t)
	input.Name = "null-management-target"
	input.AdapterKind = AdapterWebhook
	input.Config = webhookConfig(t, "none")
	input.Enabled = true
	input.Secret = SecretInput{Mode: "set", Value: json.RawMessage(`{"adapter_kind":"webhook","endpoint_url":"https://example.test/notify"}`)}
	if _, err := service.CreateTarget(ctx, input, "null-management-create"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	request := plugintargetport.BusinessDeliveryRequest{SourceOwnerType: plugintargetport.OwnerFileOperation, SourceOwnerID: "historical-operation", SourceOwnerVersion: 2, TriggerKind: plugintargetport.TriggerFileOperationCompleted, ResultKey: "historical-result", Payload: json.RawMessage(`{"anime.title":"Frozen historical title","file.name":"video.mkv","result.outcome":"success"}`)}
	if err := service.EnqueueBusinessDelivery(ctx, txscope.Wrap(tx), request); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	resolver := &AdmissionResolver{Service: service, Logger: observabilityport.NopRuntimeLogger{}, SweepInterval: time.Second}
	if count, err := resolver.SweepOnce(ctx); err != nil || count != 1 {
		t.Fatalf("resolved=%d err=%v", count, err)
	}
	var admissions, deliveries, works int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM notification_admissions WHERE managed_anime_id IS NULL AND state='resolved'),(SELECT count(*) FROM plugin_deliveries WHERE managed_anime_id IS NULL AND state='pending'),(SELECT count(*) FROM work_item_history WHERE kind='plugin_deliver' AND managed_anime_id IS NULL AND owner_lifecycle_epoch IS NULL)`).Scan(&admissions, &deliveries, &works); err != nil || admissions != 1 || deliveries != 1 || works != 1 {
		t.Fatalf("admissions=%d deliveries=%d works=%d err=%v", admissions, deliveries, works, err)
	}
	if count, err := resolver.SweepOnce(ctx); err != nil || count != 0 {
		t.Fatalf("repeat=%d err=%v", count, err)
	}
	failureTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer failureTx.Rollback()
	var targetID string
	if err := failureTx.QueryRow(`SELECT id FROM plugin_target_configs WHERE name='null-management-target'`).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	target, err := service.lockTargetTx(ctx, failureTx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	// A historical template can fail rendering without acquiring a management FK.
	target.TemplateSet = json.RawMessage(`{"events":{"file_operation_completed":{"mode":"custom","title_template":"{{unknown.variable}}","body_template":"Body"}}}`)
	failed := request
	failed.ResultKey = "historical-failed-template"
	if err := service.enqueueForTargetWithKey(ctx, failureTx, target, failed, "historical-failed-template"); err != nil {
		t.Fatal(err)
	}
	var failedCount int
	if err := failureTx.QueryRow(`SELECT count(*) FROM plugin_deliveries WHERE source_result_key='historical-failed-template' AND managed_anime_id IS NULL AND state='failed' AND last_error_code='notification_template_invalid'`).Scan(&failedCount); err != nil || failedCount != 1 {
		t.Fatalf("failed null delivery=%d err=%v", failedCount, err)
	}
}
