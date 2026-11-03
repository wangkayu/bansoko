package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"bansoko/internal/events"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/filemanager"
	"bansoko/internal/observability/businesslog"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// Commands is the transactional write side of the file manager.
// Every write re-checks, inside its own transaction, the owner facts the
// service admitted it on, so a concurrent owner change refuses the write
// instead of racing it. The admission write itself (owner row plus its
// execute work item) lives in filemaintenance, which owns the write side of
// file_manager_operations; this type translates the file manager's vocabulary
// and keeps the read model.
type Commands struct {
	db       *sql.DB
	intake   *filemaintenance.UserOperationIntake
	activity observabilityport.ActivityWriter
	hub      *events.Hub
	now      func() time.Time
}

// NewCommands wires the write side.
func NewCommands(db *sql.DB, workItems *workitems.Store, activity observabilityport.ActivityWriter, hub *events.Hub) (*Commands, error) {
	if db == nil || workItems == nil {
		return nil, errors.New("file manager commands need a database and the work item store")
	}
	intake, err := filemaintenance.NewUserOperationIntake(workItems)
	if err != nil {
		return nil, err
	}
	return &Commands{db: db, intake: intake, activity: activity, hub: hub, now: time.Now}, nil
}

var _ filemanager.CommandStore = (*Commands)(nil)

const operationColumns = `id, operation, state, source_path, COALESCE(target_path, ''), COALESCE(result_path, ''),
 COALESCE(failure_code, ''), created_at, completed_at, COALESCE(match_anime_id, ''), COALESCE(match_season, 0),
 COALESCE(match_episode, 0), created_dirs`

type rowScanner interface{ Scan(...any) error }

func scanOperation(row rowScanner, extra ...any) (*filemanager.UserOperation, error) {
	var op filemanager.UserOperation
	var kind, state, matchAnime string
	var matchSeason, matchEpisode int
	var completed sql.NullTime
	var createdDirs []byte
	targets := append([]any{&op.ID, &kind, &state, &op.SourcePath, &op.TargetPath, &op.ResultPath, &op.Failure,
		&op.CreatedAt, &completed, &matchAnime, &matchSeason, &matchEpisode, &createdDirs}, extra...)
	if err := row.Scan(targets...); err != nil {
		return nil, err
	}
	if matchAnime != "" {
		op.Match = &filemanager.MatchInfo{ManagedAnimeID: matchAnime, Season: matchSeason, Episode: matchEpisode}
	}
	if len(createdDirs) > 0 {
		_ = json.Unmarshal(createdDirs, &op.CreatedDirs)
	}
	op.Operation, op.State = filemanager.UserOperationKind(kind), filemanager.UserOperationState(state)
	if completed.Valid {
		at := completed.Time
		op.CompletedAt = &at
	}
	return &op, nil
}

