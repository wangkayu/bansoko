package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"bansoko/internal/apperrors"
	observabilitypersistence "bansoko/internal/observability/adapter/persistence"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

// purgeExpiredDeliveries runs the evidence janitor's delivery cleaner over the
// disposable database in one caller-owned transaction.
func purgeExpiredDeliveries(t *testing.T, db *sql.DB, batchSize int) int64 {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin delivery retention transaction: %v", err)
	}
	defer tx.Rollback()
	deleted, err := observabilitypersistence.NewRepository(db).PurgePluginDeliveries(context.Background(), txscope.Wrap(tx), batchSize)
	if err != nil {
		t.Fatalf("purge plugin deliveries: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit delivery retention transaction: %v", err)
	}
	return deleted
}

// seedRetiredDelivery writes one disabled target and two completed deliveries:
// one long past its retention bound and one still retained.
func seedRetiredDelivery(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO plugin_target_configs(
    id, name, plugin_kind, adapter_kind, enabled, trigger_kinds, config_schema_version, config, template_set
) VALUES ('target-retention', 'Replacement Target', 'notification', 'webhook', FALSE, '[]', 1,
    '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb,
    '{"language_version":"bansoko-template-1","catalog_version":"notification-catalog-v1","events":{}}'::jsonb)`); err != nil {
		t.Fatalf("seed plugin target: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO plugin_deliveries(
    id, plugin_target_config_id, plugin_target_config_version, adapter_kind, adapter_config_snapshot,
    delivery_kind, trigger_kind, source_owner_type, source_owner_id, source_owner_version, source_result_key,
    state, idempotency_key, payload_schema_version, payload, payload_digest, template_language_version,
    template_catalog_version, title_template_snapshot, body_template_snapshot, rendered_title, rendered_body,
    rendered_message_digest, target_display, delivered_at, retention_until
) VALUES
    ('delivery-expired', 'target-retention', 1, 'webhook', '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb,
     'business', 'download_submitted', 'download_task', 'task-expired', 1, 'result-expired',
     'delivered', 'delivery-key-expired', 1, '{}'::jsonb,
     'sha256:0000000000000000000000000000000000000000000000000000000000000001',
     'bansoko-template-1', 'notification-catalog-v1', 'Title', 'Body', 'Expired title', 'Expired body',
     'sha256:0000000000000000000000000000000000000000000000000000000000000002', 'example.test',
     CURRENT_TIMESTAMP - INTERVAL '40 days', CURRENT_TIMESTAMP - INTERVAL '1 hour'),
    ('delivery-retained', 'target-retention', 1, 'webhook', '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb,
     'business', 'download_submitted', 'download_task', 'task-retained', 1, 'result-retained',
     'delivered', 'delivery-key-retained', 1, '{}'::jsonb,
     'sha256:0000000000000000000000000000000000000000000000000000000000000003',
     'bansoko-template-1', 'notification-catalog-v1', 'Title', 'Body', 'Retained title', 'Retained body',
     'sha256:0000000000000000000000000000000000000000000000000000000000000004', 'example.test',
     CURRENT_TIMESTAMP - INTERVAL '1 hour', CURRENT_TIMESTAMP + INTERVAL '30 days')`); err != nil {
		t.Fatalf("seed plugin deliveries: %v", err)
	}
}

// TestDeliveryDetailAfterRetentionExpiry proves the delivery read surface after
// the janitor removed an expired delivery: the detail is a typed not-found and
// the list no longer carries it, while a retained delivery keeps both.
func TestDeliveryDetailAfterRetentionExpiry(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	service := newTestService(t, db)
	seedRetiredDelivery(t, disposabledb.Owner(t))

	before, err := service.GetDelivery(ctx, "delivery-expired")
	if err != nil || len(before.Attempts) != 0 {
		t.Fatalf("delivery before expiry: %+v %v", before, err)
	}
	if page, err := service.ListDeliveries(ctx, DeliveryQuery{}); err != nil || page.Total != 2 {
		t.Fatalf("both deliveries must be listed before expiry: %+v %v", page, err)
	}

	if deleted := purgeExpiredDeliveries(t, db, 500); deleted != 1 {
		t.Fatalf("one expired delivery must be deleted, deleted %d", deleted)
	}

	_, err = service.GetDelivery(ctx, "delivery-expired")
	var notFound *apperrors.NotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("an expired delivery must read as not found, got %v", err)
	}
	page, err := service.ListDeliveries(ctx, DeliveryQuery{})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("only the retained delivery stays listed: %+v %v", page, err)
	}
	retained, err := service.GetDelivery(ctx, "delivery-retained")
	if err != nil || len(retained.Attempts) != 0 {
		t.Fatalf("the retained delivery must stay readable: %+v %v", retained, err)
	}
}

