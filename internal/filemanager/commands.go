package filemanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// P1 command error codes . Each is stable and carries no
// private path.
const (
	CodeEntryChanged          = "FILE_MANAGER_ENTRY_CHANGED"
	CodeOperationNotAllowed   = "FILE_MANAGER_OPERATION_NOT_ALLOWED"
	CodeTargetExists          = "FILE_MANAGER_TARGET_EXISTS"
	CodeTargetInvalid         = "FILE_MANAGER_TARGET_INVALID"
	CodeIdempotencyConflict   = "FILE_MANAGER_IDEMPOTENCY_CONFLICT"
	CodeIdempotencyKeyInvalid = "FILE_MANAGER_IDEMPOTENCY_KEY_INVALID"
	CodeAnimeRetired          = "FILE_MANAGER_ANIME_RETIRED"
	CodeAlignmentDisabled     = "FILE_MANAGER_ALIGNMENT_DISABLED"
	CodeOperationNotFound     = "FILE_MANAGER_OPERATION_NOT_FOUND"
	CodeCommandsUnavailable   = "FILE_MANAGER_COMMANDS_UNAVAILABLE"
	CodeGroupSelected         = "FILE_MANAGER_GROUP_SELECTED"
)

// maxLockedPaths bounds the managed files a refused directory reports.
const maxLockedPaths = 50

// NewConflict builds a typed 409 command error (for the command store).
func NewConflict(code, message string) error { return conflict(code, message) }

// UserOperationKind is one user file operation on a file.
type UserOperationKind string

const (
	UserMove   UserOperationKind = "move"
	UserRename UserOperationKind = "rename"
	UserCopy   UserOperationKind = "copy"
	UserDelete UserOperationKind = "delete"
	// UserMatch moves a detached video to the path its anime's naming
	// template renders for a chosen episode; the file stays detached.
	UserMatch UserOperationKind = "match"
)

// ConflictChoice is what the user chose for an occupied target: cancel
// (block), add a suffix (version), or replace the existing file (overwrite).
// Overwrite never replaces a managed or in-progress file; renaming to another
// target is a new request.
type ConflictChoice string

const (
	ConflictBlock     ConflictChoice = "block"
	ConflictVersion   ConflictChoice = "version"
	ConflictOverwrite ConflictChoice = "overwrite"
)

// UserOperationState is the owner state of a user file operation.
type UserOperationState string

const (
	UserOperationReady     UserOperationState = "ready"
	UserOperationRunning   UserOperationState = "running"
	UserOperationCompleted UserOperationState = "completed"
	UserOperationBlocked   UserOperationState = "blocked"
	UserOperationFailed    UserOperationState = "failed"
)

// Terminal reports whether the state is final.
func (s UserOperationState) Terminal() bool {
	return s == UserOperationCompleted || s == UserOperationBlocked || s == UserOperationFailed
}

// Failure codes a user file operation settles with.
const (
	FailureTargetOccupied = "target_occupied"
	FailureSourceChanged  = "source_changed"
	FailureSourceMissing  = "source_missing"
	FailureSourceManaged  = "source_managed"
	FailureExecuteFailed  = "execute_failed"
	// FailureAnimeUnavailable: the anime a match targets no longer takes
	// files (retired, or its subscription is gone).
	FailureAnimeUnavailable = "anime_unavailable"
)

// UserOperation is one admitted user file operation. Paths are absolute and
// frozen at admission after the source and any target pass the authorized-root
// checks; later execution follows this frozen request rather than reauthorizing
// a different path.
type UserOperation struct {
	ID          string
	Operation   UserOperationKind
	State       UserOperationState
	SourcePath  string
	TargetPath  string
	ResultPath  string
	Failure     string
	CreatedAt   time.Time
	CompletedAt *time.Time
	// Match is the chosen anime, season and episode of a match operation.
	Match *MatchInfo
	// CreatedDirs are the absolute directories a settled match created.
	CreatedDirs []string
}

