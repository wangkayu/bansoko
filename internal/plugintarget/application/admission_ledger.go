package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/redact"
)

// NotificationAdmission state constants.
const (
	AdmissionPendingTargetResolution = "pending_target_resolution"
	AdmissionFailed                  = "admission_failed"
	AdmissionResolved                = "resolved"
)

// EventCatalogVersion is the frozen admission catalog boundary. Bump when the
// admission contract changes (schema or semantics).
const EventCatalogVersion = "plugin-event-catalog-v1"

// TemplateCatalogVersion is the frozen template catalog boundary at admission.
const TemplateCatalogVersion = "notification-template-catalog-v1"

// LedgerEntry is the deterministic row appended inside the core owner
// transaction. It freezes the source result and the catalog boundary; it never
// queries targets/templates or creates Delivery/WorkItem rows.
type LedgerEntry struct {
	TriggerKind                 string
	SourceOwnerType             string
	SourceOwnerID               string
	SourceOwnerVersion          int64
	ResultKey                   string
	ManagedAnimeID              string
	OccurredAt                  time.Time
	EventCatalogVersion         string
	EventTemplateCatalogVersion string
	Payload                     json.RawMessage
}

// StableKey builds the canonical idempotency anchor for one target admission:
// target_id + target_version + trigger + owner_type/id/version + result_key.
// target_version is the target version observed when the resolver read the
// current configuration, so a replay of the same resolution converges on the
// same delivery row.
func StableKey(targetID string, targetVersion int64, entry LedgerEntry) string {
	canonical := strings.Join([]string{
		targetID,
		fmt.Sprintf("%d", targetVersion),
		entry.TriggerKind,
		entry.SourceOwnerType,
		entry.SourceOwnerID,
		fmt.Sprintf("%d", entry.SourceOwnerVersion),
		entry.ResultKey,
	}, "\x1f")
	digest := sha256.Sum256([]byte(canonical))
	return "notification_admission:" + hex.EncodeToString(digest[:])
}

// AppendAdmission inserts one ledger row inside the caller's transaction.
// The write is deterministic and local; failures only roll back the media
// owner transaction (which is the correct behavior for a DB fault) and never
// depend on target/template/network state.
func (s *Service) AppendAdmission(ctx context.Context, scope txscope.Tx, entry LedgerEntry) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("admission ledger transaction is nil")
	}
	return s.appendAdmissionTx(ctx, tx, entry)
}

func (s *Service) appendAdmissionTx(ctx context.Context, tx *sql.Tx, entry LedgerEntry) error {
	entry.Payload = maskPayloadURLs(entry.Payload)
	if err := validateLedgerEntry(entry); err != nil {
		return err
	}
	admissionID := uuid.NewString()
	_, err := tx.ExecContext(ctx, `
INSERT INTO notification_admissions(
    id, trigger_kind, source_owner_type, source_owner_id, source_owner_version,
    result_key, managed_anime_id, occurred_at, event_catalog_version,
    event_template_catalog_version, payload, state)
VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), clock_timestamp(), $8, $9, $10, $11)
ON CONFLICT (trigger_kind, source_owner_type, source_owner_id, source_owner_version, result_key) DO NOTHING`,
		admissionID, entry.TriggerKind, entry.SourceOwnerType, entry.SourceOwnerID, entry.SourceOwnerVersion,
		entry.ResultKey, entry.ManagedAnimeID,
		EventCatalogVersion, TemplateCatalogVersion, entry.Payload, AdmissionPendingTargetResolution)
	if err != nil {
		return fmt.Errorf("append notification admission: %w", err)
	}
	return nil
}

func validateLedgerEntry(entry LedgerEntry) error {
	if !allowedBusinessTrigger(entry.TriggerKind) {
		return fmt.Errorf("unsupported admission trigger %q", entry.TriggerKind)
	}
	if !plugintargetport.IsOwnerType(entry.SourceOwnerType) {
		return fmt.Errorf("unsupported admission owner type %q", entry.SourceOwnerType)
	}
	if strings.TrimSpace(entry.SourceOwnerID) == "" || len(entry.SourceOwnerID) > 128 {
		return errors.New("admission source owner ID is required and bounded")
	}
	if entry.SourceOwnerVersion < 1 {
		return errors.New("admission source owner version must be positive")
	}
	if strings.TrimSpace(entry.ResultKey) == "" || len(entry.ResultKey) > 512 {
		return errors.New("admission result key is required and bounded")
	}
	if len(entry.Payload) == 0 || len(entry.Payload) > 64*1024 {
		return errors.New("admission payload is required and bounded")
	}
	var payload any
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return fmt.Errorf("decode admission payload: %w", err)
	}
	if _, ok := payload.(map[string]any); !ok {
		return errors.New("admission payload must be a JSON object")
	}
	if unsafeBusinessPayload(payload, "") {
		return errors.New("admission payload contains a secret or URL fact")
	}
	return nil
}

var payloadURLPattern = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://\S*`)

// maskPayloadURLs replaces every URL inside a payload string value with the
// shared redaction marker. Payload values carry upstream text (release titles,
// torrent names); a URL inside one must never reach a notification target, but
// it is not a reason to roll back the media owner transaction that admits the
// notification. Keys are code-owned and stay reject-only in validation; a
// payload that does not decode is returned unchanged for validation to report.
func maskPayloadURLs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || !strings.Contains(string(raw), "://") {
		return raw
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw
	}
	if !maskPayloadURLValues(&payload) {
		return raw
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return raw
	}
	return json.RawMessage(bytes.TrimRight(encoded.Bytes(), "\n"))
}

func maskPayloadURLValues(value *any) bool {
	changed := false
	switch typed := (*value).(type) {
	case map[string]any:
		for key, child := range typed {
			if maskPayloadURLValues(&child) {
				typed[key], changed = child, true
			}
		}
	case []any:
		for index := range typed {
			if maskPayloadURLValues(&typed[index]) {
				changed = true
			}
		}
	case string:
		if strings.Contains(typed, "://") {
			*value, changed = payloadURLPattern.ReplaceAllString(typed, redact.Redacted), true
		}
	}
	return changed
}
