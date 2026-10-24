package domain

import (
	"errors"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

var settledAt = time.Date(2026, 10, 4, 1, 2, 3, 456789123, time.UTC)

func dg(c string) *DigestText {
	d := DigestText("sha256:" + repeat(c, 64))
	return &d
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func okResult() *FileResult { return &FileResult{Outcome: OutcomeSucceeded, Effect: EffectConfirmed} }
func failedResult(reason Reason) *FileResult {
	return &FileResult{Outcome: OutcomeFailed, Effect: EffectNone, ReasonCode: reason}
}

// sealedFacts is a sealed set of one required and one excluded file, both
// settled, with nothing live.
func sealedFacts(files ...FileFact) SettlementFacts {
	var required int64
	for _, f := range files {
		if f.Required {
			required++
		}
	}
	return SettlementFacts{
		Branch: BranchDownload, Requirements: RequirementsSealed, RequirementsDigest: dg("a"), TargetDigest: dg("b"),
		HeaderItemCount: int64(len(files)), HeaderRequired: required, Files: files, SettledAt: settledAt,
	}
}

func mustSettle(t *testing.T, f SettlementFacts) Decision {
	t.Helper()
	d, err := Decide(f)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Kind != Settle {
		t.Fatalf("want a settlement, got continue")
	}
	if err := Validate("TerminalReceiptV1", d.Receipt.Value()); err != nil {
		t.Fatalf("the receipt must satisfy its registry: %v\n%+v", err, d.Receipt)
	}
	return d
}

func TestEverySettlementIsAValidReceiptAndRoundTrips(t *testing.T) {
	d := mustSettle(t, sealedFacts(FileFact{Ref: "f1", Required: true, Result: okResult()}))
	parsed, err := ParseStrict(d.Receipt.Canonical())
	if err != nil {
		t.Fatalf("canonical bytes must parse strictly: %v", err)
	}
	if err := Validate("TerminalReceiptV1", parsed); err != nil {
		t.Fatalf("parsed canonical receipt: %v", err)
	}
	if !reflect.DeepEqual(parsed, d.Receipt.Value()) {
		t.Fatalf("canonical encode/parse changed the receipt")
	}
	if got := d.Receipt.Value().Object["settled_at"].String; got != "2026-10-04T01:02:03.456789Z" {
		t.Fatalf("settled_at must be UTC at microsecond precision, got %q", got)
	}
}

func TestLiveWorkKeepsTheRootRunningWhateverHasFailed(t *testing.T) {
	f := sealedFacts(
		FileFact{Ref: "f1", Required: true, Result: failedResult("TARGET_EXISTS")},
		FileFact{Ref: "f2", Required: true}, // still being delivered
	)
	f.LiveWork = true
	d, err := Decide(f)
	if err != nil || d.Kind != Continue {
		t.Fatalf("live work must continue, got %+v, %v", d, err)
	}
	// Even a graph that would otherwise be broken is just waiting while work is live.
	f = SettlementFacts{Branch: BranchDownload, Requirements: RequirementsNotReached, LiveWork: true, SettledAt: settledAt}
	if d, err := Decide(f); err != nil || d.Kind != Continue {
		t.Fatalf("an unsealed set with live work must continue, got %+v, %v", d, err)
	}
}

func TestAllRequiredSucceededIsSuccessAndCountsExcluded(t *testing.T) {
	d := mustSettle(t, sealedFacts(
		FileFact{Ref: "f1", Required: true, Result: okResult()},
		FileFact{Ref: "f2", Required: false},
		FileFact{Ref: "f3", Required: true, Result: &FileResult{Outcome: OutcomeSucceeded, Effect: EffectNone}},
	))
	r := d.Receipt
	if r.Outcome != OutcomeSucceeded || r.PrimaryFailure != nil {
		t.Fatalf("want success with no failure: %+v", r)
	}
	if *r.Counts != (Counts{Total: 3, Required: 2, Excluded: 1, Succeeded: 2, Failed: 0}) {
		t.Fatalf("counts: %+v", *r.Counts)
	}
	if !r.Effects.Complete || !r.Effects.HasConfirmed || r.Effects.HasUnknown {
		t.Fatalf("effects: %+v", r.Effects)
	}
	if len(d.Synthesized) != 0 {
		t.Fatalf("nothing to synthesize: %+v", d.Synthesized)
	}
}

func TestOneRequiredFailureFailsTheWholeAndNamesTheFirstByRef(t *testing.T) {
	facts := sealedFacts(
		FileFact{Ref: "f9", Required: true, Result: failedResult("LATE_FAILURE")},
		FileFact{Ref: "f2", Required: true, Result: okResult()},
		FileFact{Ref: "f4", Required: true, Result: failedResult("EARLY_FAILURE")},
	)
	d := mustSettle(t, facts)
	r := d.Receipt
	if r.Outcome != OutcomeFailed || r.PrimaryFailure == nil {
		t.Fatalf("want failed: %+v", r)
	}
	// The first failed file by reference, whatever order the rows were read in.
	if r.PrimaryFailure.ReasonCode != "EARLY_FAILURE" || r.PrimaryFailure.FileRef == nil || *r.PrimaryFailure.FileRef != "f4" ||
		r.PrimaryFailure.Stage != StageFileExec {
		t.Fatalf("primary failure: %+v", r.PrimaryFailure)
	}
	if r.Counts.Succeeded != 1 || r.Counts.Failed != 2 {
		t.Fatalf("counts: %+v", *r.Counts)
	}
}

func TestUpstreamPlanFailureSynthesizesATypedFailurePerRequiredFile(t *testing.T) {
	facts := sealedFacts(
		FileFact{Ref: "f1", Required: true},
		FileFact{Ref: "f2", Required: false},
		FileFact{Ref: "f3", Required: true},
	)
	facts.TargetDigest = nil // no target set was ever built
	facts.Upstream = &Failure{Stage: StagePlan, ReasonCode: "NAMING_COLLISION"}
	d := mustSettle(t, facts)
	r := d.Receipt
	if r.Outcome != OutcomeFailed || r.PrimaryFailure.Stage != StagePlan || r.PrimaryFailure.ReasonCode != "NAMING_COLLISION" {
		t.Fatalf("the upstream failure is the primary one: %+v", r)
	}
	if r.TargetDigest != nil || *r.Counts != (Counts{Total: 3, Required: 2, Excluded: 1, Succeeded: 0, Failed: 2}) {
		t.Fatalf("receipt: %+v counts %+v", r, *r.Counts)
	}
	want := []Synthesized{{FileRef: "f1", ReasonCode: "NAMING_COLLISION"}, {FileRef: "f3", ReasonCode: "NAMING_COLLISION"}}
	if !reflect.DeepEqual(d.Synthesized, want) {
		t.Fatalf("synthesized %+v, want %+v", d.Synthesized, want)
	}
	if r.Effects.HasConfirmed || r.Effects.HasUnknown || !r.Effects.Complete {
		t.Fatalf("nothing ran, so no effect: %+v", r.Effects)
	}
}

func TestUnsealedRequirementsSettleOnlyOnADefiniteUpstreamFailure(t *testing.T) {
	for _, status := range []RequirementsStatus{RequirementsNotReached, RequirementsIncomplete} {
		f := SettlementFacts{Branch: BranchDownload, Requirements: status, SettledAt: settledAt,
			Upstream: &Failure{Stage: StageInspect, ReasonCode: "PAYLOAD_UNAVAILABLE"}}
		r := mustSettle(t, f).Receipt
		if r.Outcome != OutcomeFailed || r.Counts != nil || r.RequirementsDigest != nil || r.TargetDigest != nil ||
			r.PrimaryFailure.Stage != StageInspect || r.Effects.Complete {
			t.Fatalf("%s: %+v", status, r)
		}
		f.Upstream = nil
		if _, err := Decide(f); !errors.Is(err, ErrExecutionGraphIncomplete) {
			t.Fatalf("%s with nothing live and no failure is a broken graph, got %v", status, err)
		}
	}
}

func TestZeroRequiredFilesIsAFailureNeverSuccess(t *testing.T) {
	r := mustSettle(t, sealedFacts(FileFact{Ref: "f1", Required: false})).Receipt
	if r.Outcome != OutcomeFailed || r.PrimaryFailure.ReasonCode != ReasonNoRequiredFiles || r.PrimaryFailure.Stage != StagePlan {
		t.Fatalf("zero required: %+v", r)
	}
	r = mustSettle(t, sealedFacts()).Receipt
	if r.Outcome != OutcomeFailed || r.Counts.Total != 0 {
		t.Fatalf("an empty sealed set: %+v", r)
	}
}

func TestAnUnknownEffectBlocksSuccessButNotFailure(t *testing.T) {
	facts := sealedFacts(FileFact{Ref: "f1", Required: true, Result: okResult()})
	facts.HasUnknownEffect = true
	r := mustSettle(t, facts).Receipt
	if r.Outcome != OutcomeFailed || r.PrimaryFailure.ReasonCode != ReasonEffectUnknown || !r.Effects.HasUnknown || r.Effects.Complete {
		t.Fatalf("an unknown effect blocks success: %+v", r)
	}

	// An already failed file keeps its own reason, and the unknown effect is still reported.
	facts = sealedFacts(FileFact{Ref: "f1", Required: true,
		Result: &FileResult{Outcome: OutcomeFailed, Effect: EffectUnknown, ReasonCode: "PUBLISH_UNCERTAIN"}})
	r = mustSettle(t, facts).Receipt
	if r.Outcome != OutcomeFailed || r.PrimaryFailure.ReasonCode != "PUBLISH_UNCERTAIN" || !r.Effects.HasUnknown || r.Effects.Complete {
		t.Fatalf("a failed unknown effect: %+v", r)
	}

	// A confirmed partial effect and an unknown one are not exclusive.
	facts = sealedFacts(
		FileFact{Ref: "f1", Required: true, Result: &FileResult{Outcome: OutcomeFailed, Effect: EffectConfirmed, ReasonCode: "SOURCE_CLEANUP_FAILED"}},
		FileFact{Ref: "f2", Required: true, Result: &FileResult{Outcome: OutcomeFailed, Effect: EffectUnknown, ReasonCode: "PUBLISH_UNCERTAIN"}},
	)
	r = mustSettle(t, facts).Receipt
	if !r.Effects.HasConfirmed || !r.Effects.HasUnknown {
		t.Fatalf("both flags: %+v", r.Effects)
	}
}

func TestBrokenFactsAreAStructuralErrorNotAQuietSuccess(t *testing.T) {
	base := func() SettlementFacts {
		return sealedFacts(FileFact{Ref: "f1", Required: true, Result: okResult()}, FileFact{Ref: "f2", Required: false})
	}
	cases := []struct {
		name   string
		mutate func(f *SettlementFacts)
	}{
		{"an unknown branch", func(f *SettlementFacts) { f.Branch = "queued" }},
		{"a required file with no result and no upstream failure", func(f *SettlementFacts) { f.Files[0].Result = nil }},
		{"fewer rows than the header", func(f *SettlementFacts) { f.HeaderItemCount = 3 }},
		{"another required count than the header", func(f *SettlementFacts) { f.HeaderRequired = 2 }},
		{"a file listed twice", func(f *SettlementFacts) {
			f.Files = append(f.Files, f.Files[0])
			f.HeaderItemCount = 3
			f.HeaderRequired = 2
		}},
		{"a file with no reference", func(f *SettlementFacts) { f.Files[0].Ref = "" }},
		{"a result on an excluded file", func(f *SettlementFacts) { f.Files[1].Result = okResult() }},
		{"a success with a failure reason", func(f *SettlementFacts) { f.Files[0].Result.ReasonCode = "OOPS" }},
		{"a success with an unknown effect", func(f *SettlementFacts) { f.Files[0].Result.Effect = EffectUnknown }},
		{"a failure without a reason", func(f *SettlementFacts) { f.Files[0].Result = &FileResult{Outcome: OutcomeFailed, Effect: EffectNone} }},
		{"a failure with a lower-case reason", func(f *SettlementFacts) { f.Files[0].Result = failedResult("lower_case") }},
		{"an unknown outcome", func(f *SettlementFacts) { f.Files[0].Result.Outcome = "skipped" }},
		{"an unknown effect class", func(f *SettlementFacts) { f.Files[0].Result.Effect = "maybe" }},
		{"a sealed set with no requirements digest", func(f *SettlementFacts) { f.RequirementsDigest = nil }},
		{"an unknown requirements status", func(f *SettlementFacts) { f.Requirements = "half" }},
		{"an unsealed set that carries a digest", func(f *SettlementFacts) {
			f.Requirements = RequirementsIncomplete
			f.Upstream = &Failure{Stage: StagePlan, ReasonCode: "X_FAILURE"}
		}},
	}
	for _, c := range cases {
		f := base()
		c.mutate(&f)
		if _, err := Decide(f); !errors.Is(err, ErrExecutionGraphIncomplete) {
			t.Errorf("%s: want ErrExecutionGraphIncomplete, got %v", c.name, err)
		}
	}
	if _, err := Decide(base()); err != nil {
		t.Fatalf("the base facts are valid: %v", err)
	}
}

// The receipt's registry refuses every receipt the settler could never produce.
func TestTerminalReceiptRegistryRules(t *testing.T) {
	good := mustSettle(t, sealedFacts(FileFact{Ref: "f1", Required: true, Result: okResult()}, FileFact{Ref: "f2"})).Receipt.Value()
	failedGood := mustSettle(t, sealedFacts(FileFact{Ref: "f1", Required: true, Result: failedResult("X_FAILURE")})).Receipt.Value()
	unsealedGood := mustSettle(t, SettlementFacts{Branch: BranchDownload, Requirements: RequirementsNotReached, SettledAt: settledAt,
		Upstream: &Failure{Stage: StageSubmit, ReasonCode: "SUBMIT_REJECTED"}}).Receipt.Value()
	for name, v := range map[string]Value{"success": good, "failure": failedGood, "unsealed": unsealedGood} {
		if err := Validate("TerminalReceiptV1", v); err != nil {
			t.Fatalf("%s must be valid: %v", name, err)
		}
	}
	someFailure := obj(map[string]Value{"stage": str("plan"), "reason_code": str("X_FAILURE"), "file_ref": null()})
	counts := func(total, required, excluded, succeeded, failed int64) Value {
		return obj(map[string]Value{"total": i64(total), "required": i64(required), "excluded": i64(excluded),
			"succeeded": i64(succeeded), "failed": i64(failed)})
	}
	effects := func(complete, confirmed, unknown bool) Value {
		return obj(map[string]Value{"complete": boolean(complete), "has_confirmed": boolean(confirmed), "has_unknown": boolean(unknown)})
	}
	reject := func(name string, v Value, code Code, path string) {
		t.Helper()
		assertRejected(t, name, "TerminalReceiptV1", v, code, path)
	}

	reject("a success with a failure", with(good, "/primary_failure", someFailure), CodeConstraintViolated, "/primary_failure")
	reject("a success over an unsealed set", with(with(good, "/requirements_status", str("incomplete")), "/counts", null()),
		CodeConstraintViolated, "/requirements_digest")
	reject("a success with a failed file", with(good, "/counts", counts(2, 1, 1, 0, 1)), CodeConstraintViolated, "/counts")
	reject("a success with nothing required", with(good, "/counts", counts(1, 0, 1, 0, 0)), CodeConstraintViolated, "/counts")
	reject("a success with an unknown effect", with(good, "/effect_summary", effects(false, false, true)), CodeConstraintViolated, "/effect_summary")
	reject("a success that did not check every fact", with(good, "/effect_summary", effects(false, true, false)), CodeConstraintViolated, "/effect_summary")
	reject("a failure with no primary failure", with(failedGood, "/primary_failure", null()), CodeConstraintViolated, "/primary_failure")
	reject("a complete summary with an unknown effect", with(failedGood, "/effect_summary", effects(true, false, true)), CodeConstraintViolated, "/effect_summary")
	reject("a tally whose required is not succeeded plus failed", with(good, "/counts", counts(2, 1, 1, 0, 0)), CodeConstraintViolated, "/counts/required")
	reject("a tally whose total is not required plus excluded", with(good, "/counts", counts(5, 1, 1, 1, 0)), CodeConstraintViolated, "/counts/total")
	reject("a negative count", with(good, "/counts", counts(2, 1, 1, 2, -1)), CodeConstraintViolated, "/counts/failed")
	reject("a sealed set with no tally", with(failedGood, "/counts", null()), CodeConstraintViolated, "/counts")
	reject("a sealed set with no requirements digest", with(failedGood, "/requirements_digest", null()), CodeConstraintViolated, "/counts")
	reject("an unsealed set that has a tally", with(unsealedGood, "/counts", counts(0, 0, 0, 0, 0)), CodeConstraintViolated, "/counts")
	reject("an unsealed set that has a digest", with(unsealedGood, "/requirements_digest", str(string(*dg("c")))), CodeConstraintViolated, "/requirements_digest")
	reject("an unsealed set that has a target digest", with(unsealedGood, "/target_digest", str(string(*dg("c")))), CodeConstraintViolated, "/target_digest")
	reject("an unknown schema version", with(good, "/schema_version", i64(2)), CodeConstraintViolated, "/schema_version")
	reject("an unknown outcome", with(good, "/outcome", str("skipped")), CodeUnionBranchInvalid, "/outcome")
	reject("an unknown stage", with(failedGood, "/primary_failure/stage", str("download")), CodeUnionBranchInvalid, "/primary_failure/stage")
	reject("a lower-case reason", with(failedGood, "/primary_failure/reason_code", str("lower")), CodeTypeInvalid, "/primary_failure/reason_code")
	reject("a settled time with a trailing zero", with(good, "/settled_at", str("2026-10-04T01:02:03.450000Z")), CodeTypeInvalid, "/settled_at")
}

// randomFacts draws a fact set; the oracle below recomputes the expected decision
// from the same facts without sharing any code with Decide.
func randomFacts(rng *rand.Rand) SettlementFacts {
	f := SettlementFacts{Branch: BranchDownload, SettledAt: settledAt, LiveWork: rng.Intn(5) == 0,
		HasConfirmedEffect: rng.Intn(6) == 0, HasUnknownEffect: rng.Intn(8) == 0}
	if rng.Intn(6) == 0 {
		f.Upstream = &Failure{Stage: StagePlan, ReasonCode: "UP_FAILURE"}
	}
	if rng.Intn(5) != 0 {
		f.Requirements = RequirementsSealed
		f.RequirementsDigest = dg("a")
		if rng.Intn(2) == 0 {
			f.TargetDigest = dg("b")
		}
		n := rng.Intn(6)
		for i := 0; i < n; i++ {
			file := FileFact{Ref: ID(string(rune('a'+i)) + "-file"), Required: rng.Intn(3) != 0}
			if file.Required {
				f.HeaderRequired++
				switch rng.Intn(4) {
				case 0:
					file.Result = okResult()
				case 1:
					file.Result = failedResult("F_FAILURE")
				case 2:
					file.Result = &FileResult{Outcome: OutcomeFailed, Effect: EffectUnknown, ReasonCode: "F_UNKNOWN"}
				}
			}
			f.Files = append(f.Files, file)
		}
		f.HeaderItemCount = int64(n)
	} else {
		f.Requirements = RequirementsNotReached
	}
	return f
}

func TestDecideAgainstAnIndependentOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(20261004))
	var settled, continued, broken int
	for i := 0; i < 5000; i++ {
		f := randomFacts(rng)
		got, err := Decide(f)

		// The oracle.
		if f.LiveWork {
			if err != nil || got.Kind != Continue {
				t.Fatalf("live work must continue: %+v -> %+v, %v", f, got, err)
			}
			continued++
			continue
		}
		wantBroken := false
		if f.Requirements != RequirementsSealed {
			wantBroken = f.Upstream == nil
		} else {
			for _, file := range f.Files {
				if file.Required && file.Result == nil && f.Upstream == nil {
					wantBroken = true
				}
			}
		}
		if wantBroken {
			if !errors.Is(err, ErrExecutionGraphIncomplete) {
				t.Fatalf("want a structural error for %+v, got %+v, %v", f, got, err)
			}
			broken++
			continue
		}
		if err != nil || got.Kind != Settle {
			t.Fatalf("want a settlement for %+v, got %+v, %v", f, got, err)
		}
		settled++
		r := got.Receipt
		if err := Validate("TerminalReceiptV1", r.Value()); err != nil {
			t.Fatalf("invalid receipt %v for %+v", err, f)
		}
		if f.Requirements != RequirementsSealed {
			if r.Outcome != OutcomeFailed || r.Counts != nil {
				t.Fatalf("an unsealed set settles failed with no tally: %+v", r)
			}
			continue
		}
		var succeeded, failed, required int64
		for _, file := range f.Files {
			if !file.Required {
				continue
			}
			required++
			switch {
			case file.Result == nil, file.Result.Outcome == OutcomeFailed:
				failed++
			default:
				succeeded++
			}
		}
		if r.Counts.Succeeded != succeeded || r.Counts.Failed != failed || r.Counts.Required != required ||
			r.Counts.Total != int64(len(f.Files)) {
			t.Fatalf("counts %+v, want succeeded %d failed %d required %d for %+v", *r.Counts, succeeded, failed, required, f)
		}
		hasUnknown := f.HasUnknownEffect
		for _, file := range f.Files {
			if file.Result != nil && file.Result.Effect == EffectUnknown {
				hasUnknown = true
			}
		}
		wantSuccess := f.Upstream == nil && required > 0 && failed == 0 && !hasUnknown
		if (r.Outcome == OutcomeSucceeded) != wantSuccess {
			t.Fatalf("outcome %s, want success=%v for %+v", r.Outcome, wantSuccess, f)
		}
		if r.Effects.HasUnknown != hasUnknown || r.Effects.Complete != !hasUnknown {
			t.Fatalf("effects %+v, hasUnknown %v", r.Effects, hasUnknown)
		}

		// The decision never depends on the order the rows were read in.
		shuffled := f
		shuffled.Files = append([]FileFact(nil), f.Files...)
		rng.Shuffle(len(shuffled.Files), func(a, b int) { shuffled.Files[a], shuffled.Files[b] = shuffled.Files[b], shuffled.Files[a] })
		again, err := Decide(shuffled)
		if err != nil || !reflect.DeepEqual(again, got) {
			t.Fatalf("row order changed the decision:\n%+v\n%+v (%v)", got, again, err)
		}
	}
	if settled < 500 || continued < 500 || broken < 100 {
		t.Fatalf("the generator must reach every branch: settled %d continued %d broken %d", settled, continued, broken)
	}
	t.Logf("5000 random fact sets: %d settled, %d continued, %d broken", settled, continued, broken)
}