// LatestUserOperation returns the newest user file operation whose source,
// requested target or result is one of the paths.
func (s *Store) LatestUserOperation(ctx context.Context, paths []string) (*filemanager.UserOperation, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	op, err := scanOperation(s.db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM file_manager_operations
WHERE source_path = ANY($1::text[]) OR target_path = ANY($1::text[]) OR result_path = ANY($1::text[])
ORDER BY created_at DESC, id DESC LIMIT 1`, paths))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read latest user operation: %w", err)
	}
	return op, nil
}

// OperationByKey returns the operation admitted under an idempotency key.
func (c *Commands) OperationByKey(ctx context.Context, key string) (*filemanager.UserOperation, string, error) {
	var digest string
	op, err := scanOperation(c.db.QueryRowContext(ctx, `SELECT `+operationColumns+`, request_digest
FROM file_manager_operations WHERE idempotency_key = $1`, key), &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read user operation by key: %w", err)
	}
	return op, digest, nil
}

// Operation reads one user file operation.
func (c *Commands) Operation(ctx context.Context, id string) (*filemanager.UserOperation, error) {
	op, err := scanOperation(c.db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM file_manager_operations WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read user operation: %w", err)
	}
	return op, nil
}

// heldPaths reports whether an unfinished owner holds any of the paths.
func heldPaths(ctx context.Context, tx *sql.Tx, paths []string) (bool, error) {
	return filemaintenance.OwnerPathsHeld(ctx, tx, paths)
}

// managedPaths reports whether an active placement or an active companion of
// an active placement names any of the paths.
func managedPaths(ctx context.Context, tx *sql.Tx, paths []string) (bool, error) {
	return filemaintenance.ManagedPaths(ctx, tx, paths)
}

// AdmitOperation re-checks and admits one user file operation.
func (c *Commands) AdmitOperation(ctx context.Context, admission filemanager.OperationAdmission) (filemanager.UserOperation, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return filemanager.UserOperation{}, err
	}
	defer tx.Rollback()
	op, replay, err := c.admitTx(ctx, tx, admission, toIntakeAdmission(admission))
	if err != nil {
		return filemanager.UserOperation{}, err
	}
	if replay != nil {
		return *replay, nil
	}
	if err := tx.Commit(); err != nil {
		return filemanager.UserOperation{}, err
	}
	c.hub.Publish(events.Event{Target: events.TargetRecords})
	return op, nil
}

// AdmitOperations admits a batch of user file operations in one transaction:
// a refusal anywhere admits nothing. The batch's paths are locked in one
// sorted pass first, so concurrent batches cannot deadlock on each other.
func (c *Commands) AdmitOperations(ctx context.Context, admissions []filemanager.OperationAdmission) ([]filemanager.UserOperation, error) {
	if len(admissions) == 0 {
		return nil, nil
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	intakeAdmissions := make([]filemaintenance.UserOperationAdmission, 0, len(admissions))
	for _, admission := range admissions {
		intakeAdmissions = append(intakeAdmissions, toIntakeAdmission(admission))
	}
	if err := filemaintenance.LockOperationBatchTx(ctx, tx, intakeAdmissions); err != nil {
		return nil, err
	}
	operations := make([]filemanager.UserOperation, 0, len(admissions))
	for i, admission := range admissions {
		op, replay, err := c.admitTx(ctx, tx, admission, intakeAdmissions[i])
		if err != nil {
			return nil, err
		}
		if replay != nil {
			operations = append(operations, *replay)
			continue
		}
		operations = append(operations, op)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	c.hub.Publish(events.Event{Target: events.TargetRecords})
	return operations, nil
}

// OperationBatch returns the operations admitted under a batch key and their
// request digest. The batch's per-file keys are the batch key plus ":" plus a
// digest, so both forms are read.
func (c *Commands) OperationBatch(ctx context.Context, key string) ([]filemanager.UserOperation, string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT `+operationColumns+`, request_digest FROM file_manager_operations
WHERE idempotency_key = $1 OR starts_with(idempotency_key, $1 || ':')
ORDER BY created_at, id`, key)
	if err != nil {
		return nil, "", fmt.Errorf("read user operation batch: %w", err)
	}
	defer rows.Close()
	var operations []filemanager.UserOperation
	digest := ""
	for rows.Next() {
		var stored string
		op, err := scanOperation(rows, &stored)
		if err != nil {
			return nil, "", fmt.Errorf("read user operation batch: %w", err)
		}
		if stored != "" {
			digest = stored
		}
		operations = append(operations, *op)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("read user operation batch: %w", err)
	}
	return operations, digest, nil
}

// toIntakeAdmission translates the file manager's admission to the intake's
// vocabulary.
func toIntakeAdmission(a filemanager.OperationAdmission) filemaintenance.UserOperationAdmission {
	admission := filemaintenance.UserOperationAdmission{
		ID: a.ID, IdempotencyKey: a.IdempotencyKey, Digest: a.Digest, Actor: a.Actor,
		Operation: string(a.Operation), SourcePath: a.SourcePath, SourceSpellings: a.SourceSpellings,
		TargetPath: a.TargetPath, TargetSpellings: a.TargetSpellings, ExpectedSize: a.ExpectedSize,
		Collision: string(a.Collision), RequireDetachedSource: a.RequireDetachedSource,
		ReleasePlacementID: a.ReleasePlacementID,
	}
	if a.Match != nil {
		admission.Match = &filemaintenance.UserOperationMatch{
			ManagedAnimeID: a.Match.ManagedAnimeID, Season: a.Match.Season, Episode: a.Match.Episode,
		}
	}
	return admission
}

