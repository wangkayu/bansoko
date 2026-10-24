package application

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"bansoko/internal/apperrors"
	"bansoko/internal/persistence/txscope"
	adapterconfig "bansoko/internal/plugintarget/config"
	deliverypayload "bansoko/internal/plugintarget/payload"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
	"bansoko/internal/workitems"
)

var _ plugintargetport.BusinessDeliveryEnqueuer = (*Service)(nil)

// EnqueueBusinessDelivery appends a deterministic NotificationAdmission
// ledger row inside the caller's transaction (P1.3). It never queries
// targets/templates, never renders, and never creates Delivery/WorkItem rows
// inside the media owner transaction — those move to the ledger-only resolver.
// The write is local and deterministic, so notification configuration,
// template, database or network failures can never roll back the confirmed
// media owner result.
func (s *Service) EnqueueBusinessDelivery(ctx context.Context, scope txscope.Tx, request plugintargetport.BusinessDeliveryRequest) error {
	if s == nil || s.db == nil {
		return errors.New("business delivery ledger is not configured")
	}
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("business delivery transaction is nil")
	}
	entry := LedgerEntry{
		TriggerKind:                 request.TriggerKind,
		SourceOwnerType:             request.SourceOwnerType,
		SourceOwnerID:               request.SourceOwnerID,
		SourceOwnerVersion:          request.SourceOwnerVersion,
		ResultKey:                   request.ResultKey,
		ManagedAnimeID:              request.ManagedAnimeID,
		OccurredAt:                  time.Now().UTC(),
		EventCatalogVersion:         EventCatalogVersion,
		EventTemplateCatalogVersion: TemplateCatalogVersion,
		Payload:                     request.Payload,
	}
	return s.AppendAdmission(ctx, scope, entry)
}

func allowedBusinessTrigger(trigger string) bool {
	return plugintargetport.IsTrigger(trigger)
}

func unsafeBusinessPayload(value any, key string) bool {
	lowerKey := strings.ToLower(key)
	for _, marker := range []string{"secret", "token", "password", "credential", "url"} {
		if strings.Contains(lowerKey, marker) {
			return true
		}
	}
	switch item := value.(type) {
	case map[string]any:
		for childKey, child := range item {
			if unsafeBusinessPayload(child, childKey) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if unsafeBusinessPayload(child, key) {
				return true
			}
		}
	case string:
		return strings.Contains(strings.ToLower(item), "://")
	}
	return false
}

func (s *Service) enqueueForTarget(ctx context.Context, tx *sql.Tx, target targetRecord, request plugintargetport.BusinessDeliveryRequest) error {
	return s.enqueueForTargetWithKey(ctx, tx, target, request, businessDeliveryKey(target.ID, request))
}

