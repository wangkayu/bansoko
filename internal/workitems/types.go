// Package workitems is the single asynchronous execution runtime.
// A WorkItem is a reliability record for a typed owner handler; product state
// and reasons live on the owning rows, never here.
package workitems

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"bansoko/internal/persistence/txscope"
)

var (
	// ErrNoDueWork reports that no due available or reclaimable leased item exists.
	ErrNoDueWork = errors.New("no due work item")
	// ErrStaleClaim rejects a finish/retry whose fencing token no longer matches.
	ErrStaleClaim = errors.New("stale work item claim")
	// ErrLiveItemConflict rejects a second live item for the same owner and kind.
	ErrLiveItemConflict = errors.New("a live work item already exists for this owner and kind")
	// ErrInvalidSpec rejects an unknown owner type, kind, or malformed spec.
	ErrInvalidSpec = errors.New("invalid work item spec")
	// ErrIdempotencyConflict rejects reuse of a request key with a different body.
	ErrIdempotencyConflict = errors.New("idempotency key was already used with a different request")
	// ErrRetentionInvalid rejects a retention outside the 1..30 day bound.
	ErrRetentionInvalid = errors.New("retention days must be between 1 and 30")
	// ErrNotFound reports a missing or non-terminal work item for manual retry.
	ErrNotFound = errors.New("failed work item not found for owner")
	// ErrOwnerRetired rejects admission for an owner whose lifecycle fence was advanced.
	ErrOwnerRetired = errors.New("managed anime is retired")
	// ErrPublicationUnresolved is the typed worker outcome when a maintenance
	// effect left an attempt whose result cannot be decided yet.
	ErrPublicationUnresolved = errors.New("publication reservation is unresolved")
	// ErrOneShotReclaiming is the typed worker outcome when a one-shot marker
	// left `running` or the claimed admission epoch is stale: perform no
	// external effect and mutate no owner or WorkItem row.
	ErrOneShotReclaiming = errors.New("one-shot acquisition is not admitting effects")
	// ErrInvalidOneShotClaim rejects a claim whose input snapshot declares a
	// malformed one-shot marker reference. The item stays leased until normal
	// expiry so a corrupt snapshot can never bypass the admission gate.
	ErrInvalidOneShotClaim = errors.New("one-shot work item claim carries an invalid marker reference")
	// ErrOneShotMarkerMissing reports a marker anchor that no longer resolves.
	ErrOneShotMarkerMissing = errors.New("one-shot acquisition marker was not found")
	// ErrInvalidOneShotLedger reports marker admission-ledger corruption. The
	// ledger is an internal integrity boundary: malformed, duplicate, or
	// ambiguous entries must fail closed before a worker can perform an effect.
	ErrInvalidOneShotLedger = errors.New("one-shot admission ledger is invalid")
)

// EffectDisposition is the typed external-effect result every one-shot handler
// returns through its Outcome. It decides settlement of the marker-owned
// admission lease: confirmed/no_external_effect consume the lease exactly
// once; ambiguous keeps the lease so reclamation stays blocked until lookup or
// recovery settles the disposition.
type EffectDisposition string

const (
	// DispositionNoExternalEffect records that this attempt provably performed
	// no mutating external effect (pre-effect validation failure, lookup-only
	// path, read-only inspection).
	DispositionNoExternalEffect EffectDisposition = "no_external_effect"
	// DispositionConfirmed records a known external disposition: the effect
	// happened and was verified, or deterministically did not happen.
	DispositionConfirmed EffectDisposition = "confirmed"
	// DispositionAmbiguous records an unknown or unresolved external result.
	// The admission lease stays unconsumed and the marker/owner remain
	// retained until lookup or recovery settles the disposition.
	DispositionAmbiguous EffectDisposition = "ambiguous"
)

// Owner types and kinds are code-owned allowlists matching the baseline
// check constraints; arbitrary request strings cannot create work.
const (
	OwnerRSSSourceRuntime    = "rss_source_runtime"
	OwnerRelease             = "release"
	OwnerDownloadTask        = "download_task"
	OwnerArtifact            = "artifact"
	OwnerFileOperation       = "file_operation"
	OwnerPluginDelivery      = "plugin_delivery"
	OwnerFileMaintenance     = "file_maintenance"
	OwnerAlignment           = "alignment_operation"
	OwnerReplacementDeletion = "replacement_deletion"
	OwnerOffsetMigration     = "offset_migration_operation"
	// OwnerPostProcessingPlan is the dedicated post-processing planning stage
	// where one frozen plan per verified artifact executes naming/target
	// planning in its own transaction so the artifact success never rolls back
	// for a naming failure.
	OwnerPostProcessingPlan = "postprocessing_plan"
	// OwnerFileManagerOperation is one confirmed user file operation (move,
	// rename, copy, delete) admitted from the file manager on a file no owner
	// manages; it carries no managed anime.
	OwnerFileManagerOperation = "file_manager_operation"
	// OwnerDownloadCompletion is the post-success action on the qBittorrent
	// task of one acquisition. It acts on a task, not on a managed
	// anime, so its WorkItems declare no lifecycle fence.
	OwnerDownloadCompletion = "download_completion"
	// OwnerAcquisition is one download root's terminal settlement item. The row
	// is inserted by the accept and settle SQL functions, never by the Go
	// store, so it appears in dbOnlyOwnerKinds rather than ownerKinds; the name
	// exists so the exclusion and the conformance set can refer to it.
	OwnerAcquisition = "acquisition"
)

