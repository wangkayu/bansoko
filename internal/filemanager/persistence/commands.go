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
// instead of racing it.
type Commands struct {
	db        *sql.DB
	workItems *workitems.Store
	activity  observabilityport.ActivityWriter
	hub       *events.Hub
	now       func() time.Time
}

// NewCommands wires the write side.
func NewCommands(db *sql.DB, workItems *workitems.Store, activity observabilityport.ActivityWriter, hub *events.Hub) (*Commands, error) {
	if db == nil || workItems == nil {
		return nil, errors.New("file manager commands need a database and the work item store")
	}
	return &Commands{db: db, workItems: workItems, activity: activity, hub: hub, now: time.Now}, nil
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
	if len(paths) == 0 {
		return false, nil
	}
	var held bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM (`+inProgressOwners+`) o WHERE o.path = ANY($1::text[]))`, paths).Scan(&held)
	return held, err
}

// managedPaths reports whether an active placement or an active companion of
// an active placement names any of the paths.
func managedPaths(ctx context.Context, tx *sql.Tx, paths []string) (bool, error) {
	if len(paths) == 0 {
		return false, nil
	}
	var managed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM placements WHERE state = 'active' AND physical_path COLLATE "C" = ANY($1::text[]))
 OR EXISTS (SELECT 1 FROM placement_companions c JOIN placements p ON p.id = c.placement_id
   WHERE c.state = 'active' AND p.state = 'active' AND c.current_path = ANY($1::text[]))`, paths).Scan(&managed)
	return managed, err
}

// AdmitOperation re-checks and admits one user file operation.
func (c *Commands) AdmitOperation(ctx context.Context, admission filemanager.OperationAdmission) (filemanager.UserOperation, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return filemanager.UserOperation{}, err
	}
	defer tx.Rollback()
	op, replay, err := c.admitTx(ctx, tx, admission)
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

func (c *Commands) admitTx(ctx context.Context, tx *sql.Tx, a filemanager.OperationAdmission) (filemanager.UserOperation, *filemanager.UserOperation, error) {
	// Serialize admissions of the same paths so two concurrent requests
	// cannot both pass the held check.
	for _, path := range append(append([]string{}, a.SourcePath), a.TargetPath) {
		if path == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('file_manager:' || $1, 0))`, path); err != nil {
			return filemanager.UserOperation{}, nil, err
		}
	}
	held, err := heldPaths(ctx, tx, append(append([]string{}, a.SourceSpellings...), a.TargetSpellings...))
	if err != nil {
		return filemanager.UserOperation{}, nil, fmt.Errorf("check unfinished owners: %w", err)
	}
	if held {
		return filemanager.UserOperation{}, nil, filemanager.NewConflict(filemanager.CodeEntryChanged, "another operation holds the file or its target")
	}
	if a.RequireDetachedSource {
		managed, err := managedPaths(ctx, tx, a.SourceSpellings)
		if err != nil {
			return filemanager.UserOperation{}, nil, fmt.Errorf("check source owners: %w", err)
		}
		if managed {
			return filemanager.UserOperation{}, nil, filemanager.NewConflict(filemanager.CodeEntryChanged, "file is managed")
		}
	}
	if a.TargetPath != "" && a.Collision == filemanager.ConflictBlock {
		managed, err := managedPaths(ctx, tx, a.TargetSpellings)
		if err != nil {
			return filemanager.UserOperation{}, nil, fmt.Errorf("check target owners: %w", err)
		}
		if managed {
			return filemanager.UserOperation{}, nil, filemanager.NewConflict(filemanager.CodeTargetExists, "target is occupied")
		}
	}
	nullable := func(value string) any {
		if value == "" {
			return nil
		}
		return value
	}
	var matchAnime, matchSeason, matchEpisode any
	if a.Match != nil {
		if err := checkMatchAnimeTx(ctx, tx, a.Match.ManagedAnimeID); err != nil {
			return filemanager.UserOperation{}, nil, err
		}
		matchAnime, matchSeason, matchEpisode = a.Match.ManagedAnimeID, a.Match.Season, a.Match.Episode
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO file_manager_operations
 (id, idempotency_key, request_digest, actor, operation, source_path, target_path, expected_size_bytes,
  collision, release_placement_id, match_anime_id, match_season, match_episode)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (idempotency_key) DO NOTHING`,
		a.ID, a.IdempotencyKey, a.Digest, a.Actor, string(a.Operation), a.SourcePath, nullable(a.TargetPath),
		a.ExpectedSize, string(a.Collision), nullable(a.ReleasePlacementID),
		matchAnime, matchSeason, matchEpisode)
	if err != nil {
		return filemanager.UserOperation{}, nil, fmt.Errorf("admit user operation: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return filemanager.UserOperation{}, nil, err
	} else if affected == 0 {
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
	snapshot, _ := json.Marshal(map[string]any{"file_manager_operation_id": a.ID, "operation": a.Operation})
	if _, err := c.workItems.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileManagerOperation, ID: a.ID},
		Kind:                 workitems.KindFileManagerExecute,
		IdempotencyKey:       "fm:execute:" + a.ID,
		InputSnapshot:        snapshot,
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
	}); err != nil {
		return filemanager.UserOperation{}, nil, fmt.Errorf("enqueue user operation: %w", err)
	}
	op, err := scanOperation(tx.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM file_manager_operations WHERE id = $1`, a.ID))
	if err != nil {
		return filemanager.UserOperation{}, nil, fmt.Errorf("read admitted user operation: %w", err)
	}
	return *op, nil, nil
}

// matchAnimeTakesFilesTx reports whether an anime still takes matched files:
// its lifecycle is not retired, its subscription is active or stopped, and it
// is no one-shot's temporary anime. lock holds the lifecycle row against a
// concurrent retirement until the transaction ends.
func matchAnimeTakesFilesTx(ctx context.Context, tx *sql.Tx, managedAnimeID string, lock bool) (retired, takes bool, err error) {
	query := `SELECT lifecycle_state FROM managed_anime WHERE id = $1`
	if lock {
		query += ` FOR SHARE`
	}
	var lifecycle string
	if err := tx.QueryRowContext(ctx, query, managedAnimeID).Scan(&lifecycle); errors.Is(err, sql.ErrNoRows) {
		return true, false, nil
	} else if err != nil {
		return false, false, fmt.Errorf("read anime lifecycle: %w", err)
	}
	if lifecycle != "active" {
		return true, false, nil
	}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM subscriptions WHERE managed_anime_id = $1 AND state IN ('active', 'stopped'))
 AND NOT EXISTS (SELECT 1 FROM one_shot_acquisitions WHERE managed_anime_id = $1)`, managedAnimeID).Scan(&takes)
	if err != nil {
		return false, false, fmt.Errorf("read anime subscription: %w", err)
	}
	return false, takes, nil
}

func checkMatchAnimeTx(ctx context.Context, tx *sql.Tx, managedAnimeID string) error {
	retired, takes, err := matchAnimeTakesFilesTx(ctx, tx, managedAnimeID, true)
	if err != nil {
		return err
	}
	if retired {
		return filemanager.NewConflict(filemanager.CodeAnimeRetired, "the anime no longer admits changes")
	}
	if !takes {
		return filemanager.NewConflict(filemanager.CodeMatchAnimeUnavailable, "the anime does not take matched files")
	}
	return nil
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
		op, replay, err := c.admitTx(ctx, tx, *a.Move)
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
		retired, takes, err := matchAnimeTakesFilesTx(ctx, tx, check.MatchAnimeID, false)
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
