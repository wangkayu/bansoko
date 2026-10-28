package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/redact"
)

const (
	maxIdentityLength  = 128
	maxGroupRefLength  = 256
	maxDedupeKeyLength = 512
	maxSummaryLength   = 4096
	maxErrorLength     = 8192
	maxDetailsBytes    = 262144
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)

var activityKinds = map[string]struct{}{
	"subscription_created": {}, "subscription_started": {}, "subscription_stopped": {}, "subscription_resumed": {}, "metadata_applied": {}, "group_changed": {}, "policy_changed": {},
	"rss_new_content": {}, "rss_refresh_failed": {}, "release_processed": {},
	"download_changed": {}, "artifact_verified": {}, "placement_changed": {}, "replacement_decided": {}, "cleanup_completed": {}, "cleanup_failed": {},
	"file_alignment_completed": {}, "file_alignment_failed": {}, "acquisition_settled": {},
}
var activityOutcomes = map[string]struct{}{"info": {}, "success": {}, "failure": {}}

// AppendTx validates and persists one Activity using the legacy subscription-log
// columns as the compatibility projection. The unique legacy key preserves
// replay idempotency while the new envelope columns carry typed lineage.
func (s *Service) AppendTx(ctx context.Context, scope txscope.Tx, record observabilityport.ActivityRecord) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("activity transaction is nil")
	}
	record = sanitizeActivity(record)
	if err := validateActivity(record); err != nil {
		return err
	}
	details := nullableJSON(record.Details)
	dedupeKey := activityDedupeKey(record)
	var occurredAt any
	if !record.OccurredAt.IsZero() {
		occurredAt = record.OccurredAt.UTC()
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO subscription_log_entries(
    id, subscription_id, managed_anime_id, kind, outcome, title, summary,
    rss_source_id, release_id, group_ref, details, dedupe_key, reason_code,
    subject_type, subject_id, subject_version, correlation_id, causation_type, causation_id,
    occurred_at, retention_until
)
VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''), $11, $12, $13,
        $14, $15, $16, NULLIF($17, ''), NULLIF($18, ''), NULLIF($19, ''),
        COALESCE($20::timestamptz, CURRENT_TIMESTAMP),
        -- Replacement counts from the fact time (the table caps it there), so a
        -- late-derived entry is kept no longer than one written in-stage.
        COALESCE($20::timestamptz, CURRENT_TIMESTAMP) + INTERVAL '30 days')
