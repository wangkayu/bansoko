package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"bansoko/internal/observability/outbound"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/redact"
)

const (
	maxRecentOccurrences = 5
	maxAffectedIDs       = 1000
	maxTargetNameLength  = 256
	maxHostLength        = 253
	diagnosticRetention  = 14 * 24 * time.Hour
)

var errorClasses = map[outbound.Class]struct{}{
	outbound.ClassTimeout: {}, outbound.ClassConnectionRefused: {}, outbound.ClassConnectionReset: {},
	outbound.ClassDNS: {}, outbound.ClassTLS: {}, outbound.ClassHTTPStatus: {}, outbound.ClassUnknown: {},
}

// recentOccurrence is one entry of an episode's bounded history.
type recentOccurrence struct {
	At         time.Time `json:"at"`
	ErrorClass string    `json:"error_class,omitempty"`
	Via        string    `json:"via,omitempty"`
	Host       string    `json:"host,omitempty"`
}

// episodeState is the mutable part of one open episode. mergeFailure derives
// the next state from it in Go; the database only enforces the invariants.
type episodeState struct {
	Variant       string
	Severity      string
	TargetName    string
	TargetHost    string
	ErrorClass    string
	HTTPStatus    int
	Via           string
	AffectedIDs   []string
	Recent        []recentOccurrence
	Occurrences   int
	LastRequestID string
}

// preparedFailure is a validated, sanitized failure request.
type preparedFailure struct {
	req observabilityport.DiagnosticFailureRequest
}

func prepareFailure(req observabilityport.DiagnosticFailureRequest) (preparedFailure, error) {
	if !req.Validate() {
		return preparedFailure{}, fmt.Errorf("diagnostic condition %s.%s is not in the catalog or its request is malformed", req.Condition.Kind, req.Condition.Variant)
	}
	req.TargetName = strings.TrimSpace(sanitizeFreeText(req.TargetName, maxTargetNameLength))
	if req.TargetName == redact.Redacted {
		req.TargetName = ""
	}
	if err := validateSafeText("diagnostic target name", req.TargetName, maxTargetNameLength, false); err != nil {
		return preparedFailure{}, err
	}
	f := req.Failure
	if f.Class != "" {
		if _, ok := errorClasses[f.Class]; !ok {
			return preparedFailure{}, fmt.Errorf("diagnostic error class %q is not allowlisted", f.Class)
		}
	}
	if f.Status != 0 && (f.Class != outbound.ClassHTTPStatus || f.Status < 100 || f.Status > 599) {
		return preparedFailure{}, errors.New("diagnostic http status is only valid for the http_status class")
	}
	if f.Via != "" && f.Via != outbound.ViaDirect && f.Via != outbound.ViaProxy {
		return preparedFailure{}, fmt.Errorf("diagnostic route %q is not allowlisted", f.Via)
	}
	if len(f.Host) > maxHostLength || strings.ContainsAny(f.Host, "/?#@: \r\n\x00") {
		return preparedFailure{}, errors.New("diagnostic host must be a bare host name")
	}
	if len(req.AffectedID) > maxIdentityLength {
		return preparedFailure{}, errors.New("diagnostic affected id must be bounded")
	}
	return preparedFailure{req: req}, nil
}

// mergeFailure folds one failure into the open episode (nil starts one). The
// newest failure decides the variant, severity, target text and detail; the
// occurrence count grows; recent keeps the last five; the affected set is a
// deduplicated id set that saturates at its bound.
func mergeFailure(prev *episodeState, p preparedFailure, now time.Time) episodeState {
	req := p.req
	next := episodeState{}
	if prev != nil {
		next = *prev
		next.AffectedIDs = append([]string(nil), prev.AffectedIDs...)
		next.Recent = append([]recentOccurrence(nil), prev.Recent...)
	}
	next.Variant = string(req.Condition.Variant)
	next.Severity = string(req.Severity)
	if req.TargetName != "" {
		next.TargetName = req.TargetName
	}
	next.TargetHost = req.Failure.Host
	next.ErrorClass = string(req.Failure.Class)
	next.HTTPStatus = req.Failure.Status
	next.Via = string(req.Failure.Via)
	next.Occurrences++
	if req.RequestID != "" {
		next.LastRequestID = req.RequestID
	}
	if req.AffectedID != "" && len(next.AffectedIDs) < maxAffectedIDs {
		seen := false
		for _, id := range next.AffectedIDs {
			if id == req.AffectedID {
				seen = true
				break
			}
		}
		if !seen {
			next.AffectedIDs = append(next.AffectedIDs, req.AffectedID)
		}
	}
	next.Recent = append(next.Recent, recentOccurrence{At: now.UTC(), ErrorClass: next.ErrorClass, Via: next.Via, Host: next.TargetHost})
	if len(next.Recent) > maxRecentOccurrences {
		next.Recent = next.Recent[len(next.Recent)-maxRecentOccurrences:]
	}
	return next
}

