package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var (
	ErrChecksumMismatch    = errors.New("migration checksum mismatch")
	ErrInvalidMigrationSet = errors.New("invalid migration set")
	ErrUnsupportedSchema   = errors.New("unsupported database schema")
	ErrSchemaIdentity      = errors.New("schema identity mismatch")
)

// baselineMigrationFile is the first migration of every installed schema;
// schemaIdentityValue is the identity row it writes. A database whose
// identity row differs was built by another migration history and is refused.
const (
	baselineMigrationFile = "001_baseline.sql"
	schemaIdentityValue   = "bansoko:baseline"
)

type Runner struct {
	DB       *sql.DB
	Dir      string
	ReadDir  func(string) ([]os.DirEntry, error)
	ReadFile func(string) ([]byte, error)
}

type MigrationApplyError struct {
	Operation string
	Version   string
	Err       error
}

func (e *MigrationApplyError) Error() string {
	return fmt.Sprintf("%s migration %s: %v", e.Operation, e.Version, e.Err)
}

func (e *MigrationApplyError) Unwrap() error { return e.Err }

type migration struct {
	version  string
	contents []byte
}

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// knownRelations is every relation the baseline and the later forward
// migrations create in the public schema, by kind. A relation outside this
// list, or one of another kind, means the database was not built by this
// migration set and is refused.
var knownRelations = map[string]string{
	"acquisition_files":                "r",
	"acquisition_import_retries":       "r",
	"acquisition_targets":              "r",
	"acquisitions":                     "r",
	"alignment_operations":             "r",
	"anime_config_candidate_revisions": "r",
	"anime_business_configs":           "r",
	"anime_config_candidates":          "r",
	"anime_config_overrides":           "r",
	"anime_metadata":                   "r",
	"api_tokens":                       "r",
	"artifact_files":                   "r",
	"artifacts":                        "r",
	"attr_rule_versions":               "r",
	"auth_sessions":                    "r",
	"automation_dispatch_runs":         "r",
	"automation_schedules":             "r",
	"catalog_suppressions":             "r",
	"cleanup_operations":               "r",
	"connections":                      "r",
	"deployment_recovery_runs":         "r",
	"diagnostic_episodes":              "r",
	"download_completions":             "r",
	"download_tasks":                   "r",
	"embedded_update_locks":            "r",
	"episode_title_cache":              "r",
	"file_manager_operations":          "r",
	"file_operations":                  "r",
	"filemaintenance_policies":         "r",
	"global_config_sections":           "r",
	"idempotency_records":              "r",
	"instance_bootstrap":               "r",
	"local_accounts":                   "r",
	"managed_anime":                    "r",
	"media_fact_files":                 "r",
	"media_fact_snapshots":             "r",
	"media_poster_cache":               "r",
	"provider_image_cache":             "r",
	"metadata_work_titles":             "r",
	"naming_template_library":          "r",
	"naming_vars_versions":             "r",
	"network_routes":                   "r",
	"notification_admissions":          "r",
	"offset_migration_generations":     "r",
	"offset_migration_plans":           "r",
	"one_shot_acquisitions":            "r",
	"one_shot_execution_attempts":      "r",
	"placement_companions":             "r",
	"placement_coverage":               "r",
	"placement_obligations":            "r",
	"placements":                       "r",
	"plugin_deliveries":                "r",
	"plugin_delivery_attempts":         "r",
	"plugin_target_configs":            "r",
	"postprocessing_plans":             "r",
	"provider_catalog_entries":         "r",
	"provider_catalog_preferences":     "r",
	"provider_catalog_snapshots":       "r",
	"provider_feed_snapshot_seeds":     "r",
	"provider_feed_snapshots":          "r",
	"provider_group_dictionary":        "r",
	"provider_group_refresh_health":    "r",
	"provider_group_scope_generations": "r",
	"provider_group_selections":        "r",
	"provider_group_snapshots":         "r",
	"reconciliation_files":             "r",
	"reconciliation_scans":             "r",
	"release_candidate_facts":          "r",
	"release_torrent_inputs":           "r",
	"releases":                         "r",
	"replacement_decisions":            "r",
	"routine_activities":               "r",
	"rss_provider_circuits":            "r",
	"rss_source_runtime":               "r",
	"rss_sources":                      "r",
	"schema_identity":                  "r",
	"schema_migrations":                "r",
	"secret_values":                    "r",
	"seed_torrent_cache":               "r",
	"subscription_log_entries":         "r",
	"subscriptions":                    "r",
	"work_items":                       "r",
	"work_item_history":                "v",
	"work_item_outcomes":               "r",
	"attr_rule_versions_id_seq":        "S",
	"naming_vars_versions_id_seq":      "S",
}

