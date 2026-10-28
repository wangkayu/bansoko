package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// Alignment routing: what one video unit does when its target already holds
// a file. Retention goes first, then the collision policy:
//
//  1. The occupant is a file of the same instance on the same coverage key
//     that meets the moving file in one retention pool (single_copy; per_group
//     within one group), or a same-instance acquisition still publishing
//     there, or a retention loser whose deletion is in flight: the move waits
//     for retention (retention_pending). The comparison is a retention
//     recheck: the loser is deleted by the existing retention path and the
//     keeper moves afterwards; the moving file itself losing ends the move.
//  2. Retention keeps both (all, per_group with different groups, a tie):
//     the collision policy decides — skip leaves the file where it is
//     (target_occupied), version takes a free "name (n)" target, overwrite
//     replaces the occupant as a publish would.
//  3. Any other occupant (another key, another instance, an unmanaged file):
//     the collision policy, unchanged.
//
// A same-instance occupant whose own alignment is about to move it elsewhere
// is waited for instead of collided with.

// Alignment reason codes the routing adds.
const (
	// ReasonRetentionPending: the move waits for the retention comparison of
	// a same-key file (or for its loser's deletion).
	ReasonRetentionPending = "retention_pending"
	// reasonTargetOccupantMoving: the occupant's own alignment moves it away
	// first (an in-progress wait, not surfaced).
	reasonTargetOccupantMoving = "target_occupant_moving"
)

// AlignmentRetention is the retention port the routing consults.
type AlignmentRetention interface {
	SameRetentionPool(ctx context.Context, animeID, left, right string) (AdmissionMode, bool, error)
	RecheckPlacements(ctx context.Context, animeID string, placementIDs []string) (RetentionContest, error)
}

// SetRetention wires the retention port (composition root). Unwired, a
// same-key occupant is routed by the collision policy alone.
func (s *AlignmentService) SetRetention(retention AlignmentRetention) {
	if s != nil {
		s.retention = retention
	}
}

type routeAction string

const (
	routeProceed   routeAction = "proceed"
	routeHold      routeAction = "hold"
	routeWait      routeAction = "wait"
	routeDrop      routeAction = "drop"
	routeSkip      routeAction = "skip"
	routeVersion   routeAction = "version"
	routeOverwrite routeAction = "overwrite"
)

// retentionOutcome is what the same-key recheck left.
type retentionOutcome string

const (
	retentionKeptBoth     retentionOutcome = "kept_both"
	retentionMoverLost    retentionOutcome = "mover_lost"
	retentionOccupantLost retentionOutcome = "occupant_lost"
	retentionUnsettled    retentionOutcome = "unsettled"
)

// targetOccupancy is what holds a unit's target.
type targetOccupancy struct {
	Occupied bool
	// SameInstance: the occupant is a placement of the moving file's
	// instance, or a publication of it still in flight (InFlight).
	SameInstance bool
	InFlight     bool
	// LoserPendingDeletion: the occupant lost retention and its deletion is
	// admitted but not done.
	LoserPendingDeletion bool
	// MovingAway: the occupant's own live alignment moves it elsewhere.
	MovingAway bool
	// SharesKey / SameGroup: the occupant covers one of the moving file's
	// keys, and both belong to one subgroup (a file without one counts as
	// every group's).
	SharesKey bool
	SameGroup bool
	// Retention is the recheck outcome of a comparable same-key occupant.
	Retention retentionOutcome
	// Name is the occupant's file name for the reason text.
	Name string
}

// alignmentRoute is one routing decision.
type alignmentRoute struct {
	Action routeAction
	Reason string
	// Settle: a hold that nothing in flight will resolve; the operation
	// ends blocked with the reason instead of retrying.
	Settle bool
}

// routeAlignment is the routing decision table (see the file comment).
func routeAlignment(mode AdmissionMode, policy domain.CollisionPolicy, occupancy targetOccupancy) alignmentRoute {
	if !occupancy.Occupied {
		return alignmentRoute{Action: routeProceed}
	}
	if occupancy.SameInstance {
		switch {
		case occupancy.LoserPendingDeletion:
			return alignmentRoute{Action: routeHold, Reason: ReasonRetentionPending}
		case occupancy.InFlight:
			if mode == ModeAll {
				// Nothing to compare and nothing there yet: the publication
				// applies its own collision policy when it lands.
				return alignmentRoute{Action: routeProceed}
			}
			return alignmentRoute{Action: routeHold, Reason: ReasonRetentionPending}
		case occupancy.MovingAway:
			return alignmentRoute{Action: routeWait, Reason: reasonTargetOccupantMoving}
		case occupancy.SharesKey && retentionComparable(mode, occupancy.SameGroup):
			switch occupancy.Retention {
			case retentionMoverLost:
				return alignmentRoute{Action: routeDrop, Reason: "placement_not_active"}
			case retentionOccupantLost:
				return alignmentRoute{Action: routeHold, Reason: ReasonRetentionPending}
			case retentionUnsettled, "":
				// Retention cannot decide now (the subscription is not
				// active): the move ends waiting, and a later scan routes
				// it again.
				return alignmentRoute{Action: routeHold, Reason: ReasonRetentionPending, Settle: true}
			}
		}
	}
	switch policy {
	case domain.CollisionVersion:
		return alignmentRoute{Action: routeVersion}
	case domain.CollisionOverwrite:
		return alignmentRoute{Action: routeOverwrite}
	}
	return alignmentRoute{Action: routeSkip, Reason: "target_occupied"}
}