// MatchInfo is the frozen choice of a match operation.
type MatchInfo struct {
	ManagedAnimeID string
	Season         int
	Episode        int
}

// Target is where a move, rename or copy goes.
type Target struct {
	Path     string
	Conflict ConflictChoice
}

// OperationRequest is one confirmed user file operation. Path and Target.Path
// are absolute.
type OperationRequest struct {
	IdempotencyKey   string
	Actor            string
	Operation        UserOperationKind
	Path             string
	ExpectedRevision string
	Target           *Target
}

// DeletePlacementRequest deletes one managed video and its bound companions.
// Only a placement from a currently unselected provider group is eligible;
// the command store re-checks that stable identity in its transaction.
type DeletePlacementRequest struct {
	IdempotencyKey   string
	Actor            string
	PlacementID      string
	PlacementVersion int64
	Path             string
}

// ReleaseRequest releases one managed video, optionally moving it afterwards.
type ReleaseRequest struct {
	IdempotencyKey   string
	Actor            string
	Path             string
	ExpectedRevision string
	MoveTo           *Target
}

// ReleaseOutcome is the result of a release.
type ReleaseOutcome struct {
	ManagedAnimeID string
	Released       int
	Operation      *UserOperation
	// RequestDigest lets a composite command distinguish exact replay from a
	// different request that happens to name the same released path.
	RequestDigest string
}

// AlignRequest aligns one managed video through the existing chain.
type AlignRequest struct {
	IdempotencyKey   string
	Actor            string
	Path             string
	ExpectedRevision string
}

// AlignOutcome is the result of an align.
type AlignOutcome struct {
	ManagedAnimeID string
}

// OperationAdmission is everything the command store re-checks and freezes
// when it admits a user file operation in one transaction. Spellings are the
// spellings owner rows may use for a path.
type OperationAdmission struct {
	ID              string
	IdempotencyKey  string
	Digest          string
	Actor           string
	Operation       UserOperationKind
	SourcePath      string
	SourceSpellings []string
	TargetPath      string
	TargetSpellings []string
	ExpectedSize    int64
	Collision       ConflictChoice
	// RequireDetachedSource: move, rename and delete act only on a file no
	// owner manages; copy reads any file.
	RequireDetachedSource bool
	// ReleasePlacementID is the placement the same request released.
	ReleasePlacementID string
	// Match is set for a match operation.
	Match *MatchAdmission
}

// VersionRef is one owner row and the version a command saw.
type VersionRef struct {
	ID      string
	Version int64
}

// ReleaseCompanionDelete freezes one bound companion's ownership version and
// its safe-delete admission. The transaction compares the complete active set
// before ending any relation.
type ReleaseCompanionDelete struct {
	Companion VersionRef
	Path      string
	Operation OperationAdmission
}

// ReleaseDeleteAdmission is the one-action release-and-delete form. Video is
// also the operation returned to the caller; each companion has its own fenced
// execute item so the existing path-level identity checks still apply.
type ReleaseDeleteAdmission struct {
	Video      OperationAdmission
	Companions []ReleaseCompanionDelete
}

// ReleaseAdmission is what the command store re-checks and writes for a
// release in one transaction.
type ReleaseAdmission struct {
	IdempotencyKey string
	Actor          string
	ManagedAnimeID string
	AnimeTitle     string
	Placement      VersionRef
	Spellings      []string
	Move           *OperationAdmission
	Delete         *ReleaseDeleteAdmission
}

// AlignAdmission is one admitted align of a managed video.
type AlignAdmission struct {
	IdempotencyKey string
	ManagedAnimeID string
	PlacementID    string
	PlacementPath  string
}

