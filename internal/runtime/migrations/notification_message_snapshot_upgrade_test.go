package migrations

import (
	"context"
	"strings"
	"testing"
)

func TestNotificationMessageSnapshotUpgradePreservesLegacyDelivery(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "146_manual_operation_events.sql")
	if _, err := db.ExecContext(ctx, `
INSERT INTO plugin_target_configs(id,name,plugin_kind,adapter_kind,trigger_kinds,config_schema_version,config,template_set)
VALUES ('target-149','Fixture','notification','webhook','["download_submitted"]',1,'{"adapter_kind":"webhook","auth_kind":"none"}','{}');
INSERT INTO plugin_deliveries(id,plugin_target_config_id,plugin_target_config_version,adapter_kind,
    adapter_config_snapshot,delivery_kind,trigger_kind,source_owner_type,source_owner_id,source_owner_version,
    source_result_key,state,idempotency_key,payload_schema_version,payload,payload_digest,
    template_language_version,template_catalog_version,title_template_snapshot,body_template_snapshot,
    rendered_title,rendered_body,rendered_message_digest,target_display)
VALUES ('delivery-149','target-149',1,'webhook','{"adapter_kind":"webhook","auth_kind":"none"}',
    'business','download_submitted','download_task','task-149',1,'result-149','pending','delivery-149',1,
    '{}','sha256:'||repeat('a',64),'bansoko-template-1','notification-catalog-v1','title','body',
    'title','body','sha256:'||repeat('b',64),'Fixture');`); err != nil {
		t.Fatal(err)
	}
	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	var legacy bool
	var state, title, body string
	if err := db.QueryRowContext(ctx, `SELECT message_snapshot IS NULL, state, rendered_title, rendered_body
FROM plugin_deliveries WHERE id='delivery-149'`).Scan(&legacy, &state, &title, &body); err != nil {
		t.Fatal(err)
	}
	if !legacy || state != "pending" || title != "title" || body != "body" {
		t.Fatalf("upgraded delivery: legacy=%v state=%q title=%q body=%q", legacy, state, title, body)
	}
	if _, err := db.ExecContext(ctx, `UPDATE plugin_deliveries SET message_snapshot='{"version":1,"title":"title","parts":[]}'
WHERE id='delivery-149'`); err == nil || !strings.Contains(err.Error(), "NOTIFICATION_MESSAGE_IMMUTABLE") {
		t.Fatalf("legacy delivery was open to backfill: %v", err)
	}
}