// A download task deleted in the downloader before it completed ends the root
// cancelled, not failed, whether or not the requirement set was sealed; the same
// code at any later stage is an ordinary failure.
func TestDownloaderRemovalSettlesCancelled(t *testing.T) {
	for _, stage := range []Stage{StageSubmit, StageDownloadWait} {
		for _, status := range []RequirementsStatus{RequirementsNotReached, RequirementsIncomplete} {
			r := mustSettle(t, SettlementFacts{Branch: BranchDownload, Requirements: status, SettledAt: settledAt,
				Upstream: &Failure{Stage: stage, ReasonCode: ReasonDownloaderTaskRemoved}}).Receipt
			if r.Outcome != OutcomeCancelled || r.PrimaryFailure.ReasonCode != ReasonDownloaderTaskRemoved || r.PrimaryFailure.Stage != stage {
				t.Fatalf("%s/%s: %+v", stage, status, r)
			}
		}
		facts := sealedFacts(FileFact{Ref: "f1", Required: true}, FileFact{Ref: "f2"})
		facts.TargetDigest = nil
		facts.Upstream = &Failure{Stage: stage, ReasonCode: ReasonDownloaderTaskRemoved}
		d := mustSettle(t, facts)
		if d.Receipt.Outcome != OutcomeCancelled || len(d.Synthesized) != 1 || d.Synthesized[0].ReasonCode != ReasonDownloaderTaskRemoved {
			t.Fatalf("sealed %s: %+v %+v", stage, d.Receipt, d.Synthesized)
		}
	}
	r := mustSettle(t, SettlementFacts{Branch: BranchDownload, Requirements: RequirementsNotReached, SettledAt: settledAt,
		Upstream: &Failure{Stage: StageInspect, ReasonCode: ReasonDownloaderTaskRemoved}}).Receipt
	if r.Outcome != OutcomeFailed {
		t.Fatalf("a removal code past the download is a failure: %+v", r)
	}
	r = mustSettle(t, SettlementFacts{Branch: BranchDownload, Requirements: RequirementsNotReached, SettledAt: settledAt,
		Upstream: &Failure{Stage: StageDownloadWait, ReasonCode: "SYNC_ERROR_BUDGET_EXHAUSTED"}}).Receipt
	if r.Outcome != OutcomeFailed {
		t.Fatalf("an unobservable task stays a failure: %+v", r)
	}
}