// requiredColumns are the columns the runtime depends on by name; a database
// that lacks one is refused before the server starts.
var requiredColumns = map[string][]string{
	"schema_identity":                  {"singleton_id", "identity", "baseline_version", "installed_at"},
	"schema_migrations":                {"version", "checksum", "applied_at"},
	"connections":                      {"id", "kind", "name", "endpoint", "secret_ref", "secret_present", "network_route_id", "state", "health_version", "version", "created_at", "updated_at", "config"},
	"instance_bootstrap":               {"singleton_id", "state", "install_id", "version", "completed_at", "created_at", "updated_at"},
	"auth_sessions":                    {"id", "account_id", "token_hash", "expires_at", "created_at", "revoked_at"},
	"api_tokens":                       {"id", "account_id", "name", "token_hash", "created_at", "last_used_at", "version", "secret_ref", "scopes"},
	"managed_anime":                    {"title_override", "provider_title", "lifecycle_state", "fence_epoch", "lifecycle_version", "retired_at", "retired_subscription_version"},
	"anime_metadata":                   {"media_kind_override", "year", "season"},
	"subscriptions":                    {"origin", "origin_entry_ref", "instance_label"},
	"rss_sources":                      {"source_kind", "locator", "locator_display", "group_name", "title", "episode_offset", "provenance", "http_feed_connection_ref", "network_route_ref"},
	"provider_group_selections":        {"id", "subscription_id", "provider", "group_ref", "display_name", "selection_mode", "manual_selected", "effective_selected", "effective_score", "count_in_limit", "version", "available", "availability_generation"},
	"provider_catalog_snapshots":       {"id", "provider", "season", "status", "observed_at", "source_version", "cursor", "etag", "last_modified", "contract_version", "health_state", "health_code", "health_message", "health_checked_at", "error_code", "error_message", "version", "trace_id", "retention_until"},
	"provider_feed_snapshots":          {"id", "provider", "provider_entry_ref", "provider_group_ref", "version", "fetched_at", "content_hash", "hash_schema", "projection_schema", "fresh_until", "retention_until", "seed_count", "created_at"},
	"provider_feed_snapshot_seeds":     {"id", "snapshot_id", "stable_seed_key", "locator", "locator_display", "title", "published_at", "size_bytes", "info_hash", "payload_digest", "torrent_filename", "torrent_state", "torrent_error_code", "torrent_error_reason", "torrent_version", "info_hash_v1", "info_hash_v2", "payload", "payload_size_bytes", "manifest_payload_digest", "manifest_torrent_parser_version", "manifest_torrent_name", "manifest_total_size_bytes", "manifest_file_count", "manifest_projection_digest", "files", "anibt_tags", "created_at"},
	"seed_torrent_cache":               {"locator", "payload", "payload_digest", "payload_size_bytes", "torrent_version", "info_hash_v1", "info_hash_v2", "torrent_name", "total_size_bytes", "file_count", "projection_digest", "parser_version", "files", "fetched_at", "created_at", "updated_at"},
	"provider_group_snapshots":         {"id", "provider", "catalog_entry_ref", "observed_at", "source_version", "schema_version", "group_count", "version", "retention_until", "canonical_observation", "observation", "observation_digest", "entries"},
	"provider_group_refresh_health":    {"provider", "catalog_entry_ref", "state", "last_attempt_at", "last_success_at", "error_code", "error_message", "version"},
	"catalog_suppressions":             {"id", "provider", "provider_entry_ref", "title_snapshot", "suppressed_at", "version"},
	"provider_group_dictionary":        {"id", "provider", "provider_group_ref", "display_name", "available", "source", "added_at", "version"},
	"anime_config_candidates":          {"id", "origin", "origin_entry_ref", "title", "title_override", "instance_label", "draft", "version", "activated_managed_anime_id", "created_at", "updated_at", "draft_digest", "last_activity", "status", "retention_until", "current_revision", "current_revision_digest", "cleanup_eligible_at", "cleanup_reason"},
	"anime_config_candidate_revisions": {"candidate_id", "revision", "draft", "draft_digest", "effective", "section_versions", "section_schemas", "effective_digest", "parser_policy", "parser_policy_digest", "created_at", "business_snapshot", "business_digest"},
	"automation_schedules":             {"kind", "enabled", "interval_value", "interval_unit", "next_run_at", "last_dispatch_at", "version"},
	"automation_dispatch_runs":         {"id", "kind", "trigger", "state", "target_count", "enqueued_count", "merged_count", "skipped_count", "dispatch_error_count", "started_at", "finished_at", "retention_until", "request_idempotency_key"},
	"routine_activities":               {"id", "kind", "scope_kind", "scope_id", "managed_anime_id", "outcome", "last_executed_at", "last_result_code", "summary", "details", "updated_at"},
	"diagnostic_episodes":              {"id", "severity", "component", "operation", "error_code", "scope_kind", "scope_id", "managed_anime_id", "first_failed_at", "last_failed_at", "occurrence_count", "last_error", "details", "succeeded_after_at", "retention_until"},
	"episode_title_cache":              {"source", "work_id", "season", "episode", "state", "title", "fetched_at", "version"},
	"release_torrent_inputs":           {"id", "release_id", "payload", "payload_digest", "source_url_digest", "filename", "parser_version", "torrent_version", "info_hash_v1", "info_hash_v2", "payload_size_bytes"},
	"file_operations":                  {"id", "artifact_id", "artifact_version", "artifact_file_id", "operation", "source_path", "target_path", "source_fingerprint", "target_fingerprint", "collision_policy", "frozen_render_snapshot", "frozen_config_snapshot", "overwrite_execution_snapshot", "expected_size_bytes", "observed_target_size_bytes", "intent", "publication_checkpoint", "state", "observed_at", "failure_code", "failure_reason", "idempotency_key", "version"},
	"acquisition_targets":              {"id", "release_id", "target_ref", "source_path", "media_kind", "target_path", "expected_size_bytes", "intent", "collision_policy", "render_snapshot", "config_snapshot", "spec_digest", "version", "observation_state", "observed_size_bytes", "observation_reason_code", "observed_at", "observation_version", "created_at", "updated_at"},
	"work_items":                       {"owner_type", "owner_id", "acquisition_id", "managed_anime_id", "owner_lifecycle_epoch", "external_effect_admitted_at", "recovery_blocked", "recovery_reason"},
	"work_item_outcomes":               {"owner_type", "owner_id", "kind", "state", "acquisition_id", "managed_anime_id", "terminal_at", "retention_until"},
	"one_shot_acquisitions":            {"id", "managed_anime_id", "subscription_id", "rss_source_id", "release_id", "download_task_id", "correlation_id", "request_idempotency_key", "request_hash", "result_snapshot", "input_snapshot", "state", "terminal_outcome", "terminal_reason_code", "terminal_reason", "terminal_at", "retention_until", "admission_epoch", "active_effect_count", "active_effect_leases", "version", "created_at", "updated_at"},
	"subscription_log_entries":         {"id", "subscription_id", "managed_anime_id", "kind", "outcome", "occurred_at", "title", "summary", "rss_source_id", "release_id", "group_ref", "details", "dedupe_key", "retention_until", "reason_code", "subject_type", "subject_id", "subject_version", "correlation_id", "causation_type", "causation_id"},
	"plugin_target_configs":            {"id", "name", "plugin_kind", "adapter_kind", "enabled", "trigger_kinds", "config_schema_version", "config", "config_status", "template_set", "secret_ref", "signing_secret_set", "version", "created_at", "updated_at", "deleted_at"},
	"idempotency_records":              {"kind", "idempotency_key", "request_hash", "managed_anime_id", "release_id", "plugin_target_config_id", "owner_type", "owner_id", "failed_work_item_id", "result_work_item_id", "expected_version", "trigger_kind", "canonical_request_body", "delivery_id", "result_snapshot", "created_at"},
	"plugin_deliveries":                {"id", "plugin_target_config_id", "plugin_target_config_version", "adapter_kind", "adapter_config_snapshot", "config_status", "config_schema_version", "secret_ref", "delivery_kind", "trigger_kind", "source_owner_type", "source_owner_id", "source_owner_version", "source_result_key", "test_request_idempotency_key", "test_request_hash", "managed_anime_id", "state", "version", "idempotency_key", "payload_schema_version", "payload", "payload_digest", "template_language_version", "template_catalog_version", "title_template_snapshot", "body_template_snapshot", "rendered_title", "rendered_body", "rendered_message_digest", "target_display", "attempt_count", "last_error_code", "last_error", "delivered_at", "next_attempt_at", "created_at", "updated_at", "retention_until"},
	"plugin_delivery_attempts":         {"id", "plugin_delivery_id", "attempt_number", "started_at", "request_started_at", "response_received_at", "duration_ms", "target_display", "payload_digest", "request_summary", "response_status", "response_preview", "provider_ack", "error_code", "error", "retention_until"},
	"notification_admissions":          {"id", "trigger_kind", "source_owner_type", "source_owner_id", "source_owner_version", "result_key", "occurred_at", "event_catalog_version", "event_template_catalog_version", "payload", "state", "failure_code", "failure_reason", "created_at"},
	"deployment_recovery_runs":         {"id", "window_id", "old_bundle_identity", "new_bundle_identity", "backup_identity", "schema_identity", "build_identity", "registry_inventory", "checkpoint", "state", "version", "started_at", "updated_at"},
	"reconciliation_scans":             {"managed_anime_id", "state", "generation", "scan_scope", "progress", "capacity_stats", "input_fingerprint", "failure_code", "failure_reason", "version", "started_at", "completed_at", "updated_at", "alignment_checkpoint_generation", "alignment_checkpoint_state", "alignment_checkpoint_reason_code", "alignment_checkpoint_reason", "alignment_checkpoint_version", "alignment_checkpoint_updated_at", "replacement_checkpoint_generation", "replacement_checkpoint_state", "replacement_checkpoint_reason_code", "replacement_checkpoint_reason", "replacement_checkpoint_version", "replacement_checkpoint_updated_at"},
	"reconciliation_files":             {"managed_anime_id", "generation", "file_path", "derived_missing", "batch_index", "fingerprint", "size_bytes", "facts", "observed_at", "disposition", "coverage", "resolution", "revision", "source", "language", "group", "parser_version", "facts_fingerprint", "version", "created_at", "updated_at"},
	"release_candidate_facts":          {"release_id", "release_version", "parser_version", "input_fingerprint", "raw_evidence", "facts", "coverage_keys", "resolution", "revision", "source", "language", "group", "size_bytes", "created_at", "compare_values"},
	"attr_rule_versions":               {"id", "fingerprint", "content", "active", "created_at"},
	"naming_vars_versions":             {"id", "fingerprint", "content", "active", "created_at"},
	"filemaintenance_policies":         {"scope_key", "enabled", "scan_interval_seconds", "alignment_enabled", "safety", "version", "updated_at"},
	"alignment_operations":             {"id", "idempotency_key", "managed_anime_id", "scan_generation", "observation_ref", "candidate_ref", "policy_version", "metadata_version", "rules_version", "naming_version", "source_path", "source_fingerprint", "target_path", "target_contract", "dry_run_result", "state", "attempt", "failure_code", "reason", "version", "created_at", "completed_at", "updated_at", "overwrite_recovery"},
	"placements":                       {"id", "physical_path", "managed_anime_id", "source_evidence_type", "source_owner_ref", "source_fingerprint", "target_fingerprint", "quality_facts_ref", "facts_version", "snapshot_generation", "verification_state", "state", "displaced_cause", "displaced_by_placement_id", "displaced_by_anime_id", "redownload_requested_at", "version", "created_at", "updated_at"},
	"placement_coverage":               {"placement_id", "observation_key", "coverage_ordinal", "facts_version", "evidence_ref"},
	"placement_obligations":            {"file_operation_id", "state", "attempts", "max_attempts", "reason_code", "reason", "created_at", "updated_at", "settled_at"},
	"replacement_decisions":            {"id", "idempotency_key", "managed_anime_id", "candidate_release_id", "candidate_placement_id", "policy_version", "profile_version", "subscription_version", "input_digest", "candidate_facts_ref", "snapshot_generation", "coverage_keys", "coverage_key", "admission_mode", "pools", "verdict", "state", "stale_reason", "version", "created_at", "updated_at"},
	"cleanup_operations":               {"id", "idempotency_key", "placement_id", "managed_anime_id", "coverage_keys", "replacement_ref", "policy_version", "retention_mode", "source_path", "source_fingerprint", "quarantine_path", "effect", "grace_deadline", "state", "attempt", "failure_code", "reason", "version", "created_at", "updated_at"},
	"offset_migration_generations":     {"id", "managed_anime_id", "scope_kind", "scope_owner_id", "policy_version", "old_offset", "new_offset", "state", "planned_count", "blocked_count", "applied_count", "superseded_count", "reason_code", "reason", "version", "created_at", "updated_at", "completed_at"},
	"offset_migration_plans":           {"id", "generation_id", "managed_anime_id", "scope_kind", "scope_owner_id", "placement_id", "placement_version", "policy_version", "source_path", "source_fingerprint", "fingerprint", "media_fact_snapshot_id", "media_fact_file_ref", "rss_source_id", "source_episode_offset", "media_kind", "bound_season", "old_identity", "new_identity", "old_coverage_key", "new_coverage_key", "target_contract", "attempt", "state", "reason_code", "version", "created_at", "updated_at", "completed_at"},
	"anime_business_configs":           {"managed_anime_id", "rules_version", "subgroups_version", "parser_kind", "parser_source_default_version", "parser_version", "admission_mode", "quality_profile", "cleanup_policy", "retention_source_default_version", "retention_version", "unclassified_route", "file_effect_source_default_version", "file_effect_version", "candidate_id", "revision", "business_snapshot", "business_digest", "business_version", "updated_at"},
	"embedded_update_locks":            {"op_id", "admitted_by", "sequence", "manifest_digest", "state", "created_at", "released_at", "recovered_at", "recovery_terminal_state", "recovery_evidence"},
	"provider_catalog_preferences":     {"id", "provider", "season", "refresh_enabled", "schedule", "network_route_id", "version", "updated_at"},
	"media_fact_snapshots":             {"id", "release_id", "schema_version", "resolver_version", "parser_kind", "parser_config_fingerprint", "parser_config", "input_digest", "release_identity", "release_facts", "created_at"},
	"media_poster_cache":               {"bgm_id", "image_bytes", "content_type", "image_url", "fetched_at", "expires_at", "last_used"},
	"provider_image_cache":             {"image_key", "provider", "source_url", "image_bytes", "content_type", "fetched_at", "expires_at", "last_used"},
	"metadata_work_titles":             {"provider", "media_kind", "work_id", "title", "title_cn", "original_title", "overview", "year", "observed_at"},
	"provider_group_scope_generations": {"subscription_id", "provider", "generation"},
	"postprocessing_plans":             {"id", "artifact_id", "artifact_version", "acquisition_id"},
	"artifacts":                        {"acquisition_id", "inspection_requirements_status", "activity_logged_at"},
	"download_tasks":                   {"activity_submit_logged_at", "activity_terminal_logged_at", "submit_permit_id", "submit_dispatch_state", "submit_effect_class", "submit_retry_safe", "submit_attempt"},
	"artifact_files":                   {"acquisition_id"},
	"acquisitions":                     {"id", "execution_branch", "entry_kind", "access_scope", "request_namespace", "request_key", "request_hash", "admission_digest", "accepted_at", "retention_anchor_at", "receipt_until", "detail_until", "state", "record_state", "preparation_mode", "version", "facts_revision", "checked_revision", "settlement_error_count", "display_title", "compacted_at", "compact_trigger", "completion_open", "compact_pinned", "activity_logged_at", "input_canonical_utf8", "input", "payload_digest", "preparation_witness_canonical_utf8", "preparation_witness", "manifest_digest", "manifest_item_count", "manifest_sealed_at", "requirements_manifest_digest", "requirements_selection_digest", "requirements_evidence_digest", "requirements_digest", "requirements_item_count", "requirements_required_count", "requirements_sealed_at", "target_requirements_digest", "target_digest", "target_sealed_at", "vars_evaluation_canonical_utf8", "vars_evaluation"},
	"acquisition_files":                {"acquisition_id", "file_ref", "ordinal", "relative_path", "exact_size", "media_kind", "identity_evidence", "requirement", "requirement_reason_code", "selection_evidence", "spec_digest", "intent", "operation", "canonical_path", "collision", "target_source_cleanup", "render_snapshot", "plan_kind", "plan_target_file_ref", "plan_operation_ref", "plan_evidence", "plan_reason_code", "plan_diagnostic", "delivery_schema_version", "outcome", "completion_mode", "effect_class", "final_object", "delivery_source_cleanup", "delivery_reason_code", "evidence_digest", "operation_id", "receipt", "delivered_at", "activity_logged_at"},
}