// routeUnitTarget gathers the occupancy of target for the moving placement
// (with the retention recheck of a comparable same-key occupant) and routes
// it. claimant is a same-plan placement that claimed the same target earlier
// in this admission ("" for none); it counts as the occupant when the target
// is otherwise free.
func (s *AlignmentService) routeUnitTarget(ctx context.Context, animeID, moverID, sourcePath, fingerprint, target, claimant string, policy domain.CollisionPolicy) (alignmentRoute, error) {
	occupancy, mode, err := s.targetOccupancy(ctx, animeID, moverID, sourcePath, fingerprint, target, claimant)
	if err != nil {
		return alignmentRoute{}, err
	}
	return routeAlignment(mode, policy, occupancy), nil
}

// targetOccupancy reads what holds target. A comparable same-key occupant is
// put through retention here (RecheckPlacements) so the route reads its
// outcome; a failed recheck (a stopped subscription) leaves it unsettled.
func (s *AlignmentService) targetOccupancy(ctx context.Context, animeID, moverID, sourcePath, fingerprint, target, claimant string) (targetOccupancy, AdmissionMode, error) {
	mode, err := instanceAdmissionMode(ctx, s.db, animeID)
	if err != nil {
		return targetOccupancy{}, "", err
	}
	free, err := s.targetFree(ctx, target, fingerprint, sourcePath)
	if err != nil {
		return targetOccupancy{}, mode, err
	}
	occupantID := ""
	occupancy := targetOccupancy{Name: filepath.Base(target)}
	if free {
		if claimant != "" && claimant != moverID {
			occupantID = claimant
		} else {
			inFlight, err := sameInstancePublicationTo(ctx, s.db, animeID, target)
			if err != nil {
				return targetOccupancy{}, mode, err
			}
			if inFlight {
				occupancy.Occupied, occupancy.SameInstance, occupancy.InFlight = true, true, true
			}
			return occupancy, mode, nil
		}
	}
	occupancy.Occupied = true
	if occupantID == "" {
		var occupantAnime, state string
		err := s.db.QueryRowContext(ctx, `
SELECT id, managed_anime_id, state FROM placements
WHERE physical_path = $1 AND id <> $2 AND state IN ('active', 'superseded')
ORDER BY (state = 'active') DESC, updated_at DESC LIMIT 1`, target, moverID).Scan(&occupantID, &occupantAnime, &state)
		if errors.Is(err, sql.ErrNoRows) {
			return occupancy, mode, nil // unmanaged
		}
		if err != nil {
			return targetOccupancy{}, mode, err
		}
		if occupantAnime != animeID {
			return occupancy, mode, nil // another instance's file
		}
		occupancy.SameInstance = true
		if state == "superseded" {
			pending, err := retentionDeletionPending(ctx, s.db, occupantID)
			if err != nil {
				return targetOccupancy{}, mode, err
			}
			occupancy.LoserPendingDeletion = pending
			return occupancy, mode, nil
		}
		moving, err := occupantMovingAway(ctx, s.db, animeID, target, sourcePath)
		if err != nil {
			return targetOccupancy{}, mode, err
		}
		if moving {
			occupancy.MovingAway = true
			return occupancy, mode, nil
		}
	}
	occupancy.SameInstance = true
	if s.retention == nil || moverID == "" {
		return occupancy, mode, nil
	}
	poolMode, comparable, err := s.retention.SameRetentionPool(ctx, animeID, moverID, occupantID)
	if err != nil {
		occupancy.SharesKey, occupancy.SameGroup, occupancy.Retention = true, true, retentionUnsettled
		return occupancy, mode, nil
	}
	mode = poolMode
	if !comparable {
		// Another key, or a pool the moving file is not in: policy only.
		return occupancy, mode, nil
	}
	occupancy.SharesKey, occupancy.SameGroup = true, true
	contest, err := s.retention.RecheckPlacements(ctx, animeID, []string{moverID, occupantID})
	if err != nil {
		occupancy.Retention = retentionUnsettled
		return occupancy, mode, nil
	}
	occupancy.Retention = retentionKeptBoth
	for _, id := range contest.Superseded {
		switch id {
		case moverID:
			occupancy.Retention = retentionMoverLost
		case occupantID:
			if occupancy.Retention != retentionMoverLost {
				occupancy.Retention = retentionOccupantLost
			}
		}
	}
	return occupancy, mode, nil
}

// instanceAdmissionMode is the instance replacement mode (all when none is
// recorded: nothing is compared).
func instanceAdmissionMode(ctx context.Context, db *sql.DB, animeID string) (AdmissionMode, error) {
	var mode string
	err := db.QueryRowContext(ctx, `SELECT admission_mode FROM anime_business_configs WHERE managed_anime_id = $1 AND replacement_version IS NOT NULL`, animeID).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return ModeAll, nil
	}
	return AdmissionMode(mode), err
}