const (
	KindRSSRefresh          = "rss_refresh"
	KindReleaseEvaluate     = "release_evaluate"
	KindQBSubmit            = "qb_submit"
	KindQBSync              = "qb_sync"
	KindArtifactInspect     = "artifact_inspect"
	KindFileExecute         = "file_execute"
	KindPluginDeliver       = "plugin_deliver"
	KindReconciliationScan  = "reconciliation_scan"
	KindAlignmentExecute    = "alignment_execute"
	KindReplacementEvaluate = "replacement_evaluate"
	// KindReplacementFallback is the fallback query of one anime: after an
	// instance acquisition settles, the best release each freed pool skipped as
	// covered_in_flight is evaluated again. It reads and enqueues only.
	KindReplacementFallback = "replacement_fallback"
	// KindReplacementDelete deletes one managed replacement loser: path-level
	// identity check, capture into a private namespace and purge, all inside
	// one claim. There is no quarantine or grace period.
	KindReplacementDelete = "replacement_delete"
	// KindReconciliationCalibrate is the completed-generation calibration
	// owner: it persists typed calibration facts, reconciles verified active
	// Placements, and fans the generation out to the two typed consumers.
	KindReconciliationCalibrate = "reconciliation_calibrate"
	// KindReconciliationConsumeAlignment is the alignment consumer of one
	// completed generation (independent checkpoint/retry from replacement).
	KindReconciliationConsumeAlignment = "reconciliation_consume_alignment"
	// KindReconciliationConsumeReplacement is the replacement consumer of one
	// completed generation (active-gated; stopped generations produce zero
	// replacement increments).
	KindReconciliationConsumeReplacement = "reconciliation_consume_replacement"
	// KindOffsetMigrationExecute is the durable execution kind for one frozen
	// offset migration operation. Its file-effect handler/recovery are wired by
	// a later slice.
	KindOffsetMigrationExecute = "offset_migration_execute"
	// KindPostProcessPlan executes one post-processing plan: naming/target
	// planning from the frozen verified artifact, freezing FileOperations and
	// their file_execute WorkItems. No external effect happens in this kind.
	KindPostProcessPlan = "postprocess_plan"
	// KindFileManagerExecute performs one user file operation's file effect
	// behind a durable attempt checkpoint.
	KindFileManagerExecute = "file_manager_execute"
	// KindCompletionExecute performs one acquisition's download-completion
	// action on its qBittorrent task. The action is idempotent (pause, remove
	// and remove-with-data all converge on an already-done task), so it is not
	// an external effect that needs admission or recovery gating.
	KindCompletionExecute = "completion_execute"
	// KindAcquisitionSettle is one download root's terminal settlement attempt.
	// Only the SQL accept/settle functions create it; see OwnerAcquisition.
	KindAcquisitionSettle = "acquisition_settle"
)

func KindHasExternalEffect(kind string) bool {
	switch kind {
	case KindQBSubmit, KindFileExecute, KindPluginDeliver, KindAlignmentExecute,
		KindReplacementDelete, KindOffsetMigrationExecute, KindFileManagerExecute:
		return true
	default:
		return false
	}
}

const (
	StateAvailable = "available"
	StateLeased    = "leased"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
)

