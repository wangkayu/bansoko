// Package application implements the plugin target owner. A plugin
// target is a small CAS-owned notification destination (telegram, feishu, or
// webhook) with a complete per-target template set and an opaque immutable
// secret-version reference. Read models expose has_secret only; the secret
// value and secret_ref never cross the read boundary. Test deliveries create a
// real PluginDelivery plus its typed WorkItem and never invoke an adapter.
package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"bansoko/internal/apperrors"
	"bansoko/internal/persistence/txscope"
	adapterconfig "bansoko/internal/plugintarget/config"
	deliverypayload "bansoko/internal/plugintarget/payload"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
	"bansoko/internal/workitems"
)

// TransactionalSecretStore persists immutable secret versions inside the
// caller's transaction so the target row and its secret reference commit
// atomically. It is implemented by the shared DatabaseSecretStore.
type TransactionalSecretStore interface {
	PutTx(ctx context.Context, tx txscope.Tx, purpose, value string) (string, error)
}

// SecretResolver resolves a stored secret value from its opaque reference. It
// is used only to materialize the write-only secret bundle for a test
// delivery's frozen secret snapshot; ordinary reads never expose it.
type SecretResolver interface {
	Resolve(ref string) (string, bool)
}

// SecretStore is the full secret boundary required by the plugin target owner:
// transactional writes plus value resolution for test deliveries.
type SecretStore interface {
	TransactionalSecretStore
	SecretResolver
}

const (
	pluginKindNotification  = "notification"
	templateLanguageVersion = notification.LanguageVersion
	// templateCatalogVersion is the catalog a template set is written against.
	templateCatalogVersion     = notification.CatalogVersion
	minConfigSchemaVersion     = 1
	testDeliveryRetentionDays  = 30
	defaultDeliveryMaxAttempts = 5
	minIdempotencyKeyLength    = 8
	maxIdempotencyKeyLength    = 128
	maxSecretValueLength       = 8192
	minSigningSecretLength     = 16
	maxSigningSecretLength     = 512
	maxTitleTemplateLength     = 512
	maxBodyTemplateLength      = 8192
	maxRenderedMessageLength   = 8192
)

// allowedTriggerKinds is the closed allowlist of notification triggers: the
// port's trigger registry.
var allowedTriggerKinds = func() map[TriggerKind]bool {
	out := make(map[TriggerKind]bool, len(plugintargetport.Triggers))
	for _, trigger := range plugintargetport.Triggers {
		out[TriggerKind(trigger)] = true
	}
	return out
}()

// Service owns the plugin target configuration rows and their test deliveries.
type Service struct {
	db        *sql.DB
	secrets   SecretStore
	workItems *workitems.Store
}

// NewService returns a plugin target owner bound to the shared pool, secret
// store, and work item store.
func NewService(db *sql.DB, secrets SecretStore, workItems *workitems.Store) (*Service, error) {
	if db == nil {
		return nil, errors.New("plugin target service database is nil")
	}
	if workItems == nil {
		return nil, errors.New("plugin target work item store is nil")
	}
	return &Service{db: db, secrets: secrets, workItems: workItems}, nil
}