// mapAdmissionError maps the intake's typed refusals to the file manager's
// typed conflicts.
func mapAdmissionError(err error) error {
	var refusal *filemaintenance.AdmissionRefusal
	if !errors.As(err, &refusal) {
		return err
	}
	switch refusal.Reason {
	case filemaintenance.AdmissionHeld, filemaintenance.AdmissionSourceManaged:
		return filemanager.NewConflict(filemanager.CodeEntryChanged, refusal.Message)
	case filemaintenance.AdmissionTargetOccupied:
		return filemanager.NewConflict(filemanager.CodeTargetExists, refusal.Message)
	case filemaintenance.AdmissionAnimeRetired:
		return filemanager.NewConflict(filemanager.CodeAnimeRetired, refusal.Message)
	case filemaintenance.AdmissionAnimeUnavailable:
		return filemanager.NewConflict(filemanager.CodeMatchAnimeUnavailable, refusal.Message)
	}
	return err
}

func (c *Commands) admitTx(ctx context.Context, tx *sql.Tx, a filemanager.OperationAdmission, ia filemaintenance.UserOperationAdmission) (filemanager.UserOperation, *filemanager.UserOperation, error) {
	admitted, err := c.intake.AdmitTx(ctx, tx, ia)
	if err != nil {
		return filemanager.UserOperation{}, nil, mapAdmissionError(err)
	}
	if !admitted {
		// A concurrent request with the same key won.
		existing, digest, err := c.OperationByKey(ctx, a.IdempotencyKey)
		if err != nil {
			return filemanager.UserOperation{}, nil, err
		}
		if existing == nil || digest != a.Digest {
			return filemanager.UserOperation{}, nil, filemanager.NewConflict(filemanager.CodeIdempotencyConflict, "idempotency key was used for another request")
		}
		return filemanager.UserOperation{}, existing, nil
	}
	op, err := scanOperation(tx.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM file_manager_operations WHERE id = $1`, a.ID))
	if err != nil {
		return filemanager.UserOperation{}, nil, fmt.Errorf("read admitted user operation: %w", err)
	}
	return *op, nil, nil
}

// ReleasedByKey returns a release already performed under a key.
func (c *Commands) ReleasedByKey(ctx context.Context, key string) (*filemanager.ReleaseOutcome, []string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT managed_anime_id, physical_path FROM placements WHERE release_request_key = $1 ORDER BY id`, key)
	if err != nil {
		return nil, nil, fmt.Errorf("read release by key: %w", err)
	}
	var outcome *filemanager.ReleaseOutcome
	var paths []string
	for rows.Next() {
		var anime, path string
		if err := rows.Scan(&anime, &path); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if outcome == nil {
			outcome = &filemanager.ReleaseOutcome{ManagedAnimeID: anime}
		}
		outcome.Released++
		paths = append(paths, path)
	}
	if err := closeRows(rows); err != nil {
		return nil, nil, err
	}
	if outcome == nil {
		return nil, nil, nil
	}
	op, _, err := c.OperationByKey(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	outcome.Operation = op
	return outcome, paths, nil
}

// Release ends the management of one placement and its companion relations in
// one transaction, with the activity entry and an optional move admission.
func (c *Commands) Release(ctx context.Context, a filemanager.ReleaseAdmission) (filemanager.ReleaseOutcome, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return filemanager.ReleaseOutcome{}, err
	}
	defer tx.Rollback()
	outcome, err := c.releaseTx(ctx, tx, a)
	if err != nil {
		return filemanager.ReleaseOutcome{}, mapFenceError(err)
	}
	if err := tx.Commit(); err != nil {
		return filemanager.ReleaseOutcome{}, mapFenceError(err)
	}
	c.hub.Publish(events.Event{Target: events.TargetRecords, ManagedAnimeID: a.ManagedAnimeID})
	c.hub.Publish(events.Event{Target: events.TargetRSS, ManagedAnimeID: a.ManagedAnimeID})
	return outcome, nil
}