// ownerKinds is the set of owner/kind pairs the Go store admits: the pairs it
// may create, claim and retry. It is NOT a copy of
// work_items_owner_kind_check — the schema is wider by exactly
// dbOnlyOwnerKinds, because those rows are created by SECURITY DEFINER SQL that
// never passes through this package. A conformance test compares the installed
// CHECK to ownerKinds ∪ dbOnlyOwnerKinds as closed sets, so the schema and this
// catalog cannot drift apart in either direction.
var ownerKinds = map[string]struct {
	kinds []string
}{
	OwnerRSSSourceRuntime:    {kinds: []string{KindRSSRefresh}},
	OwnerRelease:             {kinds: []string{KindReleaseEvaluate}},
	OwnerDownloadTask:        {kinds: []string{KindQBSubmit, KindQBSync}},
	OwnerArtifact:            {kinds: []string{KindArtifactInspect}},
	OwnerFileOperation:       {kinds: []string{KindFileExecute}},
	OwnerPluginDelivery:      {kinds: []string{KindPluginDeliver}},
	OwnerFileMaintenance:     {kinds: []string{KindReconciliationScan, KindReplacementEvaluate, KindReplacementFallback, KindReconciliationCalibrate, KindReconciliationConsumeAlignment, KindReconciliationConsumeReplacement}},
	OwnerAlignment:           {kinds: []string{KindAlignmentExecute}},
	OwnerReplacementDeletion: {kinds: []string{KindReplacementDelete}},
	// Offset migration operations are durable typed owners. The execute handler
	// and recovery lane are intentionally registered by a later slice.
	OwnerOffsetMigration: {kinds: []string{KindOffsetMigrationExecute}},
	// Post-processing plans execute the dedicated planning stage; the stage
	// performs no external effect, so file conflicts stay write-stage guarded.
	OwnerPostProcessingPlan: {kinds: []string{KindPostProcessPlan}},
	// User file operations of the file manager.
	OwnerFileManagerOperation: {kinds: []string{KindFileManagerExecute}},
	// Download completion of one acquisition.
	OwnerDownloadCompletion: {kinds: []string{KindCompletionExecute}},
}

// dbOnlyOwnerKinds are the owner/kind pairs work_items_owner_kind_check accepts
// but Go never creates and never validates for creation. The settle item is
// inserted by the accept and settle SECURITY DEFINER functions in SQL, so no Go
// path needs to pass OwnerRef.validate for it — but the store must still read
// and claim the row, which is why the pair cannot simply be dropped from the
// schema. Naming it here keeps the exclusion visible instead of leaving the
// installed CHECK silently wider than the Go catalog.
var dbOnlyOwnerKinds = []struct {
	ownerType string
	kind      string
}{
	{OwnerAcquisition, KindAcquisitionSettle},
}

// manualRetryOwnerTypes is the set of owners the manual-retry entry admits. It
// mirrors idempotency_records_owner_type_check, which is the schema row the
// request key lands in: a type admitted here but absent there would fail the
// insert inside the retry transaction after the owner row was already reset.
var manualRetryOwnerTypes = map[string]bool{
	OwnerRSSSourceRuntime: true,
	OwnerDownloadTask:     true,
	OwnerArtifact:         true,
}

// ManualRetryOwnerTypes returns the owner types the manual-retry entry admits,
// sorted. It exists so packages that must agree with this allowlist — the
// application service that routes each type to a version reader, and its tests —
// can compare against it instead of restating the list.
func ManualRetryOwnerTypes() []string {
	types := make([]string, 0, len(manualRetryOwnerTypes))
	for ownerType := range manualRetryOwnerTypes {
		types = append(types, ownerType)
	}
	sort.Strings(types)
	return types
}

type OwnerRef struct {
	Type string
	ID   string
}

// OneShotRef is the typed marker reference a finite one-shot WorkItem carries.
// It travels as structured metadata inside the input snapshot
// (`{"one_shot":{...}}`), never as a new owner type or kind. The store
// validates structural consistency before returning a claim so an ungated
// handler dispatch is impossible for a one-shot item.
type OneShotRef struct {
	MarkerID       string `json:"marker_id"`
	CorrelationID  string `json:"correlation_id"`
	AdmissionEpoch int64  `json:"admission_epoch"`
}

func (r OneShotRef) validate() error {
	if strings.TrimSpace(r.MarkerID) == "" || len(r.MarkerID) > 128 {
		return fmt.Errorf("%w: one-shot marker id is required and bounded", ErrInvalidSpec)
	}
	if strings.TrimSpace(r.CorrelationID) == "" || len(r.CorrelationID) > 128 {
		return fmt.Errorf("%w: one-shot correlation id is required and bounded", ErrInvalidSpec)
	}
	if r.AdmissionEpoch < 1 {
		return fmt.Errorf("%w: one-shot admission epoch must be at least 1", ErrInvalidSpec)
	}
	return nil
}

func (o OwnerRef) validate() error {
	if strings.TrimSpace(o.Type) == "" || strings.TrimSpace(o.ID) == "" {
		return fmt.Errorf("%w: owner type and owner ID are required", ErrInvalidSpec)
	}
	if _, ok := ownerKinds[o.Type]; !ok {
		return fmt.Errorf("%w: unknown owner type %q", ErrInvalidSpec, o.Type)
	}
	if len(o.ID) > 128 {
		return fmt.Errorf("%w: owner ID is too long", ErrInvalidSpec)
	}
	return nil
}