// RecordDiagnosticFailure creates or updates the open episode of one target.
func (s *Service) RecordDiagnosticFailure(ctx context.Context, req observabilityport.DiagnosticFailureRequest) error {
	if _, err := prepareFailure(req); err != nil {
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
	prepared, err := prepareFailure(req)
	if err != nil {
		return err
	}
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&now); err != nil {
		return fmt.Errorf("read diagnostic clock: %w", err)
	}
	kind, target := string(req.Condition.Kind), req.TargetID
	// An episode closed by a concurrent success between the conflict and the
	// lock simply starts the next attempt as a fresh insert.
	for attempt := 0; attempt < 3; attempt++ {
		fresh := mergeFailure(nil, prepared, now)
		inserted, err := insertEpisode(ctx, tx, kind, target, fresh, now)
		if err != nil {
			return fmt.Errorf("record diagnostic failure: %w", err)
		}
		if inserted {
			return nil
		}
		current, id, found, err := lockOpenEpisode(ctx, tx, kind, target)
		if err != nil {
			return fmt.Errorf("record diagnostic failure: %w", err)
		}
		if !found {
			continue
		}
		merged := mergeFailure(&current, prepared, now)
		if err := updateEpisode(ctx, tx, id, merged, now); err != nil {
			return fmt.Errorf("record diagnostic failure: %w", err)
		}
		return nil
	}
	return errors.New("record diagnostic failure: episode kept changing")
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func episodeArgs(st episodeState) ([]any, error) {
	recent, err := json.Marshal(st.Recent)
	if err != nil {
		return nil, err
	}
	affected := st.AffectedIDs
	if affected == nil {
		affected = []string{}
	}
	return []any{
		nullString(st.Variant), st.Severity, nullString(st.TargetName), nullString(st.TargetHost),
		nullString(st.ErrorClass), nullInt(st.HTTPStatus), nullString(st.Via), pq.Array(affected), recent,
		nullString(st.LastRequestID), st.Occurrences,
	}, nil
}

func insertEpisode(ctx context.Context, tx *sql.Tx, kind, target string, st episodeState, now time.Time) (bool, error) {
	args, err := episodeArgs(st)
	if err != nil {
		return false, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `
INSERT INTO diagnostic_episodes(
    id, kind, target_id, first_failed_at, last_failed_at, retention_until,
    variant, severity, target_name, target_host, error_class, http_status, via,
    affected_ids, recent, last_request_id, occurrence_count)
VALUES ($1, $2, $3, $4, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
ON CONFLICT (kind, target_id) WHERE succeeded_after_at IS NULL DO NOTHING
RETURNING id`,
		append([]any{uuid.NewString(), kind, target, now, now.Add(diagnosticRetention)}, args...)...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func lockOpenEpisode(ctx context.Context, tx *sql.Tx, kind, target string) (episodeState, string, bool, error) {
	var (
		st                                         episodeState
		id                                         string
		variant, name, host, class, via, requestID sql.NullString
		status                                     sql.NullInt64
		affected                                   pq.StringArray
		recent                                     []byte
	)
	err := tx.QueryRowContext(ctx, `
SELECT id, variant, severity, target_name, target_host, error_class, http_status, via,
       affected_ids, recent, last_request_id, occurrence_count
FROM diagnostic_episodes
WHERE kind = $1 AND target_id = $2 AND succeeded_after_at IS NULL
FOR UPDATE`, kind, target).Scan(&id, &variant, &st.Severity, &name, &host, &class, &status, &via,
		&affected, &recent, &requestID, &st.Occurrences)
	if errors.Is(err, sql.ErrNoRows) {
		return st, "", false, nil
	}
	if err != nil {
		return st, "", false, err
	}
	st.Variant, st.TargetName, st.TargetHost = variant.String, name.String, host.String
	st.ErrorClass, st.Via, st.LastRequestID = class.String, via.String, requestID.String
	st.HTTPStatus = int(status.Int64)
	st.AffectedIDs = []string(affected)
	if err := json.Unmarshal(recent, &st.Recent); err != nil {
		return st, "", false, fmt.Errorf("decode recent occurrences: %w", err)
	}
	return st, id, true, nil
}

func updateEpisode(ctx context.Context, tx *sql.Tx, id string, st episodeState, now time.Time) error {
	args, err := episodeArgs(st)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE diagnostic_episodes SET
    last_failed_at = $2, retention_until = $3,
    variant = $4, severity = $5, target_name = $6, target_host = $7, error_class = $8,
    http_status = $9, via = $10, affected_ids = $11, recent = $12, last_request_id = $13,
    occurrence_count = $14
WHERE id = $1`, append([]any{id, now, now.Add(diagnosticRetention)}, args...)...)
	return err
}

// RecordDiagnosticSuccess closes the open episode of a target, if present.
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
WHERE kind = $1 AND target_id = $2 AND succeeded_after_at IS NULL`,
		string(req.Kind), req.TargetID)
	if err != nil {
		return fmt.Errorf("record diagnostic success: %w", err)
	}
	return nil
}

func validateDiagnosticSuccess(req observabilityport.DiagnosticSuccessRequest) error {
	known := false
	for _, spec := range observabilityport.ConditionCatalog() {
		if spec.Kind == req.Kind {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("diagnostic kind %q is not allowlisted", req.Kind)
	}
	return validateIdentifierOrBounded("diagnostic target id", req.TargetID, maxIdentityLength)
}
