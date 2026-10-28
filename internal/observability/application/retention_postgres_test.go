package application

import (
	"context"
	"database/sql"
	"testing"

	retentionpersistence "bansoko/internal/observability/adapter/persistence"
	"bansoko/internal/testutil/disposabledb"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	return db
}

func TestRetentionServicePurgeExpired(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	seedReplacementRows(t, ctx, db)

	service, err := NewRetentionServiceWithRepository(db, retentionpersistence.NewRepository(db))
	if err != nil {
		t.Fatalf("new retention service: %v", err)
	}
	result, err := service.PurgeExpired(ctx)
	if err != nil {
		t.Fatalf("purge expired evidence: %v", err)
	}
	want := RetentionResult{
		PluginDeliveryAttempts: 3,
		PluginDeliveries:       3,
		SubscriptionLogEntries: 2,
		AutomationDispatchRuns: 1,
		DiagnosticEpisodes:     1,
	}
	if result != want {
		t.Fatalf("purge result: got %+v, want %+v", result, want)
	}
	if result.Total() != 10 {
		t.Fatalf("purge total: got %d, want 10", result.Total())
	}

	assertCount(t, ctx, db, "subscription_log_entries", "id = 'log-future'", 1)
	assertCount(t, ctx, db, "subscription_log_entries", "id = 'log-expired'", 0)
	assertCount(t, ctx, db, "subscription_log_entries", "id = 'log-expired-stopped'", 0)
	assertCount(t, ctx, db, "automation_dispatch_runs", "id = 'dispatch-future'", 1)
	assertCount(t, ctx, db, "automation_dispatch_runs", "id = 'dispatch-expired'", 0)
	assertCount(t, ctx, db, "automation_dispatch_runs", "state IN ('queued', 'running')", 2)
	assertCount(t, ctx, db, "diagnostic_episodes", "id = 'diagnostic-future'", 1)
	assertCount(t, ctx, db, "diagnostic_episodes", "id = 'diagnostic-expired'", 0)
	assertCount(t, ctx, db, "diagnostic_episodes", "id = 'diagnostic-open-expired'", 1)
	assertCount(t, ctx, db, "plugin_deliveries", "id = 'delivery-future'", 1)
	assertCount(t, ctx, db, "plugin_deliveries", "state IN ('pending', 'delivering', 'waiting_retry')", 4)
	assertCount(t, ctx, db, "plugin_deliveries", "id = 'delivery-delivered-expired'", 0)
	assertCount(t, ctx, db, "plugin_deliveries", "id = 'delivery-failed-expired'", 0)
	assertCount(t, ctx, db, "plugin_delivery_attempts", "id = 'attempt-future'", 1)
	assertCount(t, ctx, db, "plugin_delivery_attempts", "id = 'attempt-pending-incomplete'", 1)
	assertCount(t, ctx, db, "plugin_delivery_attempts", "id = 'attempt-unknown-complete'", 0)
	// An expired unknown delivery has spent its retry budget; with no live WorkItem it expires like delivered/failed.
	assertCount(t, ctx, db, "plugin_deliveries", "id = 'delivery-unknown-expired'", 0)
	assertCount(t, ctx, db, "plugin_delivery_attempts", "id = 'attempt-delivered-complete'", 0)
	assertCount(t, ctx, db, "plugin_delivery_attempts", "id = 'attempt-failed-error'", 0)
	assertCount(t, ctx, db, "routine_activities", "id = 'routine-retention'", 1)
	assertCount(t, ctx, db, "managed_anime", "id IN ('anime-retention', 'anime-retention-stopped')", 2)
	assertCount(t, ctx, db, "subscriptions", "id IN ('subscription-retention', 'subscription-retention-stopped')", 2)
}

