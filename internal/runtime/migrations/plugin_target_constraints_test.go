package migrations

import "testing"

// A plugin target keeps a closed trigger set (no duplicates, strings only) and
// an inner adapter kind that agrees with the outer one.
func TestPluginTargetConstraints(t *testing.T) {
	p := newConstraintProbe(t)
	const insert = `
		INSERT INTO plugin_target_configs(
			id, name, plugin_kind, adapter_kind, enabled, trigger_kinds,
			config_schema_version, config, template_set
		) VALUES ($1, $2, 'notification', 'webhook', FALSE, $3::jsonb, 1, $4::jsonb, '{}')`
	const webhookConfig = `{"adapter_kind":"webhook","auth_kind":"none"}`
	p.accept("valid plugin target", insert, "target-1", "Test target", `[]`, webhookConfig)
	p.reject("plugin target trigger allowlist", insert, "target-2", "Invalid target", `["unknown_trigger"]`, webhookConfig)
	p.reject("plugin target trigger uniqueness", insert, "target-3", "Duplicate trigger target", `["download_submitted", "download_submitted"]`, webhookConfig)
	p.reject("plugin target adapter kind mismatch", insert, "target-mismatch", "Mismatched target", `[]`, `{"adapter_kind":"telegram","chat_id":"1"}`)
	p.reject("plugin target trigger item type", insert, "target-4", "Non-string trigger target", `[1]`, webhookConfig)
}

// A plugin test request is recorded once per idempotency key and references its
// delivery by an opaque id.
func TestPluginTestIdempotencyRecordConstraints(t *testing.T) {
	p := newConstraintProbe(t)
	p.accept("plugin target", `
		INSERT INTO plugin_target_configs(
			id, name, plugin_kind, adapter_kind, enabled, trigger_kinds,
			config_schema_version, config, template_set
		) VALUES ('target-1', 'Test target', 'notification', 'webhook', FALSE, '[]', 1, '{"adapter_kind":"webhook","auth_kind":"none"}', '{}')`)
	p.accept("opaque plugin test delivery reference", `
		INSERT INTO idempotency_records(
			kind, idempotency_key, request_hash, plugin_target_config_id,
			expected_version, trigger_kind, canonical_request_body, delivery_id
		) VALUES ('plugin_test', 'request-1', 'sha256:' || repeat('a', 64), 'target-1', 1,
			'download_submitted', '{}', 'delivery-expired-1')`)
	p.reject("plugin test delivery reference format", `
		INSERT INTO idempotency_records(
			kind, idempotency_key, request_hash, plugin_target_config_id,
			expected_version, trigger_kind, canonical_request_body, delivery_id
		) VALUES ('plugin_test', 'request-2', 'sha256:' || repeat('b', 64), 'target-1', 1,
			'download_submitted', '{}', 'delivery id with spaces')`)
	p.reject("duplicate plugin test idempotency identity", `
		INSERT INTO idempotency_records(
			kind, idempotency_key, request_hash, plugin_target_config_id,
			expected_version, trigger_kind, canonical_request_body, delivery_id
		) VALUES ('plugin_test', 'request-1', 'sha256:' || repeat('c', 64), 'target-1', 1,
			'download_submitted', '{}', 'delivery-expired-2')`)
}

// A plugin delivery may reference its anime by an opaque id and carries the
// template language and catalog labels.
func TestPluginDeliveryConstraints(t *testing.T) {
	p := newConstraintProbe(t)
	p.accept("managed anime", `INSERT INTO managed_anime(id, title) VALUES ('anime-1', 'Title')`)
	p.accept("plugin target", `
		INSERT INTO plugin_target_configs(
			id, name, plugin_kind, adapter_kind, enabled, trigger_kinds,
			config_schema_version, config, template_set
		) VALUES ('target-1', 'Test target', 'notification', 'webhook', FALSE, '[]', 1, '{"adapter_kind":"webhook","auth_kind":"none"}', '{}')`)
	const insert = `
		INSERT INTO plugin_deliveries(
			id, plugin_target_config_id, plugin_target_config_version, adapter_kind,
			adapter_config_snapshot, delivery_kind, trigger_kind, source_owner_type,
			source_owner_id, source_owner_version, source_result_key, test_request_idempotency_key,
			managed_anime_id, state, idempotency_key, payload_schema_version, payload,
			payload_digest, template_language_version, template_catalog_version,
			title_template_snapshot, body_template_snapshot, target_display
		) VALUES ($1, 'target-1', 1, 'webhook', '{}', 'test', 'download_submitted',
			'plugin_target', 'target-1', 1, 'request-1', 'request-1', 'anime-1', 'pending',
			$2, 1, '{}', 'sha256:' || repeat('c', 64), $3,
			$4, '{{anime.title}}', '{{release.title}}', 'webhook target')`
	p.accept("plugin delivery with opaque anime reference", insert, "delivery-1", "delivery-effect-1", "bansoko-template-1", "notification-catalog-v1")
	p.reject("unknown template language version", insert, "delivery-2", "delivery-effect-2", "other-template-1", "notification-catalog-v1")
	p.reject("unknown template catalog version", insert, "delivery-3", "delivery-effect-3", "bansoko-template-1", "other-catalog-v1")
}

// A subscribe idempotency record pins exactly one managed anime and no other
// kind's columns.
func TestSubscribeIdempotencyRecordConstraints(t *testing.T) {
	p := newConstraintProbe(t)
	p.accept("first anime", `INSERT INTO managed_anime(id, title) VALUES ('anime-1', 'Title')`)
	p.accept("second anime", `INSERT INTO managed_anime(id, title) VALUES ('anime-2', 'Title 2')`)
	p.accept("subscribe idempotency record", `
		INSERT INTO idempotency_records(kind, idempotency_key, request_hash, managed_anime_id, result_snapshot)
		VALUES ('subscribe', 'request-1', 'sha256:' || repeat('d', 64), 'anime-1', '{}')`)
	p.reject("subscribe record without its managed anime",
		`INSERT INTO idempotency_records(kind, idempotency_key, request_hash, result_snapshot)
		 VALUES ('subscribe', 'request-subscribe-orphan', 'sha256:' || repeat('e', 64), '{}')`)
	p.reject("subscribe record carrying another kind's column",
		`INSERT INTO idempotency_records(kind, idempotency_key, request_hash, managed_anime_id, delivery_id, result_snapshot)
		 VALUES ('subscribe', 'request-subscribe-mixed', 'sha256:' || repeat('f', 64), 'anime-1', 'delivery-mixed', '{}')`)
	p.reject("second subscribe record for the same anime",
		`INSERT INTO idempotency_records(kind, idempotency_key, request_hash, managed_anime_id, result_snapshot)
		 VALUES ('subscribe', 'request-subscribe-second', 'sha256:' || repeat('0', 64), 'anime-1', '{}')`)
	p.reject("duplicate subscribe idempotency key",
		`INSERT INTO idempotency_records(kind, idempotency_key, request_hash, managed_anime_id, result_snapshot)
		 VALUES ('subscribe', 'request-1', 'sha256:' || repeat('1', 64), 'anime-2', '{}')`)
}
