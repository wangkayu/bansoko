package application

import (
	"context"
	"errors"
	"strings"

	"bansoko/internal/acquisition/target"
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
		if IsFrozenSizeViolation(err) {
			return torrentTooLarge(err)
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

// TorrentTooLargeCode is the typed refusal of an admission whose frozen record
// would exceed a stored bound. Every frozen carrier is bounded independently
// of the target count, but the release-level file inventory (release_files
// and the naming vars input) is still frozen once per carrier, so a torrent
// that lists enough files is refused here instead of failing as an internal
// error.
const TorrentTooLargeCode = "TORRENT_TOO_LARGE"

// MaxFrozenTaskBlobBytes is the stored bound of download_tasks.frozen_input,
// download_tasks.frozen_policy and work_items.input_snapshot
// (pg_column_size <= 1048576).
const MaxFrozenTaskBlobBytes = 1 << 20

// frozenBlobRawLimit is the raw JSON bound checked before anything is
// written. The column checks measure the jsonb datum, which for these
// documents is a few percent larger than their compact text, so the raw
// bound keeps one eighth of headroom: a document refused by the database is
// always refused here first, and the transaction stays usable for the
// caller's own typed outcome.
const frozenBlobRawLimit = MaxFrozenTaskBlobBytes - MaxFrozenTaskBlobBytes/8

// frozenSizeConstraints are the stored size bounds an admission can reach.
var frozenSizeConstraints = map[string]bool{
	"releases_internal_facts_check":                    true,
	"releases_raw_facts_check":                         true,
	"releases_source_facts_check":                      true,
	"releases_parsed_facts_check":                      true,
	"acquisition_targets_render_snapshot_check":        true,
	"acquisition_targets_config_snapshot_check":        true,
	"acquisitions_canonical_request_check":             true,
	"acquisitions_input_canonical_bytes_check":         true,
	"download_tasks_frozen_input_check":                true,
	"download_tasks_frozen_policy_check":               true,
	"work_items_input_snapshot_check":                  true,
	"one_shot_acquisitions_input_snapshot_check":       true,
	"one_shot_execution_attempts_input_snapshot_check": true,
	"release_torrent_inputs_payload_check":             true,
	"media_fact_snapshots_release_facts_check":         true,
}

// IsFrozenSizeViolation reports a database refusal of a frozen document over
// one of its stored size bounds.
func IsFrozenSizeViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23514" && frozenSizeConstraints[pg.ConstraintName]
}

// IsTorrentTooLarge reports the typed size refusal, whether this package
// classified it, the release's media fact snapshot refused its batch plan, or
// the database raised it directly.
func IsTorrentTooLarge(err error) bool {
	var typed *AcceptanceError
	if errors.As(err, &typed) {
		return typed.Code == TorrentTooLargeCode
	}
	return errors.Is(err, target.ErrReleaseMediaFactsTooLarge) || IsFrozenSizeViolation(err)
}

func torrentTooLarge(cause error) error {
	return acceptError(422, TorrentTooLargeCode, "torrent_too_large", cause)
}

// checkFrozenTaskSizes refuses a task input or policy over its stored bound
// before the acceptance writes anything.
func checkFrozenTaskSizes(input EntryTask) error {
	if len(input.FrozenInput) > frozenBlobRawLimit || len(input.FrozenPolicy) > frozenBlobRawLimit {
		return torrentTooLarge(nil)
	}
	return nil
}
