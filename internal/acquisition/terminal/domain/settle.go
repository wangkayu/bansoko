package domain

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// The pure settler. Given the facts of one root that the caller read under the
// root lock, it decides one of three things: the root keeps running, the root
// settles with this terminal receipt, or the graph is structurally broken. It
// reads no clock, no database and no configuration; every input is a field, so
// the same facts always give the same decision and each rule can be tested alone.
//
// The rules are the settlement predicate of the execution design: a live
// business work keeps the root running however much has already failed, since an
// independent file may still finish; a missing result with no live work and no
// definite upstream failure is a broken graph, never a quiet success; a required
// failure makes the whole outcome failed; an unknown effect blocks success.

// Safe reason codes the settler itself raises. Every other reason comes from a
// recorded fact.
const (
	ReasonNoRequiredFiles Reason = "NO_REQUIRED_FILES"
	ReasonEffectUnknown   Reason = "EFFECT_UNKNOWN"
)

// ErrExecutionGraphIncomplete is the structural answer: the facts cannot be
// settled and cannot be waited on. The caller records EXECUTION_GRAPH_INCOMPLETE
// and never settles around it.
var ErrExecutionGraphIncomplete = errors.New("EXECUTION_GRAPH_INCOMPLETE")

// Branch is the execution branch of the root.
type Branch string

const (
	BranchDownload Branch = "download"
)

// EffectClass is the settled class of an outward effect.
type EffectClass string

const (
	EffectUnknown   EffectClass = "unknown"
	EffectNone      EffectClass = "no_external_effect"
	EffectConfirmed EffectClass = "confirmed"
)

// FileResult is the recorded delivery result of one required file.
type FileResult struct {
	Outcome    Outcome
	Effect     EffectClass
	ReasonCode Reason // set exactly when the outcome is failed
}

// FileFact is one manifest file as the settler sees it: whether it is required
// and the delivery result it already has, if any.
type FileFact struct {
	Ref      ID
	Required bool
	Result   *FileResult
}

// SettlementFacts is everything the settler may look at.
type SettlementFacts struct {
	Branch Branch

	// Requirements describes the file requirement set. For a sealed set the digests
	// and the header counts are its own; Files lists every manifest file.
	Requirements       RequirementsStatus
	RequirementsDigest *DigestText
	TargetDigest       *DigestText
	HeaderItemCount    int64
	HeaderRequired     int64
	Files              []FileFact

	// LiveWork is true while any business work other than the settle itself can
	// still produce an effect or a result.
	LiveWork bool

	// Upstream is a definite failure upstream of the files (the task, the
	// inspection, the plan), when there is one.
	Upstream *Failure

	// HasConfirmedEffect and HasUnknownEffect summarise the effect evidence beyond
	// the per-file results.
	HasConfirmedEffect bool
	HasUnknownEffect   bool

	SettledAt time.Time
}

// DecisionKind is what the settler decided.
type DecisionKind int

const (
	// Continue keeps the root running: something can still change it.
	Continue DecisionKind = iota
	// Settle ends the root with Receipt.
	Settle
)

// Synthesized is a failed delivery result the settler writes for a required file
// that never got one because the plan failed upstream: a typed result carrying
// the upstream reason, never an invented operation.
type Synthesized struct {
	FileRef    ID
	ReasonCode Reason
}

// Decision is the settler's answer.
type Decision struct {
	Kind        DecisionKind
	Receipt     TerminalReceipt
	Synthesized []Synthesized
}

// Decide settles the facts. A returned error is always ErrExecutionGraphIncomplete,
// wrapped with what was missing; a malformed fact set (a duplicate file, a
// result on an excluded file) is the same kind of error because it cannot be
// trusted either.
func Decide(f SettlementFacts) (Decision, error) {
	if f.Branch != BranchDownload {
		return Decision{}, incomplete("unknown branch %q", f.Branch)
	}
	if f.LiveWork {
		return Decision{Kind: Continue}, nil
	}
	if f.Requirements != RequirementsSealed {
		return settleUnsealed(f)
	}
	return settleSealed(f)
}

// settleUnsealed ends a root whose requirements never sealed. That is only
// legitimate when something definite upstream failed.
func settleUnsealed(f SettlementFacts) (Decision, error) {
	if f.Requirements != RequirementsNotReached && f.Requirements != RequirementsIncomplete {
		return Decision{}, incomplete("unknown requirements status %q", f.Requirements)
	}
	if f.RequirementsDigest != nil || f.TargetDigest != nil {
		return Decision{}, incomplete("an unsealed set cannot carry a digest")
	}
	if f.Upstream == nil {
		return Decision{}, incomplete("requirements are %s with no live work and no definite failure", f.Requirements)
	}
	failure := *f.Upstream
	return Decision{Kind: Settle, Receipt: TerminalReceipt{
		Outcome: upstreamOutcome(failure), RequirementsStatus: f.Requirements, PrimaryFailure: &failure,
		Effects:   EffectSummary{Complete: false, HasConfirmed: f.HasConfirmedEffect, HasUnknown: f.HasUnknownEffect},
		SettledAt: f.SettledAt.UTC().Truncate(time.Microsecond),
	}}, nil
}