func (r Runner) Verify(ctx context.Context) error {
	if r.DB == nil {
		return errors.New("migration database is nil")
	}
	migrations, err := r.loadMigrations()
	if err != nil {
		return err
	}
	return verifyInstalled(ctx, r.DB, migrations)
}

func (r Runner) Apply(ctx context.Context) error {
	if r.DB == nil {
		return errors.New("migration database is nil")
	}
	migrations, err := r.loadMigrations()
	if err != nil {
		return err
	}

	// A reset database starts from the immutable baseline. Later changes are
	// ordered, checksummed forward migrations applied under the same DB lock.
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration lock: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('bansoko:migrations')::bigint)`); err != nil {
		return fmt.Errorf("lock migration runner: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL search_path TO public`); err != nil {
		return fmt.Errorf("set migration search path: %w", err)
	}
	relations, err := publicRelations(ctx, tx)
	if err != nil {
		return fmt.Errorf("inspect schema before apply: %w", err)
	}
	applied := 0
	if len(relations) == 0 {
		if _, err := tx.ExecContext(ctx, `
CREATE TABLE schema_migrations (
    version TEXT PRIMARY KEY,
    checksum TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
			return fmt.Errorf("create migration history: %w", err)
		}
	} else {
		if err := verifySchemaIdentity(ctx, tx); err != nil {
			return err
		}
		applied, err = verifyAppliedMigrations(ctx, tx, migrations)
		if err != nil {
			return err
		}
	}
	for _, item := range migrations[applied:] {
		if _, err := tx.ExecContext(ctx, string(item.contents)); err != nil {
			return fmt.Errorf("migration apply failed: %w", &MigrationApplyError{Operation: "apply", Version: item.version, Err: err})
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, checksum) VALUES ($1, $2)`,
			item.version, checksum(item.contents)); err != nil {
			return fmt.Errorf("migration record failed: %w", &MigrationApplyError{Operation: "record", Version: item.version, Err: err})
		}
	}
	if err := verifyInstalled(ctx, tx, migrations); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func (r Runner) loadMigrations() ([]migration, error) {
	readDir := r.ReadDir
	if readDir == nil {
		readDir = os.ReadDir
	}
	readFile := r.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	entries, err := readDir(r.Dir)
	if err != nil {
		return nil, fmt.Errorf("read migration directory: %w", err)
	}
	files := migrationFiles(entries)
	if len(files) == 0 || files[0] != baselineMigrationFile {
		return nil, fmt.Errorf("%w: first migration must be %s, found %v", ErrInvalidMigrationSet, baselineMigrationFile, files)
	}
	migrations := make([]migration, 0, len(files))
	for _, file := range files {
		contents, err := readFile(filepath.Join(r.Dir, file))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", file, err)
		}
		if len(contents) == 0 {
			return nil, fmt.Errorf("%w: migration %s is empty", ErrInvalidMigrationSet, file)
		}
		migrations = append(migrations, migration{version: file, contents: contents})
	}
	return migrations, nil
}