ON CONFLICT (subscription_id, dedupe_key) DO NOTHING`,
		uuid.NewString(), record.SubscriptionID, record.ManagedAnimeID, record.Kind,
		record.Outcome, record.Title, record.Summary, record.RSSSourceID, record.ReleaseID,
		record.GroupRef, details, dedupeKey, record.ReasonCode, record.SubjectType,
		record.SubjectID, record.SubjectVersion, record.CorrelationID, record.CausationType,
		record.CausationID, occurredAt)
	if err != nil {
		return fmt.Errorf("append activity: %w", err)
	}
	return nil
}

const maxActivityTitleLength = 512

// sanitizeActivity applies the write-side redaction policy to the free-text
// fields of one Activity. Titles, summaries, group names and detail string
// values carry upstream text (release titles, torrent names, error reasons), so
// a sensitive fragment inside them is masked in place and the text is bounded,
// instead of failing the owner transaction the Activity belongs to. Structural
// fields (kind, identities, reason code, detail keys) stay reject-only: those
// are produced by code, and a bad value there is a programming error.
func sanitizeActivity(record observabilityport.ActivityRecord) observabilityport.ActivityRecord {
	record.Title = sanitizeFreeText(record.Title, maxActivityTitleLength)
	record.Summary = sanitizeFreeText(record.Summary, maxSummaryLength)
	if record.GroupRef != "" {
		record.GroupRef = sanitizeFreeText(record.GroupRef, maxGroupRefLength)
	}
	record.Details = sanitizeDetails(record.Details)
	return record
}

// sanitizeFreeText folds control characters, masks each sensitive fragment
// (URL, credential, private path) with the shared redaction marker and bounds
// the result to maxBytes without splitting a rune. Whitespace-only input stays
// empty so the non-empty validation still reports a caller bug.
func sanitizeFreeText(value string, maxBytes int) string {
	safe, _ := redact.Apply(value, maxBytes, redact.MaskFragments, redact.Redacted)
	safe = truncateUTF8Bytes(safe, maxBytes)
	// A byte cut can expose a new match ("/database" cut to "/data"); one more
	// pass masks it, and anything still flagged collapses to the marker.
	if _, matched := redact.Apply(safe, maxBytes, redact.Reject, ""); matched {
		safe, _ = redact.Apply(safe, maxBytes, redact.MaskFragments, redact.Redacted)
		safe = truncateUTF8Bytes(safe, maxBytes)
		if _, matched := redact.Apply(safe, maxBytes, redact.Reject, ""); matched {
			safe = redact.Redacted
		}
	}
	return safe
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// sanitizeDetails masks sensitive fragments inside detail string values. Keys
// are left untouched so validateDetails still rejects a sensitive key. Invalid
// JSON is returned unchanged for validateDetails to report.
func sanitizeDetails(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || len(raw) > maxDetailsBytes {
		return raw
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return raw
	}
	if !sanitizeJSONStrings(object) {
		return raw
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(object); err != nil {
		return raw
	}
	return json.RawMessage(bytes.TrimRight(encoded.Bytes(), "\n"))
}

func sanitizeJSONStrings(value any) bool {
	changed := false
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if text, ok := child.(string); ok {
				if safe := sanitizeDetailString(text); safe != text {
					typed[key], changed = safe, true
				}
				continue
			}
			if sanitizeJSONStrings(child) {
				changed = true
			}
		}
	case []any:
		for index, child := range typed {
			if text, ok := child.(string); ok {
				if safe := sanitizeDetailString(text); safe != text {
					typed[index], changed = safe, true
				}
				continue
			}
			if sanitizeJSONStrings(child) {
				changed = true
			}
		}
	}
	return changed
}

// sanitizeDetailString leaves a value without sensitive fragments byte-for-byte
// unchanged (detail values are often exact identifiers); only a value the
// detector flags, or one over the bound, is rewritten.
func sanitizeDetailString(value string) string {
	if _, matched := redact.Apply(value, maxErrorLength, redact.Reject, ""); !matched && len(value) <= maxErrorLength && !strings.ContainsRune(value, 0) {
		return value
	}
	return sanitizeFreeText(value, maxErrorLength)
}

func validateActivity(record observabilityport.ActivityRecord) error {
	if _, ok := activityKinds[record.Kind]; !ok {
		return fmt.Errorf("activity kind %q is not allowlisted", record.Kind)
	}
	if _, ok := activityOutcomes[record.Outcome]; !ok {
		return fmt.Errorf("activity outcome %q is not allowlisted", record.Outcome)
	}
	for label, value := range map[string]string{
		"subscription ID": record.SubscriptionID, "managed anime ID": record.ManagedAnimeID,
		"reason code": record.ReasonCode, "subject type": record.SubjectType, "subject ID": record.SubjectID,
	} {
		if err := validateIdentifierOrBounded(label, value, maxIdentityLength); err != nil {
			return err
		}
	}
	if record.SubjectVersion < 1 {
		return errors.New("activity subject version must be positive")
	}
	if err := validateOptionalActivityField("stable result key", record.StableResultKey, maxDedupeKeyLength); err != nil {
		return err
	}
	if err := validateOptionalActivityField("RSS source ID", record.RSSSourceID, maxIdentityLength); err != nil {
		return err
	}
	if err := validateOptionalActivityField("release ID", record.ReleaseID, maxIdentityLength); err != nil {
		return err
	}
	if err := validateOptionalActivityField("group ref", record.GroupRef, maxGroupRefLength); err != nil {
		return err
	}
	if record.DedupeKey != "" {
		if err := validateIdentifierOrBounded("dedupe key", record.DedupeKey, maxDedupeKeyLength); err != nil {
			return err
		}
	}
	if dedupeKey := activityDedupeKey(record); len(dedupeKey) > maxDedupeKeyLength {
		return fmt.Errorf("activity dedupe key exceeds %d bytes", maxDedupeKeyLength)
	}
	if err := validateSafeText("activity title", record.Title, maxActivityTitleLength, true); err != nil {
		return err
	}
	if err := validateSafeText("activity summary", record.Summary, maxSummaryLength, true); err != nil {
		return err
	}
	if err := validateDetails(record.Details); err != nil {
		return fmt.Errorf("activity details: %w", err)
	}
	for label, value := range map[string]string{
		"correlation ID": record.CorrelationID, "causation type": record.CausationType, "causation ID": record.CausationID,
	} {
		if value != "" {
			if err := validateIdentifierOrBounded(label, value, maxIdentityLength); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateOptionalActivityField(label, value string, max int) error {
	if value == "" {
		return nil
	}
	return validateIdentifierOrBounded(label, value, max)
}

func activityDedupeKey(record observabilityport.ActivityRecord) string {
	if record.DedupeKey != "" {
		return record.DedupeKey
	}
	stableResultKey := record.StableResultKey
	if stableResultKey == "" {
		// Existing migrated callers did not provide a stable result key. Keep
		// their replay identity stable while all new RSS/Release callers use the
		// explicit field below.
		stableResultKey = record.ReasonCode
	}
	return fmt.Sprintf("%s:%s:%s:v%d:%s", record.Kind, record.SubjectType, record.SubjectID, record.SubjectVersion, stableResultKey)
}

var _ observabilityport.Recorder = (*Service)(nil)

// UpsertRoutine records the last known result for a fixed routine identity.
func (s *Service) UpsertRoutine(ctx context.Context, req observabilityport.RoutineRequest) error {
	req.Summary = sanitizeFreeText(req.Summary, maxSummaryLength)
	req.Details = sanitizeDetails(req.Details)
	if err := validateRoutine(req); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin routine transaction: %w", err)
	}
	defer tx.Rollback()
	if err := s.UpsertRoutineTx(ctx, txscope.Wrap(tx), req); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit routine transaction: %w", err)
	}
	return nil
}

// UpsertRoutineTx updates the fixed routine row without creating history.
func (s *Service) UpsertRoutineTx(ctx context.Context, scope txscope.Tx, req observabilityport.RoutineRequest) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("routine transaction is nil")
	}
	req.Summary = sanitizeFreeText(req.Summary, maxSummaryLength)
	req.Details = sanitizeDetails(req.Details)
	if err := validateRoutine(req); err != nil {
		return err
	}
	details := nullableJSON(req.Details)
	_, err := tx.ExecContext(ctx, `