func (c *Commands) releaseTx(ctx context.Context, tx *sql.Tx, a filemanager.ReleaseAdmission) (filemanager.ReleaseOutcome, error) {
	var lifecycle string
	err := tx.QueryRowContext(ctx, `SELECT lifecycle_state FROM managed_anime WHERE id = $1 FOR SHARE`, a.ManagedAnimeID).Scan(&lifecycle)
	if errors.Is(err, sql.ErrNoRows) || err == nil && lifecycle != "active" {
		return filemanager.ReleaseOutcome{}, filemanager.NewConflict(filemanager.CodeAnimeRetired, "the anime no longer admits changes")
	}
	if err != nil {
		return filemanager.ReleaseOutcome{}, fmt.Errorf("lock anime lifecycle: %w", err)
	}
	var state string
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT state, version FROM placements WHERE id = $1 AND managed_anime_id = $2 FOR UPDATE`, a.Placement.ID, a.ManagedAnimeID).Scan(&state, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return filemanager.ReleaseOutcome{}, filemanager.NewConflict(filemanager.CodeEntryChanged, "entry changed since it was read")
	}
	if err != nil {
		return filemanager.ReleaseOutcome{}, fmt.Errorf("lock placement: %w", err)
	}
	if state != "active" || version != a.Placement.Version {
		return filemanager.ReleaseOutcome{}, filemanager.NewConflict(filemanager.CodeEntryChanged, "entry changed since it was read")
	}
	held, err := heldPaths(ctx, tx, a.Spellings)
	if err != nil {
		return filemanager.ReleaseOutcome{}, fmt.Errorf("check unfinished owners: %w", err)
	}
	if held {
		return filemanager.ReleaseOutcome{}, filemanager.NewConflict(filemanager.CodeEntryChanged, "another operation holds the file")
	}
	// Every active row at the path is released: the owner placement and the
	// reconciliation projections of the same file.
	rows, err := tx.QueryContext(ctx, `UPDATE placements
SET state = 'released', released_at = CURRENT_TIMESTAMP, released_by = $3, release_request_key = $4,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE managed_anime_id = $1 AND physical_path = ANY($2::text[]) AND state = 'active'
RETURNING id, version`, a.ManagedAnimeID, a.Spellings, a.Actor, a.IdempotencyKey)
	if err != nil {
		return filemanager.ReleaseOutcome{}, fmt.Errorf("release placement: %w", err)
	}
	var released []string
	var subjectVersion int64
	for rows.Next() {
		var id string
		var v int64
		if err := rows.Scan(&id, &v); err != nil {
			rows.Close()
			return filemanager.ReleaseOutcome{}, err
		}
		released = append(released, id)
		if id == a.Placement.ID {
			subjectVersion = v
		}
	}
	if err := closeRows(rows); err != nil {
		return filemanager.ReleaseOutcome{}, fmt.Errorf("release placement: %w", err)
	}
	if subjectVersion == 0 {
		return filemanager.ReleaseOutcome{}, filemanager.NewConflict(filemanager.CodeEntryChanged, "entry changed since it was read")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE placement_companions
SET state = 'ended', ended_reason = 'released', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE placement_id = ANY($1::text[]) AND state = 'active'`, released); err != nil {
		return filemanager.ReleaseOutcome{}, fmt.Errorf("end released companions: %w", err)
	}
	if err := c.appendReleaseActivity(ctx, tx, a, subjectVersion); err != nil {
		return filemanager.ReleaseOutcome{}, err
	}
	outcome := filemanager.ReleaseOutcome{ManagedAnimeID: a.ManagedAnimeID, Released: len(released)}
	if a.Move != nil {
		op, replay, err := c.admitTx(ctx, tx, *a.Move, toIntakeAdmission(*a.Move))
		if err != nil {
			return filemanager.ReleaseOutcome{}, err
		}
		if replay != nil {
			op = *replay
		}
		outcome.Operation = &op
	}
	return outcome, nil
}

