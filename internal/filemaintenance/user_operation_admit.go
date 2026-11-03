package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"bansoko/internal/workitems"
)

// UserOperationAdmission is everything the intake re-checks and freezes when
// it admits one user file operation in the caller's transaction. Spellings
// are the spellings owner rows may use for a path. The file manager cannot be
// imported here (it imports this package), so the admission mirrors its
// request vocabulary; the file manager's persistence adapter translates.
type UserOperationAdmission struct {
	ID              string
	IdempotencyKey  string
	Digest          string
	Actor           string
	Operation       string
	SourcePath      string
	SourceSpellings []string
	TargetPath      string
	TargetSpellings []string
	ExpectedSize    int64
	// Collision is the file manager's block/version/overwrite choice, frozen
	// verbatim on the row. The version choice resolves a fresh target name at
	// execution, so admission never checks the requested target's owners for
	// it.
	Collision string
	// RequireDetachedSource: move, rename and delete act only on a file no
	// owner manages; copy reads any file.
	RequireDetachedSource bool
	// ReleasePlacementID is the placement the same request released.
	ReleasePlacementID string
	// Match is the chosen anime, season and episode of a match operation.
	Match *UserOperationMatch
}

// UserOperationMatch is the frozen anime choice of a match operation.
type UserOperationMatch struct {
	ManagedAnimeID string
	Season         int
	Episode        int
}

// collisionVersion is the file manager's add-a-suffix conflict choice.
const collisionVersion = "version"

// AdmissionRefusalReason names why the intake refused an operation; the file
// manager's persistence adapter maps each reason to its typed conflict.
type AdmissionRefusalReason string

const (
	// AdmissionHeld: another unfinished owner holds the source or the target.
	AdmissionHeld AdmissionRefusalReason = "held"
	// AdmissionSourceManaged: the operation needs a detached file and the
	// source is managed.
	AdmissionSourceManaged AdmissionRefusalReason = "source_managed"
	// AdmissionTargetOccupied: a managed file sits at the target.
	AdmissionTargetOccupied AdmissionRefusalReason = "target_occupied"
	// AdmissionAnimeRetired: the match's anime no longer admits changes.
	AdmissionAnimeRetired AdmissionRefusalReason = "anime_retired"
	// AdmissionAnimeUnavailable: the match's anime does not take files.
	AdmissionAnimeUnavailable AdmissionRefusalReason = "anime_unavailable"
)

// AdmissionRefusal is a typed admission refusal.
type AdmissionRefusal struct {
	Reason  AdmissionRefusalReason
	Message string
}

func (e *AdmissionRefusal) Error() string { return e.Message }

// UserOperationIntake owns every write that creates a file_manager_operations
// row: the admission re-checks, the owner row and its file_manager_execute
// work item, all in the caller's transaction. Execution writes the same rows
// afterwards, so the table's whole write side lives in this package; the file
// manager's persistence keeps the read model.
type UserOperationIntake struct {
	workItems *workitems.Store
}

// NewUserOperationIntake wires the intake.
func NewUserOperationIntake(workItems *workitems.Store) (*UserOperationIntake, error) {
	if workItems == nil {
		return nil, errors.New("user operation intake needs the work item store")
	}
	return &UserOperationIntake{workItems: workItems}, nil
}