// CommandStore persists the P1 writes.
type CommandStore interface {
	// OperationByKey returns the operation admitted under an idempotency key
	// and its request digest (nil when none).
	OperationByKey(ctx context.Context, key string) (*UserOperation, string, error)
	Operation(ctx context.Context, id string) (*UserOperation, error)
	AdmitOperation(ctx context.Context, admission OperationAdmission) (UserOperation, error)
	// AdmitOperations admits a batch of operations in one transaction: either
	// every operation is admitted or none is. A replayed batch returns the
	// operations already admitted under the same keys.
	AdmitOperations(ctx context.Context, admissions []OperationAdmission) ([]UserOperation, error)
	// OperationBatch returns the operations admitted under a batch key (the
	// key itself, or one of its "key:digest" per-file keys) and their request
	// digest (empty when none).
	OperationBatch(ctx context.Context, key string) ([]UserOperation, string, error)
	// ReleasedByKey returns a release already performed under a key, with the
	// paths it released (nil when none).
	ReleasedByKey(ctx context.Context, key string) (*ReleaseOutcome, []string, error)
	Release(ctx context.Context, admission ReleaseAdmission) (ReleaseOutcome, error)
}

// Aligner admits an align of one managed video through the existing
// reconciliation chain, refreshing its episode titles first when its
// template reads them.
type Aligner interface {
	Align(ctx context.Context, admission AlignAdmission) (AlignOutcome, error)
}

// SetCommands wires the P1 write ports. Unwired, every command is refused.
func (s *Service) SetCommands(store CommandStore, aligner Aligner) {
	if s != nil {
		s.commands, s.aligner = store, aligner
	}
}

const maxIdempotencyKeyBytes = 128

func validKey(key string) error {
	if strings.TrimSpace(key) == "" || len(key) > maxIdempotencyKeyBytes {
		return invalid(CodeIdempotencyKeyInvalid, "idempotency key is required")
	}
	return nil
}

func requestDigest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// internalNamespace reports a path inside a namespace the executors own
// (capture and staging directories); the file manager never writes there.
func internalNamespace(absolute string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(absolute), "/") {
		if strings.HasPrefix(segment, ".bansoko-") {
			return true
		}
	}
	return false
}

func (s *Service) requireCommands() error {
	if s.commands == nil {
		return conflict(CodeCommandsUnavailable, "file manager commands are not available")
	}
	return nil
}

// inspectForWrite re-derives one entry and checks the revision the user saw
// and that the admission matrix allows the operation now. A directory is judged by
// its subtree: an operation that would move, rename or delete it is refused
// while a managed file lives below it, and the refusal names those files.
func (s *Service) inspectForWrite(ctx context.Context, absolutePath, revision string, operation Operation) (inspection, error) {
	inspected, err := s.inspect(ctx, absolutePath)
	if err != nil {
		return inspection{}, err
	}
	if inspected.detail.Revision != revision {
		return inspection{}, conflict(CodeEntryChanged, "entry changed since it was read")
	}
	if internalNamespace(inspected.absolute) {
		return inspection{}, conflict(CodeOperationNotAllowed, "operation does not apply to this entry")
	}
	entry := inspected.detail.Entry
	switch entry.Kind {
	case KindDirectory:
		switch operation {
		case OperationMove, OperationRename, OperationDelete:
		default:
			return inspection{}, conflict(CodeOperationNotAllowed, "operation does not apply to this entry")
		}
		if entry.Locked {
			return inspection{}, s.lockedSubtree(ctx, inspected.absolute)
		}
		return inspected, nil
	case KindFile:
	default:
		return inspection{}, conflict(CodeOperationNotAllowed, "operation does not apply to this entry")
	}
	for _, rule := range inspected.detail.Operations {
		if rule.Operation != operation {
			continue
		}
		if !rule.Allowed {
			switch rule.Block {
			case BlockAlignmentDisabled:
				return inspection{}, conflict(CodeAlignmentDisabled, "alignment is disabled for this anime")
			default:
				return inspection{}, conflict(CodeOperationNotAllowed, "operation is not allowed: "+string(rule.Block))
			}
		}
		return inspected, nil
	}
	return inspection{}, conflict(CodeOperationNotAllowed, "operation does not apply to this entry")
}