func seedReplacementRows(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin retention fixture: %v", err)
	}
	defer tx.Rollback()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed retention fixture: %v", err)
		}
	}

	exec(`INSERT INTO managed_anime(id, title) VALUES ($1, $2), ($3, $4)`,
		"anime-retention", "Replacement Test Anime", "anime-retention-stopped", "Stopped Replacement Test Anime")
	exec(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
		VALUES ($1, $2, 'custom', $3, 'active'),
		       ($4, $5, 'custom', $6, 'stopped')`,
		"subscription-retention", "anime-retention", "retention",
		"subscription-retention-stopped", "anime-retention-stopped", "stopped-retention")
	exec(`INSERT INTO routine_activities(
		id, kind, scope_kind, scope_id, managed_anime_id, outcome, last_executed_at,
		last_result_code, summary
	) VALUES ($1, 'rss_refresh', 'subscription', $2, $3, 'success', CURRENT_TIMESTAMP,
		'success', 'retention fixture activity')`, "routine-retention", "subscription-retention", "anime-retention")

	exec(`INSERT INTO subscription_log_entries(
		id, subscription_id, managed_anime_id, kind, outcome, occurred_at, message_code, message_params,
		dedupe_key, retention_until
	) VALUES
		('log-expired', 'subscription-retention', 'anime-retention', 'rss_new_content', 'info',
		 CURRENT_TIMESTAMP - INTERVAL '2 hours', 'rss_new_content', '{"count":1}', 'expired',
		 CURRENT_TIMESTAMP - INTERVAL '1 hour'),
		('log-expired-stopped', 'subscription-retention-stopped', 'anime-retention-stopped', 'rss_new_content', 'info',
		 CURRENT_TIMESTAMP - INTERVAL '2 hours', 'rss_new_content', '{"count":1}', 'expired-stopped',
		 CURRENT_TIMESTAMP - INTERVAL '1 hour'),
		('log-future', 'subscription-retention', 'anime-retention', 'rss_new_content', 'info',
		 CURRENT_TIMESTAMP, 'rss_new_content', '{"count":1}', 'future',
		 CURRENT_TIMESTAMP + INTERVAL '1 day')`)

	exec(`INSERT INTO automation_dispatch_runs(
		id, kind, trigger, state, target_count, enqueued_count, started_at, finished_at, retention_until
	) VALUES
		('dispatch-expired', 'rss_refresh', 'scheduled', 'completed', 1, 1,
		 CURRENT_TIMESTAMP - INTERVAL '2 hours', CURRENT_TIMESTAMP - INTERVAL '90 minutes',
		 CURRENT_TIMESTAMP - INTERVAL '1 hour'),
		('dispatch-future', 'rss_refresh', 'manual', 'completed', 1, 1,
		 CURRENT_TIMESTAMP - INTERVAL '1 minute', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '1 day'),
		('dispatch-queued-expired', 'metadata_match', 'manual', 'queued', 0, 0,
		 NULL, NULL, CURRENT_TIMESTAMP - INTERVAL '1 hour'),
		('dispatch-running-expired', 'group_discovery', 'manual', 'running', 0, 0,
		 CURRENT_TIMESTAMP - INTERVAL '2 hours', NULL, CURRENT_TIMESTAMP - INTERVAL '1 hour')`)

	exec(`INSERT INTO diagnostic_episodes(
		id, kind, variant, severity, target_id, affected_ids, recent,
		first_failed_at, last_failed_at, succeeded_after_at, occurrence_count, retention_until
	) VALUES
		('diagnostic-expired', 'metadata', 'unreachable', 'error', 'tmdb-expired', '{}', '[]'::jsonb,
		 CURRENT_TIMESTAMP - INTERVAL '2 hours', CURRENT_TIMESTAMP - INTERVAL '2 hours',
		 CURRENT_TIMESTAMP - INTERVAL '30 minutes', 1, CURRENT_TIMESTAMP - INTERVAL '1 hour'),
		('diagnostic-future', 'metadata', 'unreachable', 'warning', 'tmdb-future', '{}', '[]'::jsonb,
		 CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, NULL, 1, CURRENT_TIMESTAMP + INTERVAL '1 day'),
		('diagnostic-open-expired', 'metadata', 'unreachable', 'error', 'tmdb-open', '{}', '[]'::jsonb,
		 CURRENT_TIMESTAMP - INTERVAL '2 hours', CURRENT_TIMESTAMP - INTERVAL '2 hours', NULL, 1,
		 CURRENT_TIMESTAMP - INTERVAL '1 hour')`)

	exec(`INSERT INTO plugin_target_configs(
		id, name, plugin_kind, adapter_kind, enabled, trigger_kinds, config_schema_version,
		config, template_set
	) VALUES ($1, $2, 'notification', 'webhook', FALSE, '[]', 1,
		'{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb,
		'{"language_version":"bansoko-template-1","catalog_version":"notification-catalog-v1","events":{}}'::jsonb)`,
		"plugin-target-retention", "Replacement Test Target")

	exec(`INSERT INTO plugin_deliveries(
		id, plugin_target_config_id, plugin_target_config_version, adapter_kind,
		adapter_config_snapshot, delivery_kind, trigger_kind, source_owner_type, source_owner_id,
		source_owner_version, source_result_key, managed_anime_id, state, idempotency_key,
		payload_schema_version, payload, payload_digest, template_language_version,
		template_catalog_version, title_template_snapshot, body_template_snapshot,
		rendered_title, rendered_body, rendered_message_digest, target_display, delivered_at, retention_until
	) VALUES
		('delivery-pending-expired', 'plugin-target-retention', 1, 'webhook',
		 '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, 'business',
		 'download_submitted', 'download_task', 'task-pending', 1, 'result-pending', 'anime-retention',
		 'pending', 'delivery-key-pending', 1, '{}'::jsonb,
		 'sha256:0000000000000000000000000000000000000000000000000000000000000000',
		 'bansoko-template-1', 'notification-catalog-v1', 'Title', 'Body', 'Rendered title', 'Rendered body',
		 'sha256:1111111111111111111111111111111111111111111111111111111111111111', 'example.test', NULL,
		 CURRENT_TIMESTAMP - INTERVAL '1 hour'),
		('delivery-delivering-expired', 'plugin-target-retention', 1, 'webhook',
		 '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, 'business',
		 'download_submitted', 'download_task', 'task-delivering', 1, 'result-delivering', 'anime-retention',
		 'delivering', 'delivery-key-delivering', 1, '{}'::jsonb,
		 'sha256:2222222222222222222222222222222222222222222222222222222222222222',
		 'bansoko-template-1', 'notification-catalog-v1', 'Title', 'Body', 'Rendered title', 'Rendered body',
		 'sha256:3333333333333333333333333333333333333333333333333333333333333333', 'example.test', NULL,
		 CURRENT_TIMESTAMP - INTERVAL '1 hour'),
		('delivery-waiting-retry-expired', 'plugin-target-retention', 1, 'webhook',
		 '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, 'business',
		 'download_submitted', 'download_task', 'task-waiting-retry', 1, 'result-waiting-retry', 'anime-retention',
		 'waiting_retry', 'delivery-key-waiting-retry', 1, '{}'::jsonb,
		 'sha256:4444444444444444444444444444444444444444444444444444444444444444',
		 'bansoko-template-1', 'notification-catalog-v1', 'Title', 'Body', 'Rendered title', 'Rendered body',
		 'sha256:5555555555555555555555555555555555555555555555555555555555555555', 'example.test', NULL,
		 CURRENT_TIMESTAMP - INTERVAL '1 hour'),
		('delivery-unknown-expired', 'plugin-target-retention', 1, 'webhook',
		 '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, 'business',
		 'download_submitted', 'download_task', 'task-unknown', 1, 'result-unknown', 'anime-retention',
		 'unknown', 'delivery-key-unknown', 1, '{}'::jsonb,
		 'sha256:6666666666666666666666666666666666666666666666666666666666666666',
		 'bansoko-template-1', 'notification-catalog-v1', 'Title', 'Body', 'Rendered title', 'Rendered body',
		 'sha256:7777777777777777777777777777777777777777777777777777777777777777', 'example.test', NULL,
		 CURRENT_TIMESTAMP - INTERVAL '1 hour'),
		('delivery-future', 'plugin-target-retention', 1, 'webhook',
		 '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, 'business',
		 'download_submitted', 'download_task', 'task-future', 1, 'result-future', 'anime-retention',
		 'pending', 'delivery-key-future', 1, '{}'::jsonb,
		 'sha256:8888888888888888888888888888888888888888888888888888888888888888',
		 'bansoko-template-1', 'notification-catalog-v1', 'Title', 'Body', 'Rendered title', 'Rendered body',
		 'sha256:9999999999999999999999999999999999999999999999999999999999999999', 'example.test', NULL,
		 CURRENT_TIMESTAMP + INTERVAL '1 day'),
		('delivery-delivered-expired', 'plugin-target-retention', 1, 'webhook',
		 '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, 'business',
		 'download_submitted', 'download_task', 'task-delivered', 1, 'result-delivered', 'anime-retention',
		 'delivered', 'delivery-key-delivered', 1, '{}'::jsonb,
		 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
		 'bansoko-template-1', 'notification-catalog-v1', 'Title', 'Body', 'Rendered title', 'Rendered body',
		 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'example.test',
		 CURRENT_TIMESTAMP - INTERVAL '90 minutes', CURRENT_TIMESTAMP - INTERVAL '1 hour'),
		('delivery-failed-expired', 'plugin-target-retention', 1, 'webhook',
		 '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, 'business',
		 'download_submitted', 'download_task', 'task-failed', 1, 'result-failed', 'anime-retention',
		 'failed', 'delivery-key-failed', 1, '{}'::jsonb,
		 'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
		 'bansoko-template-1', 'notification-catalog-v1', 'Title', 'Body', 'Rendered title', 'Rendered body',
		 'sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd', 'example.test', NULL,
		 CURRENT_TIMESTAMP - INTERVAL '1 hour')`)

	exec(`INSERT INTO plugin_delivery_attempts(
		id, plugin_delivery_id, attempt_number, started_at, response_received_at, target_display,
		payload_digest, response_status, error_code, error, retention_until
	) VALUES
		('attempt-pending-incomplete', 'delivery-pending-expired', 1,
		 CURRENT_TIMESTAMP - INTERVAL '2 hours', NULL, 'example.test',
		 'sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee', NULL, NULL, NULL,
		 CURRENT_TIMESTAMP - INTERVAL '1 hour'),
		('attempt-unknown-complete', 'delivery-unknown-expired', 1,
		 CURRENT_TIMESTAMP - INTERVAL '2 hours', CURRENT_TIMESTAMP - INTERVAL '90 minutes', 'example.test',
		 'sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff', 200, NULL, NULL,
		 CURRENT_TIMESTAMP - INTERVAL '1 hour'),
		('attempt-delivered-complete', 'delivery-delivered-expired', 1,
		 CURRENT_TIMESTAMP - INTERVAL '2 hours', CURRENT_TIMESTAMP - INTERVAL '90 minutes', 'example.test',
		 'sha256:1212121212121212121212121212121212121212121212121212121212121212', 200, NULL, NULL,
		 CURRENT_TIMESTAMP - INTERVAL '1 hour'),
		('attempt-failed-error', 'delivery-failed-expired', 1,
		 CURRENT_TIMESTAMP - INTERVAL '2 hours', NULL, 'example.test',
		 'sha256:3434343434343434343434343434343434343434343434343434343434343434', NULL, 'delivery_failed', 'failed',
		 CURRENT_TIMESTAMP - INTERVAL '1 hour'),
		('attempt-future', 'delivery-future', 1,
		 CURRENT_TIMESTAMP, NULL, 'example.test',
		 'sha256:5656565656565656565656565656565656565656565656565656565656565656', NULL, NULL, NULL,
		 CURRENT_TIMESTAMP + INTERVAL '1 day')`)

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit retention fixture: %v", err)
	}
}

func TestRetentionServicePurgeExpiredIsBoundedAndIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO managed_anime(id, title) VALUES ('anime-retention-batch', 'Replacement Batch Anime');
		INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
		VALUES ('subscription-retention-batch', 'anime-retention-batch', 'custom', 'retention-batch', 'active')`); err != nil {
		t.Fatalf("seed retention batch owner: %v", err)
	}
	rowCount := retentionBatchSize*2 + 1
	if _, err := db.ExecContext(ctx, `
		INSERT INTO subscription_log_entries(
			id, subscription_id, managed_anime_id, kind, outcome, occurred_at, message_code, message_params,
			dedupe_key, retention_until
		)
		SELECT format('batch-log-%s', series), 'subscription-retention-batch', 'anime-retention-batch',
			'rss_new_content', 'info', CURRENT_TIMESTAMP - INTERVAL '2 hours',
			'rss_new_content', '{"count":1}'::jsonb, format('batch-%s', series),
			CURRENT_TIMESTAMP - INTERVAL '1 hour'
		FROM generate_series(1, $1) AS series`, rowCount); err != nil {
		t.Fatalf("seed expired retention batch: %v", err)
	}

	service, err := NewRetentionServiceWithRepository(db, retentionpersistence.NewRepository(db))
	if err != nil {
		t.Fatalf("new retention service: %v", err)
	}
	for i, want := range []int64{int64(retentionBatchSize), int64(retentionBatchSize), 1, 0} {
		result, err := service.PurgeExpired(ctx)
		if err != nil {
			t.Fatalf("purge expired batch %d: %v", i+1, err)
		}
		if result.PluginDeliveryAttempts > int64(retentionBatchSize) ||
			result.PluginDeliveries > int64(retentionBatchSize) ||
			result.SubscriptionLogEntries > int64(retentionBatchSize) ||
			result.AutomationDispatchRuns > int64(retentionBatchSize) ||
			result.DiagnosticEpisodes > int64(retentionBatchSize) {
			t.Fatalf("purge batch %d exceeded fixed batch size: %+v", i+1, result)
		}
		if result.SubscriptionLogEntries != want || result.Total() != want {
			t.Fatalf("purge batch %d: got %+v, want %d rows", i+1, result, want)
		}
	}
	assertCount(t, ctx, db, "subscription_log_entries", "subscription_id = 'subscription-retention-batch'", 0)
}

func assertCount(t *testing.T, ctx context.Context, db *sql.DB, table, predicate string, want int) {
	t.Helper()
	var got int
	query := "SELECT COUNT(*) FROM " + table + " WHERE " + predicate
	if err := db.QueryRowContext(ctx, query).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Errorf("count %s: got %d, want %d", table, got, want)
	}
}