func TestCancelledReceiptAdmitsOnlyTheCancellationReasons(t *testing.T) {
	removed := mustSettle(t, SettlementFacts{Branch: BranchDownload, Requirements: RequirementsNotReached, SettledAt: settledAt,
		Upstream: &Failure{Stage: StageDownloadWait, ReasonCode: ReasonDownloaderTaskRemoved}}).Receipt.Value()
	retired := with(removed, "/primary_failure/reason_code", str(string(ReasonOwnerRetired)))
	if err := Validate("TerminalReceiptV1", retired); err != nil {
		t.Fatalf("a retirement receipt must stay valid: %v", err)
	}
	retiredAtPlan := with(retired, "/primary_failure/stage", str("plan"))
	if err := Validate("TerminalReceiptV1", retiredAtPlan); err != nil {
		t.Fatalf("retirement may cancel at any stage: %v", err)
	}
	complete := obj(map[string]Value{"complete": boolean(true), "has_confirmed": boolean(false), "has_unknown": boolean(false)})
	if err := Validate("TerminalReceiptV1", with(removed, "/effect_summary", complete)); err != nil {
		t.Fatalf("a removal settles with whatever effect summary the facts give: %v", err)
	}
	assertRejected(t, "a retirement with every fact checked", "TerminalReceiptV1", with(retired, "/effect_summary", complete), CodeConstraintViolated, "/effect_summary")
	assertRejected(t, "a cancellation for another reason", "TerminalReceiptV1", with(removed, "/primary_failure/reason_code", str("SYNC_ERROR_BUDGET_EXHAUSTED")), CodeConstraintViolated, "/primary_failure")
	assertRejected(t, "a removal past the download", "TerminalReceiptV1", with(removed, "/primary_failure/stage", str("inspect")), CodeConstraintViolated, "/primary_failure")
	assertRejected(t, "a cancellation without a failure", "TerminalReceiptV1", with(removed, "/primary_failure", null()), CodeConstraintViolated, "/primary_failure")
}