// ListTargets returns every configured target as a non-secret read model.
func (s *Service) ListTargets(ctx context.Context) (TargetPage, error) {
	if s == nil || s.db == nil {
		return TargetPage{}, errors.New("plugin target service database is nil")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, plugin_kind, adapter_kind, enabled, trigger_kinds,
       config_schema_version, config, template_set, version, updated_at,
       (secret_ref IS NOT NULL) AS has_secret, signing_secret_set
FROM plugin_target_configs
WHERE deleted_at IS NULL
ORDER BY name, id`)
	if err != nil {
		return TargetPage{}, fmt.Errorf("list plugin targets: %w", err)
	}
	defer rows.Close()
	targets := make([]TargetConfig, 0)
	for rows.Next() {
		target, err := scanTarget(rows)
		if err != nil {
			return TargetPage{}, err
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return TargetPage{}, fmt.Errorf("iterate plugin targets: %w", err)
	}
	return TargetPage{Targets: targets}, nil
}

// CreateTarget atomically validates the input, persists any secret, and inserts
// the target row, returning the non-secret read model.
func (s *Service) CreateTarget(ctx context.Context, input TargetInput, idempotencyKey string) (TargetConfig, error) {
	if s == nil || s.db == nil {
		return TargetConfig{}, errors.New("plugin target service database is nil")
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return TargetConfig{}, err
	}
	if err := validateTargetCommon(input.PluginKind, input.AdapterKind, input.ConfigSchemaVersion, input.Config, input.TemplateSet, input.TriggerKinds, input.Name); err != nil {
		return TargetConfig{}, err
	}
	canonicalConfig, err := canonicalTargetConfig(input.AdapterKind, input.ConfigSchemaVersion, input.Config)
	if err != nil {
		return TargetConfig{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TargetConfig{}, fmt.Errorf("begin create plugin target: %w", err)
	}
	defer tx.Rollback()

	secretRef, signingSet, err := s.createSecretTx(ctx, tx, input.AdapterKind, input.Secret)
	if err != nil {
		return TargetConfig{}, err
	}
	if input.Enabled && secretRef == "" {
		return TargetConfig{}, &apperrors.Validation{
			Code:    "PLUGIN_TARGET_SECRET_REQUIRED",
			Details: []apperrors.FieldViolation{{Code: "PLUGIN_TARGET_SECRET_REQUIRED", Message: "An enabled target requires a configured secret."}},
		}
	}

	id := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO plugin_target_configs(id, name, plugin_kind, adapter_kind, enabled, trigger_kinds,
    config_schema_version, config, template_set, secret_ref, signing_secret_set)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), $11)`,
		id, input.Name, string(input.PluginKind), string(input.AdapterKind), input.Enabled,
		mustMarshal(input.TriggerKinds), input.ConfigSchemaVersion, canonicalConfig,
		mustMarshal(input.TemplateSet), secretRef, signingSet); err != nil {
		return TargetConfig{}, fmt.Errorf("create plugin target: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TargetConfig{}, fmt.Errorf("commit create plugin target: %w", err)
	}
	return s.getTarget(ctx, id)
}

// UpdateTarget applies a CAS update to an existing target. The expected version
// must match; secret action keep/replace/remove controls the secret reference.
func (s *Service) UpdateTarget(ctx context.Context, targetID string, expectedVersion int64, input TargetUpdateInput) (TargetConfig, error) {
	if s == nil || s.db == nil {
		return TargetConfig{}, errors.New("plugin target service database is nil")
	}
	if strings.TrimSpace(targetID) == "" {
		return TargetConfig{}, &apperrors.BadRequest{Code: "PLUGIN_TARGET_ID_REQUIRED", Message: "Plugin target ID is required."}
	}
	if err := validateTargetCommon(input.PluginKind, input.AdapterKind, input.ConfigSchemaVersion, input.Config, input.TemplateSet, input.TriggerKinds, input.Name); err != nil {
		return TargetConfig{}, err
	}
	canonicalConfig, err := canonicalTargetConfig(input.AdapterKind, input.ConfigSchemaVersion, input.Config)
	if err != nil {
		return TargetConfig{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TargetConfig{}, fmt.Errorf("begin update plugin target: %w", err)
	}
	defer tx.Rollback()

	var currentVersion int64
	var existingSecretRef sql.NullString
	var existingAdapter string
	var signingSet bool
	err = tx.QueryRowContext(ctx, `
SELECT version, secret_ref, adapter_kind, signing_secret_set FROM plugin_target_configs WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, targetID).
		Scan(&currentVersion, &existingSecretRef, &existingAdapter, &signingSet)
	if errors.Is(err, sql.ErrNoRows) {
		return TargetConfig{}, &apperrors.NotFound{Resource: "plugin target", ID: targetID}
	}
	if err != nil {
		return TargetConfig{}, fmt.Errorf("read plugin target version: %w", err)
	}
	if currentVersion != expectedVersion {
		return TargetConfig{}, &apperrors.VersionConflict{Owner: "plugin target", CurrentVersion: currentVersion}
	}

	secretRef := existingSecretRef.String
	switch input.Secret.Mode {
	case "keep":
		// Retain the existing secret reference unchanged. A kept bundle keeps
		// its signing secret only while the target stays a webhook.
		signingSet = signingSet && existingAdapter == string(input.AdapterKind)
	case "replace":
		value, replaceErr := s.extractCreateSecretValue(input.AdapterKind, input.Secret)
		if replaceErr != nil {
			return TargetConfig{}, replaceErr
		}
		ref, putErr := s.storeSecretTx(ctx, tx, input.AdapterKind, targetID, value)
		if putErr != nil {
			return TargetConfig{}, putErr
		}
		secretRef = ref
		signingSet = secretHasSigningSecret(input.AdapterKind, value)
	case "remove":
		secretRef = ""
		signingSet = false
	default:
		return TargetConfig{}, &apperrors.Validation{
			Code:    "PLUGIN_TARGET_CONFIG_INVALID",
			Details: []apperrors.FieldViolation{{Field: fieldPtr("secret"), Code: "PLUGIN_TARGET_CONFIG_INVALID", Message: "Secret action must be keep, replace, or remove."}},
		}
	}

	if input.Enabled && secretRef == "" {
		return TargetConfig{}, &apperrors.Validation{
			Code:    "PLUGIN_TARGET_SECRET_REQUIRED",
			Details: []apperrors.FieldViolation{{Code: "PLUGIN_TARGET_SECRET_REQUIRED", Message: "An enabled target requires a configured secret."}},
		}
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE plugin_target_configs
SET name = $2, plugin_kind = $3, adapter_kind = $4, enabled = $5, trigger_kinds = $6,
    config_schema_version = $7, config = $8, template_set = $9, secret_ref = NULLIF($10, ''),
    signing_secret_set = $12, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $11`,
		targetID, input.Name, string(input.PluginKind), string(input.AdapterKind), input.Enabled,
		mustMarshal(input.TriggerKinds), input.ConfigSchemaVersion, canonicalConfig,
		mustMarshal(input.TemplateSet), secretRef, expectedVersion, signingSet); err != nil {
		return TargetConfig{}, fmt.Errorf("update plugin target: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TargetConfig{}, fmt.Errorf("commit update plugin target: %w", err)
	}
	return s.getTarget(ctx, targetID)
}

// DeleteTarget soft-deletes a notification target: delivery evidence stays
// intact and the FK chain is never broken. Idempotent on repeat calls.
func (s *Service) DeleteTarget(ctx context.Context, targetID string) error {
	if s == nil || s.db == nil {
		return errors.New("plugin target service database is nil")
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE plugin_target_configs SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND deleted_at IS NULL`, targetID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plugin_target_configs WHERE id = $1)`, targetID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errors.New("plugin target not found")
		}
	}
	return nil
}

// TestTarget creates a real test PluginDelivery and its typed WorkItem for the
// selected business trigger. It validates target CAS and idempotency, freezes
// the target config/version and template, and never invokes an adapter.
func (s *Service) TestTarget(ctx context.Context, targetID string, input TargetTestInput, idempotencyKey string) (TestAccepted, error) {
	if s == nil || s.db == nil {
		return TestAccepted{}, errors.New("plugin target service database is nil")
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return TestAccepted{}, err
	}
	if !allowedTriggerKinds[input.TriggerKind] {
		return TestAccepted{}, &apperrors.Validation{
			Code:    "PLUGIN_TARGET_CONFIG_INVALID",
			Details: []apperrors.FieldViolation{{Field: fieldPtr("trigger_kind"), Code: "PLUGIN_TARGET_CONFIG_INVALID", Message: "Trigger kind must be an allowlisted business trigger."}},
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TestAccepted{}, fmt.Errorf("begin test plugin target: %w", err)
	}
	defer tx.Rollback()

	target, err := s.lockTargetTx(ctx, tx, targetID)
	if err != nil {
		return TestAccepted{}, err
	}
	if target.Version != input.ExpectedVersion {
		return TestAccepted{}, &apperrors.VersionConflict{Owner: "plugin target", CurrentVersion: target.Version}
	}
	if target.Enabled && target.secretRef == "" {
		return TestAccepted{}, &apperrors.Validation{
			Code:    "PLUGIN_TARGET_SECRET_REQUIRED",
			Details: []apperrors.FieldViolation{{Code: "PLUGIN_TARGET_SECRET_REQUIRED", Message: "An enabled target requires a configured secret before testing."}},
		}
	}

	titleTemplate, bodyTemplate, err := selectTemplate(target.TemplateSet, input.TriggerKind)
	if err != nil {
		return TestAccepted{}, err
	}

	// The request identity (expected_version + trigger) is the idempotency
	// record's business; the delivery row persists the frozen delivery payload
	// itself. A test delivery has no owner facts, so its fact snapshot is empty
	// rather than borrowed from the request.
	canonical := canonicalTestBody(input.TriggerKind, input.ExpectedVersion)
	requestHash := hashTestRequest(canonical)
	frozenPayload, err := deliverypayload.Encode(string(input.TriggerKind), nil)
	if err != nil {
		return TestAccepted{}, fmt.Errorf("encode test delivery payload: %w", err)
	}

	// A repeated request with the same key replays the original delivery; a key
	// reused with a different body/version/trigger is a typed conflict.
	var existingHash, existingDeliveryID string
	err = tx.QueryRowContext(ctx, `
SELECT request_hash, delivery_id FROM idempotency_records
WHERE kind = 'plugin_test' AND plugin_target_config_id = $1 AND idempotency_key = $2
FOR UPDATE`, targetID, idempotencyKey).Scan(&existingHash, &existingDeliveryID)
	if err == nil {
		if existingHash != requestHash {
			return TestAccepted{}, &apperrors.Business{Status: http.StatusConflict, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different test request."}
		}
		if err := tx.Commit(); err != nil {
			return TestAccepted{}, fmt.Errorf("commit test replay: %w", err)
		}
		return TestAccepted{Accepted: true, DeliveryID: existingDeliveryID}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TestAccepted{}, fmt.Errorf("read plugin test idempotency: %w", err)
	}

	// The delivery freezes the target's canonical adapter config; a target whose
	// stored config is not provably canonical cannot be tested, so an unprovable
	// document never reaches a snapshot.
	if target.ConfigStatus != adapterconfig.StatusAvailable {
		return TestAccepted{}, &apperrors.Validation{
			Code:    "PLUGIN_TARGET_CONFIG_INVALID",
			Details: []apperrors.FieldViolation{{Field: fieldPtr("config"), Code: "PLUGIN_TARGET_CONFIG_INVALID", Message: "The target adapter config is not a canonical allowlisted document; re-save the target before testing."}},
		}
	}
	secretRef := ""
	if target.secretRef != "" {
		secretRef = target.secretRef
	}

	deliveryID := uuid.NewString()
	effectKey := "plugin_deliver:" + deliveryID
	targetDisplay := buildTargetDisplay(target)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO plugin_deliveries(
    id, plugin_target_config_id, plugin_target_config_version, adapter_kind,
    adapter_config_snapshot, secret_ref, delivery_kind, trigger_kind,
    source_owner_type, source_owner_id, source_owner_version, source_result_key,
    test_request_idempotency_key, test_request_hash, state, idempotency_key,
    payload_schema_version, payload, payload_digest, template_language_version,
    template_catalog_version, title_template_snapshot, body_template_snapshot, target_display)
VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), 'test', $7, 'plugin_target', $2, $3, $8,
    $9, $10, 'pending', $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
		deliveryID, targetID, target.Version, string(target.AdapterKind),
		target.Config, secretRef, string(input.TriggerKind), idempotencyKey,
		idempotencyKey, requestHash, effectKey, deliverypayload.SchemaVersion,
		frozenPayload, hashBytes(frozenPayload), templateLanguageVersion, templateCatalogVersion,
		titleTemplate, bodyTemplate, targetDisplay); err != nil {
		return TestAccepted{}, fmt.Errorf("create test plugin delivery: %w", err)
	}

	if _, err := s.workItems.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerPluginDelivery, ID: deliveryID},
		Kind:                 workitems.KindPluginDeliver,
		IdempotencyKey:       effectKey,
		MaxAttempts:          defaultDeliveryMaxAttempts,
		ExpectedOwnerVersion: 1,
	}); err != nil {
		return TestAccepted{}, fmt.Errorf("schedule plugin delivery: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO idempotency_records(kind, idempotency_key, request_hash, plugin_target_config_id,
    expected_version, trigger_kind, canonical_request_body, delivery_id)
VALUES ('plugin_test', $1, $2, $3, $4, $5, $6, $7)`,
		idempotencyKey, requestHash, targetID, input.ExpectedVersion, string(input.TriggerKind), canonical, deliveryID); err != nil {
		return TestAccepted{}, fmt.Errorf("record plugin test idempotency: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return TestAccepted{}, fmt.Errorf("commit test plugin target: %w", err)
	}
	return TestAccepted{Accepted: true, DeliveryID: deliveryID}, nil
}

// ListDeliveries returns the bounded read-only delivery list (no secret
// material), filtered by optional criteria, ordered by updated_at descending.
func (s *Service) ListDeliveries(ctx context.Context, params DeliveryQuery) (DeliveryPage, error) {
	if s == nil || s.db == nil {
		return DeliveryPage{}, errors.New("plugin target service database is nil")
	}
	limit := int64(20)
	if params.Limit != nil && *params.Limit > 0 && *params.Limit <= 100 {
		limit = int64(*params.Limit)
	}
	offset := int64(0)
	if params.Offset != nil && *params.Offset > 0 {
		offset = int64(*params.Offset)
	}
	where := []string{"TRUE"}
	args := []any{}
	arg := func(value any, column string) {
		args = append(args, value)
		where = append(where, fmt.Sprintf("d.%s = $%d", column, len(args)))
	}
	if params.TriggerKind != nil && *params.TriggerKind != "" {
		arg(string(*params.TriggerKind), "trigger_kind")
	}
	if params.SourceOwnerType != nil && *params.SourceOwnerType != "" {
		arg(string(*params.SourceOwnerType), "source_owner_type")
	}
	if params.SourceOwnerID != nil && *params.SourceOwnerID != "" {
		arg(string(*params.SourceOwnerID), "source_owner_id")
	}
	if params.State != nil && *params.State != "" {
		arg(string(*params.State), "state")
	}
	filter := strings.Join(where, " AND ")

	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_deliveries d WHERE `+filter, args...).Scan(&total); err != nil {
		return DeliveryPage{}, fmt.Errorf("count plugin deliveries: %w", err)
	}
	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.QueryContext(ctx, `
SELECT to_jsonb(d) - 'secret_ref'
FROM (
  SELECT d.* FROM plugin_deliveries d
  WHERE `+filter+`
  ORDER BY d.updated_at DESC, d.id DESC
  LIMIT $`+fmt.Sprintf("%d", len(args)+1)+` OFFSET $`+fmt.Sprintf("%d", len(args)+2)+`
) d
ORDER BY d.updated_at DESC, d.id DESC`, queryArgs...)
	if err != nil {
		return DeliveryPage{}, fmt.Errorf("list plugin deliveries: %w", err)
	}
	defer rows.Close()
	items := make([]Delivery, 0, limit)
	for rows.Next() {
		var row []byte
		if err := rows.Scan(&row); err != nil {
			return DeliveryPage{}, fmt.Errorf("scan plugin delivery row: %w", err)
		}
		// One projection for every read surface: payload becomes the frozen
		// canonical payload (or null) plus its status, so a legacy row is
		// reported as unavailable instead of being re-serialised.
		projected, err := projectDeliveryRow(row)
		if err != nil {
			return DeliveryPage{}, err
		}
		items = append(items, Delivery{Raw: append(json.RawMessage(nil), projected...)})
	}
	if err := rows.Err(); err != nil {
		return DeliveryPage{}, fmt.Errorf("iterate plugin deliveries: %w", err)
	}
	return DeliveryPage{Items: items, Total: int(total)}, nil
}