func (c *Commands) appendReleaseActivity(ctx context.Context, tx *sql.Tx, a filemanager.ReleaseAdmission, version int64) error {
	if c.activity == nil {
		return nil
	}
	var subscriptionID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM subscriptions WHERE managed_anime_id = $1`, a.ManagedAnimeID).Scan(&subscriptionID)
	if errors.Is(err, sql.ErrNoRows) {
		// A temporary anime without a subscription has no activity stream.
		return nil
	}
	if err != nil {
		return fmt.Errorf("read release activity subscription: %w", err)
	}
	var details json.RawMessage
	if len(a.Spellings) > 0 {
		if details, err = businesslog.MarshalDetails(map[string]any{"file_name": businesslog.FileName(a.Spellings[0])}); err != nil {
			return fmt.Errorf("encode release activity details: %w", err)
		}
	}
	return c.activity.AppendTx(ctx, txscope.Wrap(tx), observabilityport.ActivityRecord{
		SubscriptionID:  subscriptionID,
		ManagedAnimeID:  a.ManagedAnimeID,
		Kind:            "placement_changed",
		Outcome:         "info",
		ReasonCode:      "placement_released",
		SubjectType:     "placement",
		SubjectID:       a.Placement.ID,
		SubjectVersion:  version,
		StableResultKey: "placement_released",
		MessageCode:     observabilityport.MessagePlacementReleased,
		Details:         details,
	})
}

// mapFenceError maps a lifecycle-fence violation raised by an owner trigger
// to the typed retired refusal.
func mapFenceError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" &&
		(strings.HasPrefix(pgErr.Message, "MANAGED_ANIME_") || pgErr.Message == "WORK_ITEM_OWNER_CHAIN_CHANGED") {
		return filemanager.NewConflict(filemanager.CodeAnimeRetired, "the anime no longer admits changes")
	}
	return err
}

// CheckUserOperationTx re-checks, in the caller's transaction, that a user
// operation may still take its effect: no other unfinished owner holds the
// source or the target, the source is not managed when the operation needs a
// detached file, and no owner names the target.
func CheckUserOperationTx(ctx context.Context, tx *sql.Tx, check filemaintenance.UserOperationCheck) (string, bool, error) {
	busy := func(paths []string) (bool, error) {
		if len(paths) == 0 {
			return false, nil
		}
		var held bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM (`+automaticOwners+`) o WHERE o.path = ANY($1::text[]))
 OR EXISTS (SELECT 1 FROM file_manager_operations u WHERE u.id <> $2 AND u.state IN ('ready', 'running')
   AND (u.source_path = ANY($1::text[]) OR u.target_path = ANY($1::text[]) OR u.result_path = ANY($1::text[])
     OR u.attempt->>'quarantine_path' = ANY($1::text[])))`, paths, check.OperationID).Scan(&held)
		return held, err
	}
	if held, err := busy(check.SourceSpellings); err != nil {
		return "", false, fmt.Errorf("check source owners: %w", err)
	} else if held {
		return filemanager.FailureSourceChanged, false, nil
	}
	if check.RequireDetached {
		managed, err := managedPaths(ctx, tx, check.SourceSpellings)
		if err != nil {
			return "", false, fmt.Errorf("check source owners: %w", err)
		}
		if managed {
			return filemanager.FailureSourceManaged, false, nil
		}
	}
	if check.MatchAnimeID != "" {
		retired, takes, err := filemaintenance.MatchAnimeTakesFilesTx(ctx, tx, check.MatchAnimeID, false)
		if err != nil {
			return "", false, err
		}
		if retired || !takes {
			return filemanager.FailureAnimeUnavailable, false, nil
		}
	}
	if held, err := busy(check.TargetSpellings); err != nil {
		return "", false, fmt.Errorf("check target owners: %w", err)
	} else if held {
		return "", true, nil
	}
	managed, err := managedPaths(ctx, tx, check.TargetSpellings)
	if err != nil {
		return "", false, fmt.Errorf("check target owners: %w", err)
	}
	return "", managed, nil
}

// UserOperationGuard adapts the file manager's path spelling and owner facts
// to the file_manager_execute handler.
type UserOperationGuard struct {
	Service *filemanager.Service
}

var _ filemaintenance.UserOperationGuard = UserOperationGuard{}

// Spellings returns the browsed absolute path and every spelling owner rows
// may use for it.
func (g UserOperationGuard) Spellings(ctx context.Context, absolute string) (string, []string, error) {
	return g.Service.PathSpellings(absolute)
}

// CheckTx runs CheckUserOperationTx.
func (g UserOperationGuard) CheckTx(ctx context.Context, tx *sql.Tx, check filemaintenance.UserOperationCheck) (string, bool, error) {
	return CheckUserOperationTx(ctx, tx, check)
}