// enqueueForTargetWithKey creates the frozen Delivery + plugin_deliver
// WorkItem for one target. idempotencyKey is the caller-supplied stable key
// (business delivery key for the legacy path, admission stable key for the
// resolver); the Delivery UNIQUE constraint and WorkItem idempotency make
// retries converge.
func (s *Service) enqueueForTargetWithKey(ctx context.Context, tx *sql.Tx, target targetRecord, request plugintargetport.BusinessDeliveryRequest, idempotencyKey string) error {
	// A delivery freezes the target's canonical adapter config. A target whose
	// stored config is not provably canonical cannot produce a delivery: failing
	// closed here keeps an unprovable document out of the snapshot and out of
	// every later read.
	if target.ConfigStatus != adapterconfig.StatusAvailable {
		return &apperrors.Validation{
			Code:    "PLUGIN_TARGET_CONFIG_INVALID",
			Details: []apperrors.FieldViolation{{Field: fieldPtr("config"), Code: "PLUGIN_TARGET_CONFIG_INVALID", Message: "The target adapter config is not a canonical allowlisted document; re-save the target before delivering."}},
		}
	}
	facts, err := decodeBusinessFacts(request.Payload)
	if err != nil {
		return err
	}
	frozenPayload, err := deliverypayload.Encode(request.TriggerKind, facts)
	if err != nil {
		return fmt.Errorf("encode business delivery payload: %w", err)
	}
	incomingDigest := hashBytes(frozenPayload)

	// The delivery identity is (target, trigger, owner, version, result key). A
	// repeat with the same identity and the same frozen payload is the same
	// delivery and replays; the same identity with a different payload is a
	// conflict, because the row already owns a different frozen fact snapshot.
	var existingDigest string
	err = tx.QueryRowContext(ctx, `
SELECT payload_digest
FROM plugin_deliveries
WHERE plugin_target_config_id = $1
  AND trigger_kind = $2
  AND source_owner_type = $3
  AND source_owner_id = $4
  AND source_owner_version = $5
  AND source_result_key = $6
FOR UPDATE`, target.ID, request.TriggerKind, request.SourceOwnerType, request.SourceOwnerID, request.SourceOwnerVersion, request.ResultKey).Scan(&existingDigest)
	if err == nil {
		if existingDigest != incomingDigest {
			return &apperrors.Business{
				Status:  http.StatusConflict,
				Code:    "DELIVERY_PAYLOAD_CONFLICT",
				Message: "The delivery identity was already admitted with a different frozen payload.",
			}
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check business delivery idempotency: %w", err)
	}

	titleTemplate, bodyTemplate, err := selectTemplate(target.TemplateSet, TriggerKind(request.TriggerKind))
	if err != nil {
		return fmt.Errorf("select business delivery template: %w", err)
	}
	message := renderBusinessMessage(request.TriggerKind, titleTemplate, bodyTemplate, facts)
	renderedTitle, renderedBody := message.Title, message.Body
	deliveryID := uuid.NewString()
	digest := incomingDigest
	targetDisplay := buildTargetDisplay(target)
	if !message.Valid() {
		first := message.Errors[0]
		reason := string(first.Field) + ": " + first.Message
		if err := s.insertFailedDeliveryRowTx(ctx, tx, target, request, idempotencyKey, frozenPayload, titleTemplate, bodyTemplate, "notification_template_invalid", reason); err != nil {
			return fmt.Errorf("create failed business delivery: %w", err)
		}
		return nil
	}

	messageDigest := hashBytes([]byte(renderedTitle + "\n\n" + renderedBody))
	_, err = tx.ExecContext(ctx, `
INSERT INTO plugin_deliveries(
    id, plugin_target_config_id, plugin_target_config_version, adapter_kind,
    adapter_config_snapshot, secret_ref, delivery_kind, trigger_kind,
    source_owner_type, source_owner_id, source_owner_version, source_result_key,
    managed_anime_id, state, idempotency_key, payload_schema_version, payload,
    payload_digest, template_language_version, template_catalog_version,
    title_template_snapshot, body_template_snapshot, rendered_title, rendered_body,
    rendered_message_digest, target_display)
VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), 'business', $7, $8, $9, $10, $11,
        NULLIF($12, ''), 'pending', $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)`,
		deliveryID, target.ID, target.Version, string(target.AdapterKind), target.Config, target.secretRef,
		request.TriggerKind, request.SourceOwnerType, request.SourceOwnerID, request.SourceOwnerVersion, request.ResultKey,
		request.ManagedAnimeID, idempotencyKey, deliverypayload.SchemaVersion, frozenPayload, digest,
		templateLanguageVersion, templateCatalogVersion, titleTemplate, bodyTemplate, renderedTitle, renderedBody,
		messageDigest, targetDisplay)
	if err != nil {
		return fmt.Errorf("create business delivery: %w", err)
	}
	if _, err := s.workItems.Create(ctx, tx, workitems.WorkItemSpec{
		Owner: workitems.OwnerRef{Type: workitems.OwnerPluginDelivery, ID: deliveryID},
		Kind:  workitems.KindPluginDeliver, IdempotencyKey: idempotencyKey,
		InputSnapshot: frozenPayload, MaxAttempts: deliveryWorkItemMaxAttempts, ExpectedOwnerVersion: 1,
	}); err != nil {
		return fmt.Errorf("schedule business plugin delivery: %w", err)
	}
	return nil
}

// insertFailedDeliveryTx records a terminal failed delivery for a target that
// cannot produce a sendable one. It tolerates the inputs that caused the
// failure: an undecodable payload freezes as an empty fact set, a missing
// template as empty snapshots, a non-canonical config as an invalid snapshot.
func (s *Service) insertFailedDeliveryTx(ctx context.Context, tx *sql.Tx, target targetRecord, request plugintargetport.BusinessDeliveryRequest, idempotencyKey, code, reason string) error {
	facts, err := decodeBusinessFacts(request.Payload)
	if err != nil {
		facts = map[string]any{}
	}
	frozenPayload, err := deliverypayload.Encode(request.TriggerKind, facts)
	if err != nil {
		if frozenPayload, err = deliverypayload.Encode(request.TriggerKind, map[string]any{}); err != nil {
			return err
		}
	}
	titleTemplate, bodyTemplate, err := selectTemplate(target.TemplateSet, TriggerKind(request.TriggerKind))
	if err != nil {
		titleTemplate, bodyTemplate = "", ""
	}
	return s.insertFailedDeliveryRowTx(ctx, tx, target, request, idempotencyKey, frozenPayload, titleTemplate, bodyTemplate, code, reason)
}

func (s *Service) insertFailedDeliveryRowTx(ctx context.Context, tx *sql.Tx, target targetRecord, request plugintargetport.BusinessDeliveryRequest, idempotencyKey string, frozenPayload []byte, titleTemplate, bodyTemplate, code, reason string) error {
	config, configStatus := target.Config, adapterconfig.StatusAvailable
	if target.ConfigStatus != adapterconfig.StatusAvailable || len(config) == 0 {
		// Never copy a non-canonical document into a delivery snapshot.
		config, _ = json.Marshal(map[string]string{"adapter_kind": string(target.AdapterKind)})
		configStatus = adapterconfig.StatusInvalid
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO plugin_deliveries(
    id, plugin_target_config_id, plugin_target_config_version, adapter_kind,
    adapter_config_snapshot, config_status, secret_ref, delivery_kind, trigger_kind,
    source_owner_type, source_owner_id, source_owner_version, source_result_key,
    managed_anime_id, state, idempotency_key, payload_schema_version, payload,
    payload_digest, template_language_version, template_catalog_version,
    title_template_snapshot, body_template_snapshot, target_display,
    last_error_code, last_error)
VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), 'business', $8, $9, $10, $11, $12,
        NULLIF($13, ''), 'failed', $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)
ON CONFLICT DO NOTHING`,
		uuid.NewString(), target.ID, target.Version, string(target.AdapterKind), config, string(configStatus), target.secretRef,
		request.TriggerKind, request.SourceOwnerType, request.SourceOwnerID, request.SourceOwnerVersion, request.ResultKey,
		request.ManagedAnimeID, idempotencyKey, deliverypayload.SchemaVersion, frozenPayload, hashBytes(frozenPayload),
		templateLanguageVersion, templateCatalogVersion, titleTemplate, bodyTemplate, buildTargetDisplay(target), code, truncate(reason, 4096))
	return err
}

func businessDeliveryKey(targetID string, request plugintargetport.BusinessDeliveryRequest) string {
	canonical := strings.Join([]string{targetID, request.TriggerKind, request.SourceOwnerType, request.SourceOwnerID,
		fmt.Sprintf("%d", request.SourceOwnerVersion), request.ResultKey}, "\x1f")
	return "plugin_business:" + hashBytes([]byte(canonical))
}

// decodeBusinessFacts decodes the frozen payload facts. Numbers keep their
// literal form so an integer fact renders as written.
func decodeBusinessFacts(raw json.RawMessage) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var facts map[string]any
	if err := decoder.Decode(&facts); err != nil {
		return nil, fmt.Errorf("decode business notification facts: %w", err)
	}
	return facts, nil
}

// renderBusinessMessage renders a saved template against frozen payload facts
// through the same notification core preview and test delivery use.
func renderBusinessMessage(trigger, titleTemplate, bodyTemplate string, facts map[string]any) notification.Message {
	message, ok := notification.RenderMessage(trigger, titleTemplate, bodyTemplate, facts)
	if !ok {
		return notification.Message{Errors: []notification.SourceError{{Code: "unknown_trigger", Message: "unknown notification trigger " + trigger, Field: notification.FieldTitle}}}
	}
	return message
}
