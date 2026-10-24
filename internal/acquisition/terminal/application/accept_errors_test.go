package application

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestAcceptanceErrorMatrix(t *testing.T) {
	for _, tc := range []struct {
		name         string
		err          error
		status       int
		code, reason string
	}{
		{"existing constructor", existingUnsupported(), 422, "INPUT_SCHEMA_UNSUPPORTED", "placement_existing_unsupported"},
		{"SQL unsupported proof", &pgconn.PgError{Code: "23514", Message: "ACQ_QUALIFICATION_PROOF_UNSUPPORTED"}, 500, "INTERNAL_INVARIANT", "qualification_constructor_missed_unsupported_proof"},
		{"key reused", &pgconn.PgError{Code: "23514", Message: "IDEMPOTENCY_KEY_REUSED"}, 409, "IDEMPOTENCY_KEY_REUSED", "idempotency_key_reused"},
		{"expired", &pgconn.PgError{Code: "23514", Message: "ACQ_QUALIFICATION_FEED_EXPIRED"}, 409, "SEED_REFRESH_REQUIRED", "seed_refresh_required"},
		{"feed changed", &pgconn.PgError{Code: "23514", Message: "ACQ_QUALIFICATION_FEED_CHANGED"}, 409, "SEED_REFRESH_REQUIRED", "seed_refresh_required"},
		{"capacity", &pgconn.PgError{Code: "23514", Message: "ACQ_WITNESS_LIMIT"}, 422, "INPUT_TOO_LARGE", "witness_limit"},
		{"unregistered SQL", &pgconn.PgError{Code: "23514", Message: "secret internal error"}, 500, "INTERNAL_INVARIANT", "acceptance_database_invariant"},
		{"connection", &pgconn.PgError{Code: "08006", Message: "secret connection"}, 503, "ACCEPTANCE_UNKNOWN", "acceptance_unknown"},
		{"cancellation", context.Canceled, 503, "ACCEPTANCE_UNKNOWN", "acceptance_unknown"},
		{"deadline", context.DeadlineExceeded, 503, "ACCEPTANCE_UNKNOWN", "acceptance_unknown"},
		{"missing config", materialError("config", nil), 409, "INPUT_STALE", "capture_material_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got *AcceptanceError
			if !errors.As(mapAcceptanceError(tc.err), &got) || got.Status != tc.status || got.Code != tc.code || got.Reason != tc.reason {
				t.Fatalf("mapping: %#v", got)
			}
			if got.Error() != tc.code+": "+tc.reason {
				t.Fatal("unsafe error projection")
			}
		})
	}
	for _, message := range []string{"ACQ_QUALIFICATION_MANAGEMENT_CHANGED", "ACQ_QUALIFICATION_CONFIG_CHANGED", "ACQ_QUALIFICATION_SOURCE_CHANGED", "ACQ_QUALIFICATION_OBSERVATION_CHANGED", "ACQ_QUALIFICATION_PROGRAM_CHANGED"} {
		t.Run(message, func(t *testing.T) {
			var got *AcceptanceError
			if !errors.As(mapAcceptanceError(&pgconn.PgError{Code: "23514", Message: message}), &got) || got.Status != 409 || got.Code != "INPUT_STALE" {
				t.Fatal(got)
			}
		})
	}
}