// TestUnknownDeliveryRetention proves an unknown delivery (the request may have
// been sent, its retry budget is spent) is bounded like delivered and failed
// ones: once expired, its settled attempts and then the delivery go, while an
// unknown delivery whose WorkItem is still live keeps both.
func TestUnknownDeliveryRetention(t *testing.T) {
	db := openTestDB(t)
	owner := disposabledb.Owner(t)
	ctx := context.Background()
	seedRetiredDelivery(t, owner)
	for _, id := range []string{"delivery-unknown", "delivery-unknown-live"} {
		if _, err := owner.ExecContext(ctx, `
INSERT INTO plugin_deliveries(
    id, plugin_target_config_id, plugin_target_config_version, adapter_kind, adapter_config_snapshot,
    delivery_kind, trigger_kind, source_owner_type, source_owner_id, source_owner_version, source_result_key,
    state, idempotency_key, payload_schema_version, payload, payload_digest, template_language_version,
    template_catalog_version, title_template_snapshot, body_template_snapshot, target_display, retention_until
) VALUES ($1, 'target-retention', 1, 'webhook', '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb,
    'business', 'download_failed', 'download_task', 'task-' || $1, 1, 'result-' || $1,
    'unknown', 'key-' || $1, 1, '{}'::jsonb,
    'sha256:0000000000000000000000000000000000000000000000000000000000000005',
    'bansoko-template-1', 'notification-catalog-v1', 'Title', 'Body', 'example.test',
    CURRENT_TIMESTAMP - INTERVAL '1 hour')`, id); err != nil {
			t.Fatalf("seed unknown delivery %s: %v", id, err)
		}
		if _, err := owner.ExecContext(ctx, `
INSERT INTO plugin_delivery_attempts(id, plugin_delivery_id, attempt_number, started_at, target_display, payload_digest,
    error_code, retention_until)
VALUES ('attempt-' || $1, $1, 1, CURRENT_TIMESTAMP - INTERVAL '20 days', 'example.test',
    'sha256:0000000000000000000000000000000000000000000000000000000000000005', 'adapter_timeout',
    CURRENT_TIMESTAMP - INTERVAL '1 hour')`, id); err != nil {
			t.Fatalf("seed unknown attempt %s: %v", id, err)
		}
	}
	if _, err := owner.ExecContext(ctx, `
INSERT INTO work_items(id, owner_type, owner_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ('wi-unknown-live', 'plugin_delivery', 'delivery-unknown-live', 'plugin_deliver', 'deliver:delivery-unknown-live', 10, '{}', 1)`); err != nil {
		t.Fatalf("seed live delivery work item: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := observabilitypersistence.NewRepository(db).PurgePluginDeliveryAttempts(ctx, txscope.Wrap(tx), 500)
	if err != nil {
		t.Fatalf("purge attempts: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempts deleted %d, want 1 (the live unknown delivery keeps its attempt)", attempts)
	}
	// delivery-expired and delivery-unknown; the live one and the retained one stay.
	if deleted := purgeExpiredDeliveries(t, db, 500); deleted != 2 {
		t.Fatalf("deliveries deleted %d, want 2", deleted)
	}
	for id, want := range map[string]int{"delivery-unknown": 0, "delivery-unknown-live": 1, "delivery-retained": 1} {
		var n int
		if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM plugin_deliveries WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("%s rows %d, want %d", id, n, want)
		}
	}
	var liveAttempts int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM plugin_delivery_attempts WHERE plugin_delivery_id = 'delivery-unknown-live'`).Scan(&liveAttempts); err != nil {
		t.Fatal(err)
	}
	if liveAttempts != 1 {
		t.Fatalf("the live unknown delivery lost its attempt")
	}
}