// GetDelivery returns a single delivery with its bounded attempts as a
// non-secret read model.
func (s *Service) GetDelivery(ctx context.Context, deliveryID string) (DeliveryDetail, error) {
	if s == nil || s.db == nil {
		return DeliveryDetail{}, errors.New("plugin target service database is nil")
	}
	if strings.TrimSpace(deliveryID) == "" {
		return DeliveryDetail{}, &apperrors.BadRequest{Code: "PLUGIN_DELIVERY_ID_REQUIRED", Message: "Plugin delivery ID is required."}
	}

	var deliveryRow []byte
	err := s.db.QueryRowContext(ctx, `
SELECT to_jsonb(d) - 'secret_ref'
FROM plugin_deliveries d
WHERE d.id = $1`, deliveryID).Scan(&deliveryRow)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryDetail{}, &apperrors.NotFound{Resource: "plugin delivery", ID: deliveryID}
	}
	if err != nil {
		return DeliveryDetail{}, fmt.Errorf("read plugin delivery: %w", err)
	}
	projectedRow, err := projectDeliveryRow(deliveryRow)
	if err != nil {
		return DeliveryDetail{}, err
	}
	delivery := Delivery{Raw: append(json.RawMessage(nil), projectedRow...)}

	attemptRows, err := s.db.QueryContext(ctx, `
SELECT to_jsonb(plugin_delivery_attempts.*)
FROM plugin_delivery_attempts
WHERE plugin_delivery_id = $1
ORDER BY attempt_number`, deliveryID)
	if err != nil {
		return DeliveryDetail{}, fmt.Errorf("read plugin delivery attempts: %w", err)
	}
	defer attemptRows.Close()
	attempts := make([]json.RawMessage, 0)
	for attemptRows.Next() {
		var row []byte
		if err := attemptRows.Scan(&row); err != nil {
			return DeliveryDetail{}, fmt.Errorf("scan plugin delivery attempt: %w", err)
		}
		attempts = append(attempts, append(json.RawMessage(nil), row...))
	}
	if err := attemptRows.Err(); err != nil {
		return DeliveryDetail{}, fmt.Errorf("iterate plugin delivery attempts: %w", err)
	}
	return DeliveryDetail{Delivery: delivery, Attempts: attempts}, nil
}