type WorkItemSpec struct {
	Owner                OwnerRef
	Kind                 string
	IdempotencyKey       string
	InputSnapshot        json.RawMessage
	AvailableAfter       time.Duration
	MaxAttempts          int
	ExpectedOwnerVersion int64

	// OneShot marks the spec as belonging to a finite one-shot acquisition.
	// When set, Create/CreateOrMerge merge the canonical one_shot object into
	// the stored input snapshot; CreateOrMergeOneShot additionally locks the
	// marker first. No new owner type or kind is introduced.
	OneShot *OneShotRef
}

func (s WorkItemSpec) validate() error {
	if err := s.Owner.validate(); err != nil {
		return err
	}
	shape := ownerKinds[s.Owner.Type]
	valid := false
	for _, kind := range shape.kinds {
		if s.Kind == kind {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("%w: kind %q is not valid for owner type %q", ErrInvalidSpec, s.Kind, s.Owner.Type)
	}
	if strings.TrimSpace(s.IdempotencyKey) == "" || len(s.IdempotencyKey) > 512 {
		return fmt.Errorf("%w: idempotency key is required and bounded", ErrInvalidSpec)
	}
	if s.MaxAttempts < 1 || s.MaxAttempts > 100 {
		return fmt.Errorf("%w: max attempts must be between 1 and 100", ErrInvalidSpec)
	}
	if s.ExpectedOwnerVersion < 1 {
		return fmt.Errorf("%w: expected owner version must be at least 1", ErrInvalidSpec)
	}
	if s.AvailableAfter < 0 || s.AvailableAfter > 30*24*time.Hour {
		return fmt.Errorf("%w: available delay is outside the allowed bounds", ErrInvalidSpec)
	}
	var existingOneShot *OneShotRef
	if len(s.InputSnapshot) > 0 {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(s.InputSnapshot, &probe); err != nil || probe == nil {
			if err != nil {
				return fmt.Errorf("%w: input snapshot must be a JSON object: %v", ErrInvalidSpec, err)
			}
			return fmt.Errorf("%w: input snapshot must be a JSON object", ErrInvalidSpec)
		}
		// Parse every snapshot, not only specs that already carry OneShot. A
		// declared marker must never be accepted by the ordinary Create path;
		// malformed marker metadata is surfaced as ErrInvalidOneShotClaim.
		var err error
		existingOneShot, err = parseOneShotRef(s.InputSnapshot)
		if err != nil {
			return err
		}
	}
	if s.OneShot != nil {
		if err := s.OneShot.validate(); err != nil {
			return err
		}
		// The declared ref and any one_shot object already present in the
		// snapshot must agree; the store merges the canonical object otherwise.
		if existingOneShot != nil && *existingOneShot != *s.OneShot {
			return fmt.Errorf("%w: input snapshot one_shot reference does not match the declared marker reference", ErrInvalidSpec)
		}
	} else if existingOneShot != nil {
		return fmt.Errorf("%w: input snapshot declares a one-shot marker without a gated spec", ErrInvalidOneShotClaim)
	}
	return nil
}

type Claim struct {
	ID                   string
	Owner                OwnerRef
	Kind                 string
	ExpectedOwnerVersion int64
	FencingToken         int64
	Attempt              int
	MaxAttempts          int
	IdempotencyKey       string
	InputSnapshot        json.RawMessage
	CancelRequested      bool
	ManagedAnimeID       string
	OwnerLifecycleEpoch  int64
	// OneShot is parsed from the input snapshot at claim time. A one-shot
	// claim must pass the marker admission gate before any external effect.
	OneShot *OneShotRef
}

// OwnerResult is the authoritative owner transition written by the handler's
// OwnerWriter inside the WorkItem transaction. State and reason codes must be
// allowlisted by the owning context, not by this package.
type OwnerResult struct {
	State      string
	ReasonCode string
	Reason     string
}

// OwnerWriter applies the concrete owner's state/reason/version transition in
// the same transaction as the WorkItem transition. It returns the new owner
// version; a CAS failure aborts the whole transaction.
type OwnerWriter func(ctx context.Context, tx txscope.Tx, result OwnerResult) (ownerVersion int64, err error)

// OwnerReader reads the current owner version inside the transaction.
type OwnerReader func(ctx context.Context, tx txscope.Tx) (ownerVersion int64, err error)

// OwnerResetter resets a failed owner inside the manual-retry transaction. It
// receives the version read by OwnerReader and must return the version written
// by the reset. The WorkItem created by that transaction uses the returned
// version as its ExpectedOwnerVersion.
type OwnerResetter func(ctx context.Context, tx txscope.Tx, expectedOwnerVersion int64) (newOwnerVersion int64, err error)

// ManualRetryInputCanonicalizer rebuilds a replacement claim snapshot from the
// current owner row while holding the manual-retry transaction.
type ManualRetryInputCanonicalizer func(ctx context.Context, tx txscope.Tx, owner OwnerRef, kind string, legacy json.RawMessage) (json.RawMessage, error)
