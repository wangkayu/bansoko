package application

import (
	"context"
	"errors"
	"strings"

	protocol "bansoko/internal/acquisition/terminal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

// AcceptanceError contains only registered public classifications, never SQL
// messages, private locators or an assertion that an unknown commit rolled back.
type AcceptanceError struct {
	Status       int
	Code, Reason string
	cause        error
}

func (e *AcceptanceError) Error() string { return e.Code + ": " + e.Reason }
func (e *AcceptanceError) Unwrap() error { return e.cause }
func acceptError(status int, code, reason string, cause error) error {
	return &AcceptanceError{status, code, reason, cause}
}
func existingUnsupported() error {
	return acceptError(422, "INPUT_SCHEMA_UNSUPPORTED", "placement_existing_unsupported", nil)
}
func mapAcceptanceError(err error) error {
	if err == nil {
		return nil
	}
	var typed *AcceptanceError
	if errors.As(err, &typed) {
		return typed
	}
	var registry *protocol.Error
	if errors.As(err, &registry) {
		if registry.Code == protocol.CodeInputSchemaUnsupported {
			return acceptError(422, "INPUT_SCHEMA_UNSUPPORTED", "input_schema_unsupported", err)
		}
		return acceptError(400, "INPUT_INVALID", "input_invalid", err)
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Message {
		case "ACQ_QUALIFICATION_PROOF_UNSUPPORTED":
			return acceptError(500, "INTERNAL_INVARIANT", "qualification_constructor_missed_unsupported_proof", err)
		case "IDEMPOTENCY_KEY_REUSED":
			return acceptError(409, "IDEMPOTENCY_KEY_REUSED", "idempotency_key_reused", err)
		case "ACQ_QUALIFICATION_FEED_EXPIRED", "ACQ_QUALIFICATION_FEED_CHANGED":
			return acceptError(409, "SEED_REFRESH_REQUIRED", "seed_refresh_required", err)
		case "ACQ_QUALIFICATION_PARSER_CHANGED", "ACQ_QUALIFICATION_METADATA_CHANGED", "ACQ_QUALIFICATION_MANAGEMENT_CHANGED", "ACQ_QUALIFICATION_CONFIG_CHANGED", "ACQ_QUALIFICATION_SOURCE_CHANGED", "ACQ_QUALIFICATION_OBSERVATION_CHANGED", "ACQ_QUALIFICATION_PROGRAM_CHANGED":
			return acceptError(409, "INPUT_STALE", "input_stale", err)
		case "ACQ_WITNESS_LIMIT":
			return acceptError(422, "INPUT_TOO_LARGE", "witness_limit", err)
		}
		if strings.HasPrefix(pg.Code, "08") {
			return acceptError(503, "ACCEPTANCE_UNKNOWN", "acceptance_unknown", err)
		}
		return acceptError(500, "INTERNAL_INVARIANT", "acceptance_database_invariant", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return acceptError(503, "ACCEPTANCE_UNKNOWN", "acceptance_unknown", err)
	}
	var material *qualificationMaterialError
	if errors.As(err, &material) {
		if material.group == "database" {
			return acceptError(503, "ACCEPTANCE_UNKNOWN", "capture_unavailable", err)
		}
		return acceptError(409, "INPUT_STALE", "capture_material_unavailable", err)
	}
	if errors.Is(err, errWitnessLimit) {
		return acceptError(422, "INPUT_TOO_LARGE", "witness_limit", err)
	}
	return acceptError(500, "INTERNAL_INVARIANT", "acceptance_internal_invariant", err)
}