// lockedSubtree builds the refusal of a directory operation, naming the
// managed files that hold the directory.
func (s *Service) lockedSubtree(ctx context.Context, directory string) error {
	locked, err := s.store.SubtreeLockedPaths(ctx, directory, maxLockedPaths)
	if err != nil {
		return err
	}
	refusal := conflict(CodeEntryLocked, "directory holds files the product manages: "+strings.Join(locked, ", "))
	refusal.Paths = locked
	return refusal
}

// resolvedTarget is a checked operation target.
type resolvedTarget struct {
	absolute  string
	spellings []string
	conflict  ConflictChoice
}

// resolveTarget checks a move, rename or copy target: the destination
// must be absolute, inside an authorized root and outside the executors' own
// namespaces, and its parent directory must exist without passing a symlink.
// A rename keeps the source directory. An occupied target is refused under
// block; overwrite replaces a plain file on disk but is refused for a managed
// or in-progress file.
func (s *Service) resolveTarget(ctx context.Context, source inspection, kind UserOperationKind, target *Target) (resolvedTarget, error) {
	if target == nil {
		return resolvedTarget{}, invalid(CodeTargetInvalid, "operation needs a target")
	}
	choice := target.Conflict
	if choice == "" {
		choice = ConflictBlock
	}
	if choice != ConflictBlock && choice != ConflictVersion && choice != ConflictOverwrite {
		return resolvedTarget{}, invalid(CodeTargetInvalid, "conflict choice is not valid")
	}
	if choice == ConflictOverwrite && source.info != nil && source.info.IsDir() {
		// The overwrite exchange replaces a file; a directory only moves onto
		// a free or versioned path.
		return resolvedTarget{}, invalid(CodeTargetInvalid, "overwrite does not apply to a directory")
	}
	if err := s.authorize(ctx, target.Path); err != nil {
		return resolvedTarget{}, invalid(CodeTargetInvalid, "target is not a valid path")
	}
	absolute, ok := cleanAbsolute(target.Path)
	if !ok || absolute == string(filepath.Separator) {
		return resolvedTarget{}, invalid(CodeTargetInvalid, "target must be an absolute path")
	}
	if internalNamespace(absolute) {
		return resolvedTarget{}, invalid(CodeTargetInvalid, "target is not a valid path")
	}
	if absolute == source.absolute {
		return resolvedTarget{}, invalid(CodeTargetInvalid, "target is the file itself")
	}
	if kind == UserRename && filepath.Dir(absolute) != filepath.Dir(source.absolute) {
		return resolvedTarget{}, invalid(CodeTargetInvalid, "a rename keeps the directory")
	}
	parent, info, err := walkAbsolute(filepath.Dir(absolute), false)
	if err != nil || !info.IsDir() {
		return resolvedTarget{}, invalid(CodeTargetInvalid, "target directory does not exist")
	}
	resolved := resolvedTarget{
		absolute:  joinAbsolute(parent, filepath.Base(absolute)),
		spellings: absoluteSpellings(joinAbsolute(parent, filepath.Base(absolute))),
		conflict:  choice,
	}
	occupant, err := s.occupant(ctx, resolved)
	if err != nil {
		return resolvedTarget{}, err
	}
	if occupant != "" && choice != ConflictVersion {
		if choice == ConflictOverwrite && occupant == OccupantFile {
			return resolved, nil
		}
		return resolvedTarget{}, conflict(CodeTargetExists, "target is occupied")
	}
	return resolved, nil
}