INSERT INTO routine_activities(
    id, kind, scope_kind, scope_id, managed_anime_id, outcome,
    last_executed_at, last_result_code, summary, details, updated_at
)
VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, CURRENT_TIMESTAMP, $7, $8, $9, CURRENT_TIMESTAMP)
ON CONFLICT (kind, scope_kind, scope_id) DO UPDATE SET
    managed_anime_id = EXCLUDED.managed_anime_id,
    outcome = EXCLUDED.outcome,
    last_executed_at = CURRENT_TIMESTAMP,
    last_result_code = EXCLUDED.last_result_code,
    summary = EXCLUDED.summary,
    details = EXCLUDED.details,
    updated_at = CURRENT_TIMESTAMP`,
		uuid.NewString(), req.Identity.Kind, req.Identity.ScopeKind, req.ScopeID, req.ManagedAnimeID,
		req.Identity.Outcome, req.Identity.ResultCode, req.Summary, details)
	if err != nil {
		return fmt.Errorf("upsert routine activity: %w", err)
	}
	return nil
}

// RecordDiagnosticFailure creates or updates the currently open fault episode.
func (s *Service) RecordDiagnosticFailure(ctx context.Context, req observabilityport.DiagnosticFailureRequest) error {
	req.LastError = sanitizeFreeText(req.LastError, maxErrorLength)
	req.Details = sanitizeDetails(req.Details)
	if err := validateDiagnosticFailure(req); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin diagnostic failure transaction: %w", err)
	}
	defer tx.Rollback()
	if err := s.RecordDiagnosticFailureTx(ctx, txscope.Wrap(tx), req); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit diagnostic failure transaction: %w", err)
	}
	return nil
}

// RecordDiagnosticFailureTx writes diagnostic evidence in the caller transaction.
func (s *Service) RecordDiagnosticFailureTx(ctx context.Context, scope txscope.Tx, req observabilityport.DiagnosticFailureRequest) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("diagnostic failure transaction is nil")
	}
	req.LastError = sanitizeFreeText(req.LastError, maxErrorLength)
	req.Details = sanitizeDetails(req.Details)
	if err := validateDiagnosticFailure(req); err != nil {
		return err
	}
	details := nullableJSON(req.Details)
	_, err := tx.ExecContext(ctx, `
INSERT INTO diagnostic_episodes(
    id, severity, component, operation, error_code, scope_kind, scope_id,
    managed_anime_id, first_failed_at, last_failed_at, occurrence_count,
    last_error, details, retention_until
)
VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), CURRENT_TIMESTAMP,
        CURRENT_TIMESTAMP, 1, $9, $10, CURRENT_TIMESTAMP + INTERVAL '14 days')
