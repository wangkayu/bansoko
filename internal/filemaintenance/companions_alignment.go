package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/filemaintenance/planner"
	"bansoko/internal/library/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// CompanionCollisionSource reads the effective collision setting (the
// instance override over the global value, the same resolution acquisition
// uses) as the executor policy: block, version or overwrite. Alignment applies
// it to the unit of a video and its companions.
type CompanionCollisionSource interface {
	EffectiveCollisionPolicy(ctx context.Context, managedAnimeID string) (string, error)
}

// SetCompanionCollisionSource wires the preset collision mode used for
// alignment units. Unwired, a unit collision blocks.
func (s *CalibrationService) SetCompanionCollisionSource(source CompanionCollisionSource) {
	if s != nil {
		s.companionCollision = source
	}
}

// companionAlignmentContract is the frozen target contract of a companion
// alignment operation. Collision is the unit's mode; Suffix is set when a
// companion-only move takes a version number, which then becomes part of the
// companion's suffix.
type companionAlignmentContract struct {
	TargetPath  string  `json:"target_path"`
	Fingerprint string  `json:"fingerprint"`
	CompanionID string  `json:"companion_id"`
	VideoTarget string  `json:"video_target"`
	Collision   string  `json:"collision,omitempty"`
	Suffix      *string `json:"suffix,omitempty"`
}

// videoAlignmentContract is the frozen target contract of a video alignment
// operation: the video target and the targets of the companions that move with
// it. The video moves only when every target in the unit is free.
type videoAlignmentContract struct {
	TargetPath  string                `json:"target_path"`
	Fingerprint string                `json:"fingerprint"`
	Collision   string                `json:"collision,omitempty"`
	Companions  []unitCompanionTarget `json:"companions,omitempty"`
}

type unitCompanionTarget struct {
	TargetPath  string `json:"target_path"`
	Fingerprint string `json:"fingerprint"`
}

// unitCompanion is one present companion that follows its video. baseSuffix
// is its suffix without a version number it took on its own: its target
// derives from the video's name plus that suffix and takes a version number
// only if that target collides.
type unitCompanion struct {
	fact        observedCompanionFact
	fingerprint string
	baseSuffix  string
}

func (c unitCompanion) target(videoTarget string) string {
	return CompanionPath(videoTarget, c.baseSuffix, c.fact.Extension)
}

type observedCompanionFact struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Suffix    string `json:"suffix"`
	Extension string `json:"extension"`
	State     string `json:"state"`
}