func (s *Service) operationAdmission(ctx context.Context, request OperationRequest, source inspection, digest string) (OperationAdmission, error) {
	admission := OperationAdmission{
		ID: uuid.NewString(), IdempotencyKey: request.IdempotencyKey, Digest: digest, Actor: request.Actor,
		Operation: request.Operation, SourcePath: source.absolute, SourceSpellings: source.spellings,
		Collision: ConflictBlock, RequireDetachedSource: request.Operation != UserCopy,
	}
	if source.info != nil && !source.info.IsDir() {
		// A directory's size is a filesystem detail, not a content fact: its
		// admission freezes ExpectedSize zero and execution verifies identity
		// only.
		admission.ExpectedSize = source.info.Size()
	}
	if request.Operation == UserDelete {
		return admission, nil
	}
	target, err := s.resolveTarget(ctx, source, request.Operation, request.Target)
	if err != nil {
		return OperationAdmission{}, err
	}
	admission.TargetPath, admission.TargetSpellings, admission.Collision = target.absolute, target.spellings, target.conflict
	return admission, nil
}

func operationFor(kind UserOperationKind) (Operation, bool) {
	switch kind {
	case UserMove:
		return OperationMove, true
	case UserRename:
		return OperationRename, true
	case UserCopy:
		return OperationCopy, true
	case UserDelete:
		return OperationDelete, true
	case UserMatch:
		return OperationMatch, true
	}
	return "", false
}

// SubmitOperation admits one user file operation: it re-derives
// the entry, checks the revision the user confirmed, the admission matrix and the
// target, then hands the frozen request to the command store, which re-checks
// owner facts and enqueues the fenced file_manager_execute work in the same
// transaction. A repeated key with the same request returns the operation it
// admitted.
func (s *Service) SubmitOperation(ctx context.Context, request OperationRequest) (UserOperation, error) {
	if err := s.requireCommands(); err != nil {
		return UserOperation{}, err
	}
	if err := validKey(request.IdempotencyKey); err != nil {
		return UserOperation{}, err
	}
	operation, ok := operationFor(request.Operation)
	if !ok || request.Operation == UserMatch {
		// A match has its own request (SubmitMatch).
		return UserOperation{}, invalid(CodeTargetInvalid, "operation is not valid")
	}
	if request.Operation == UserDelete && request.Target != nil {
		return UserOperation{}, invalid(CodeTargetInvalid, "delete takes no target")
	}
	digest := requestDigest(struct {
		Operation UserOperationKind
		Path      string
		Revision  string
		Target    *Target
	}{request.Operation, request.Path, request.ExpectedRevision, request.Target})
	if existing, existingDigest, err := s.commands.OperationByKey(ctx, request.IdempotencyKey); err != nil {
		return UserOperation{}, err
	} else if existing != nil {
		if existingDigest != digest {
			return UserOperation{}, conflict(CodeIdempotencyConflict, "idempotency key was used for another request")
		}
		return *existing, nil
	}
	source, err := s.inspectForWrite(ctx, request.Path, request.ExpectedRevision, operation)
	if err != nil {
		return UserOperation{}, err
	}
	admission, err := s.operationAdmission(ctx, request, source, digest)
	if err != nil {
		return UserOperation{}, err
	}
	return s.commands.AdmitOperation(ctx, admission)
}

// Operation reads one user file operation.
func (s *Service) Operation(ctx context.Context, id string) (UserOperation, error) {
	if err := s.requireCommands(); err != nil {
		return UserOperation{}, err
	}
	op, err := s.commands.Operation(ctx, id)
	if err != nil {
		return UserOperation{}, err
	}
	if op == nil {
		return UserOperation{}, notFound(CodeOperationNotFound, "operation does not exist")
	}
	return *op, nil
}