// sameInstancePublicationTo reports a publication of the instance still in
// flight to path (its file is not there yet).
func sameInstancePublicationTo(ctx context.Context, db *sql.DB, animeID, path string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM file_operations fo
    JOIN artifacts art ON art.id = fo.artifact_id
    JOIN download_tasks dt ON dt.id = art.download_task_id
    JOIN releases rel ON rel.id = dt.release_id
    JOIN rss_sources src ON src.id = rel.rss_source_id
    JOIN subscriptions sub ON sub.id = src.subscription_id
    WHERE fo.target_path = $2 AND fo.state NOT IN ('completed', 'failed')
      AND sub.managed_anime_id = $1)`, animeID, path).Scan(&exists)
	return exists, err
}

// retentionDeletionPending reports a retention deletion of the placement that
// has not finished.
func retentionDeletionPending(ctx context.Context, db *sql.DB, placementID string) (bool, error) {
	var pending bool
	err := db.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM replacement_deletions WHERE placement_id = $1 AND state = 'admitted')`, placementID).Scan(&pending)
	return pending, err
}

// occupantMovingAway reports a live alignment that moves the file at path
// elsewhere — but not onto the moving file's own path (a swap would wait on
// itself; the policy decides it instead).
func occupantMovingAway(ctx context.Context, db *sql.DB, animeID, path, moverPath string) (bool, error) {
	var moving bool
	err := db.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM alignment_operations
    WHERE managed_anime_id = $1 AND source_path = $2 AND companion_id IS NULL
      AND state IN ('planned', 'ready', 'running')
      AND target_path <> $2 AND target_path <> $3)`, animeID, path, moverPath).Scan(&moving)
	return moving, err
}

// activePlacementAt is the instance's active placement at path ("" none) and
// the state of the most recent placement there.
func activePlacementAt(ctx context.Context, db *sql.DB, animeID, path string) (string, string, error) {
	var id, state string
	err := db.QueryRowContext(ctx, `
SELECT id, state FROM placements WHERE managed_anime_id = $1 AND physical_path = $2
ORDER BY (state = 'active') DESC, updated_at DESC LIMIT 1`, animeID, path).Scan(&id, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if state != "active" {
		return "", state, nil
	}
	return id, state, nil
}

// alignmentPendingWriter records why a ready operation is waiting, without
// changing its state or version; the next successful step clears it.
func alignmentPendingWriter(op *AlignmentOperation, code, reason string) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("alignment pending owner transaction is nil")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alignment_operations SET failure_code=$2, reason=$3, updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND state IN ('ready','running')`, op.ID, code, reason); err != nil {
			return 0, err
		}
		return op.Version, nil
	}
}

// routedPrecheck is the execution-time routing of a video unit's target: the
// occupancy is read again (the scan is older than the move) and the route
// decides. nil continues to the unit's companion targets.
func (s *AlignmentService) routedPrecheck(ctx context.Context, op *AlignmentOperation, sourceFingerprint string, policy domain.CollisionPolicy) (*worker.Outcome, error) {
	moverID, state, err := activePlacementAt(ctx, s.db, op.ManagedAnimeID, op.SourcePath)
	if err != nil {
		return nil, err
	}
	if moverID == "" && state == "superseded" {
		// Retention superseded the file after admission: its deletion owns
		// the path now.
		outcome := s.companionSettle(op, AlignmentBlocked, "placement_not_active", "the file lost the retention comparison")
		return &outcome, nil
	}
	route, err := s.routeUnitTarget(ctx, op.ManagedAnimeID, moverID, op.SourcePath, sourceFingerprint, op.TargetPath, "", policy)
	if err != nil {
		return nil, err
	}
	switch route.Action {
	case routeHold:
		reason := "waiting for retention to compare the file at the target: " + filepath.Base(op.TargetPath)
		if route.Settle {
			outcome := s.companionSettle(op, AlignmentBlocked, ReasonRetentionPending, reason)
			return &outcome, nil
		}
		outcome := worker.RetryUntilResolved(ReasonRetentionPending, reason, 15*time.Second, alignmentPendingWriter(op, ReasonRetentionPending, reason))
		return &outcome, nil
	case routeWait:
		outcome := worker.RetryUntilResolved(reasonTargetOccupantMoving, "waiting for the file at the target to move away", 15*time.Second, nil)
		return &outcome, nil
	case routeDrop:
		outcome := s.companionSettle(op, AlignmentBlocked, "placement_not_active", "the file lost the retention comparison")
		return &outcome, nil
	case routeSkip, routeVersion:
		// A version number was chosen at admission; a file that landed on
		// it since is skipped like any occupant.
		reason := safeReason(fmt.Errorf("occupied: %s", filepath.Base(op.TargetPath)))
		outcome := s.companionSettle(op, AlignmentBlocked, "target_occupied", reason)
		return &outcome, nil
	}
	return nil, nil
}