ON CONFLICT (component, operation, error_code, scope_kind, scope_id)
WHERE succeeded_after_at IS NULL
DO UPDATE SET
    last_failed_at = CURRENT_TIMESTAMP,
    occurrence_count = diagnostic_episodes.occurrence_count + 1,
    managed_anime_id = EXCLUDED.managed_anime_id,
    last_error = EXCLUDED.last_error,
    details = EXCLUDED.details,
    retention_until = CURRENT_TIMESTAMP + INTERVAL '14 days'`,
		uuid.NewString(), req.Severity, req.Identity.Component, req.Identity.Operation, req.Identity.ErrorCode,
		req.Identity.ScopeKind, req.ScopeID, req.ManagedAnimeID, req.LastError, details)
	if err != nil {
		return fmt.Errorf("record diagnostic failure: %w", err)
	}
	return nil
}

// RecordDiagnosticSuccess closes the current open episode, if present.
func (s *Service) RecordDiagnosticSuccess(ctx context.Context, req observabilityport.DiagnosticSuccessRequest) error {
	if err := validateDiagnosticSuccess(req); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin diagnostic success transaction: %w", err)
	}
	defer tx.Rollback()
	if err := s.RecordDiagnosticSuccessTx(ctx, txscope.Wrap(tx), req); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit diagnostic success transaction: %w", err)
	}
	return nil
}

// RecordDiagnosticSuccessTx closes an open episode in the caller transaction.
func (s *Service) RecordDiagnosticSuccessTx(ctx context.Context, scope txscope.Tx, req observabilityport.DiagnosticSuccessRequest) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("diagnostic success transaction is nil")
	}
	if err := validateDiagnosticSuccess(req); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE diagnostic_episodes
SET succeeded_after_at = CURRENT_TIMESTAMP,
    retention_until = CURRENT_TIMESTAMP + INTERVAL '14 days'
WHERE component = $1 AND operation = $2 AND error_code = $3
  AND scope_kind = $4 AND scope_id = $5
  AND succeeded_after_at IS NULL`,
		req.Identity.Component, req.Identity.Operation, req.Identity.ErrorCode, req.Identity.ScopeKind, req.ScopeID)
	if err != nil {
		return fmt.Errorf("record diagnostic success: %w", err)
	}
	return nil
}

func validateRoutine(req observabilityport.RoutineRequest) error {
	id := req.Identity
	if !observabilityport.IsRoutineKind(id.Kind) {
		return fmt.Errorf("routine kind %q is not allowlisted", id.Kind)
	}
	if !observabilityport.IsRoutineScopeKind(id.ScopeKind) {
		return fmt.Errorf("routine scope kind %q is not allowlisted", id.ScopeKind)
	}
	if err := validateScopeID(string(id.ScopeKind), req.ScopeID); err != nil {
		return err
	}
	if !observabilityport.IsRoutineOutcome(id.Outcome) {
		return fmt.Errorf("routine outcome %q is not allowlisted", id.Outcome)
	}
	if !observabilityport.IsRoutineResultCode(id.Kind, id.ResultCode) {
		return fmt.Errorf("routine result code %q is not allowlisted for kind %q", id.ResultCode, id.Kind)
	}
	if err := validateSafeText("routine summary", req.Summary, maxSummaryLength, true); err != nil {
		return err
	}
	if err := validateManagedAnimeID(req.ManagedAnimeID); err != nil {
		return err
	}
	return validateDetails(req.Details)
}

func validateDiagnosticFailure(req observabilityport.DiagnosticFailureRequest) error {
	id := req.Identity
	if !observabilityport.IsDiagnosticSeverity(req.Severity) {
		return fmt.Errorf("diagnostic severity %q is not allowlisted", req.Severity)
	}
	if !observabilityport.IsDiagnosticComponent(id.Component) {
		return fmt.Errorf("diagnostic component %q is not allowlisted", id.Component)
	}
	if !observabilityport.IsDiagnosticOperation(id.Operation) {
		return fmt.Errorf("diagnostic operation %q is not allowlisted", id.Operation)
	}
	if !observabilityport.IsDiagnosticErrorCode(id.ErrorCode) {
		return fmt.Errorf("diagnostic error code %q is not allowlisted", id.ErrorCode)
	}
	if !observabilityport.IsDiagnosticScopeKind(id.ScopeKind) {
		return fmt.Errorf("diagnostic scope kind %q is not allowlisted", id.ScopeKind)
	}
	if err := validateScopeID(string(id.ScopeKind), req.ScopeID); err != nil {
		return err
	}
	if err := validateManagedAnimeID(req.ManagedAnimeID); err != nil {
		return err
	}
	if err := validateSafeText("diagnostic last error", req.LastError, maxErrorLength, true); err != nil {
		return err
	}
	return validateDetails(req.Details)
}