func settleSealed(f SettlementFacts) (Decision, error) {
	if f.RequirementsDigest == nil {
		return Decision{}, incomplete("a sealed set needs its requirements digest")
	}
	seen := make(map[ID]struct{}, len(f.Files))
	files := make([]FileFact, 0, len(f.Files))
	var required int64
	for _, file := range f.Files {
		if file.Ref == "" {
			return Decision{}, incomplete("a file with no reference")
		}
		if _, dup := seen[file.Ref]; dup {
			return Decision{}, incomplete("file %s listed twice", file.Ref)
		}
		seen[file.Ref] = struct{}{}
		if !file.Required && file.Result != nil {
			return Decision{}, incomplete("excluded file %s has a delivery result", file.Ref)
		}
		if file.Required {
			required++
		}
		files = append(files, file)
	}
	if int64(len(files)) != f.HeaderItemCount || required != f.HeaderRequired {
		return Decision{}, incomplete("the file rows (%d, %d required) disagree with the sealed header (%d, %d required)",
			len(files), required, f.HeaderItemCount, f.HeaderRequired)
	}
	// A stable order makes the primary failure independent of how the rows were read.
	sort.Slice(files, func(i, j int) bool { return files[i].Ref < files[j].Ref })

	counts := Counts{Total: int64(len(files)), Required: required, Excluded: int64(len(files)) - required}
	hasConfirmed, hasUnknown := f.HasConfirmedEffect, f.HasUnknownEffect
	var firstFailed *Failure
	var synthesized []Synthesized
	for _, file := range files {
		if !file.Required {
			continue
		}
		result := file.Result
		if result == nil {
			// No result and nothing live can still make one: the plan failed upstream and the
			// settler writes the typed failure, or the graph is broken.
			if f.Upstream == nil {
				return Decision{}, incomplete("required file %s has no result, no live work and no upstream failure", file.Ref)
			}
			result = &FileResult{Outcome: OutcomeFailed, Effect: EffectNone, ReasonCode: f.Upstream.ReasonCode}
			synthesized = append(synthesized, Synthesized{FileRef: file.Ref, ReasonCode: f.Upstream.ReasonCode})
		}
		if err := checkResult(file.Ref, *result); err != nil {
			return Decision{}, err
		}
		switch result.Effect {
		case EffectConfirmed:
			hasConfirmed = true
		case EffectUnknown:
			hasUnknown = true
		}
		if result.Outcome == OutcomeSucceeded {
			counts.Succeeded++
			continue
		}
		counts.Failed++
		if firstFailed == nil {
			ref := file.Ref
			firstFailed = &Failure{Stage: StageFileExec, ReasonCode: result.ReasonCode, FileRef: &ref}
		}
	}

	receipt := TerminalReceipt{
		RequirementsStatus: RequirementsSealed, Counts: &counts, RequirementsDigest: f.RequirementsDigest, TargetDigest: f.TargetDigest,
		Effects:   EffectSummary{Complete: !hasUnknown, HasConfirmed: hasConfirmed, HasUnknown: hasUnknown},
		SettledAt: f.SettledAt.UTC().Truncate(time.Microsecond),
	}
	switch {
	case f.Upstream != nil:
		failure := *f.Upstream
		receipt.Outcome, receipt.PrimaryFailure = upstreamOutcome(failure), &failure
	case required == 0:
		receipt.Outcome = OutcomeFailed
		receipt.PrimaryFailure = &Failure{Stage: StagePlan, ReasonCode: ReasonNoRequiredFiles}
	case firstFailed != nil:
		receipt.Outcome, receipt.PrimaryFailure = OutcomeFailed, firstFailed
	case hasUnknown:
		// Every file succeeded but an effect is still unknown: that blocks success.
		receipt.Outcome = OutcomeFailed
		receipt.PrimaryFailure = &Failure{Stage: StageFileExec, ReasonCode: ReasonEffectUnknown}
	default:
		receipt.Outcome = OutcomeSucceeded
	}
	return Decision{Kind: Settle, Receipt: receipt, Synthesized: synthesized}, nil
}

// upstreamOutcome is the outcome a definite upstream failure ends the root with.
// A download task deleted in the downloader before it completed is a
// cancellation, not a failure; every other upstream
// failure, including the same code at any later stage, fails the root.
func upstreamOutcome(f Failure) Outcome {
	if f.ReasonCode == ReasonDownloaderTaskRemoved && (f.Stage == StageSubmit || f.Stage == StageDownloadWait) {
		return OutcomeCancelled
	}
	return OutcomeFailed
}

// checkResult rejects a result that contradicts itself: a failure needs its
// reason, a success has none, and a success cannot leave its effect unknown (an
// unknown effect is resolved into a failure by the bounded reconciliation before
// it reaches settlement).
func checkResult(ref ID, r FileResult) error {
	switch r.Outcome {
	case OutcomeSucceeded:
		if r.ReasonCode != "" {
			return incomplete("file %s succeeded with a failure reason", ref)
		}
		if r.Effect == EffectUnknown {
			return incomplete("file %s succeeded with an unknown effect", ref)
		}
	case OutcomeFailed:
		if !validReason(string(r.ReasonCode)) {
			return incomplete("file %s failed without a valid reason", ref)
		}
	default:
		return incomplete("file %s has outcome %q", ref, r.Outcome)
	}
	switch r.Effect {
	case EffectUnknown, EffectNone, EffectConfirmed:
		return nil
	}
	return incomplete("file %s has effect class %q", ref, r.Effect)
}

func incomplete(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrExecutionGraphIncomplete, fmt.Sprintf(format, args...))
}
