// Package journal implements the durable state-machine truth of the
// container-embedded self-update chain.
//
// The journal is a single deployment-owned file on the updates volume
// (/var/lib/bansoko/updates/journal.json). Both the server-side updater
// (which writes admission through pending_migration checkpoints before
// exiting) and the bootstrap wrapper (which consumes the handoff after the
// restart and records migration/promotion outcomes) persist through this
// package, so every irreversible effect satisfies
// checkpoint -> effect -> verify -> next-checkpoint discipline.
//
// Persistence contract mirrors the supervisor hardening journal:
//   - read-modify-write under an in-process mutex with a conditional version
//     assert (CAS); stale versions are rejected instead of merged;
//   - writes go to a temp file, fsync, atomic rename, then fsync of the parent
//     directory so the rename itself survives a crash;
//   - an exclusive flock on <path>.lock is held for the process lifetime: a
//     second writer fails closed at Open instead of racing CAS updates;
//   - an unreadable journal is quarantined as <stem>.corrupt-<ts> and reported
//     as ErrJournalCorrupted. The caller must fail closed into maintenance
//     rather than crash-looping or silently resetting update state.
//
// This package only owns state; it never executes effects itself.
package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// FormatVersion guards forward-incompatible journal layout changes. An
// envelope carrying another schema version must be treated as data the local
// binary cannot consume (fail closed), not as recoverable noise.
const FormatVersion = 1

// maxJournalBytes bounds any journal read. The file holds one record plus its
// append-only history; the limit is generous for that shape and turns a
// hostile or truncated-to-huge file into a typed corruption instead of an
// unbounded allocation.
const maxJournalBytes = 4 << 20

// errReadTooLarge marks a read that stopped at the size bound. Callers map it
// to ErrJournalCorrupted: a document the local binary cannot consume is data it
// must fail closed on, not a transient read error.
var errReadTooLarge = errors.New("file exceeds the bounded read size")

// Step names one persisted checkpoint of the embedded update chain.
type Step string

const (
	// StepPrepared is the admission-prepared checkpoint: the durable record of
	// the intent to admit this exact release, written BEFORE the database fence
	// is acquired. Writing it first is what guarantees that a crash anywhere
	// after the fence insert still leaves a journal record behind — an open
	// fence row or a consumed release-sequence floor without journal evidence
	// would be unrecoverable.
	StepPrepared Step = "prepared"

	// Server-side updater checkpoints (admission through exit(0) handoff).
	StepAccepted           Step = "accepted"
	StepFetched            Step = "fetched"
	StepStaged             Step = "staged"
	StepMaintenanceEntered Step = "maintenance_entered"
	StepBackupVerified     Step = "backup_verified"
	StepPendingMigration   Step = "pending_migration"

	// StepAbortPending is the durable operator abort intent: it is written
	// before any cleanup runs, so the cleanup and the terminal failure
	// checkpoint are replayable across a crash inside the file/DB window.
	StepAbortPending Step = "abort_pending"

	// Bootstrap-side checkpoints (post-restart consumption).
	StepMigrationSucceeded Step = "migration_succeeded"
	StepPromoted           Step = "promoted"

	// Rollback chain checkpoints.
	StepReadinessFailed  Step = "readiness_failed"
	StepRollbackStarted  Step = "rollback_started"
	StepDatabaseRestored Step = "database_restored"
	// FenceClosurePending records a terminal outcome whose owner-scoped
	// PostgreSQL fence could not yet be closed. It remains active so bootstrap
	// retries closure before serving or allowing workspace reuse.
	StepFenceClosurePending Step = "fence_closure_pending"

	// Terminal states.
	StepSucceeded       Step = "succeeded"
	StepFailed          Step = "failed" // pre-effect abort, no side effects landed
	StepRolledBack      Step = "rolled_back"
	StepRollbackBlocked Step = "rollback_blocked"
)

// transitions lists every legal non-self advance. Re-advancing to the current
// step is always legal and models idempotent restart replay: the checkpoint
// version still moves forward so the history trail shows the retry happened.
var transitions = map[Step][]Step{
	// Admission: prepared holds the intent before the fence is acquired, and
	// accepted only exists once that fence is held.
	StepPrepared:           {StepAccepted, StepFailed, StepAbortPending},
	StepAccepted:           {StepFetched, StepFailed, StepAbortPending},
	StepFetched:            {StepStaged, StepFailed, StepAbortPending},
	StepStaged:             {StepMaintenanceEntered, StepFailed, StepAbortPending},
	StepMaintenanceEntered: {StepBackupVerified, StepFailed, StepAbortPending},
	StepBackupVerified:     {StepPendingMigration, StepFailed, StepAbortPending},
	// A durable abort intent only ever ends in the pre-effect failure state.
	StepAbortPending: {StepFailed},
	// From pending_migration on, irreversible effects have begun: failure
	// paths go through the explicit rollback chain instead of plain failed.
	// pending_migration keeps the pre-migration backup abort: the bootstrap
	// consumer must be able to fall back before any irreversible effect when the
	// verified backup is unusable.
	StepPendingMigration: {StepMigrationSucceeded, StepRollbackStarted, StepFailed},
	// Past the migration every failure is a rollback-chain decision: a direct
	// `failed` transition would claim the pre-effect abort semantics that no
	// longer hold.
	StepMigrationSucceeded:  {StepPromoted, StepRollbackStarted},
	StepPromoted:            {StepSucceeded, StepFenceClosurePending, StepReadinessFailed},
	StepReadinessFailed:     {StepRollbackStarted},
	StepRollbackStarted:     {StepDatabaseRestored, StepRollbackBlocked},
	StepDatabaseRestored:    {StepRolledBack, StepFenceClosurePending, StepRollbackBlocked},
	StepFailed:              {StepFenceClosurePending},
	StepSucceeded:           {StepFenceClosurePending},
	StepRolledBack:          {StepFenceClosurePending},
	StepFenceClosurePending: {StepSucceeded, StepRolledBack, StepFailed},
}

