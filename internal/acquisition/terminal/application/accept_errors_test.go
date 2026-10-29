package application

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"bansoko/internal/acquisition/target"
	"github.com/jackc/pgx/v5/pgconn"
)

// A task input or policy over the raw pre-check bound is refused with the
// typed size refusal before anything is written; one within it passes.
func TestCheckFrozenTaskSizesRefusesOverTheRawBound(t *testing.T) {
	within := EntryTask{FrozenInput: make([]byte, frozenBlobRawLimit), FrozenPolicy: make([]byte, frozenBlobRawLimit)}
	if err := checkFrozenTaskSizes(within); err != nil {
		t.Fatalf("a task at the raw bound was refused: %v", err)
	}
	for name, task := range map[string]EntryTask{
		"input":  {FrozenInput: make([]byte, frozenBlobRawLimit+1)},
		"policy": {FrozenPolicy: make([]byte, frozenBlobRawLimit+1)},
	} {
		err := checkFrozenTaskSizes(task)
		var got *AcceptanceError
		if !errors.As(err, &got) || got.Status != 422 || got.Code != TorrentTooLargeCode || !IsTorrentTooLarge(err) {
			t.Fatalf("%s over the raw bound: %v", name, err)
		}
	}
	if frozenBlobRawLimit >= MaxFrozenTaskBlobBytes {
		t.Fatal("the raw pre-check bound leaves no headroom below the stored bound")
	}
	if !IsTorrentTooLarge(fmt.Errorf("freeze release media facts: %w", target.ErrReleaseMediaFactsTooLarge)) {
		t.Fatal("a batch plan over the media fact snapshot's bound is not the typed size refusal")
	}
}

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
		{"frozen input over its bound", &pgconn.PgError{Code: "23514", ConstraintName: "download_tasks_frozen_input_check"}, 422, TorrentTooLargeCode, "torrent_too_large"},
		{"one-shot snapshot over its bound", &pgconn.PgError{Code: "23514", ConstraintName: "one_shot_acquisitions_input_snapshot_check"}, 422, TorrentTooLargeCode, "torrent_too_large"},
		{"release facts over their bound", &pgconn.PgError{Code: "23514", ConstraintName: "releases_internal_facts_check"}, 422, TorrentTooLargeCode, "torrent_too_large"},
		{"media facts over their bound", &pgconn.PgError{Code: "23514", ConstraintName: "media_fact_snapshots_release_facts_check"}, 422, TorrentTooLargeCode, "torrent_too_large"},
		{"other column check", &pgconn.PgError{Code: "23514", ConstraintName: "download_tasks_state_check"}, 500, "INTERNAL_INVARIANT", "acceptance_database_invariant"},
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