// LockOperationPathsTx serializes admissions of the same paths so two
// concurrent requests cannot both pass the held check.
func LockOperationPathsTx(ctx context.Context, tx *sql.Tx, paths ...string) error {
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('file_manager:' || $1, 0))`, path); err != nil {
			return err
		}
	}
	return nil
}

// LockOperationBatchTx locks a batch's paths in one sorted pass first, so
// concurrent batches cannot deadlock on each other.
func LockOperationBatchTx(ctx context.Context, tx *sql.Tx, admissions []UserOperationAdmission) error {
	lockSet := map[string]bool{}
	var lockPaths []string
	for _, admission := range admissions {
		for _, path := range []string{admission.SourcePath, admission.TargetPath} {
			if path == "" || lockSet[path] {
				continue
			}
			lockSet[path] = true
			lockPaths = append(lockPaths, path)
		}
	}
	sort.Strings(lockPaths)
	return LockOperationPathsTx(ctx, tx, lockPaths...)
}

// AdmitTx re-checks the admission against the live owner facts and freezes
// it: the owner row and its execute work item land in the caller's
// transaction. admitted is false when the idempotency key was taken
// concurrently; the caller then reads the existing row to replay it or to
// refuse a digest mismatch.
func (i *UserOperationIntake) AdmitTx(ctx context.Context, tx *sql.Tx, a UserOperationAdmission) (bool, error) {
	if err := LockOperationPathsTx(ctx, tx, a.SourcePath, a.TargetPath); err != nil {
		return false, err
	}
	held, err := OwnerPathsHeld(ctx, tx, append(append([]string{}, a.SourceSpellings...), a.TargetSpellings...))
	if err != nil {
		return false, fmt.Errorf("check unfinished owners: %w", err)
	}
	if held {
		return false, &AdmissionRefusal{Reason: AdmissionHeld, Message: "another operation holds the file or its target"}
	}
	if a.RequireDetachedSource {
		managed, err := ManagedPaths(ctx, tx, a.SourceSpellings)
		if err != nil {
			return false, fmt.Errorf("check source owners: %w", err)
		}
		if managed {
			return false, &AdmissionRefusal{Reason: AdmissionSourceManaged, Message: "file is managed"}
		}
	}
	// A managed target is never written over: block refuses any occupant, and
	// overwrite refuses one that is managed or held (the held check above).
	if a.TargetPath != "" && a.Collision != collisionVersion {
		managed, err := ManagedPaths(ctx, tx, a.TargetSpellings)
		if err != nil {
			return false, fmt.Errorf("check target owners: %w", err)
		}
		if managed {
			return false, &AdmissionRefusal{Reason: AdmissionTargetOccupied, Message: "target is occupied"}
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
		retired, takes, err := MatchAnimeTakesFilesTx(ctx, tx, a.Match.ManagedAnimeID, true)
		if err != nil {
			return false, fmt.Errorf("check match anime: %w", err)
		}
		if retired {
			return false, &AdmissionRefusal{Reason: AdmissionAnimeRetired, Message: "the anime no longer admits changes"}
		}
		if !takes {
			return false, &AdmissionRefusal{Reason: AdmissionAnimeUnavailable, Message: "the anime does not take matched files"}
		}
		matchAnime, matchSeason, matchEpisode = a.Match.ManagedAnimeID, a.Match.Season, a.Match.Episode
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO file_manager_operations
 (id, idempotency_key, request_digest, actor, operation, source_path, target_path, expected_size_bytes,
  collision, release_placement_id, match_anime_id, match_season, match_episode)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (idempotency_key) DO NOTHING`,
		a.ID, a.IdempotencyKey, a.Digest, a.Actor, a.Operation, a.SourcePath, nullable(a.TargetPath),
		a.ExpectedSize, a.Collision, nullable(a.ReleasePlacementID),
		matchAnime, matchSeason, matchEpisode)
	if err != nil {
		return false, fmt.Errorf("admit user operation: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return false, err
	} else if affected == 0 {
		// A concurrent request with the same key won.
		return false, nil
	}
	snapshot, _ := json.Marshal(map[string]any{"file_manager_operation_id": a.ID, "operation": a.Operation})
	if _, err := i.workItems.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileManagerOperation, ID: a.ID},
		Kind:                 workitems.KindFileManagerExecute,
		IdempotencyKey:       "fm:execute:" + a.ID,
		InputSnapshot:        snapshot,
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
	}); err != nil {
		return false, fmt.Errorf("enqueue user operation: %w", err)
	}
	return true, nil
}