func validateDiagnosticSuccess(req observabilityport.DiagnosticSuccessRequest) error {
	id := req.Identity
	if !observabilityport.IsDiagnosticComponent(id.Component) {
		return fmt.Errorf("diagnostic component %q is not allowlisted", id.Component)
	}
	if !observabilityport.IsDiagnosticOperation(id.Operation) {
		return fmt.Errorf("diagnostic operation %q is not allowlisted", id.Operation)
	}
	if !observabilityport.IsDiagnosticErrorCode(id.ErrorCode) {
		return fmt.Errorf("diagnostic error code %q is not allowlisted", id.ErrorCode)
	}
	if !observabilityport.IsDiagnosticScopeKind(id.ScopeKind) {
		return fmt.Errorf("diagnostic scope kind %q is not allowlisted", id.ScopeKind)
	}
	return validateScopeID(string(id.ScopeKind), req.ScopeID)
}

func validateScopeID(scopeKind, scopeID string) error {
	if strings.TrimSpace(scopeID) == "" || len(scopeID) > maxIdentityLength {
		return fmt.Errorf("%s scope ID must be non-empty and bounded", scopeKind)
	}
	if scopeKind == "global" && scopeID != "global" {
		return errors.New("global scope ID must be global")
	}
	if scopeKind != "global" && scopeID == "global" {
		return errors.New("non-global scope ID cannot be global")
	}
	return nil
}

func validateManagedAnimeID(value string) error {
	if value != "" && (strings.TrimSpace(value) == "" || len(value) > maxIdentityLength) {
		return errors.New("managed anime ID must be bounded when present")
	}
	return nil
}

func validateIdentifierOrBounded(label, value string, max int) error {
	if strings.TrimSpace(value) == "" || len(value) > max {
		return fmt.Errorf("%s must be non-empty and bounded", label)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s contains unsafe control characters", label)
	}
	return nil
}

func validateSafeText(label, value string, max int, requireNonEmpty bool) error {
	if requireNonEmpty && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must be non-empty", label)
	}
	if len(value) > max {
		return fmt.Errorf("%s exceeds %d bytes", label, max)
	}
	if strings.ContainsAny(value, "\x00") {
		return fmt.Errorf("%s contains unsafe control characters", label)
	}
	redacted := redactText(value)
	if redacted != value {
		return fmt.Errorf("%s contains a secret, URL, or private path", label)
	}
	return nil
}

func validateDetails(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > maxDetailsBytes {
		return fmt.Errorf("details exceed %d bytes", maxDetailsBytes)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("details must be a JSON object: %w", err)
	}
	if object == nil {
		return errors.New("details must be a JSON object")
	}
	if err := validateJSONValues(object); err != nil {
		return fmt.Errorf("details contain unsafe data: %w", err)
	}
	canonical, err := json.Marshal(object)
	if err != nil || len(canonical) > maxDetailsBytes {
		return fmt.Errorf("details exceed %d bytes", maxDetailsBytes)
	}
	return nil
}

func validateJSONValues(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if strings.ContainsAny(key, "\x00\r\n") {
				return errors.New("object key contains unsafe control characters")
			}
			lowerKey := strings.ToLower(key)
			for _, marker := range []string{"password", "token", "secret", "api_key", "apikey", "authorization", "cookie", "credential", "passkey", "dsn", "private_key"} {
				if strings.Contains(lowerKey, marker) {
					return errors.New("object key contains sensitive data")
				}
			}
			if err := validateJSONValues(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateJSONValues(child); err != nil {
				return err
			}
		}
	case string:
		if err := validateSafeText("detail value", typed, maxErrorLength, false); err != nil {
			return err
		}
	}
	return nil
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

// RedactText applies the diagnostic write path's shared reject policy.
func RedactText(value string) string {
	return redactText(value)
}

func redactText(value string) string {
	_, matched := redact.Apply(value, maxErrorLength, redact.Reject, "<redacted>")
	if matched {
		return "<redacted>"
	}
	return value
}