// --- helpers ---

// targetRecord is the internal projection of a target row including the opaque
// secret reference. It is never returned across the read boundary.
type targetRecord struct {
	TargetConfig
	secretRef    string
	configStatus string
}

// projectTargetConfig bounds one stored adapter config for a read: canonical
// bytes are served, anything else is withheld and reported through
// config_status, so a legacy row can never carry an undeclared or
// secret-bearing field into a response.
func projectTargetConfig(target *TargetConfig, raw []byte) {
	normalized, status := adapterconfig.Normalize(string(target.AdapterKind), target.ConfigSchemaVersion, raw)
	target.ConfigStatus = status
	if normalized == nil {
		target.Config = json.RawMessage("null")
		return
	}
	target.Config = normalized
}

func (s *Service) getTarget(ctx context.Context, id string) (TargetConfig, error) {
	var target TargetConfig
	var hasSecret, signingSet bool
	var triggerKinds, config, templateSet []byte
	err := s.db.QueryRowContext(ctx, `
SELECT id, name, plugin_kind, adapter_kind, enabled, trigger_kinds,
       config_schema_version, config, template_set, version, updated_at,
       (secret_ref IS NOT NULL) AS has_secret, signing_secret_set
FROM plugin_target_configs WHERE id = $1 AND deleted_at IS NULL`, id).
		Scan(&target.ID, &target.Name, &target.PluginKind, &target.AdapterKind, &target.Enabled,
			&triggerKinds, &target.ConfigSchemaVersion, &config, &templateSet, &target.Version,
			&target.UpdatedAt, &hasSecret, &signingSet)
	if errors.Is(err, sql.ErrNoRows) {
		return TargetConfig{}, &apperrors.NotFound{Resource: "plugin target", ID: id}
	}
	if err != nil {
		return TargetConfig{}, fmt.Errorf("read plugin target: %w", err)
	}
	target.HasSecret = &hasSecret
	projectSigningSecret(&target, signingSet)
	if err := json.Unmarshal(triggerKinds, &target.TriggerKinds); err != nil {
		return TargetConfig{}, fmt.Errorf("decode plugin target trigger kinds: %w", err)
	}
	projectTargetConfig(&target, config)
	target.TemplateSet = append(json.RawMessage(nil), templateSet...)
	return target, nil
}