// DeletePlacement releases one eligible placement and atomically admits safe
// deletion of its video and every bound companion. Group eligibility and all
// ownership versions are re-checked by the command store in the same transaction.
func (s *Service) DeletePlacement(ctx context.Context, request DeletePlacementRequest) (ReleaseOutcome, error) {
	if err := s.requireCommands(); err != nil {
		return ReleaseOutcome{}, err
	}
	if err := validKey(request.IdempotencyKey); err != nil {
		return ReleaseOutcome{}, err
	}
	absolute, spellings, err := s.PathSpellings(request.Path)
	if err != nil {
		return ReleaseOutcome{}, err
	}
	digest := requestDigest(struct {
		PlacementID      string
		PlacementVersion int64
		Path             string
	}{request.PlacementID, request.PlacementVersion, absolute})
	if done, paths, err := s.commands.ReleasedByKey(ctx, request.IdempotencyKey); err != nil {
		return ReleaseOutcome{}, err
	} else if done != nil {
		matches := false
		for _, released := range paths {
			for _, spelling := range spellings {
				matches = matches || spelling == released
			}
		}
		if !matches || done.Operation == nil || done.Operation.Operation != UserDelete || done.RequestDigest != digest {
			return ReleaseOutcome{}, conflict(CodeIdempotencyConflict, "idempotency key was used for another request")
		}
		return *done, nil
	}
	source, err := s.inspect(ctx, absolute)
	if err != nil {
		return ReleaseOutcome{}, err
	}
	placement := source.derived.placement
	if source.detail.Entry.Kind != KindFile || internalNamespace(source.absolute) || source.derived.role != RoleVideo ||
		placement == nil || placement.State != placementActive {
		return ReleaseOutcome{}, conflict(CodeOperationNotAllowed, "only a managed video can be deleted")
	}
	if placement.ID != request.PlacementID || placement.Version != request.PlacementVersion {
		return ReleaseOutcome{}, conflict(CodeEntryChanged, "placement changed since it was read")
	}
	video := OperationAdmission{
		ID: uuid.NewString(), IdempotencyKey: request.IdempotencyKey, Digest: digest, Actor: request.Actor,
		Operation: UserDelete, SourcePath: source.absolute, SourceSpellings: source.spellings,
		ExpectedSize: source.info.Size(), Collision: ConflictBlock, RequireDetachedSource: true,
		ReleasePlacementID: placement.ID,
	}
	admission := ReleaseAdmission{
		IdempotencyKey: request.IdempotencyKey, Actor: request.Actor,
		ManagedAnimeID: placement.ManagedAnimeID, AnimeTitle: placement.AnimeTitle,
		Placement: VersionRef{ID: placement.ID, Version: placement.Version}, Spellings: source.spellings,
		Delete: &ReleaseDeleteAdmission{Video: video},
	}
	companions, err := s.store.PlacementCompanions(ctx, placement.ID)
	if err != nil {
		return ReleaseOutcome{}, err
	}
	for _, companion := range companions {
		if companion.State != companionActive {
			continue
		}
		inspected, err := s.inspect(ctx, companion.Path)
		if err != nil {
			return ReleaseOutcome{}, err
		}
		if inspected.detail.Entry.Kind != KindFile || internalNamespace(inspected.absolute) || inspected.derived.companion == nil ||
			inspected.derived.companion.ID != companion.ID || inspected.derived.companion.Placement.ID != placement.ID {
			return ReleaseOutcome{}, conflict(CodeEntryChanged, "companion changed since it was read")
		}
		key := "placement-delete-companion:" + requestDigest(struct{ Key, CompanionID string }{request.IdempotencyKey, companion.ID})
		operation := OperationAdmission{
			ID: uuid.NewString(), IdempotencyKey: key, Digest: digest, Actor: request.Actor,
			Operation: UserDelete, SourcePath: inspected.absolute, SourceSpellings: inspected.spellings,
			ExpectedSize: inspected.info.Size(), Collision: ConflictBlock, RequireDetachedSource: true,
			ReleasePlacementID: placement.ID,
		}
		admission.Delete.Companions = append(admission.Delete.Companions, ReleaseCompanionDelete{
			Companion: VersionRef{ID: companion.ID, Version: companion.Version}, Path: inspected.absolute, Operation: operation,
		})
	}
	return s.commands.Release(ctx, admission)
}

