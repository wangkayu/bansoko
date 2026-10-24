package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
)

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