// projectSigningSecret reports, for a webhook target only, whether its
// write-only secret bundle carries a signing secret. The value itself is never
// read back.
func projectSigningSecret(target *TargetConfig, signingSet bool) {
	if target.AdapterKind == AdapterWebhook {
		target.HasSigningSecret = &signingSet
	}
}

// lockTargetTx locks and loads the full target row inside a transaction.
func (s *Service) lockTargetTx(ctx context.Context, tx *sql.Tx, id string) (targetRecord, error) {
	var record targetRecord
	var triggerKinds, config, templateSet []byte
	var secretRef sql.NullString
	err := tx.QueryRowContext(ctx, `
SELECT id, name, plugin_kind, adapter_kind, enabled, trigger_kinds,
       config_schema_version, config, template_set, version, updated_at, secret_ref
FROM plugin_target_configs WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, id).
		Scan(&record.ID, &record.Name, &record.PluginKind, &record.AdapterKind, &record.Enabled,
			&triggerKinds, &record.ConfigSchemaVersion, &config, &templateSet, &record.Version,
			&record.UpdatedAt, &secretRef)
	if errors.Is(err, sql.ErrNoRows) {
		return targetRecord{}, &apperrors.NotFound{Resource: "plugin target", ID: id}
	}
	if err != nil {
		return targetRecord{}, fmt.Errorf("read plugin target: %w", err)
	}
	record.secretRef = secretRef.String
	if err := json.Unmarshal(triggerKinds, &record.TriggerKinds); err != nil {
		return targetRecord{}, fmt.Errorf("decode plugin target trigger kinds: %w", err)
	}
	// A delivery may only freeze a config that is provably canonical; a legacy
	// row keeps its bytes and its status so every writer fails closed instead of
	// executing, or copying, an unprovable document.
	record.Config, record.ConfigStatus = adapterconfig.Normalize(string(record.AdapterKind), record.ConfigSchemaVersion, config)
	if err := json.Unmarshal(templateSet, &record.TemplateSet); err != nil {
		return targetRecord{}, fmt.Errorf("decode plugin target template set: %w", err)
	}
	return record, nil
}

func scanTarget(rows *sql.Rows) (TargetConfig, error) {
	var target TargetConfig
	var hasSecret, signingSet bool
	var triggerKinds, config, templateSet []byte
	if err := rows.Scan(&target.ID, &target.Name, &target.PluginKind, &target.AdapterKind, &target.Enabled,
		&triggerKinds, &target.ConfigSchemaVersion, &config, &templateSet, &target.Version,
		&target.UpdatedAt, &hasSecret, &signingSet); err != nil {
		return TargetConfig{}, fmt.Errorf("scan plugin target: %w", err)
	}
	target.HasSecret = &hasSecret
	projectSigningSecret(&target, signingSet)
	if err := json.Unmarshal(triggerKinds, &target.TriggerKinds); err != nil {
		return TargetConfig{}, fmt.Errorf("decode plugin target trigger kinds: %w", err)
	}
	projectTargetConfig(&target, config)
	target.TemplateSet = append(json.RawMessage(nil), templateSet...)
	return target, nil
}

// createSecretTx stores a create request's secret bundle and reports whether
// it carries a webhook signing secret.
func (s *Service) createSecretTx(ctx context.Context, tx *sql.Tx, adapterKind AdapterKind, secret SecretInput) (string, bool, error) {
	if secret.Mode == "none" {
		return "", false, nil
	}
	if secret.Mode != "set" {
		return "", false, &apperrors.Validation{
			Code:    "PLUGIN_TARGET_CONFIG_INVALID",
			Details: []apperrors.FieldViolation{{Field: fieldPtr("secret"), Code: "PLUGIN_TARGET_CONFIG_INVALID", Message: "Secret mode must be none or set."}},
		}
	}
	value, err := s.extractSecretSetValue(adapterKind, secret)
	if err != nil {
		return "", false, err
	}
	ref, err := s.storeSecretTx(ctx, tx, adapterKind, "", value)
	return ref, secretHasSigningSecret(adapterKind, value), err
}

// secretHasSigningSecret reports whether a validated webhook secret bundle
// carries a non-empty signing secret.
func secretHasSigningSecret(adapterKind AdapterKind, value string) bool {
	if adapterKind != AdapterWebhook {
		return false
	}
	var bundle struct {
		SigningSecret *string `json:"signing_secret"`
	}
	return json.Unmarshal([]byte(value), &bundle) == nil && bundle.SigningSecret != nil && *bundle.SigningSecret != ""
}

// storeSecretTx persists a secret bundle value inside the transaction.
func (s *Service) storeSecretTx(ctx context.Context, tx *sql.Tx, adapterKind AdapterKind, targetID, value string) (string, error) {
	if s.secrets == nil {
		return "", &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "SECRETS_UNAVAILABLE", Message: "The secret store is not configured."}
	}
	purpose := "plugin_target:" + string(adapterKind)
	if targetID != "" {
		purpose += ":" + targetID
	}
	ref, err := s.secrets.PutTx(ctx, txscope.Wrap(tx), purpose, value)
	if err != nil {
		return "", fmt.Errorf("store plugin target secret: %w", err)
	}
	return ref, nil
}

func (s *Service) extractSecretSetValue(adapterKind AdapterKind, secret SecretInput) (string, error) {
	if secret.Mode != "set" || len(secret.Value) == 0 {
		return "", &apperrors.Validation{
			Code:    "PLUGIN_TARGET_CONFIG_INVALID",
			Details: []apperrors.FieldViolation{{Field: fieldPtr("secret"), Code: "PLUGIN_TARGET_CONFIG_INVALID", Message: "Secret value is required when mode is set."}},
		}
	}
	return serializeSecretValue(adapterKind, secret.Value)
}

func (s *Service) extractCreateSecretValue(adapterKind AdapterKind, secret SecretInput) (string, error) {
	if secret.Mode != "replace" || len(secret.Value) == 0 {
		return "", &apperrors.Validation{
			Code:    "PLUGIN_TARGET_CONFIG_INVALID",
			Details: []apperrors.FieldViolation{{Field: fieldPtr("secret"), Code: "PLUGIN_TARGET_CONFIG_INVALID", Message: "Secret value is required when mode is replace."}},
		}
	}
	return serializeSecretValue(adapterKind, secret.Value)
}

func serializeSecretValue(adapterKind AdapterKind, value json.RawMessage) (string, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(value, &envelope); err != nil {
		return "", &apperrors.Validation{
			Code:    "PLUGIN_TARGET_CONFIG_INVALID",
			Details: []apperrors.FieldViolation{{Field: fieldPtr("secret"), Code: "PLUGIN_TARGET_CONFIG_INVALID", Message: "Secret value could not be decoded."}},
		}
	}
	var encodedAdapter string
	if err := json.Unmarshal(envelope["adapter_kind"], &encodedAdapter); err != nil || encodedAdapter != string(adapterKind) {
		return "", &apperrors.Validation{
			Code:    "PLUGIN_TARGET_CONFIG_INVALID",
			Details: []apperrors.FieldViolation{{Field: fieldPtr("secret"), Code: "PLUGIN_TARGET_CONFIG_INVALID", Message: "Secret adapter kind must match the target adapter kind."}},
		}
	}
	if len(value) > maxSecretValueLength {
		return "", &apperrors.Validation{
			Code:    "PLUGIN_TARGET_CONFIG_INVALID",
			Details: []apperrors.FieldViolation{{Field: fieldPtr("secret"), Code: "PLUGIN_TARGET_CONFIG_INVALID", Message: "Secret value is too large."}},
		}
	}
	if raw, present := envelope["signing_secret"]; present {
		var signing *string
		if adapterKind != AdapterWebhook || json.Unmarshal(raw, &signing) != nil || (signing != nil && (len(*signing) < minSigningSecretLength || len(*signing) > maxSigningSecretLength)) {
			return "", &apperrors.Validation{
				Code:    "PLUGIN_TARGET_CONFIG_INVALID",
				Details: []apperrors.FieldViolation{{Field: fieldPtr("secret.signing_secret"), Code: "PLUGIN_TARGET_CONFIG_INVALID", Message: "The signing secret must be 16 to 512 characters and is only accepted for a webhook target."}},
			}
		}
	}
	return string(value), nil
}

// templateSetLocale is the language of a set's system default templates:
// en, or zh-CN when the set names none.
func templateSetLocale(templateSet json.RawMessage) string {
	var value struct {
		Locale string `json:"locale"`
	}
	if json.Unmarshal(templateSet, &value) == nil && value.Locale == notification.LocaleEN {
		return notification.LocaleEN
	}
	return notification.LocaleZH
}

// selectTemplate returns the title/body a trigger renders with. A set that
// has no entry for a trigger uses the system default: sets saved before a
// trigger existed keep working without a rewrite.
func selectTemplate(templateSet json.RawMessage, triggerKind TriggerKind) (title string, body string, err error) {
	if !allowedTriggerKinds[triggerKind] {
		return "", "", configInvalid("trigger_kind", "Unknown trigger kind.")
	}
	var value struct {
		Events map[string]json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(templateSet, &value); err != nil {
		return "", "", configInvalid("template_set", "template set could not be decoded")
	}
	event, present := value.Events[string(triggerKind)]
	if !present {
		event = json.RawMessage(`{"mode":"system_default"}`)
	}
	var envelope struct {
		Mode          string `json:"mode"`
		TitleTemplate string `json:"title_template"`
		BodyTemplate  string `json:"body_template"`
	}
	if err := json.Unmarshal(event, &envelope); err != nil {
		return "", "", configInvalid("template_set", "template event could not be decoded")
	}
	switch envelope.Mode {
	case "custom":
		return envelope.TitleTemplate, envelope.BodyTemplate, nil
	case "system_default":
		title, body := notification.DefaultsFor(string(triggerKind), templateSetLocale(templateSet))
		return title, body, nil
	default:
		return "", "", configInvalid("template_set", "Template mode must be system_default or custom.")
	}
}

func buildTargetDisplay(target targetRecord) string {
	adapter := string(target.AdapterKind)
	var config struct {
		ChatID   string `json:"chat_id"`
		AuthKind string `json:"auth_kind"`
	}
	if err := json.Unmarshal(target.Config, &config); err == nil {
		switch target.AdapterKind {
		case AdapterTelegram:
			return "telegram:" + config.ChatID
		case AdapterWebhook:
			return "webhook:" + config.AuthKind
		}
	}
	return adapter
}

// validateTargetCommon runs the shared create/update validation matrix.
func validateTargetCommon(pluginKind PluginKind, adapterKind AdapterKind, configSchemaVersion int, config json.RawMessage, templateSet json.RawMessage, triggerKinds []TriggerKind, name string) error {
	if _, err := canonicalTargetConfig(adapterKind, configSchemaVersion, config); err != nil {
		return err
	}
	if pluginKind != PluginKindNotification {
		return configInvalid("plugin_kind", "plugin_kind must be notification")
	}
	if strings.TrimSpace(name) == "" || len(name) > 256 {
		return configInvalid("name", "name is required and bounded")
	}
	if configSchemaVersion < minConfigSchemaVersion {
		return configInvalid("config_schema_version", "config_schema_version must be at least 1")
	}
	if err := validateTemplateSet(templateSet); err != nil {
		return err
	}
	return validateTriggerKinds(triggerKinds)
}

// canonicalTargetConfig validates one adapter config through the closed codec
// and returns the canonical document the caller persists. A request cannot smuggle
// a secret-bearing or undeclared field into storage, a delivery snapshot or a
// read response: unknown fields, a mismatched inner adapter_kind and
// out-of-domain values are rejected here.
func canonicalTargetConfig(adapterKind AdapterKind, configSchemaVersion int, config json.RawMessage) (json.RawMessage, error) {
	canonical, err := adapterconfig.Encode(string(adapterKind), configSchemaVersion, config)
	if err != nil {
		return nil, configInvalid("config", err.Error())
	}
	return canonical, nil
}

// requiredTemplateEvents are the triggers every saved set names. A trigger
// added later may be absent and then uses its system default.
var requiredTemplateEvents = []TriggerKind{TriggerDownloadSubmitted, TriggerFileOperationCompleted, TriggerFileOperationFailed}

func validateTemplateSet(templateSet json.RawMessage) error {
	var value struct {
		LanguageVersion string                     `json:"language_version"`
		CatalogVersion  string                     `json:"catalog_version"`
		Locale          *string                    `json:"locale"`
		Events          map[string]json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(templateSet, &value); err != nil {
		return configInvalid("template_set", "template set could not be decoded")
	}
	if value.LanguageVersion != templateLanguageVersion {
		return configInvalid("template_set.language_version", "template language_version must be "+templateLanguageVersion)
	}
	if !notification.SupportedCatalog(value.CatalogVersion) {
		return configInvalid("template_set.catalog_version", "template catalog_version must be "+notification.CatalogVersion)
	}
	if value.Locale != nil && *value.Locale != notification.LocaleZH && *value.Locale != notification.LocaleEN {
		return configInvalid("template_set.locale", "template locale must be "+notification.LocaleZH+" or "+notification.LocaleEN)
	}
	for _, trigger := range requiredTemplateEvents {
		if _, present := value.Events[string(trigger)]; !present {
			return configInvalid("template_set.events."+string(trigger), "template event could not be decoded")
		}
	}
	for _, trigger := range plugintargetport.Triggers {
		event, present := value.Events[trigger]
		if !present {
			continue
		}
		if err := validateEventTemplate(trigger, event); err != nil {
			return err
		}
	}
	for trigger := range value.Events {
		if !allowedTriggerKinds[TriggerKind(trigger)] {
			return configInvalid("template_set.events", "template event "+trigger+" is not a notification trigger")
		}
	}
	return nil
}

// validateEventTemplate checks one trigger's template entry. A custom
// template is compiled against the trigger's catalog with the same core that
// renders deliveries, so a saved template can only fail later on output that
// depends on real facts. Compile diagnostics are returned as a typed 422 with
// their source spans.
func validateEventTemplate(trigger string, event json.RawMessage) error {
	field := "template_set.events." + trigger
	var value struct {
		Mode          string `json:"mode"`
		TitleTemplate string `json:"title_template"`
		BodyTemplate  string `json:"body_template"`
	}
	if err := json.Unmarshal(event, &value); err != nil {
		return configInvalid(field, "template event could not be decoded")
	}
	switch value.Mode {
	case "system_default":
		return nil
	case "custom":
		if strings.TrimSpace(value.TitleTemplate) == "" || len(value.TitleTemplate) > maxTitleTemplateLength {
			return &apperrors.Validation{Code: "TEMPLATE_INVALID", Details: []apperrors.FieldViolation{{Field: fieldPtr(field), Code: "TEMPLATE_INVALID", Message: "Custom title template is empty or too large."}}}
		}
		if strings.TrimSpace(value.BodyTemplate) == "" || len(value.BodyTemplate) > maxBodyTemplateLength {
			return &apperrors.Validation{Code: "TEMPLATE_INVALID", Details: []apperrors.FieldViolation{{Field: fieldPtr(field), Code: "TEMPLATE_INVALID", Message: "Custom body template is empty or too large."}}}
		}
		diagnostics, _ := notification.Compile(trigger, value.TitleTemplate, value.BodyTemplate)
		if len(diagnostics) == 0 {
			return nil
		}
		details := make([]apperrors.FieldViolation, 0, len(diagnostics))
		for _, diagnostic := range diagnostics {
			start, end := diagnostic.Start, diagnostic.End
			details = append(details, apperrors.FieldViolation{
				Field: fieldPtr(field + "." + string(diagnostic.Field)), Code: diagnostic.Code, Message: diagnostic.Message,
				Start: &start, End: &end,
			})
		}
		return &apperrors.Validation{Code: "TEMPLATE_INVALID", Details: details}
	default:
		return configInvalid(field, "template mode must be system_default or custom")
	}
}

func validateTriggerKinds(triggerKinds []TriggerKind) error {
	seen := make(map[TriggerKind]bool, len(triggerKinds))
	for _, kind := range triggerKinds {
		if !allowedTriggerKinds[kind] {
			return configInvalid("trigger_kinds", "trigger kind "+string(kind)+" is not allowlisted")
		}
		if seen[kind] {
			return configInvalid("trigger_kinds", "trigger kinds must be unique")
		}
		seen[kind] = true
	}
	return nil
}

func configInvalid(field, message string) error {
	return &apperrors.Validation{
		Code:    "PLUGIN_TARGET_CONFIG_INVALID",
		Details: []apperrors.FieldViolation{{Field: fieldPtr(field), Code: "PLUGIN_TARGET_CONFIG_INVALID", Message: message}},
	}
}

func validateIdempotencyKey(key string) error {
	trimmed := strings.TrimSpace(key)
	if len(trimmed) < minIdempotencyKeyLength || len(trimmed) > maxIdempotencyKeyLength {
		return &apperrors.BadRequest{Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "Idempotency-Key must be between 8 and 128 characters."}
	}
	return nil
}

// canonicalTestBody is the deterministic request body used for the test
// idempotency hash. It captures only the fields that identify the request.
func canonicalTestBody(triggerKind TriggerKind, expectedVersion int64) json.RawMessage {
	body, _ := json.Marshal(map[string]any{
		"expected_version": expectedVersion,
		"trigger_kind":     string(triggerKind),
	})
	return body
}

func mustMarshal(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		// Values marshaled from application-owned raw JSON are expected to be valid.
		panic(fmt.Sprintf("plugin target: marshal value: %v", err))
	}
	return raw
}

func fieldPtr(field string) *string { return &field }

// unionDiscriminator normalizes a union discriminator result into a plain string.
func unionDiscriminator(disc string, err error) string {
	if err != nil {
		return ""
	}
	return disc
}

// projectDeliveryRow bounds one delivery row for a read: the frozen payload and
// the frozen adapter config are served only when they are provably canonical,
// and every other case is reported through payload_status/config_status with the
// bytes withheld.
func projectDeliveryRow(row []byte) ([]byte, error) {
	projected, err := deliverypayload.ProjectRow(row)
	if err != nil {
		return nil, err
	}
	return adapterconfig.ProjectRow(projected, "adapter_config_snapshot")
}