// generationCompanions reads each observation's companion facts keyed by the
// planner's observation ref.
func (s *CalibrationService) generationCompanions(ctx context.Context, animeID string, generation int64) (map[string][]observedCompanionFact, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT file_path, facts->'companions'
FROM reconciliation_files
WHERE managed_anime_id = $1 AND generation = $2 AND NOT derived_missing AND jsonb_typeof(facts->'companions') = 'array'`, animeID, generation)
	if err != nil {
		return nil, fmt.Errorf("read observed companions: %w", err)
	}
	defer rows.Close()
	result := map[string][]observedCompanionFact{}
	for rows.Next() {
		var filePath string
		var raw []byte
		if err := rows.Scan(&filePath, &raw); err != nil {
			return nil, err
		}
		var companions []observedCompanionFact
		if err := json.Unmarshal(raw, &companions); err != nil {
			continue
		}
		result[fmt.Sprintf("%s:%d:%s", animeID, generation, filePath)] = companions
	}
	return result, rows.Err()
}

// unitCollisionPolicy reads the effective collision setting for alignment:
// block, version or overwrite. An unreadable setting blocks.
func (s *CalibrationService) unitCollisionPolicy(ctx context.Context, animeID string) domain.CollisionPolicy {
	if s.companionCollision == nil {
		return domain.CollisionBlock
	}
	policy, err := s.companionCollision.EffectiveCollisionPolicy(ctx, animeID)
	if err != nil {
		return domain.CollisionBlock
	}
	switch policy {
	case string(domain.CollisionVersion), string(domain.CollisionOverwrite):
		return domain.CollisionPolicy(policy)
	default:
		return domain.CollisionBlock
	}
}

// frozenAlignmentCollision is the executor policy of one frozen operation:
// overwrite when the unit's mode is overwrite, otherwise block (a version
// number was already chosen at admission).
func frozenAlignmentCollision(op *AlignmentOperation) domain.CollisionPolicy {
	var contract struct {
		Collision string `json:"collision"`
	}
	if json.Unmarshal(op.TargetContract, &contract) == nil && contract.Collision == string(domain.CollisionOverwrite) {
		return domain.CollisionOverwrite
	}
	return domain.CollisionBlock
}

// versionedCompanionSuffix is the suffix of a companion that takes version
// number n on its own: "chs" -> "chs (2)", "" -> "(2)". The name keeps the
// "<video basename>." prefix, so the companion stays bound to its video;
// with a suffix it matches the executor's "name (n).ext" format.
func versionedCompanionSuffix(suffix string, n int) string {
	if suffix == "" {
		return "(" + strconv.Itoa(n) + ")"
	}
	return suffix + " (" + strconv.Itoa(n) + ")"
}

// unitCompanions reads the present companions of one video that are still
// active at their observed path, with their frozen fingerprints.
func (s *CalibrationService) unitCompanions(ctx context.Context, observed []observedCompanionFact) ([]unitCompanion, error) {
	var result []unitCompanion
	for _, fact := range observed {
		if fact.State != "present" || fact.ID == "" || fact.Extension == "" {
			continue
		}
		var fingerprint, baseSuffix string
		err := s.db.QueryRowContext(ctx, `SELECT fingerprint, COALESCE(versioned_from_suffix, suffix) FROM placement_companions WHERE id = $1 AND state = 'active' AND current_path = $2`, fact.ID, fact.Path).Scan(&fingerprint, &baseSuffix)
		if errors.Is(err, sql.ErrNoRows) {
			continue // the companion moved or ended since the scan
		}
		if err != nil {
			return nil, err
		}
		result = append(result, unitCompanion{fact: fact, fingerprint: fingerprint, baseSuffix: baseSuffix})
	}
	return result, nil
}

// chooseUnitTarget picks the video target of an alignment unit. Under fail and
// overwrite it is the rendered target; the execution precheck decides the unit.
// Under version it is the first of target, "target (2)", "target (3)", ...
// where the video target and every companion target derived from it are free
// (or already hold the file that moves there). A candidate equal to the
// video's current path means the video is already aligned under version.
func (s *CalibrationService) chooseUnitTarget(ctx context.Context, policy domain.CollisionPolicy, sourcePath, sourceFingerprint, rendered string, companions []unitCompanion) (string, error) {
	if policy != domain.CollisionVersion || s.alignment == nil || s.alignment.observer == nil {
		return rendered, nil
	}
	for index := 0; index < 1000; index++ {
		candidate := rendered
		if index > 0 {
			candidate = alignmentVersionedPath(rendered, index+1)
		}
		if samePath(candidate, sourcePath) {
			return candidate, nil
		}
		free, err := s.alignment.targetFree(ctx, candidate, sourceFingerprint, "")
		if err != nil {
			return "", err
		}
		for _, companion := range companions {
			if !free {
				break
			}
			free, err = s.alignment.targetFree(ctx, companion.target(candidate), companion.fingerprint, companion.fact.Path)
			if err != nil {
				return "", err
			}
		}
		if free {
			return candidate, nil
		}
	}
	return rendered, nil
}

// alignmentVersionedPath mirrors the file executor's version suffix.
func alignmentVersionedPath(path string, version int) string {
	extension := filepath.Ext(path)
	return strings.TrimSuffix(path, extension) + " (" + strconv.Itoa(version) + ")" + extension
}

// admitCompanionAlignments admits one alignment operation per present
// companion whose path differs from the path derived from the video's target
// (the video moves) or current path (the video is already aligned). Each
// companion operation waits for its video (companion_video_pending) and moves
// only after the video's placement sits at videoTarget.
func (s *CalibrationService) admitCompanionAlignments(ctx context.Context, base AlignmentOperation, candidate planner.PlanCandidate, videoTarget string, companions []unitCompanion, policy domain.CollisionPolicy, companionOnly bool) (int, error) {
	admitted := 0
	for _, companion := range companions {
		fact, fingerprint := companion.fact, companion.fingerprint
		target := companion.target(videoTarget)
		if samePath(target, fact.Path) {
			continue
		}
		var versionedSuffix *string
		if companionOnly && policy == domain.CollisionVersion && s.alignment != nil && s.alignment.observer != nil {
			// The video is in place: an occupied companion target takes the
			// first free version number of its own.
			free, err := s.alignment.targetFree(ctx, target, fingerprint, fact.Path)
			if err != nil {
				return admitted, err
			}
			for n := 2; !free && n <= 1000; n++ {
				suffix := versionedCompanionSuffix(companion.baseSuffix, n)
				candidateTarget := CompanionPath(videoTarget, suffix, fact.Extension)
				if free, err = s.alignment.targetFree(ctx, candidateTarget, fingerprint, fact.Path); err != nil {
					return admitted, err
				}
				if free {
					target, versionedSuffix = candidateTarget, &suffix
				}
			}
			if samePath(target, fact.Path) {
				continue
			}
		}
		idempotencyKey := "align:" + candidate.ObservationRef + ":companion:" + fact.ID
		op := base
		op.ID = alignmentOperationID(idempotencyKey)
		op.IdempotencyKey = idempotencyKey
		op.CompanionID = fact.ID
		op.SourcePath = fact.Path
		op.SourceFingerprint = fingerprint
		op.TargetPath = target
		op.TargetContract, _ = json.Marshal(companionAlignmentContract{
			TargetPath: target, Fingerprint: fingerprint, CompanionID: fact.ID, VideoTarget: videoTarget,
			Collision: string(policy), Suffix: versionedSuffix,
		})
		op.DryRunResult, _ = json.Marshal(map[string]any{"companion_id": fact.ID, "source_path": fact.Path, "target_path": target, "video_target": videoTarget})
		created, err := s.alignment.CreateOperation(ctx, op)
		if err != nil {
			return admitted, err
		}
		if created.State == AlignmentReady || created.State == AlignmentRunning {
			if err := s.ensureAlignmentExecuteWorkItem(ctx, created.ID, created.Version); err != nil {
				return admitted, err
			}
			continue
		}
		if created.State != AlignmentPlanned {
			continue
		}
		version, err := s.alignment.MarkReady(ctx, created.ID, created.Version)
		if err != nil {
			return admitted, err
		}
		if err := s.ensureAlignmentExecuteWorkItem(ctx, created.ID, version); err != nil {
			return admitted, err
		}
		admitted++
	}
	return admitted, nil
}

// companionPrecheck runs before a companion operation's effect. It returns a
// non-nil outcome when the operation must wait for its video or settle
// without an effect: the companion changed or the video did not reach its
// target (companion_video_not_aligned). It also returns the collision policy
// of the move: overwrite when the unit's mode is overwrite, otherwise block.
func (s *AlignmentService) companionPrecheck(ctx context.Context, op *AlignmentOperation) (*worker.Outcome, domain.CollisionPolicy, error) {
	var contract companionAlignmentContract
	if err := json.Unmarshal(op.TargetContract, &contract); err != nil || contract.VideoTarget == "" {
		outcome := s.companionSettle(op, AlignmentFailed, "alignment_contract_invalid", "companion alignment contract is invalid")
		return &outcome, "", nil
	}
	var placementID, currentPath, state, placementPath string
	err := s.db.QueryRowContext(ctx, `
SELECT c.placement_id, c.current_path, c.state, p.physical_path
FROM placement_companions c JOIN placements p ON p.id = c.placement_id
WHERE c.id = $1`, op.CompanionID).Scan(&placementID, &currentPath, &state, &placementPath)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (state != CompanionStateActive || currentPath != op.SourcePath)) {
		outcome := s.companionSettle(op, AlignmentCanceled, "companion_changed", "the companion moved or ended after the scan")
		return &outcome, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if !samePath(placementPath, contract.VideoTarget) {
		var videoState string
		err := s.db.QueryRowContext(ctx, `SELECT state FROM alignment_operations WHERE idempotency_key = $1`, "align:"+op.ObservationRef).Scan(&videoState)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, "", err
		}
		switch AlignmentState(videoState) {
		case AlignmentPlanned, AlignmentReady, AlignmentRunning:
			outcome := worker.RetryUntilResolved("companion_video_pending", "waiting for the video to be aligned", 15*time.Second, nil)
			return &outcome, "", nil
		}
		outcome := s.companionSettle(op, AlignmentBlocked, "companion_video_not_aligned", "the video was not aligned to its target")
		return &outcome, "", nil
	}
	// Overwrite replaces any occupant (a managed one's relation or
	// placement ends when the move commits); a version number was chosen at
	// admission, so block and version never touch an occupied target.
	return nil, frozenAlignmentCollision(op), nil
}

func (s *AlignmentService) companionSettle(op *AlignmentOperation, state AlignmentState, code, reason string) worker.Outcome {
	itemState := workitems.StateFailed
	ownerState := string(state)
	writer := alignmentSettleWriter(op, op.Version, state, code, reason, "")
	return worker.Finish(itemState, workitems.OwnerResult{State: ownerState, ReasonCode: code, Reason: reason}, writer)
}

// targetFree reports whether path holds no file, or already holds the file
// that moves there (fingerprint) or is that file's current path (self).
func (s *AlignmentService) targetFree(ctx context.Context, path, fingerprint, self string) (bool, error) {
	if self != "" && samePath(path, self) {
		return true, nil
	}
	observed, err := executorpkg.ObserveLike(ctx, s.observer, path, fingerprint)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, nil // not a readable regular file: occupied
	}
	return observed == "" || (fingerprint != "" && offsetFingerprintsEqual(observed, fingerprint)), nil
}

// videoUnitPrecheck runs before a video alignment's effect: every target in
// the unit (the video's and the companions' frozen in its contract) must be
// free, unless the unit's mode is overwrite, which replaces any occupant — a
// managed one's placement is displaced, or its companion relation ends, when
// the move commits. Under block and version any occupant (target_occupied /
// companion_target_occupied) settles the video blocked and nothing in the
// unit moves; the reason names the files.
func (s *AlignmentService) videoUnitPrecheck(ctx context.Context, op *AlignmentOperation, sourceFingerprint string, collision domain.CollisionPolicy) (*worker.Outcome, error) {
	if collision == domain.CollisionOverwrite {
		return nil, nil
	}
	var contract videoAlignmentContract
	_ = json.Unmarshal(op.TargetContract, &contract)
	type unitTarget struct {
		path, fingerprint string
		companion         bool
	}
	targets := []unitTarget{{path: op.TargetPath, fingerprint: sourceFingerprint}}
	for _, companion := range contract.Companions {
		targets = append(targets, unitTarget{path: companion.TargetPath, fingerprint: companion.Fingerprint, companion: true})
	}
	code := ""
	var named []string
	for _, target := range targets {
		self := ""
		if !target.companion {
			self = op.SourcePath
		}
		free, err := s.targetFree(ctx, target.path, target.fingerprint, self)
		if err != nil {
			return nil, err
		}
		if free {
			continue
		}
		next := map[bool]string{false: "target_occupied", true: "companion_target_occupied"}[target.companion]
		if code == "" {
			code = next
		}
		named = append(named, filepath.Base(target.path))
	}
	if len(named) == 0 {
		return nil, nil
	}
	reason := safeReason(errors.New(strings.TrimPrefix(code, "companion_") + ": " + strings.Join(named, ", ")))
	outcome := s.companionSettle(op, AlignmentBlocked, code, reason)
	return &outcome, nil
}

// overwriteCheckpoint persists each exchange stage on the operation before
// the executor continues; persisted reports that a record exists.
func (s *AlignmentService) overwriteCheckpoint(operationID string, persisted *bool) domain.FileOperationCheckpoint {
	return func(ctx context.Context, current domain.FileOperation, recovery domain.OverwriteRecovery) (domain.FileOperation, error) {
		raw, err := json.Marshal(recovery)
		if err != nil {
			return current, err
		}
		result, err := s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE alignment_operations SET overwrite_recovery=$2,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND state='running'`, operationID, raw)
		if err != nil {
			return current, err
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			return current, fmt.Errorf("alignment operation %s is not running; exchange record not persisted", operationID)
		}
		if persisted != nil {
			*persisted = true
		}
		return current, nil
	}
}

// resumeOverwriteOperation rebuilds the executor operation of an interrupted
// overwrite from the persisted exchange record.
func resumeOverwriteOperation(op *AlignmentOperation) (domain.FileOperation, bool) {
	if len(op.OverwriteRecovery) == 0 || frozenAlignmentCollision(op) != domain.CollisionOverwrite {
		return domain.FileOperation{}, false
	}
	var recovery domain.OverwriteRecovery
	if json.Unmarshal(op.OverwriteRecovery, &recovery) != nil {
		return domain.FileOperation{}, false
	}
	fingerprint, err := offsetSourceFingerprintValue(op.SourceFingerprint)
	if err != nil {
		return domain.FileOperation{}, false
	}
	return domain.FileOperation{
		ID: op.ID, Operation: domain.OperationMove, SourcePath: op.SourcePath, TargetPath: op.TargetPath,
		SourceFingerprint: fingerprint, CollisionPolicy: domain.CollisionOverwrite, OverwriteRecovery: &recovery,
	}, true
}

func alignmentErrorCode(err error, code string) bool {
	var coder executorpkg.ErrorCoder
	return errors.As(err, &coder) && coder.ErrorCode() == code
}