func verifyInstalled(ctx context.Context, db queryer, migrations []migration) error {
	relations, err := publicRelations(ctx, db)
	if err != nil {
		return fmt.Errorf("inspect installed schema: %w", err)
	}
	if len(relations) == 0 {
		return errors.New("migration history is missing; run bansoko-migrate")
	}
	if err := verifySchemaIdentity(ctx, db); err != nil {
		return err
	}
	applied, err := verifyAppliedMigrations(ctx, db, migrations)
	if err != nil {
		return err
	}
	if applied != len(migrations) {
		return fmt.Errorf("%w: only %d of %d migrations are installed", ErrUnsupportedSchema, applied, len(migrations))
	}
	for name, kind := range knownRelations {
		actual, ok := relations[name]
		if !ok {
			return fmt.Errorf("%w: required relation is missing: %s", ErrUnsupportedSchema, name)
		}
		if actual != kind {
			return fmt.Errorf("%w: relation %s has unsupported kind %q", ErrUnsupportedSchema, name, actual)
		}
	}
	for name := range relations {
		if _, ok := knownRelations[name]; !ok {
			return fmt.Errorf("%w: unexpected relation: %s", ErrUnsupportedSchema, name)
		}
	}
	for table, columns := range requiredColumns {
		actual, err := tableColumns(ctx, db, table)
		if err != nil {
			return fmt.Errorf("inspect columns for %s: %w", table, err)
		}
		for _, column := range columns {
			if _, ok := actual[column]; !ok {
				return fmt.Errorf("%w: required column %s.%s is missing", ErrUnsupportedSchema, table, column)
			}
		}
	}
	return nil
}