// CanAdvance reports whether a CAS step change from -> to is legal.
func CanAdvance(from, to Step) bool {
	if from == to {
		return true
	}
	for _, candidate := range transitions[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

// FenceClosure proof values carried by Record.FenceClosure. A terminal record
// may only be archived once its closure proof is durable: without it, a closed
// journal would delete the only evidence that the shared fence was settled.
const (
	// FenceClosureClosed: the exact owner-scoped row was closed and verified.
	FenceClosureClosed = "closed"
	// FenceClosureNotRequired: the operation never acquired the fence (it
	// stopped at the prepared checkpoint before the insert), and that absence
	// was verified against the fence itself.
	FenceClosureNotRequired = "not_required"

	RecoveryActionResume  = "resume"
	RecoveryActionAbort   = "abort"
	RecoveryReferenceType = "sha256"
)

// ValidFenceClosure reports whether value is a durable closure proof.
func ValidFenceClosure(value string) bool {
	return value == FenceClosureClosed || value == FenceClosureNotRequired
}

// Terminal reports whether no further advances are possible. A terminal record
// stays queryable forever for evidence, but consumers may reuse the updates
// workspace once succeeded/rolled_back is reached. FenceClosurePending is
// intentionally active until the owner-scoped fence close is verified.
func Terminal(step Step) bool {
	switch step {
	case StepSucceeded, StepFailed, StepRolledBack, StepRollbackBlocked:
		return true
	default:
		return false
	}
}

var safeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ValidID reports whether value is a bounded identifier that is safe both as a
// journal field and as an operation-owned file name component (no separators,
// no parent traversal, no leading space or dot).
func ValidID(value string) bool { return safeIDPattern.MatchString(value) }

func safeID(value string) bool { return ValidID(value) }

// maxManifestURLLength bounds the durable manifest URL. It is deployment
// configuration-sized evidence, not free text.
const maxManifestURLLength = 2048

// safeManifestURL accepts only a bounded absolute http(s) URL with no
// credentials, query, fragment or control characters. The journal stores the
// URL it must later rebuild a request identity from, so it refuses anything a
// request builder would have to reinterpret.
func safeManifestURL(value string) bool {
	if len(value) > maxManifestURLLength || strings.ContainsAny(value, "\r\n\t ") {
		return false
	}
	if strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return false
	}
	lowered := strings.ToLower(value)
	if !strings.HasPrefix(lowered, "http://") && !strings.HasPrefix(lowered, "https://") {
		return false
	}
	if strings.ContainsAny(value, "?#") || strings.Contains(value, "@") {
		return false
	}
	return true
}

func ValidRecoveryOriginStep(step Step) bool {
	switch step {
	case StepPrepared, StepAccepted, StepFetched, StepStaged, StepMaintenanceEntered, StepBackupVerified:
		return true
	default:
		return false
	}
}

func ValidateRecoveryDecision(record Record) error {
	present := record.RecoveryAction != "" || record.RecoveryFromStep != "" ||
		record.RecoveryFromVersion != 0 || record.RecoveryReferenceType != "" ||
		record.RecoveryReferenceHash != ""
	if !present {
		return nil
	}
	if record.RecoveryAction != RecoveryActionResume && record.RecoveryAction != RecoveryActionAbort {
		return errors.New("recovery decision action is invalid")
	}
	if !ValidRecoveryOriginStep(record.RecoveryFromStep) {
		return errors.New("recovery decision origin step is invalid")
	}
	if record.RecoveryFromVersion < 0 {
		return errors.New("recovery decision origin version is invalid")
	}
	if record.RecoveryReferenceType != RecoveryReferenceType {
		return errors.New("recovery decision reference type is invalid")
	}
	if !validSHA256(record.RecoveryReferenceHash) {
		return errors.New("recovery decision reference hash is invalid")
	}
	return nil
}

func recoveryTuple(record Record) [5]string {
	return [5]string{
		record.RecoveryAction,
		string(record.RecoveryFromStep),
		fmt.Sprintf("%d", record.RecoveryFromVersion),
		record.RecoveryReferenceType,
		record.RecoveryReferenceHash,
	}
}

func recoveryTuplePresent(record Record) bool {
	return record.RecoveryAction != "" || record.RecoveryFromStep != "" || record.RecoveryFromVersion != 0 ||
		record.RecoveryReferenceType != "" || record.RecoveryReferenceHash != ""
}

func validateRecoveryTupleTransition(before, after Record) error {
	if !recoveryTuplePresent(before) {
		return nil
	}
	if recoveryTuple(before) != recoveryTuple(after) {
		return errors.New("durable recovery decision cannot be cleared or changed")
	}
	return nil
}

func abortTuplePresent(record Record) bool {
	return record.AbortFrom != "" || record.AbortFromVersion != 0
}

func validateAbortTuple(record Record) error {
	if !abortTuplePresent(record) {
		return nil
	}
	if !ValidRecoveryOriginStep(record.AbortFrom) || record.AbortFromVersion < 0 {
		return errors.New("abort origin step/version tuple is invalid")
	}
	return nil
}

func validateAbortTupleTransition(before, after Record) error {
	if err := validateAbortTuple(after); err != nil {
		return err
	}
	if abortTuplePresent(before) && (before.AbortFrom != after.AbortFrom || before.AbortFromVersion != after.AbortFromVersion) {
		return errors.New("durable abort origin cannot be cleared or changed")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// TransitionEvent is one append-only checkpoint entry in the record history.
type TransitionEvent struct {
	Step   Step      `json:"step"`
	At     time.Time `json:"at"`
	Error  string    `json:"error,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// AttemptCounters tracks bounded retries owned by B3/B4 executors (staging
// re-fetch, readiness threshold). They ride along with checkpoints so a
// counter bump and its justifying checkpoint are one atomic write.
type AttemptCounters struct {
	Migrate   int `json:"migrate,omitempty"`
	Readiness int `json:"readiness,omitempty"`
}

// Record is the full embedded-update operation truth. There is at most one
// active operation per deployment; Channel marks which updater admitted it
// ("embedded") so future channels cannot collide on the same file.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	OperationID   string `json:"operation_id"`
	Channel       string `json:"channel"`

	Step    Step  `json:"step"`
	Version int64 `json:"version"`

	// Signed release identity the operation targets.
	TargetReleaseID       string `json:"target_release_id"`
	TargetVersion         string `json:"target_version"`
	TargetSchemaVersion   string `json:"target_schema_version"`
	TargetReleaseSequence int64  `json:"target_release_sequence,omitempty"`
	PayloadDigest         string `json:"payload_digest,omitempty"`
	ManifestDigest        string `json:"manifest_digest,omitempty"`
	// ManifestPath and ManifestSize locate the admission-time copy of the
	// verified signed manifest on the updates volume: a path relative to the
	// updates root inside the operation-owned copy area, plus the exact byte
	// size that bounds the read. ManifestCopyDigest is the sha256 of those
	// persisted bytes, so the copy on disk is bound byte-for-byte and does not
	// depend on re-encoding. ManifestDigest stays the canonical signing digest
	// the admission verified and the database fence carries. Recovery
	// re-verifies the copy against both digests and the signed identity before
	// the resumed chain takes any further effect, so these fields are part of
	// the durable operation truth. Records written before this contract existed
	// carry none of them and must fail closed instead of falling back to the
	// network or to the staged tree.
	ManifestPath       string `json:"manifest_path,omitempty"`
	ManifestSize       int64  `json:"manifest_size,omitempty"`
	ManifestCopyDigest string `json:"manifest_copy_digest,omitempty"`
	// IdempotencyKeyHash and RequestFingerprint are admission evidence. The raw
	// key is never persisted; the fingerprint binds a replay to the exact
	// signed manifest accepted by this operation.
	IdempotencyKeyHash string `json:"idempotency_key_hash,omitempty"`
	RequestFingerprint string `json:"request_fingerprint,omitempty"`
	// ManifestURL is the immutable manifest URL admission actually verified and
	// accepted. A deployment that discovers its release through a channel
	// document does not know this URL from configuration, so the operation has
	// to carry it: the payload fetch and an operator `recover resume` must use
	// the versioned package the operation was admitted from, never a channel
	// that may have moved on. Records written before this field existed leave it
	// empty and fall back to the configured exact source (or fail closed when
	// the deployment has none).
	ManifestURL string `json:"manifest_url,omitempty"`

	// Workspace pointers.
	StagingID           string `json:"staging_id,omitempty"` // directory name under staging/
	BackupID            string `json:"backup_id,omitempty"`  // backups/<id>.dump stem
	BootstrapMinVersion string `json:"bootstrap_min_version,omitempty"`
	// BackupIntent records that this operation claimed its backup artifact
	// before the dump ran. The artifact name is derived from the operation id,
	// so a replayed backup effect republishes the same path instead of
	// producing a second dump.
	BackupIntent bool `json:"backup_intent,omitempty"`

	// PublishIntent records that this operation may already have published
	// releases/<target_release_id>: the intent is written before the staging
	// effect runs, so a crash between the publish and the `staged` checkpoint
	// leaves a durable statement that the tree on the volume can belong to this
	// operation and must be verified against its accepted manifest before it is
	// adopted or removed.
	PublishIntent bool `json:"publish_intent,omitempty"`

	// AbortFrom is retained for updater-authored abort compatibility. Operator
	// recovery decisions use the complete Recovery* identity below.
	AbortFrom Step `json:"abort_from,omitempty"`
	// AbortFromVersion and RecoveryFromVersion are always serialized: the journal
	// version of a record created at its first checkpoint really is 0, and the
	// durable decision has to stay readable as the exact identity the operator
	// supplied. An omitted field would be indistinguishable from version 0 for
	// every consumer that reads the journal document rather than the Go value.
	AbortFromVersion int64 `json:"abort_from_version"`

	RecoveryAction        string `json:"recovery_action,omitempty"`
	RecoveryFromStep      Step   `json:"recovery_from_step,omitempty"`
	RecoveryFromVersion   int64  `json:"recovery_from_version"`
	RecoveryReferenceType string `json:"recovery_reference_type,omitempty"`
	RecoveryReferenceHash string `json:"recovery_reference_hash,omitempty"`

	// FenceClosure is the durable proof that the owner-scoped fence row of this
	// operation is settled, and therefore that a terminal record may be
	// archived. It is empty until closure is proven, and takes one of the
	// FenceClosure* values below.
	FenceClosure string `json:"fence_closure,omitempty"`

	// Release pointer bookkeeping maintained across promotion; readers decide
	// which tree runs next. Empty fields mean "unset", never "inherit".
	CurrentReleaseID   string `json:"current_release_id,omitempty"`
	PreviousReleaseID  string `json:"previous_release_id,omitempty"`
	FenceClosureTarget Step   `json:"fence_closure_target,omitempty"`
	// RollbackSourceCaptured records that PreviousReleaseID was durably
	// captured from the live pointer before any irreversible effect ran. An
	// empty PreviousReleaseID without this flag is ambiguous between "base
	// fallback" and "never captured"; rollback must then fail closed instead of
	// guessing, because promotion may already have moved the pointer onto the
	// rejected target.
	RollbackSourceCaptured bool `json:"rollback_source_captured,omitempty"`

	Attempts AttemptCounters `json:"attempts,omitempty"`

	LastError string `json:"last_error,omitempty"`

	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	History   []TransitionEvent `json:"history,omitempty"`
}

// Active reports whether the operation still needs a consumer (updater,
// bootstrap) or recovery decision.
func (r Record) Active() bool { return !Terminal(r.Step) }

func (r *Record) normalize(now time.Time) error {
	r.OperationID = strings.TrimSpace(r.OperationID)
	r.Channel = strings.TrimSpace(r.Channel)
	r.TargetReleaseID = strings.TrimSpace(r.TargetReleaseID)
	r.ManifestURL = strings.TrimSpace(r.ManifestURL)
	if r.OperationID == "" || !safeID(r.OperationID) {
		return errors.New("journal record requires a bounded operation id")
	}
	if r.ManifestURL != "" && !safeManifestURL(r.ManifestURL) {
		return errors.New("journal record manifest url is not a bounded absolute http(s) url")
	}
	if !safeID(r.Channel) {
		return errors.New("journal record requires a bounded channel identifier")
	}
	if !safeID(r.TargetReleaseID) {
		return errors.New("journal record requires a bounded target release id")
	}
	now = now.UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.CreatedAt = r.CreatedAt.UTC()
	r.UpdatedAt = now
	if r.Step == "" {
		r.Step = StepAccepted
	}
	if r.SchemaVersion == 0 {
		r.SchemaVersion = FormatVersion
	}
	if r.SchemaVersion != FormatVersion {
		return fmt.Errorf("unsupported journal record schema version %d", r.SchemaVersion)
	}
	return nil
}

var (
	ErrLocked             = errors.New("update journal is locked by another process")
	ErrJournalCorrupted   = errors.New("update journal is corrupted")
	ErrDuplicateOperation = errors.New("update journal already holds an active operation")
	ErrNotFound           = errors.New("operation not found in update journal")
	ErrConflict           = errors.New("update journal version conflict")
	ErrStepConflict       = errors.New("update journal step conflict")
	ErrInvalidTransition  = errors.New("invalid update journal step transition")
)

// envelope wraps the single active record so future layouts can evolve the
// file without reinterpreting old bytes.
type envelope struct {
	SchemaVersion int     `json:"schema_version"`
	Record        *Record `json:"record"`
}

// FileJournal is the persistent embedded-update journal. Instances hold the
// flock for their lifetime; Close releases it.
type FileJournal struct {
	mu       sync.Mutex
	path     string
	lockFile *os.File
	clock    func() time.Time
}

// Open creates or opens the journal at path, taking an exclusive non-blocking
// flock. A structurally corrupt file is quarantined (renamed alongside the
// original) and ErrJournalCorrupted is returned wrapping the cause: callers
// fail closed into maintenance but keep the evidence for inspection.
func Open(path string) (*FileJournal, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("update journal path is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create update journal directory: %w", err)
	}
	// The journal directory must be a real directory: a symlinked parent could
	// redirect the lock file, the journal or its quarantine outside the
	// deployment-owned volume.
	if statErr := requireRealDir(dir); statErr != nil {
		return nil, fmt.Errorf("update journal directory is not usable: %w", statErr)
	}
	// O_NOFOLLOW: a symlink planted at the lock path must fail closed instead of
	// being followed.
	lockFile, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open update journal lock: %w", err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("%w: %v", ErrLocked, err)
	}
	j := &FileJournal{path: path, lockFile: lockFile, clock: func() time.Time { return time.Now().UTC() }}
	data, readErr := readFileBounded(path, maxJournalBytes)
	if errors.Is(readErr, errReadTooLarge) {
		_ = j.Close()
		return nil, fmt.Errorf("%w: %v", ErrJournalCorrupted, readErr)
	}
	switch {
	case readErr == nil:
		var env envelope
		if decodeErr := json.Unmarshal(data, &env); decodeErr != nil {
			_ = j.Close()
			if isolateErr := quarantine(dir, baseName(path)); isolateErr != nil {
				return nil, fmt.Errorf("%w (%v); quarantine failed: %v", ErrJournalCorrupted, decodeErr, isolateErr)
			}
			return nil, fmt.Errorf("%w: quarantined %s (%v)", ErrJournalCorrupted, path, decodeErr)
		}
		if env.Record != nil && env.SchemaVersion != FormatVersion {
			_ = j.Close()
			return nil, fmt.Errorf("%w: unsupported envelope schema version %d", ErrJournalCorrupted, env.SchemaVersion)
		}
	case errors.Is(readErr, os.ErrNotExist):
		// Fresh volume: no record yet.
	default:
		_ = j.Close()
		return nil, fmt.Errorf("read update journal: %w", readErr)
	}
	return j, nil
}

func baseName(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}

// quarantine renames a corrupt journal out of the way so the next Open starts
// clean while preserving the corrupt bytes for diagnosis.
func quarantine(dir, stem string) error {
	corruptPath := filepath.Join(dir, stem+".corrupt-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if renameErr := os.Rename(filepath.Join(dir, stem+".json"), corruptPath); renameErr != nil {
		return renameErr
	}
	return nil
}

// Close releases the journal lock.
func (j *FileJournal) Close() error {
	if j == nil || j.lockFile == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(j.lockFile.Fd()), syscall.LOCK_UN)
	err := j.lockFile.Close()
	j.lockFile = nil
	if unlockErr != nil {
		return unlockErr
	}
	return err
}

// Load returns the persisted record and whether one exists. The read is fresh
// from disk on every call: disk is the CAS authority, never memory.
func (j *FileJournal) Load() (Record, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	env, err := j.readEnvelope()
	if err != nil {
		return Record{}, false, err
	}
	if env.Record == nil {
		return Record{}, false, nil
	}
	return *env.Record, true, nil
}

// Snapshot reads the journal at path without taking the exclusive lock and
// without any side effect: no quarantine, no rewrite, no file creation. It is
// the read entry point for callers that must observe durable state while
// another process has to remain able to claim the writer lock — the parked
// maintenance responder and an operator `recover status`.
//
// Because every durable write is a temp-file + atomic rename, a snapshot
// observes a checkpoint either wholly before or wholly after it; it never sees
// half a record. A structural problem is reported as ErrJournalCorrupted
// without touching the file, so a read-only caller cannot destroy evidence.
func Snapshot(path string) (Record, bool, error) {
	if strings.TrimSpace(path) == "" {
		return Record{}, false, errors.New("update journal path is required")
	}
	data, err := readFileBounded(path, maxJournalBytes)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if errors.Is(err, errReadTooLarge) {
		return Record{}, false, fmt.Errorf("%w: %v", ErrJournalCorrupted, err)
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("read update journal snapshot: %w", err)
	}
	var env envelope
	if decodeErr := json.Unmarshal(data, &env); decodeErr != nil {
		return Record{}, false, fmt.Errorf("%w: decode update journal snapshot: %v", ErrJournalCorrupted, decodeErr)
	}
	if env.SchemaVersion != FormatVersion {
		return Record{}, false, fmt.Errorf("%w: unsupported envelope schema version %d", ErrJournalCorrupted, env.SchemaVersion)
	}
	if env.Record == nil {
		return Record{}, false, nil
	}
	return *env.Record, true, nil
}

// Create persists the first checkpoint of an operation. Creating is refused
// while any other operation exists (the chain is strictly serial). Recreating
// the same operation id is an idempotent success returning the stored record.
func (j *FileJournal) Create(record Record) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	existing, err := j.readEnvelope()
	if err != nil {
		return Record{}, err
	}
	if err := record.normalize(j.clock()); err != nil {
		return Record{}, err
	}
	if existing.Record != nil {
		if existing.Record.OperationID == record.OperationID {
			return *existing.Record, nil
		}
		return Record{}, fmt.Errorf(
			"%w (active %s at %s)", ErrDuplicateOperation, existing.Record.OperationID, existing.Record.Step)
	}
	record.Version = 0
	record.History = append([]TransitionEvent{}, TransitionEvent{
		Step: record.Step, At: j.clock(), Detail: "created",
	})
	if err := j.writeEnvelope(envelope{SchemaVersion: FormatVersion, Record: &record}); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Advance CAS-moves the operation to the given step. expectedVersion must
// match the persisted Version exactly. mutate, when non-nil, applies extra
// field changes atomically with the checkpoint write.
func (j *FileJournal) Advance(operationID string, expectedVersion int64, to Step, lastError, detail string, mutate func(*Record)) (Record, error) {
	return j.advance(operationID, expectedVersion, "", to, lastError, detail, mutate)
}

// AdvanceExpected is Advance with a second compare-and-swap condition on the
// expected current step. Operator recovery must use it: the version alone does
// not prove the durable checkpoint is still the one the operator decided on, so
// a recovery advance that merged a newer step would silently re-drive an
// operation whose evidence moved underneath it. A mismatch is a typed refusal,
// never a merge.
func (j *FileJournal) AdvanceExpected(operationID string, expectedVersion int64, expectedStep Step, to Step, lastError, detail string, mutate func(*Record)) (Record, error) {
	if strings.TrimSpace(string(expectedStep)) == "" {
		return Record{}, errors.New("expected journal step is required for a conditional advance")
	}
	return j.advance(operationID, expectedVersion, expectedStep, to, lastError, detail, mutate)
}

// advance implements the shared read-modify-write. An empty expectedStep skips
// the step condition and keeps Advance's historical contract.
func (j *FileJournal) advance(operationID string, expectedVersion int64, expectedStep, to Step, lastError, detail string, mutate func(*Record)) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	env, err := j.readEnvelope()
	if err != nil {
		return Record{}, err
	}
	if env.Record == nil {
		return Record{}, ErrNotFound
	}
	record := *env.Record
	if record.OperationID != operationID {
		return Record{}, ErrNotFound
	}
	if record.Version != expectedVersion {
		return Record{}, fmt.Errorf("%w (have v%d, expected v%d)", ErrConflict, record.Version, expectedVersion)
	}
	if expectedStep != "" && record.Step != expectedStep {
		return Record{}, fmt.Errorf("%w (have %s, expected %s)", ErrStepConflict, record.Step, expectedStep)
	}
	if !CanAdvance(record.Step, to) {
		return Record{}, fmt.Errorf("%w (%s -> %s)", ErrInvalidTransition, record.Step, to)
	}
	at := j.clock()
	record.Step = to
	record.Version++
	record.LastError = lastError
	record.UpdatedAt = at
	record.History = append(record.History, TransitionEvent{Step: to, At: at, Error: lastError, Detail: detail})
	if mutate != nil {
		mutate(&record)
	}
	// Validate the *result*: a checkpoint that leaves the record in a state its
	// own fields cannot justify must never reach the disk.
	if err := validateAdvancedRecord(*env.Record, record, to); err != nil {
		return Record{}, err
	}
	if err := j.writeEnvelope(envelope{SchemaVersion: FormatVersion, Record: &record}); err != nil {
		return Record{}, err
	}
	return record, nil
}

// validateAdvancedRecord enforces the invariants that depend on the record's
// own fields, so a caller cannot write a checkpoint its evidence contradicts:
//
//   - a fence-closure retry may only settle onto the target it recorded;
//   - entering the closure-pending state requires a legal terminal target;
//   - advancing to a terminal step requires the closure proof that step implies
//     (succeeded and rolled_back are only ever reached with a proven close).
func validateAdvancedRecord(before, record Record, to Step) error {
	if err := ValidateRecoveryDecision(record); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTransition, err)
	}
	if err := validateRecoveryTupleTransition(before, record); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTransition, err)
	}
	if err := validateAbortTupleTransition(before, record); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTransition, err)
	}
	if to == StepFenceClosurePending {
		if !validFenceClosureTarget(record.FenceClosureTarget) {
			return fmt.Errorf("%w: entering fence closure pending requires a terminal target, got %q",
				ErrInvalidTransition, record.FenceClosureTarget)
		}
	}
	if before.Step == StepFenceClosurePending && to != StepFenceClosurePending {
		if record.FenceClosureTarget == "" {
			return fmt.Errorf("%w: fence closure pending has no recorded target", ErrInvalidTransition)
		}
		if to != record.FenceClosureTarget {
			return fmt.Errorf("%w: fence closure pending may only settle to its recorded target %s, got %s",
				ErrInvalidTransition, record.FenceClosureTarget, to)
		}
	}
	switch to {
	case StepSucceeded, StepRolledBack:
		if record.FenceClosure != FenceClosureClosed {
			return fmt.Errorf("%w: %s requires a proven owner-scoped fence closure (proof %q)",
				ErrInvalidTransition, to, record.FenceClosure)
		}
	case StepFailed:
		switch record.FenceClosure {
		case "", FenceClosureClosed:
			// A pre-effect abort may be recorded before the close is attempted;
			// the archive is what demands the proof.
		case FenceClosureNotRequired:
			if err := validateNoFenceHistory(record); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidTransition, err)
			}
		default:
			return fmt.Errorf("%w: %s carries an unknown closure proof %q", ErrInvalidTransition, to, record.FenceClosure)
		}
	}
	return nil
}

// validFenceClosureTarget reports whether step may be the terminal target of a
// pending owner-scoped fence closure.
func validFenceClosureTarget(step Step) bool {
	switch step {
	case StepSucceeded, StepRolledBack, StepFailed:
		return true
	default:
		return false
	}
}

// ArchiveTerminalProven moves a terminal record out of the live journal so the
// workspace can serve the next operation: the record file is preserved under
// <journal dir>/archive/journal-<operation_id>-<unix_nano>.json as immutable
// evidence while the active envelope resets to empty on the next Load/Create.
//
// Archiving is the point where durable evidence leaves the live slot, so the
// record must carry the whole terminal proof:
//   - only a terminal step may be archived, and `rollback_blocked` is
//     deliberately excluded: it is a durable fail-closed operator block, not
//     history;
//   - a pending fence-closure target must first be settled back onto the record
//     (FenceClosureTarget == Step), because archiving while the close is
//     unverified would hide an open fence;
//   - FenceClosure must record the proven closure (closed, or not_required when
//     the operation never acquired the fence).
//
// There is exactly one archive entry point: a caller that closed the fence
// durably records that proof with AdvanceExpected before calling it. The move
// is a single rename inside the same filesystem (the updates volume), never a
// rewrite of history.
func (j *FileJournal) ArchiveTerminalProven() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	env, err := j.readEnvelope()
	if err != nil {
		return err
	}
	if env.Record == nil {
		return ErrNotFound
	}
	if err := validateArchivable(*env.Record); err != nil {
		return err
	}
	if !ValidFenceClosure(env.Record.FenceClosure) {
		return fmt.Errorf("refusing to archive %s without a proven owner-scoped fence closure", env.Record.Step)
	}
	return j.archiveLocked(env)
}

// validateArchivable holds the terminal-archive preconditions. It never
// inspects the fence itself: the journal owns state, and only the caller can
// prove a database effect — but it does enforce that the record carries the
// exact proof that close leaves behind.
func validateArchivable(record Record) error {
	switch record.Step {
	case StepFenceClosurePending:
		return fmt.Errorf("refusing to archive before fence closure is verified")
	case StepRollbackBlocked:
		return fmt.Errorf("refusing to archive rollback_blocked: it stays durable for operator recovery")
	}
	if !Terminal(record.Step) {
		return fmt.Errorf("refusing to archive an active update operation (step %s)", record.Step)
	}
	if target := record.FenceClosureTarget; target != "" && target != record.Step {
		return fmt.Errorf("refusing to archive while the fence closure target %s is unsettled (step %s)",
			target, record.Step)
	}
	return validateClosureProof(record)
}

// validateClosureProof enforces the durable closure-proof semantics of one
// terminal record:
//
//   - succeeded and rolled_back held the owner-scoped fence, so their proof must
//     be an exact close;
//   - failed may carry the exact close, or the no-fence proof — and that second
//     form is only valid for an operation that never reached `accepted`, because
//     an operation that did acquire the fence can never close itself by claiming
//     it had none.
//
// The history is the evidence for "never acquired": a record whose history is
// missing fails closed.
func validateClosureProof(record Record) error {
	switch record.Step {
	case StepSucceeded, StepRolledBack:
		if record.FenceClosure != FenceClosureClosed {
			return fmt.Errorf("refusing to archive %s without a proven owner-scoped fence closure (proof %q)",
				record.Step, record.FenceClosure)
		}
		return nil
	case StepFailed:
		switch record.FenceClosure {
		case FenceClosureClosed:
			return nil
		case FenceClosureNotRequired:
			return validateNoFenceHistory(record)
		default:
			return fmt.Errorf("refusing to archive failed without a proven owner-scoped fence closure (proof %q)",
				record.FenceClosure)
		}
	default:
		return fmt.Errorf("refusing to archive step %s", record.Step)
	}
}

// validateNoFenceHistory proves the record really never acquired the fence: the
// operation must have started at `prepared` and reached `failed` through a legal
// path that never touched `accepted`. The whole history is the evidence, so a
// record created directly at `failed` (or one that acquired the fence and then
// claimed it never had one) is refused. Idempotent replays of the same
// checkpoint are collapsed before the path is checked.
func validateNoFenceHistory(record Record) error {
	if record.Step != StepFailed {
		return fmt.Errorf("the no-fence closure proof is only valid for a failed operation (step %s)", record.Step)
	}
	if len(record.History) == 0 {
		return errors.New("refusing the no-fence closure proof: the record carries no history to prove it")
	}
	path := make([]Step, 0, len(record.History))
	for _, event := range record.History {
		if len(path) > 0 && path[len(path)-1] == event.Step {
			continue
		}
		path = append(path, event.Step)
	}
	if path[0] != StepPrepared {
		return fmt.Errorf("refusing the no-fence closure proof: the operation started at %s, not prepared", path[0])
	}
	if path[len(path)-1] != StepFailed {
		return fmt.Errorf("refusing the no-fence closure proof: the operation ended at %s, not failed", path[len(path)-1])
	}
	for _, step := range path {
		if step == StepAccepted {
			return errors.New("refusing the no-fence closure proof: this operation reached the accepted checkpoint")
		}
	}
	switch len(path) {
	case 2:
		// prepared -> failed: the admission was refused before the fence insert.
		return nil
	case 3:
		if path[1] != StepAbortPending {
			return fmt.Errorf("refusing the no-fence closure proof: %s -> %s is not a legal fence-less path", path[0], path[1])
		}
		return nil
	default:
		return fmt.Errorf("refusing the no-fence closure proof: %d durable steps are not a legal fence-less path", len(path))
	}
}

// archiveLocked performs the rename; callers hold mu and have validated the
// record.
func (j *FileJournal) archiveLocked(env envelope) error {
	dir := filepath.Dir(j.path)
	archiveDir := filepath.Join(dir, "archive")
	if mkErr := os.MkdirAll(archiveDir, 0o700); mkErr != nil {
		return fmt.Errorf("create update journal archive directory: %w", mkErr)
	}
	if dirErr := requireRealDir(archiveDir); dirErr != nil {
		return fmt.Errorf("update journal archive directory is not usable: %w", dirErr)
	}
	target := filepath.Join(archiveDir, fmt.Sprintf(
		"journal-%s-%d.json", env.Record.OperationID, j.clock().UnixNano()))
	if renameErr := os.Rename(j.path, target); renameErr != nil {
		return fmt.Errorf("archive update journal: %w", renameErr)
	}
	if syncErr := syncDirectory(archiveDir); syncErr != nil {
		return fmt.Errorf("sync update journal archive directory: %w", syncErr)
	}
	if syncErr := syncDirectory(dir); syncErr != nil {
		return fmt.Errorf("sync update journal parent directory: %w", syncErr)
	}
	return nil
}

// ArchivedEntry is one archived terminal record with its verification verdict.
//
// The archive is operational evidence, not the gate: an entry that cannot be
// verified is reported instead of being silently dropped, so a caller can tell
// "there is no evidence" from "the evidence cannot be trusted" — and can fail
// closed exactly when an open fence row depends on that evidence.
type ArchivedEntry struct {
	// FileName is the archive file name, for diagnostics only.
	FileName string
	// Record is the decoded record, zero when the file could not be decoded.
	Record Record
	// Verified reports that the entry carries a complete, valid terminal proof
	// and may therefore be used as closure or recovery evidence.
	Verified bool
	// Reason explains why an entry is not verified evidence. Files are never
	// rewritten: a legacy entry stays exactly as the previous writer left it.
	Reason string
}

// ReadArchive returns every archived record with its verdict. A file that cannot
// be decoded or proven is reported as an unverified entry rather than failing
// the whole read, because an unrelated legacy archive must not block a startup
// that has no live operation and no related open fence; callers that do have a
// related open fence must fail closed on the unverified verdict.
func (j *FileJournal) ReadArchive() ([]ArchivedEntry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	archiveDir := filepath.Join(filepath.Dir(j.path), "archive")
	entries, err := os.ReadDir(archiveDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]ArchivedEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "journal-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		archived := ArchivedEntry{FileName: entry.Name()}
		data, readErr := readFileBounded(filepath.Join(archiveDir, entry.Name()), maxJournalBytes)
		if readErr != nil {
			archived.Reason = "archive entry is unreadable: " + readErr.Error()
			result = append(result, archived)
			continue
		}
		var env envelope
		if decodeErr := json.Unmarshal(data, &env); decodeErr != nil {
			archived.Reason = "archive entry is not a decodable envelope: " + decodeErr.Error()
			result = append(result, archived)
			continue
		}
		if env.Record == nil || env.SchemaVersion != FormatVersion {
			archived.Reason = "archive entry carries an invalid envelope"
			result = append(result, archived)
			continue
		}
		archived.Record = *env.Record
		if validateErr := validateRecord(*env.Record); validateErr != nil {
			archived.Reason = validateErr.Error()
			result = append(result, archived)
			continue
		}
		archived.Verified = true
		result = append(result, archived)
	}
	return result, nil
}

// ArchivedRecords returns only the archived records that carry a complete
// terminal proof. Unverified (legacy) entries are omitted: they must never be
// used as closure or recovery evidence, and their absence from this view is what
// makes that structural rather than a caller convention.
func (j *FileJournal) ArchivedRecords() ([]Record, error) {
	entries, err := j.ReadArchive()
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if entry.Verified {
			result = append(result, entry.Record)
		}
	}
	return result, nil
}

func validateRecord(record Record) error {
	if record.SchemaVersion != FormatVersion {
		return fmt.Errorf("unsupported record schema version %d", record.SchemaVersion)
	}
	if !safeID(record.OperationID) {
		return errors.New("record operation id is invalid")
	}
	if !safeID(record.Channel) {
		return errors.New("record channel is invalid")
	}
	if !safeID(record.TargetReleaseID) {
		return errors.New("record target release id is invalid")
	}
	if !Terminal(record.Step) {
		return fmt.Errorf("record step %q is not terminal", record.Step)
	}
	if record.Step == StepRollbackBlocked {
		return errors.New("record step is rollback_blocked, which is never archived")
	}
	if target := record.FenceClosureTarget; target != "" && target != record.Step {
		return fmt.Errorf("record fence closure target %s is unsettled at step %s", target, record.Step)
	}
	if strings.TrimSpace(record.ManifestDigest) == "" {
		return errors.New("record manifest digest is missing")
	}
	if record.TargetReleaseSequence < 1 {
		return errors.New("record release sequence floor is missing")
	}
	if err := ValidateRecoveryDecision(record); err != nil {
		return err
	}
	if err := validateAbortTuple(record); err != nil {
		return err
	}
	return validateClosureProof(record)
}

func (j *FileJournal) readEnvelope() (envelope, error) {
	data, err := readFileBounded(j.path, maxJournalBytes)
	if errors.Is(err, os.ErrNotExist) {
		return envelope{SchemaVersion: FormatVersion}, nil
	}
	if errors.Is(err, errReadTooLarge) {
		return envelope{}, fmt.Errorf("%w: %v", ErrJournalCorrupted, err)
	}
	if err != nil {
		return envelope{}, fmt.Errorf("read update journal: %w", err)
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		_ = j.Close()
		dir := filepath.Dir(j.path)
		if isolateErr := quarantine(dir, baseName(j.path)); isolateErr != nil {
			return envelope{}, fmt.Errorf("%w (%v); quarantine failed: %v", ErrJournalCorrupted, err, isolateErr)
		}
		return envelope{}, fmt.Errorf("%w: quarantined %s (%v)", ErrJournalCorrupted, j.path, err)
	}
	if env.SchemaVersion != FormatVersion {
		return envelope{}, fmt.Errorf("%w: unsupported envelope schema version %d", ErrJournalCorrupted, env.SchemaVersion)
	}
	return env, nil
}

// writeEnvelope persists via temp file + fsync + atomic rename + parent-dir
// fsync, matching the crash-safety discipline of the supervisor hardening
// journal. Note: it does NOT take mu; callers hold it.
func (j *FileJournal) writeEnvelope(env envelope) error {
	dir := filepath.Dir(j.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create update journal directory: %w", err)
	}
	if dirErr := requireRealDir(dir); dirErr != nil {
		return fmt.Errorf("update journal directory is not usable: %w", dirErr)
	}
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("encode update journal: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".update-journal-*")
	if err != nil {
		return fmt.Errorf("create update journal temp: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() {
		if removeErr := os.Remove(temporaryName); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			// Preserve the primary persistence error; cleanup is best effort.
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect update journal temp: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write update journal temp: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync update journal temp: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close update journal temp: %w", err)
	}
	if err := os.Rename(temporaryName, j.path); err != nil {
		return fmt.Errorf("commit update journal: %w", err)
	}
	// fsync the parent directory so the rename itself survives a crash.
	return syncDirectory(dir)
}

func syncDirectory(path string) error {
	dirHandle, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dirHandle.Sync()
	closeErr := dirHandle.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// requireRealDir refuses anything that is not a real directory: a symlink (even
// one pointing at a directory) could redirect journal writes outside the
// deployment-owned volume.
func requireRealDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; refusing to write through it", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

// readFileBounded reads a file through a handle opened with O_NOFOLLOW: the
// read can never follow a symlink planted in place of the file, the size comes
// from the handle itself (not a raceable stat on the path), and the data is
// capped so a hostile or corrupt file cannot allocate without bound.
func readFileBounded(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%w: %s is %d bytes (limit %d)", errReadTooLarge, filepath.Base(path), info.Size(), limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s grew past the limit %d while being read", errReadTooLarge, filepath.Base(path), limit)
	}
	return data, nil
}
