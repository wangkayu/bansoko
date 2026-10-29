package application

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bansoko/internal/batchplan"
	"bansoko/internal/library/displaypath"
	"bansoko/internal/redact"
)

// The pure half of the execution-record read: decoding the stored receipt and
// title, the fixed stage projection and the cursors. It reads no database and
// no clock, so each rule is testable from plain facts.

const (
	acquisitionDisplayMaxRunes = 512
	acquisitionDisplayMaxBytes = 2048
)

// acquisitionStageOwner is the current row of one stage owner (download task,
// artifact or plan).
type acquisitionStageOwner struct {
	State           string
	FailureCode     *string
	AttemptsStarted int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// WaitStartedAt is the download task's wait start; nil elsewhere.
	WaitStartedAt *time.Time
}

// acquisitionGraphFacts is everything the stage projection reads for a full root.
type acquisitionGraphFacts struct {
	Branch      string
	RootState   string
	TerminalAt  *time.Time
	BlockedCode *string
	Task        *acquisitionStageOwner
	Artifact    *acquisitionStageOwner
	Plan        *acquisitionStageOwner
	// LiveOperations counts file operations that can still run.
	LiveOperations int64
	Operations     int64
	// Requirements is nil until the requirement set is sealed.
	Requirements      *acquisitionRequirementFacts
	ManifestSealed    bool
	DeliveredOK       int64
	DeliveredFailed   int64
	FirstFailedReason *string
	LastDeliveryAt    *time.Time
	// ImportRetries counts the accepted import retries; ImportRetriedAt is the
	// latest one.
	ImportRetries   int64
	ImportRetriedAt *time.Time
	// PlacementsFailed counts the terminally failed placement handoffs of
	// completed files; they fail the import stage too.
	PlacementsFailed      int64
	PlacementFailedReason *string
	// Completion is the download-completion row of a succeeded download root;
	// nil until the completion pass has written it.
	Completion *acquisitionCompletionFacts
}