// Release unlocks one managed video: its placement turns released
// (terminal), its companion relations end with it, and the files stay in
// place. With MoveTo the same transaction admits a move of the video (its
// sidecars stay where they are).
func (s *Service) Release(ctx context.Context, request ReleaseRequest) (ReleaseOutcome, error) {
	if err := s.requireCommands(); err != nil {
		return ReleaseOutcome{}, err
	}
	if err := validKey(request.IdempotencyKey); err != nil {
		return ReleaseOutcome{}, err
	}
	_, spellings, err := s.PathSpellings(request.Path)
	if err != nil {
		return ReleaseOutcome{}, err
	}
	if done, paths, err := s.commands.ReleasedByKey(ctx, request.IdempotencyKey); err != nil {
		return ReleaseOutcome{}, err
	} else if done != nil {
		for _, released := range paths {
			for _, spelling := range spellings {
				if spelling == released {
					return *done, nil
				}
			}
		}
		return ReleaseOutcome{}, conflict(CodeIdempotencyConflict, "idempotency key was used for another request")
	}
	source, err := s.inspectForWrite(ctx, request.Path, request.ExpectedRevision, OperationRelease)
	if err != nil {
		return ReleaseOutcome{}, err
	}
	placement := source.derived.placement
	if source.derived.role != RoleVideo || placement == nil || placement.State != placementActive {
		return ReleaseOutcome{}, conflict(CodeOperationNotAllowed, "only a managed video is released")
	}
	admission := ReleaseAdmission{
		IdempotencyKey: request.IdempotencyKey, Actor: request.Actor,
		ManagedAnimeID: placement.ManagedAnimeID, AnimeTitle: placement.AnimeTitle,
		Placement: VersionRef{ID: placement.ID, Version: placement.Version},
		Spellings: source.spellings,
	}
	if request.MoveTo != nil {
		move := OperationRequest{
			IdempotencyKey: request.IdempotencyKey, Actor: request.Actor, Operation: UserMove,
			Path: request.Path, ExpectedRevision: request.ExpectedRevision, Target: request.MoveTo,
		}
		digest := requestDigest(struct {
			Release bool
			Request OperationRequest
		}{true, OperationRequest{Operation: UserMove, Path: move.Path, ExpectedRevision: move.ExpectedRevision, Target: move.Target}})
		operation, err := s.operationAdmission(ctx, move, source, digest)
		if err != nil {
			return ReleaseOutcome{}, err
		}
		operation.ReleasePlacementID = placement.ID
		admission.Move = &operation
	}
	return s.commands.Release(ctx, admission)
}

// Align aligns one managed video through the existing alignment chain. A user
// align is a repair: when the file's template reads a
// provider episode title it is looked up again, bypassing the cache, and
// frozen for the file before the scan is admitted.
func (s *Service) Align(ctx context.Context, request AlignRequest) (AlignOutcome, error) {
	if err := s.requireCommands(); err != nil {
		return AlignOutcome{}, err
	}
	if s.aligner == nil {
		return AlignOutcome{}, conflict(CodeCommandsUnavailable, "alignment is not available")
	}
	if err := validKey(request.IdempotencyKey); err != nil {
		return AlignOutcome{}, err
	}
	source, err := s.inspectForWrite(ctx, request.Path, request.ExpectedRevision, OperationAlign)
	if err != nil {
		return AlignOutcome{}, err
	}
	placement := source.derived.placement
	if source.derived.role != RoleVideo || placement == nil {
		return AlignOutcome{}, conflict(CodeOperationNotAllowed, "only a managed video is aligned")
	}
	return s.aligner.Align(ctx, AlignAdmission{
		IdempotencyKey: request.IdempotencyKey, ManagedAnimeID: placement.ManagedAnimeID,
		PlacementID: placement.ID, PlacementPath: placement.Path,
	})
}