func verifySchemaIdentity(ctx context.Context, db queryer) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('public.schema_identity') IS NOT NULL`).Scan(&exists); err != nil {
		return fmt.Errorf("inspect schema identity: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w: schema_identity is missing", ErrSchemaIdentity)
	}
	var identityCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM public.schema_identity`).Scan(&identityCount); err != nil {
		return fmt.Errorf("read schema identity rows: %w", err)
	}
	if identityCount != 1 {
		return fmt.Errorf("%w: expected exactly one schema identity row, got %d", ErrSchemaIdentity, identityCount)
	}
	var identity, baselineVersion string
	if err := db.QueryRowContext(ctx, `
SELECT identity, baseline_version
FROM public.schema_identity
WHERE singleton_id = 1`).Scan(&identity, &baselineVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: singleton identity row is missing", ErrSchemaIdentity)
		}
		return fmt.Errorf("read schema identity: %w", err)
	}
	if identity != schemaIdentityValue || baselineVersion != baselineMigrationFile {
		return fmt.Errorf("%w: identity=%q baseline=%q", ErrSchemaIdentity, identity, baselineVersion)
	}
	return nil
}

// verifyAppliedMigrations validates an ordered checksum-verified prefix and
// returns its length. Apply may extend that prefix; Verify requires all files.
func verifyAppliedMigrations(ctx context.Context, db queryer, migrations []migration) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, checksum FROM public.schema_migrations ORDER BY version`)
	if err != nil {
		return 0, fmt.Errorf("read migration history: %w", err)
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		if index >= len(migrations) {
			return 0, fmt.Errorf("%w: unexpected applied migration", ErrUnsupportedSchema)
		}
		var version, appliedChecksum string
		if err := rows.Scan(&version, &appliedChecksum); err != nil {
			return 0, fmt.Errorf("scan migration history: %w", err)
		}
		expected := migrations[index]
		if version != expected.version {
			return 0, fmt.Errorf("%w: expected migration %s at position %d, got %s", ErrUnsupportedSchema, expected.version, index+1, version)
		}
		if appliedChecksum != checksum(expected.contents) {
			return 0, fmt.Errorf("%w: %s", ErrChecksumMismatch, version)
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate migration history: %w", err)
	}
	if index == 0 {
		return 0, fmt.Errorf("%w: migration history is empty", ErrUnsupportedSchema)
	}
	return index, nil
}

func tableColumns(ctx context.Context, db queryer, table string) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `
SELECT a.attname
FROM pg_catalog.pg_attribute a
JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relname = $1
  AND a.attnum > 0 AND NOT a.attisdropped`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

// publicRelations lists the tables, views and sequences of the public schema
// (indexes and composite types belong to their tables).
func publicRelations(ctx context.Context, db queryer) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT c.relname, c.relkind
FROM pg_class AS c
JOIN pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	relations := make(map[string]string)
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, err
		}
		relations[name] = kind
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return relations, nil
}

func migrationFiles(entries []os.DirEntry) []string {
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	return files
}

func checksum(contents []byte) string {
	digest := sha256.Sum256(contents)
	return fmt.Sprintf("sha256:%x", digest[:])
}