// acquisitionCompletionFacts is one download_completions row.
type acquisitionCompletionFacts struct {
	State      string
	ResultCode *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type acquisitionRequirementFacts struct {
	Items, Required int64
}

func ptr[T any](value T) *T { return &value }

// displayValue projects one stored text through the shared redaction policy. A
// value carrying a sensitive marker (URL, credential, absolute path) is
// redacted whole; nothing is extracted from it.
func displayValue(text *string) AcquisitionDisplayValue {
	if text == nil {
		return AcquisitionDisplayValue{State: AcquisitionDisplayUnavailable}
	}
	safe, sensitive := redact.Apply(*text, acquisitionDisplayMaxRunes, redact.Reject, redact.Redacted)
	if sensitive {
		return AcquisitionDisplayValue{State: AcquisitionDisplayRedacted}
	}
	for len(safe) > acquisitionDisplayMaxBytes {
		runes := []rune(safe)
		safe = string(runes[:len(runes)-1])
	}
	return AcquisitionDisplayValue{State: AcquisitionDisplayVisible, Value: &safe}
}

// libraryRelativeDisplay projects an absolute library path through the
// display root (displaypath) and the relative-path redaction: the library's
// own folders are shown, a credential, URL or other secret marker still
// redacts the value whole.
func libraryRelativeDisplay(root, path string) AcquisitionDisplayValue {
	relative := displaypath.Relative(root, path)
	if relative == "" {
		return AcquisitionDisplayValue{State: AcquisitionDisplayUnavailable}
	}
	safe, sensitive := redact.ApplyLibraryRelative(relative, acquisitionDisplayMaxRunes, redact.Reject, redact.Redacted)
	if sensitive || safe == "" {
		return AcquisitionDisplayValue{State: AcquisitionDisplayRedacted}
	}
	for len(safe) > acquisitionDisplayMaxBytes {
		runes := []rune(safe)
		safe = string(runes[:len(runes)-1])
	}
	return AcquisitionDisplayValue{State: AcquisitionDisplayVisible, Value: &safe}
}

// Collision outcomes of one record file.
const (
	AcquisitionCollisionNone        = "none"
	AcquisitionCollisionSkipped     = "skipped"
	AcquisitionCollisionVersioned   = "versioned"
	AcquisitionCollisionOverwritten = "overwritten"
)

// acquisitionTargetOccupied is the one reason code a blocked collision
// records, at plan time (the target held another file) or at publication
// (a file appeared at the target before the rename).
const acquisitionTargetOccupied = "TARGET_OCCUPIED"

// acquisitionCollisionFacts are the facts the file's plan and execution
// recorded about its target.
type acquisitionCollisionFacts struct {
	// FrozenCollision is the plan's frozen collision (block | version |
	// overwrite); empty when the plan froze no target spec for the file.
	FrozenCollision string
	// OperationCollision is the latest file operation's collision policy.
	OperationCollision string
	PlanKind           string
	PlanReasonCode     string
	OperationFailure   string
	DeliveryOutcome    string
	CompletionMode     string
	DeliveryReasonCode string
	// FrozenTarget is the plan's frozen canonical target; FinalPath the final
	// object's canonical path the successful delivery recorded.
	FrozenTarget string
	FinalPath    string
	// DisplacedState is the publish_target proof's displaced state (known |
	// absent | ...); empty when no proof is recorded.
	DisplacedState string
}

// acquisitionCollisionOutcome derives the collision outcome from recorded
// facts only; nil when the facts do not say (not delivered yet, adopted or
// retained in place, an older row without the proof, or any other failure).
//
//   - skipped: the file was refused with TARGET_OCCUPIED under the block
//     policy (plan-time refusals freeze no spec; only block produces them).
//   - none: executed under block (published without replacing anything), or
//     under version at the frozen target itself.
//   - versioned: executed under version at a path other than the frozen
//     target (the reserved version suffix).
//   - overwritten: executed under overwrite and the publication proof
//     recorded the replaced object.
func acquisitionCollisionOutcome(f acquisitionCollisionFacts) *string {
	policy := f.OperationCollision
	if policy == "" {
		policy = f.FrozenCollision
	}
	occupied := func(code string) bool { return strings.EqualFold(strings.TrimSpace(code), acquisitionTargetOccupied) }
	if f.DeliveryOutcome == "failed" || f.PlanKind == "failed" {
		refused := occupied(f.OperationFailure) || occupied(f.DeliveryReasonCode) || occupied(f.PlanReasonCode)
		if refused && (policy == "block" || (policy == "" && f.PlanKind == "failed")) {
			return ptr(AcquisitionCollisionSkipped)
		}
		return nil
	}
	if f.DeliveryOutcome != "succeeded" || f.CompletionMode != "executed" {
		return nil
	}
	switch policy {
	case "block":
		return ptr(AcquisitionCollisionNone)
	case "version":
		if f.FrozenTarget == "" || f.FinalPath == "" {
			return nil
		}
		if samePlacedPath(f.FrozenTarget, f.FinalPath) {
			return ptr(AcquisitionCollisionNone)
		}
		return ptr(AcquisitionCollisionVersioned)
	case "overwrite":
		if f.DisplacedState == "known" {
			return ptr(AcquisitionCollisionOverwritten)
		}
	}
	return nil
}

func samePlacedPath(left, right string) bool {
	return filepath.Clean(strings.TrimSpace(left)) == filepath.Clean(strings.TrimSpace(right))
}

// storedDisplayTitle decodes the root's frozen display_title
// ({state, value, reason_code}). A known value is projected; anything else is
// unavailable.
func storedDisplayTitle(raw []byte) (AcquisitionDisplayValue, error) {
	var stored struct {
		State string  `json:"state"`
		Value *string `json:"value"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return AcquisitionDisplayValue{}, fmt.Errorf("%w: display title: %v", ErrAcquisitionReadInconsistent, err)
	}
	if stored.State != "known" || stored.Value == nil {
		return AcquisitionDisplayValue{State: AcquisitionDisplayUnavailable}, nil
	}
	return displayValue(stored.Value), nil
}

// storedTerminalReceipt decodes native_terminal_v1. Any other schema or shape is
// an inconsistent read, never a guessed outcome.
func storedTerminalReceipt(schema string, raw []byte) (*AcquisitionOutcome, error) {
	if schema != "native_terminal_v1" {
		return nil, fmt.Errorf("%w: terminal schema %q", ErrAcquisitionReadInconsistent, schema)
	}
	var stored struct {
		SchemaVersion      int    `json:"schema_version"`
		Outcome            string `json:"outcome"`
		RequirementsStatus string `json:"requirements_status"`
		Counts             *struct {
			Total     int64 `json:"total"`
			Required  int64 `json:"required"`
			Excluded  int64 `json:"excluded"`
			Succeeded int64 `json:"succeeded"`
			Failed    int64 `json:"failed"`
		} `json:"counts"`
		PrimaryFailure *struct {
			Stage      string  `json:"stage"`
			ReasonCode string  `json:"reason_code"`
			FileRef    *string `json:"file_ref"`
		} `json:"primary_failure"`
		EffectSummary struct {
			Complete     bool `json:"complete"`
			HasConfirmed bool `json:"has_confirmed"`
			HasUnknown   bool `json:"has_unknown"`
		} `json:"effect_summary"`
		SettledAt time.Time `json:"settled_at"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("%w: terminal receipt: %v", ErrAcquisitionReadInconsistent, err)
	}
	switch {
	case stored.SchemaVersion != 1:
		return nil, fmt.Errorf("%w: terminal receipt version %d", ErrAcquisitionReadInconsistent, stored.SchemaVersion)
	case stored.Outcome != "succeeded" && stored.Outcome != "failed" && stored.Outcome != "cancelled":
		return nil, fmt.Errorf("%w: terminal outcome %q", ErrAcquisitionReadInconsistent, stored.Outcome)
	case stored.RequirementsStatus != "not_reached" && stored.RequirementsStatus != "incomplete" && stored.RequirementsStatus != "sealed":
		return nil, fmt.Errorf("%w: requirements status %q", ErrAcquisitionReadInconsistent, stored.RequirementsStatus)
	case stored.Outcome != "succeeded" && stored.PrimaryFailure == nil:
		return nil, fmt.Errorf("%w: %s receipt without primary failure", ErrAcquisitionReadInconsistent, stored.Outcome)
	}
	outcome := &AcquisitionOutcome{
		Outcome: stored.Outcome, RequirementsStatus: stored.RequirementsStatus,
		EffectsComplete: stored.EffectSummary.Complete, HasConfirmed: stored.EffectSummary.HasConfirmed,
		HasUnknown: stored.EffectSummary.HasUnknown, SettledAt: stored.SettledAt,
	}
	if stored.Counts != nil {
		outcome.Counts = &AcquisitionCounts{Total: stored.Counts.Total, Required: stored.Counts.Required,
			Excluded: stored.Counts.Excluded, Succeeded: stored.Counts.Succeeded, Failed: stored.Counts.Failed}
	}
	if stored.PrimaryFailure != nil {
		outcome.PrimaryFailure = &AcquisitionFailure{Stage: stored.PrimaryFailure.Stage,
			ReasonCode: stored.PrimaryFailure.ReasonCode, FileRef: stored.PrimaryFailure.FileRef}
	}
	return outcome, nil
}

func presentStage(name, state string) AcquisitionStage {
	return AcquisitionStage{Stage: name, Availability: AcquisitionStagePresent, State: ptr(state)}
}

func absentStage(name, availability string) AcquisitionStage {
	return AcquisitionStage{Stage: name, Availability: availability}
}

func ownerAttempts(owner *acquisitionStageOwner) *int64 {
	if owner.AttemptsStarted <= 0 {
		return nil
	}
	return ptr(owner.AttemptsStarted)
}

// deriveAcquisitionStages projects the seven fixed stages of a full root, in
// order. A stage with no owner row yet is not_reached, never failed; an
// adopted root runs no download, inspection, plan or file execution.
func deriveAcquisitionStages(f acquisitionGraphFacts) []AcquisitionStage {
	stages := make([]AcquisitionStage, 0, len(acquisitionStageOrder))
	if f.Branch == "adopted" {
		for _, name := range acquisitionStageOrder[:5] {
			stages = append(stages, absentStage(name, AcquisitionStageNotApplicable))
		}
	} else {
		stages = append(stages, submitStages(f.Task)...)
		stages = append(stages, inspectStage(f.Artifact), planStage(f.Plan), fileExecutionStage(f))
	}
	stages = append(stages, settlementStage(f))
	if f.RootState != "running" {
		// The root is settled: a stage that never finished stopped with it.
		for i := range stages {
			if stages[i].State != nil && (*stages[i].State == AcquisitionStageStatePending || *stages[i].State == AcquisitionStageStateRunning ||
				*stages[i].State == AcquisitionStageStateWaitingConfig) {
				stages[i].State = ptr(AcquisitionStageStateCancelled)
			}
		}
	}
	// The completion stage starts after the root settled, so the cancel rule
	// above never applies to it.
	return append(stages, completionStage(f))
}

// completionStage is the download-completion action on the qBittorrent task.
// It only exists for a succeeded download root; before the completion pass
// has written its row the stage is not reached yet. A seed completion is a
// succeeded stage whose result says the task was left seeding.
func completionStage(f acquisitionGraphFacts) AcquisitionStage {
	switch {
	case f.Branch == "adopted" || f.RootState == "failed" || f.RootState == "cancelled":
		return absentStage(AcquisitionStageCompletion, AcquisitionStageNotApplicable)
	case f.RootState == "running" || f.Completion == nil:
		return absentStage(AcquisitionStageCompletion, AcquisitionStageNotReached)
	}
	state := AcquisitionStageStateRunning
	switch f.Completion.State {
	case "succeeded":
		state = AcquisitionStageStateSucceeded
	case "failed":
		state = AcquisitionStageStateFailed
	}
	stage := presentStage(AcquisitionStageCompletion, state)
	stage.StartedAt = ptr(f.Completion.CreatedAt)
	stage.UpdatedAt = ptr(f.Completion.UpdatedAt)
	stage.ReasonCode = f.Completion.ResultCode
	return stage
}

// The DownloadTask failure codes of a download that ended before import
// without failing: deleted in the downloader, or cancelled because its
// subgroup left the selection or the user cancelled it. The root settles them
// cancelled, so the stage is cancelled too, never a failure.
const (
	downloaderTaskRemoved   = "DOWNLOADER_TASK_REMOVED"
	providerGroupNotAllowed = "PROVIDER_GROUP_NOT_ALLOWED"
	userCancelled           = "USER_CANCELLED"
)

// failedTaskStageState is the stage state of a failed download task.
func failedTaskStageState(task *acquisitionStageOwner) string {
	if task.FailureCode != nil {
		switch *task.FailureCode {
		case downloaderTaskRemoved, providerGroupNotAllowed, userCancelled:
			return AcquisitionStageStateCancelled
		}
	}
	return AcquisitionStageStateFailed
}

func submitStages(task *acquisitionStageOwner) []AcquisitionStage {
	if task == nil {
		return []AcquisitionStage{absentStage(AcquisitionStageSubmit, AcquisitionStageNotReached), absentStage(AcquisitionStageDownloadWait, AcquisitionStageNotReached)}
	}
	submitted := task.WaitStartedAt != nil || task.State == "downloading" || task.State == "completed"
	var submit, wait AcquisitionStage
	if !submitted {
		state := AcquisitionStageStateRunning
		switch task.State {
		case "queued":
			state = AcquisitionStageStatePending
		case "failed":
			state = failedTaskStageState(task)
		}
		submit = presentStage(AcquisitionStageSubmit, state)
		submit.UpdatedAt = ptr(task.UpdatedAt)
		if state != AcquisitionStageStatePending {
			submit.ReasonCode = task.FailureCode
		}
		wait = absentStage(AcquisitionStageDownloadWait, AcquisitionStageNotReached)
	} else {
		submit = presentStage(AcquisitionStageSubmit, AcquisitionStageStateSucceeded)
		submit.UpdatedAt = task.WaitStartedAt
		state := AcquisitionStageStateRunning
		switch task.State {
		case "completed":
			state = AcquisitionStageStateSucceeded
		case "failed":
			state = failedTaskStageState(task)
		}
		wait = presentStage(AcquisitionStageDownloadWait, state)
		wait.StartedAt = task.WaitStartedAt
		wait.UpdatedAt = ptr(task.UpdatedAt)
		if state != AcquisitionStageStateSucceeded {
			wait.ReasonCode = task.FailureCode
		}
	}
	submit.StartedAt = ptr(task.CreatedAt)
	submit.AttemptsStarted = ownerAttempts(task)
	return []AcquisitionStage{submit, wait}
}

func ownerStage(name string, owner *acquisitionStageOwner, states map[string]string) AcquisitionStage {
	if owner == nil {
		return absentStage(name, AcquisitionStageNotReached)
	}
	state, ok := states[owner.State]
	if !ok {
		state = AcquisitionStageStateRunning
	}
	stage := presentStage(name, state)
	stage.StartedAt = ptr(owner.CreatedAt)
	stage.UpdatedAt = ptr(owner.UpdatedAt)
	stage.AttemptsStarted = ownerAttempts(owner)
	if state != AcquisitionStageStateSucceeded && state != AcquisitionStageStatePending {
		stage.ReasonCode = owner.FailureCode
	}
	return stage
}

func inspectStage(artifact *acquisitionStageOwner) AcquisitionStage {
	return ownerStage(AcquisitionStageInspect, artifact, map[string]string{
		"pending": AcquisitionStageStateRunning, "waiting_retry": AcquisitionStageStateRunning,
		"verified": AcquisitionStageStateSucceeded, "failed": AcquisitionStageStateFailed,
	})
}

func planStage(plan *acquisitionStageOwner) AcquisitionStage {
	return ownerStage(AcquisitionStagePlan, plan, map[string]string{
		"queued": AcquisitionStageStatePending, "waiting_config": AcquisitionStageStateWaitingConfig,
		"succeeded": AcquisitionStageStateSucceeded, "failed": AcquisitionStageStateFailed,
	})
}

// decodeConfigHold reads a waiting plan's hold ([{"item","files"}], items
// binding_missing:bgm|tmdb or template_variable_undefined:<names>) in the
// stored order. A missing or unknown hold on a waiting plan is an
// inconsistent read.
func decodeConfigHold(raw []byte) ([]AcquisitionConfigHoldItem, error) {
	var stored []struct {
		Item  string `json:"item"`
		Files int64  `json:"files"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil || len(stored) == 0 {
		return nil, fmt.Errorf("%w: plan configuration hold", ErrAcquisitionReadInconsistent)
	}
	hold := make([]AcquisitionConfigHoldItem, 0, len(stored))
	for _, entry := range stored {
		item := AcquisitionConfigHoldItem{Files: entry.Files}
		switch names, variable := strings.CutPrefix(entry.Item, "template_variable_undefined:"); {
		case variable:
			item.Missing, item.Variables = AcquisitionConfigMissingVariable, strings.Split(names, ",")
		case entry.Item == "binding_missing:bgm":
			item.Missing = AcquisitionConfigMissingBGM
		case entry.Item == "binding_missing:tmdb":
			item.Missing = AcquisitionConfigMissingTMDB
		default:
			return nil, fmt.Errorf("%w: plan configuration hold item %q", ErrAcquisitionReadInconsistent, entry.Item)
		}
		hold = append(hold, item)
	}
	return hold, nil
}

// fileExecutionStage is the aggregate over every required file: delivered of
// required. A file without a result is never counted as done.
func fileExecutionStage(f acquisitionGraphFacts) AcquisitionStage {
	planned := f.Plan != nil && f.Plan.State == "succeeded"
	delivered := f.DeliveredOK + f.DeliveredFailed
	if !planned && f.Operations == 0 && delivered == 0 {
		return absentStage(AcquisitionStageFileExecution, AcquisitionStageNotReached)
	}
	state := AcquisitionStageStatePending
	var total *int64
	if f.Requirements != nil {
		total = ptr(f.Requirements.Required)
	}
	switch {
	case total != nil && delivered >= *total && (f.DeliveredFailed > 0 || f.PlacementsFailed > 0):
		state = AcquisitionStageStateFailed
	case total != nil && delivered >= *total:
		state = AcquisitionStageStateSucceeded
	case f.LiveOperations > 0 || f.Operations > 0 || delivered > 0:
		state = AcquisitionStageStateRunning
	}
	stage := presentStage(AcquisitionStageFileExecution, state)
	stage.Done = ptr(delivered)
	stage.Total = total
	stage.UpdatedAt = f.LastDeliveryAt
	if f.DeliveredFailed > 0 {
		stage.ReasonCode = f.FirstFailedReason
	} else if f.PlacementsFailed > 0 && state == AcquisitionStageStateFailed {
		stage.ReasonCode = f.PlacementFailedReason
	}
	if f.ImportRetries > 0 {
		// Each accepted import retry is one more run of this stage; the
		// earlier runs stay counted.
		stage.AttemptsStarted = ptr(1 + f.ImportRetries)
		stage.StartedAt = f.ImportRetriedAt
	}
	return stage
}

func settlementStage(f acquisitionGraphFacts) AcquisitionStage {
	if f.RootState == "running" {
		if f.BlockedCode != nil {
			stage := presentStage(AcquisitionStageSettlement, AcquisitionStageStateBlocked)
			stage.ReasonCode = f.BlockedCode
			return stage
		}
		return presentStage(AcquisitionStageSettlement, AcquisitionStageStatePending)
	}
	state := AcquisitionStageStateSucceeded
	switch f.RootState {
	case "failed":
		state = AcquisitionStageStateFailed
	case "cancelled":
		state = AcquisitionStageStateCancelled
	}
	stage := presentStage(AcquisitionStageSettlement, state)
	stage.UpdatedAt = f.TerminalAt
	return stage
}

// requirementsView is the requirement status of a full root.
func requirementsView(f acquisitionGraphFacts) *AcquisitionRequirements {
	if f.Requirements != nil {
		return &AcquisitionRequirements{Status: "sealed", FileCount: ptr(f.Requirements.Items),
			RequiredCount: ptr(f.Requirements.Required), ExcludedCount: ptr(f.Requirements.Items - f.Requirements.Required)}
	}
	if f.ManifestSealed {
		return &AcquisitionRequirements{Status: "incomplete"}
	}
	return &AcquisitionRequirements{Status: "not_reached"}
}

// summaryPosition is the stage a record row reports: where a running root is,
// or where a terminal root failed. A success reports none.
func summaryPosition(state string, stages []AcquisitionStage, outcome *AcquisitionOutcome) (stage, stageState, reason *string) {
	if state != "running" {
		if outcome != nil && outcome.PrimaryFailure != nil {
			return ptr(outcome.PrimaryFailure.Stage), ptr(outcome.Outcome), ptr(outcome.PrimaryFailure.ReasonCode)
		}
		return nil, nil, nil
	}
	for _, s := range stages {
		switch {
		case s.Availability == AcquisitionStageNotApplicable:
			continue
		case s.Availability == AcquisitionStageNotReached:
			return ptr(s.Stage), ptr(AcquisitionStageStatePending), nil
		case *s.State != AcquisitionStageStateSucceeded:
			return ptr(s.Stage), s.State, s.ReasonCode
		}
	}
	return ptr(AcquisitionStageSettlement), ptr(AcquisitionStageStatePending), nil
}

// The two cursors are opaque positions, not authorization: every read still
// filters on the access scope and, for files, on the root. A record cursor
// also carries the digest of the filters it was issued under, so it cannot
// continue a page of a different filter set.

type recordCursor struct {
	Version int       `json:"v"`
	At      time.Time `json:"at"`
	ID      string    `json:"id"`
	Filter  string    `json:"f"`
}

// recordFilterDigest is the digest of the list filters (not the limit):
// anime, state, entry kind, the accepted-time bounds in UTC nanoseconds and
// the origin.
func recordFilterDigest(query AcquisitionRecordQuery) string {
	bound := func(at *time.Time) string {
		if at == nil {
			return ""
		}
		return strconv.FormatInt(at.UTC().UnixNano(), 10)
	}
	parts := []string{"acquisition-records:v1", query.ManagedAnimeID, query.State, query.EntryKind, bound(query.AcceptedFrom), bound(query.AcceptedTo)}
	// Appended only when set, so a cursor issued without an origin keeps its digest.
	if query.Origin != "" {
		parts = append(parts, "origin:"+query.Origin)
	}
	canonical, _ := json.Marshal(parts)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:16])
}

type fileCursor struct {
	Version int    `json:"v"`
	Root    string `json:"root"`
	After   int64  `json:"after"`
}

const cursorMaxBytes = 512

func encodeCursor(value any) string {
	raw, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(text string, into any) bool {
	if text == "" || len(text) > cursorMaxBytes || strings.ContainsAny(text, "=+/ \t\r\n") {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(into) == nil && !decoder.More()
}

func decodeRecordCursor(text string) (recordCursor, bool) {
	var cursor recordCursor
	if !decodeCursor(text, &cursor) || cursor.Version != 1 || cursor.ID == "" || len(cursor.ID) > 128 || cursor.At.IsZero() || len(cursor.Filter) != 32 {
		return recordCursor{}, false
	}
	return cursor, true
}

func decodeFileCursor(text, root string) (fileCursor, bool) {
	var cursor fileCursor
	if !decodeCursor(text, &cursor) || cursor.Version != 1 || cursor.Root != root || cursor.After < 0 || cursor.After > 9999 {
		return fileCursor{}, false
	}
	return cursor, true
}

// acquisitionEpisodesMax bounds the episode list of one record row.
const acquisitionEpisodesMax = 500

// frozenPlanEpisodes projects the coverage keys of the frozen batch plan onto
// episode numbers (batchplan.Plan.Episodes: the same projection the release
// notifications use), ascending and unique. A plan that covers no numbered TV
// episode, an undecodable value or a list over the bound reads as nil: the
// record then names its release instead.
func frozenPlanEpisodes(raw sql.NullString) []int {
	if !raw.Valid {
		return nil
	}
	var keys []string
	if json.Unmarshal([]byte(raw.String), &keys) != nil || len(keys) == 0 {
		return nil
	}
	plan := batchplan.Plan{CoverageKeys: keys}
	episodes, ok := plan.Episodes()
	if !ok {
		return nil
	}
	unique := make([]int, 0, len(episodes))
	for index, value := range episodes {
		if value < 0 {
			return nil
		}
		if index == 0 || value != episodes[index-1] {
			unique = append(unique, value)
		}
	}
	if len(unique) == 0 || len(unique) > acquisitionEpisodesMax {
		return nil
	}
	return unique
}

// downloadTransfer is the download sync's last persisted observation: the
// verified byte high-water mark over the payload total, and the provider speed
// the same poll recorded. Without a byte observation and a positive total
// there is nothing to show.
func downloadTransfer(completed, total sql.NullInt64, speed sql.NullString, observedAt sql.NullTime) *AcquisitionTransfer {
	if !completed.Valid || !total.Valid || total.Int64 <= 0 || completed.Int64 < 0 || completed.Int64 > total.Int64 || !observedAt.Valid {
		return nil
	}
	transfer := &AcquisitionTransfer{CompletedBytes: completed.Int64, TotalBytes: total.Int64, ObservedAt: observedAt.Time}
	if speed.Valid {
		if value, err := strconv.ParseInt(strings.TrimSpace(speed.String), 10, 64); err == nil && value >= 0 {
			transfer.DownloadSpeed = &value
		}
	}
	return transfer
}

// frozenSeedValue is one {state, value} field of the accepted input's source
// block; only a known value carries a value.
type frozenSeedValue struct {
	State string          `json:"state"`
	Value json.RawMessage `json:"value"`
}

func (v frozenSeedValue) text() *string {
	if v.State != "known" {
		return nil
	}
	var text string
	if json.Unmarshal(v.Value, &text) != nil {
		return nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return &text
}

func (v frozenSeedValue) integer() *int64 {
	if v.State != "known" {
		return nil
	}
	var number int64
	if json.Unmarshal(v.Value, &number) != nil || number < 0 {
		return nil
	}
	return &number
}

// unnamedSeedGroup is the label a one-shot keyword-search seed freezes when
// its title names no group; it names nothing, so it reads as no group.
const unnamedSeedGroup = "unknown"

// projectSeedIdentity builds the seed identity from the accepted input's
// source block (group, site, size, info hash) and the source release's frozen
// facts (title tags, release version, publish time). Either half may be absent
// or undecodable; what cannot be read is left out, never guessed. The torrent
// location is not selected by the read, so it cannot appear here.
func projectSeedIdentity(source, release []byte) *AcquisitionSeedIdentity {
	var seed AcquisitionSeedIdentity
	var frozen struct {
		Group       frozenSeedValue `json:"group"`
		Provider    frozenSeedValue `json:"provider"`
		InfoHashV1  frozenSeedValue `json:"infohash_v1"`
		InfoHashV2  frozenSeedValue `json:"infohash_v2"`
		TorrentSize frozenSeedValue `json:"torrent_size"`
	}
	if len(source) > 0 && json.Unmarshal(source, &frozen) == nil {
		if group := frozen.Group.text(); group != nil && !strings.EqualFold(*group, unnamedSeedGroup) {
			seed.GroupName = group
		}
		if provider := frozen.Provider.text(); provider != nil && (*provider == "mikan" || *provider == "anibt") {
			seed.Provider = provider
		}
		seed.SizeBytes = frozen.TorrentSize.integer()
		if seed.InfoHash = frozen.InfoHashV1.text(); seed.InfoHash == nil {
			seed.InfoHash = frozen.InfoHashV2.text()
		}
	}
	var facts struct {
		TitleTags *struct {
			Tags      []string `json:"tags"`
			ValueTags []string `json:"value_tags"`
		} `json:"title_tags"`
		AniBT *struct {
			Tags []string `json:"tags"`
		} `json:"anibt"`
		PublishedAt *time.Time `json:"published_at"`
	}
	if len(release) > 0 && json.Unmarshal(release, &facts) == nil {
		if facts.TitleTags != nil {
			if len(facts.TitleTags.Tags) > 0 {
				seed.TitleTags = facts.TitleTags.Tags
			}
			for _, tag := range facts.TitleTags.ValueTags {
				if value, ok := strings.CutPrefix(tag, "version:"); ok && strings.TrimSpace(value) != "" {
					value = strings.TrimSpace(value)
					seed.ReleaseVersion = &value
					break
				}
			}
		}
		if facts.AniBT != nil && len(facts.AniBT.Tags) > 0 {
			seed.AniBTTags = facts.AniBT.Tags
		}
		if facts.PublishedAt != nil && !facts.PublishedAt.IsZero() {
			seed.PublishedAt = facts.PublishedAt
		}
	}
	if seed.GroupName == nil && seed.Provider == nil && seed.SizeBytes == nil && seed.PublishedAt == nil &&
		seed.InfoHash == nil && seed.ReleaseVersion == nil && len(seed.TitleTags) == 0 && len(seed.AniBTTags) == 0 {
		return nil
	}
	return &seed
}
