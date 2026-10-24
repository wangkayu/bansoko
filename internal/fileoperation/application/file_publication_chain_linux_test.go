//go:build linux

package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	animedomain "bansoko/internal/anime/domain"
	artifactapp "bansoko/internal/artifact/application"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

func TestCleanupForeignReceiptPersistsUnknown(t *testing.T) {
	for _, operation := range []domain.OperationKind{domain.OperationCopy, domain.OperationMove} {
		t.Run(string(operation), func(t *testing.T) { runPublicationProofChain(t, operation, "cleanup_foreign") })
	}
}
func TestHardlinkProductionProofChain(t *testing.T) {
	runPublicationProofChain(t, domain.OperationHardlink)
}
func TestCopyMoveProductionProofChain(t *testing.T) {
	for _, operation := range []domain.OperationKind{domain.OperationCopy, domain.OperationMove} {
		t.Run(string(operation), func(t *testing.T) { runPublicationProofChain(t, operation) })
	}
}
func TestCopyMoveUnknownCommit(t *testing.T) {
	for _, operation := range []domain.OperationKind{domain.OperationCopy, domain.OperationMove} {
		for _, point := range []string{"file_start_dispatch(", "file_start_step(", "file_record_receipt("} {
			for _, rollback := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/rollback=%v", operation, point, rollback), func(t *testing.T) { runPublicationProofChain(t, operation, point, rollback) })
			}
		}
	}
}
func TestHardlinkJoinUnknownCommit(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%v", rollback), func(t *testing.T) {
			runPublicationProofChain(t, domain.OperationHardlink, "file_record_receipt(", rollback, int32(2))
		})
	}
}
func TestPublicationUnknownCommit(t *testing.T) {
	for _, point := range []string{"file_start_dispatch(", "file_start_step(", "file_record_receipt("} {
		for _, rollback := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/rollback=%v", point, rollback), func(t *testing.T) { runPublicationProofChain(t, domain.OperationHardlink, point, rollback) })
		}
	}
}

// TestReceiptNestedObjectClosureProbe injects an unknown key into every nested
// object layer the producer emits (object/identity/content/content.value, the
// reserve_version object, the displaced wrapper and displaced.value) and
// requires file_record_receipt to refuse each one with zero writes. The block
// run covers the displaced-absent wrapper; the version run covers
// reserve_version and a recorded displaced incumbent.
func TestReceiptNestedObjectClosureProbe(t *testing.T) {
	for _, operation := range []domain.OperationKind{domain.OperationCopy, domain.OperationMove, domain.OperationHardlink} {
		t.Run("block/"+string(operation), func(t *testing.T) {
			runPublicationProofChain(t, operation, "nested_closure", domain.CollisionBlock)
		})
		t.Run("version/"+string(operation), func(t *testing.T) {
			runPublicationProofChain(t, operation, "nested_closure", domain.CollisionVersion)
		})
	}
}

func TestVersionProductionProofChain(t *testing.T) {
	for _, operation := range []domain.OperationKind{domain.OperationCopy, domain.OperationMove, domain.OperationHardlink} {
		t.Run(string(operation), func(t *testing.T) { runPublicationProofChain(t, operation, domain.CollisionVersion) })
	}
}

func TestRecoveredResultPreservesOriginalEffectIdentity(t *testing.T) {
	for _, op := range []domain.OperationKind{domain.OperationCopy, domain.OperationMove, domain.OperationHardlink} {
		t.Run(string(op), func(t *testing.T) { runPublicationProofChain(t, op, "recovery_capture") })
	}
}

func TestCancelledDependencySuppression(t *testing.T) {
	runPublicationProofChain(t, domain.OperationCopy, "d9_suppression")
}

func TestFixedFinishOwnerCaller(t *testing.T) {
	runPublicationProofChain(t, domain.OperationHardlink, "fixed_finish_owner")
}

// TestOverwritePublicationProofChain drives the runtime accepted
// ReplacementV1 overwrite chain end to end.
//
// This runs the real chain -- attempt, prepare, committed permit, adapter
// invocation with grants/receipts/join, independent proof, fixed finish --
// over an overwrite operation whose incumbent target is occupied, and
// asserts the replacing rename landed (atomic_method=replace, the candidate
// in place of the incumbent, and the displaced incumbent recorded as proof).
func TestOverwritePublicationProofChain(t *testing.T) {
	// The fixture side freezes a COMPLETE accepted ReplacementV1 binding:
	// openFileOperationDBWithPolicy -> fixtureOverwriteIncumbent renders the
	// planner's target through the production naming path (same engine,
	// compiled rules, context builder and season binding the planner reads
	// from the accepted input), creates the incumbent there, observes it with
	// the production path observer and freezes the observed
	// expected_object (location/size/identity) before the accept
	// function computes admission_digest and the vars evidence -- the only
	// point those canonical bytes can carry the binding. The prepare proof
	// compares that binding's location and size against its own fresh
	// prepare observation of the same file.
	runPublicationProofChain(t, domain.OperationCopy, domain.CollisionOverwrite)
}

// TestUnmanagedOverwritePublicationProofChain: under the overwrite setting a
// present file no owner manages is replaced by the same rename,
// without a replacement approval.
func TestUnmanagedOverwritePublicationProofChain(t *testing.T) {
	runPublicationProofChain(t, domain.OperationCopy, "ow_unmanaged")
}

// TestUnmanagedOverwriteRaceKeepsEvidenceUnknown: a competing writer inside
// the publication window makes the unmanaged overwrite refuse, the effect stays
// unknown (never retried as success) and the competitor survives.
func TestUnmanagedOverwriteRaceKeepsEvidenceUnknown(t *testing.T) {
	for _, fault := range []string{"ow_unmanaged_frozen_changed", "ow_unmanaged_exchange_foreign"} {
		t.Run(fault, func(t *testing.T) { runPublicationProofChain(t, domain.OperationCopy, fault) })
	}
}

func TestTypedFinishDependencyHandoff(t *testing.T) {
	// The ordinary owner finish over a legal
	// two-file graph (video + bound subtitle single-level dependency, built
	// through the production binder — never a fixture-inserted edge) creates the
	// dependent subtitle's first work, so the final continuation count reaches 2.
	// Includes idempotent re-entry with zero repeated writes.
	runPublicationProofChain(t, domain.OperationCopy, "r63_handoff")
}

func TestTypedFinishDependencyHandoffCancelled(t *testing.T) {
	// A cancel that wins before the finish keeps
	// the video succeeded/confirmed but suppresses the dependent subtitle
	// (DEPENDENCY_HANDOFF_CANCELLED, no successor work) — continuations stays 1,
	// the contrast to the ordinary handoff's 2.
	runPublicationProofChain(t, domain.OperationCopy, "r63_cancel")
}

func TestRuntimeTypedFinishEntry(t *testing.T) {
	// The runtime lane: the real worker entry (Service.Handle with a
	// store-issued claim, NOT a hand-built fixture claim) runs the full typed
	// chain under the segregated runtime role — attempt → prepare → permit →
	// adapter invocation (grants/receipts/join) → independent proof → fixed
	// finish — and the handoff creates the dependent subtitle's first work
	// (continuations=2), with idempotent re-entry zero-write.
	runPublicationProofChain(t, domain.OperationCopy, "runtime_finish")
}

func TestRuntimeTypedFinishEntryCancelled(t *testing.T) {
	// Cancel variant: the cancel-won recovery claim re-enters the same
	// worker entry and the fixed entry routes to file_finish_cancelled_success
	// (DEPENDENCY_HANDOFF_CANCELLED, no successor work) — continuations stays 1.
	runPublicationProofChain(t, domain.OperationCopy, "runtime_finish_cancel")
}

func TestRuntimeTypedFinishCreatesPlacementObligation(t *testing.T) {
	// With the production placement sink wired, the real worker
	// entry finish commits the placement obligation + typed continuation
	// in the finish transaction. An injected obligation-insert failure rolls
	// the whole finish back (zero writes); the retry through the same entry
	// creates exactly one obligation/continuation, and re-entry adds none.
	runPublicationProofChain(t, domain.OperationCopy, "runtime_finish_placement")
}

func TestRuntimeTypedFinishCancelledCreatesPlacementObligation(t *testing.T) {
	// The cancelled_success lane goes through the same callback and
	// creates the same placement handoff for the cancel-won video.
	runPublicationProofChain(t, domain.OperationCopy, "runtime_finish_cancel_placement")
}

func runPublicationProofChain(t *testing.T, operation domain.OperationKind, fault ...any) {
	collision := domain.CollisionBlock
	placementWired := false
	if len(fault) == 1 && (fault[0] == "runtime_finish_placement" || fault[0] == "runtime_finish_cancel_placement") {
		placementWired = true
		fault = []any{strings.TrimSuffix(fault[0].(string), "_placement")}
	}
	r63Handoff, r63Cancel, runtimeFinish := false, false, false
	if runtimeFinish = len(fault) == 1 && fault[0] == "runtime_finish"; runtimeFinish {
		// Runtime lane over the same two-file graph as r63_handoff: the
		// real worker entry runs the full chain and the fixed finish hands off.
		r63Handoff = true
		fault = nil
	}
	runtimeFinishCancel := len(fault) == 1 && fault[0] == "runtime_finish_cancel"
	if runtimeFinishCancel {
		// Cancel variant over the r63_cancel recovery lane.
		r63Cancel = true
		runtimeFinish = true
		fault = []any{"recovery_capture"}
	}
	if !runtimeFinish {
		r63Handoff = len(fault) == 1 && fault[0] == "r63_handoff"
	}
	if r63Handoff {
		// the ordinary owner finish path over a two-file plan (video+
		// subtitle single-level dependency) — the handoff must
		// create the subtitle work so the continuation count reaches 2.
		fault = nil
	}
	fixedFinishOwner := len(fault) == 1 && fault[0] == "fixed_finish_owner"
	if fixedFinishOwner {
		fault = nil
	}
	d9 := len(fault) == 1 && fault[0] == "d9_suppression"
	if d9 {
		fault = []any{"recovery_capture"}
	}
	if !runtimeFinishCancel {
		r63Cancel = len(fault) == 1 && fault[0] == "r63_cancel"
	}
	if r63Cancel {
		// The cancel variant of the handoff: the cancel-won recovery claim
		// routes the fixed finish to file_finish_cancelled_success, which
		// suppresses the dependent subtitle (no successor work) — the contrast
		// to the ordinary handoff (continuations=2). Reuses the recovery lane.
		fault = []any{"recovery_capture"}
	}

	finishRollback := len(fault) == 1 && fault[0] == "finish_rollback"
	if finishRollback {
		// Per-write-point rollback matrix over the ordinary owner finish.
		fault = nil
	}
	finishRollbackCancel := len(fault) == 1 && fault[0] == "finish_rollback_cancel"
	if finishRollbackCancel {
		// Per-write-point matrix over the cancel-won recovery lane (cancelled finish).
		fault = []any{"recovery_capture"}
	}
	recoveryCapture := len(fault) == 1 && fault[0] == "recovery_capture"
	if recoveryCapture {
		fault = nil
	}
	if len(fault) == 2 && strings.HasPrefix(fmt.Sprint(fault[0]), "cleanup_fault:") {
		collision = fault[1].(domain.CollisionPolicy)
		fault = fault[:1]
	}
	// ow_unmanaged*: the same overwrite chain against an UNMANAGED incumbent
	// (no placement, no approval): the delivery is ensure_present/overwrite
	// and the prepare proof admits it through the unmanaged branch (022).
	unmanagedOverwrite := false
	if len(fault) == 1 && strings.HasPrefix(fmt.Sprint(fault[0]), "ow_unmanaged") {
		unmanagedOverwrite = true
		collision = domain.CollisionOverwrite
		switch fault[0] {
		case "ow_unmanaged_frozen_changed":
			fault = []any{"ow_frozen_changed"}
		case "ow_unmanaged_exchange_foreign":
			fault = []any{"ow_exchange_foreign"}
		default:
			fault = nil
		}
	}
	owFrozenChanged := len(fault) == 1 && fault[0] == "ow_frozen_changed"
	owExchangeForeign := len(fault) == 1 && fault[0] == "ow_exchange_foreign"
	if owFrozenChanged || owExchangeForeign {
		// The overwrite competition sequences carry their own collision
		// (overwrite); they are not a version-policy selector.
		collision = domain.CollisionOverwrite
		fault = nil
	}
	nestedClosureProbe := len(fault) == 2 && fault[0] == "nested_closure"
	if nestedClosureProbe {
		// The nested-closure probe consumes its own fault slot to state the
		// collision policy: version reaches reserve_version and a recorded
		// displaced incumbent, block reaches the displaced-absent wrapper.
		// Collision is chosen here, not by the fail-policy selector.
		collision = fault[1].(domain.CollisionPolicy)
		fault = nil
	}
	policy := "fail"
	if len(fault) == 1 && fault[0] != "cleanup_foreign" && !strings.HasPrefix(fmt.Sprint(fault[0]), "cleanup_fault:") {
		collision = fault[0].(domain.CollisionPolicy)
		policy = "versioned"
		fault = nil
	}
	if len(fault) > 3 {
		collision = fault[3].(domain.CollisionPolicy)
		if collision == domain.CollisionVersion {
			policy = "versioned"
		}
	}
	if collision == domain.CollisionVersion {
		policy = "versioned"
	}
	if collision == domain.CollisionOverwrite {
		// Overwrite is a distinct collision policy, not a versioned one: the
		// adapter must rename over a frozen expected
		// target (its closure includes expected_target and demands
		// atomic_method=replace), so the config has to say overwrite.
		policy = "overwrite"
	}
	db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		p["effective_sections"].(map[string]any)[animedomain.SectionFileEffect] = map[string]any{"operation": string(operation), "collision_policy": policy}
		if unmanagedOverwrite {
			p[fixtureUnmanagedOverwriteKey] = true
		}
		if d9 {
			// All d9 members in ONE call: bindingManifestFixture overwrites
			// fixture_manifest, so a second call would drop earlier members
			// (the subtitle must stay planned for verifyD9Suppression).
			// The two companion subtitles differ in language and extension
			// (sub.lang_tag zh-Hans / en), so both are distinguishable
			// subtitle dependents of one video; two companions of one video
			// on one target would keep only the first by path.
			bindingManifestFixture(t, map[string]any{"file_ref": "subtitle", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"}, map[string]any{"file_ref": "subtitle2", "relative_path": "source.eng.srt", "exact_size": 8, "media_kind": "subtitle"}, map[string]any{"file_ref": "independent", "relative_path": "Other/Show - 02.mkv", "exact_size": 5, "media_kind": "video", "episode": "2"})(p)
		}
		if r63Handoff || r63Cancel || finishRollback || finishRollbackCancel {
			bindingManifestFixture(t, map[string]any{"file_ref": "subtitle", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"})(p)
		}
	})
	if d9 {
		source := filepath.Join(t.TempDir(), "source.chs.ass")
		if err := os.WriteFile(source, []byte("subtitle"), 0600); err != nil {
			t.Fatal(err)
		}
		addArtifactFile(t, db, f, "d9-subtitle", "subtitle", "source.chs.ass", source, "subtitle", 8)
		source2 := filepath.Join(t.TempDir(), "source.eng.srt")
		if err := os.WriteFile(source2, []byte("subtitle"), 0600); err != nil {
			t.Fatal(err)
		}
		addArtifactFile(t, db, f, "d9-subtitle2", "subtitle2", "source.eng.srt", source2, "subtitle", 8)
		independent := filepath.Join(t.TempDir(), "Show - 02.mkv")
		if err := os.WriteFile(independent, []byte("video"), 0600); err != nil {
			t.Fatal(err)
		}
		addArtifactFile(t, db, f, "d9-independent", "independent", "Other/Show - 02.mkv", independent, "video", 5)
	}
	if r63Handoff || r63Cancel || finishRollback || finishRollbackCancel {
		source := filepath.Join(t.TempDir(), "source.chs.ass")
		if err := os.WriteFile(source, []byte("subtitle"), 0600); err != nil {
			t.Fatal(err)
		}
		addArtifactFile(t, db, f, "r63-subtitle", "subtitle", "source.chs.ass", source, "subtitle", 8)
	}
	s := newFixtureService(t, db, f)
	if placementWired {
		s.SetPlacementSink(&recordingPlacementSink{})
	}
	ctx := context.Background()
	if collision == domain.CollisionOverwrite {
		// The incumbent already sits at the planner-rendered target: the FIXTURE
		// created it before freezing the accepted ReplacementV1 binding
		// (openFileOperationDBWithPolicy -> fixtureOverwriteIncumbent), because
		// the prepare proof compares the accepted binding's expected_object
		// (location and size) against the prepare-time observation of that
		// same file. Rewriting it here with another size would fail the
		// binding comparison, and
		// rewriting it at the wrong path would degenerate the overwrite to a
		// plain placement. PreparePublication's own frozen observation below
		// is the negative proof: it refuses an absent or drifted incumbent.
	}
	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapp.VerifiedArtifactRequest{ArtifactID: f.artifactID}, nil)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	plan := claimPlanWorkItem(t, db, "s4-hardlink-plan")
	if _, err := s.PostProcessingPlanHandler().Handle(ctx, plan); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d9 {
		if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE public.work_items SET available_at=clock_timestamp()+interval '1 hour' WHERE owner_type='file_operation' AND owner_id IN (SELECT id FROM public.file_operations WHERE artifact_id=$1 AND file_ref='independent')`, f.artifactID); err != nil {
			t.Fatal(err)
		}
	}
	c, err := store.ClaimDue(ctx, "s4-hardlink-file", []string{workitems.KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var op domain.FileOperation
	op.ID = c.Owner.ID
	op.Operation = operation
	op.CollisionPolicy = collision
	op.ExpectedSizeKnown = true
	if err := db.QueryRow(`SELECT source_path,target_path,source_fingerprint->>'value',expected_size_bytes FROM public.file_operations WHERE id=$1`, c.Owner.ID).Scan(&op.SourcePath, &op.TargetPath, &op.SourceFingerprint, &op.ExpectedSizeBytes); err != nil {
		t.Fatal(err)
	}
	// Owner arranges the accepted target parent; runtime adapter performs every
	// actual publication and trusted proof/closure write under runtime credentials.
	if err := os.MkdirAll(filepath.Dir(op.TargetPath), 0700); err != nil {
		t.Fatal(err)
	}
	if collision == domain.CollisionVersion {
		// Version replaces an occupied target via its reserved candidate. The
		// OVERWRITE incumbent is NOT rewritten here: the fixture already created
		// it at the rendered target and froze its observed identity into the
		// accepted ReplacementV1 binding, and the finish re-checks that binding against
		// the prepare-time observation — rewriting would change the inode and
		// fail the admission it is meant to satisfy.
		if err := os.WriteFile(op.TargetPath, []byte("occupied previous object"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	useRuntimePlanObserver(t, s)
	executor := filesystem.OSFileExecutor{}
	if owFrozenChanged {
		// Sequence 1 (frozen-target-yields): a real competing writer
		// swaps the frozen incumbent right before the publishing rename — the
		// same rename a concurrent adopt performs. The competitor has another
		// size, so the replace guard (regular file of the frozen size) must
		// refuse, the receipts must stay unknown, and the competitor must
		// survive at the target.
		executor.Hooks.BeforePublish = func(_, destination string) error {
			competing := destination + ".ow-compete"
			if err := os.WriteFile(competing, []byte("competing object"), 0o600); err != nil {
				return err
			}
			return os.Rename(competing, destination)
		}
	}
	if owExchangeForeign {
		// Sequence 2 (temp-exhausts-set): a real competing writer
		// swaps a foreign object into the permit temp before the publishing
		// rename. The staged-object guard (same invocation device/inode) must
		// refuse, the receipts stay unknown, the incumbent stays in place and
		// the foreign object survives at the temp.
		executor.Hooks.BeforePublish = func(source, _ string) error {
			foreign := source + ".ow-foreign"
			if err := os.WriteFile(foreign, []byte("foreign must survive"), 0o600); err != nil {
				return err
			}
			return os.Rename(foreign, source)
		}
	}
	if len(fault) == 1 && strings.HasPrefix(fmt.Sprint(fault[0]), "cleanup_fault:") {
		point := strings.TrimPrefix(fault[0].(string), "cleanup_fault:")
		executor.Hooks.PublicationCleanupPoint = func(at string) error {
			if at == point {
				return fmt.Errorf("interrupted after %s", point)
			}
			return nil
		}
	}
	invocation, err := executor.NewPublicationInvocation()
	if err != nil {
		t.Fatal(err)
	}
	session := invocation.Session()
	if d9 {
		var root string
		if err := db.QueryRow(`SELECT acquisition_id FROM file_operations WHERE id=$1`, c.Owner.ID).Scan(&root); err != nil {
			t.Fatal(err)
		}
		probeUnadmittedPrepare(t, ctx, db, root, session.ExecutorSession)
	}
	permit := "s4-hardlink-permit"
	attempt, err := s.startFileAttempt(ctx, c, session.ExecutorSession)
	if err != nil || !attempt.Granted {
		t.Fatalf("attempt=%+v err=%v", attempt, err)
	}
	prep, err := executor.PreparePublication(ctx, op, permit)
	if err != nil {
		t.Fatal(err)
	}
	if prep.Source.SizeBytes != op.ExpectedSizeBytes {
		t.Fatalf("publication source size=%d want %d", prep.Source.SizeBytes, op.ExpectedSizeBytes)
	}
	created, err := s.prepareFilePermit(ctx, c, session.ExecutorSession, permit, op, prep)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if len(fault) > 1 {
		originalDB := s.db
		cfg, err := pgx.ParseConfig(os.Getenv("BANSOKO_TEST_RUNTIME_DATABASE_URL"))
		if err != nil {
			t.Fatal(err)
		}
		connector := &planLostCommitConnector{Connector: stdlib.GetConnector(*cfg), queryNeedle: fault[0].(string), rollbackBeforeReply: fault[1].(bool)}
		if len(fault) > 2 {
			connector.faultOrdinal = fault[2].(int32)
		}
		faultDB := sql.OpenDB(connector)
		defer faultDB.Close()
		s.db = faultDB
		execute := invocation.ExecuteHardlink
		if operation != domain.OperationHardlink || collision == domain.CollisionVersion {
			execute = invocation.ExecuteCopy
		}
		err = execute(ctx, op, prep, s.typedPublicationCallbacks(c, permit, op))
		if err == nil {
			t.Fatal("lost commit acknowledgement accepted")
		}
		s.db = originalDB
		if connector.commits.Load() < 1 {
			t.Fatal("fault not injected")
		}
		if len(fault) > 3 {
			ordinal := fault[2].(int32)
			steps := []string{"stage_temp", "publish_target", "temp_cleanup"}
			if collision == domain.CollisionVersion {
				steps = []string{"stage_temp", "reserve_version", "publish_target", "temp_cleanup"}
			}
			if operation == domain.OperationMove {
				pos := len(steps) - 1
				steps = append(steps[:pos], "source_cleanup", "temp_cleanup")
			}
			isReceipt := fault[0].(string) == "file_record_receipt("
			join := isReceipt && int(ordinal) == len(steps)+1
			var cpRaw []byte
			if err := db.QueryRow(`SELECT publication_step_checkpoint FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&cpRaw); err != nil {
				t.Fatal(err)
			}
			var slots map[string]map[string]any
			if err := json.Unmarshal(cpRaw, &slots); err != nil {
				t.Fatal(err)
			}
			if !join {
				step := steps[int(ordinal)-1]
				expected := "started"
				if isReceipt && !fault[1].(bool) {
					expected = "confirmed"
				}
				if !isReceipt && fault[1].(bool) {
					expected = "not_started"
				}
				if slots[step]["state"] != expected {
					t.Fatalf("step %s state=%v expected=%s", step, slots[step]["state"], expected)
				}
			}
			assertPublicationPrefix(t, op, prep, steps, int(ordinal), isReceipt)
			before := planGraphSnapshot(t, db)
			_, proofErr := s.loadPublicationProof(ctx, permit)
			if isReceipt && (join || int(ordinal) == len(steps)) && !fault[1].(bool) {
				if proofErr != nil {
					t.Fatal(proofErr)
				}
			} else if proofErr == nil {
				t.Fatal("partial/rollback proof accepted as success")
			}
			if planGraphSnapshot(t, db) != before {
				t.Fatal("independent proof wrote graph")
			}
			if err := execute(ctx, op, prep, s.typedPublicationCallbacks(c, permit, op)); err == nil {
				t.Fatal("unknown invocation replayed")
			}
			return
		}
		exists := false
		if _, err := os.Lstat(op.TargetPath); err == nil {
			exists = true
		}
		if exists != (operation == domain.OperationHardlink && fault[0].(string) == "file_record_receipt(") {
			t.Fatalf("mutation after unknown grant: exists=%v", exists)
		}
		before := planGraphSnapshot(t, db)
		if !fault[1].(bool) && fault[0].(string) != "file_record_receipt(" {
			granted, err := s.filePublicationGrant(ctx, c, permit, session, "")
			if err == nil && granted {
				t.Fatal("committed dispatch replay granted")
			}
		}
		if fault[0].(string) == "file_record_receipt(" {
			_, proofErr := s.loadPublicationProof(ctx, permit)
			if fault[1].(bool) && proofErr == nil {
				t.Fatal("uncommitted receipt fabricated success")
			}
			if operation == domain.OperationHardlink && !fault[1].(bool) && proofErr != nil {
				t.Fatalf("committed receipt independent proof failed: %v", proofErr)
			}
			if operation != domain.OperationHardlink {
				if _, err := os.Stat(prep.TempPath); err != nil {
					t.Fatal("staging prefix lost after unknown receipt")
				}
				var state string
				if err := db.QueryRow(`SELECT publication_step_checkpoint->'stage_temp'->>'state' FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&state); err != nil {
					t.Fatal(err)
				}
				if !fault[1].(bool) && state != "confirmed" {
					t.Fatalf("committed staging proof=%s", state)
				}
				if fault[1].(bool) && state == "confirmed" {
					t.Fatal("uncommitted staging proof fabricated")
				}
			}
		}
		if !fault[1].(bool) && planGraphSnapshot(t, db) != before {
			t.Fatal("independent observation/repeated grant wrote graph")
		}
		if err := invocation.ExecuteHardlink(ctx, op, prep, s.typedPublicationCallbacks(c, permit, op)); err == nil {
			t.Fatal("unknown commit invocation replayed")
		}
		return
	}
	if len(fault) == 1 && strings.HasPrefix(fmt.Sprint(fault[0]), "cleanup_fault:") {
		point := strings.TrimPrefix(fault[0].(string), "cleanup_fault:")
		callbacks := s.typedPublicationCallbacks(c, permit, op)
		if err := invocation.ExecuteCopy(ctx, op, prep, callbacks); err == nil {
			t.Fatal("cleanup interruption accepted")
		}
		step := "temp_cleanup"
		path := prep.TempPath
		if operation == domain.OperationMove {
			step = "source_cleanup"
			path = op.SourcePath
		}
		var slot string
		var closed, resolution bool
		if err := db.QueryRow(`SELECT publication_step_checkpoint->$2->>'state',publication_closed_proof IS NOT NULL,publication_resolution_proof IS NOT NULL FROM public.file_operations WHERE publication_permit_id=$1`, permit, step).Scan(&slot, &closed, &resolution); err != nil {
			t.Fatal(err)
		}
		if slot != "unknown" || !closed || resolution {
			t.Fatalf("uncertain cleanup proof %s %v %v", slot, closed, resolution)
		}
		// The probe fires right before the unlink, so the cleaned object is
		// still present at its own path (there is no capture namespace).
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("actual prefix %s %v", path, err)
		}
		publishedPath := op.TargetPath
		if collision == domain.CollisionVersion {
			if err := db.QueryRow(`SELECT publication_step_checkpoint->'publish_target'->'proof'->'target'->'location'->>'canonical_path' FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&publishedPath); err != nil {
				t.Fatal(err)
			}
		}
		if bytes, err := os.ReadFile(publishedPath); err != nil || string(bytes) != "source-content" {
			t.Fatal("published prefix lost")
		}
		assertCleanupFaultInventory(t, op, prep, point, publishedPath)
		before := planGraphSnapshot(t, db)
		if _, err := s.loadPublicationProof(ctx, permit); err == nil {
			t.Fatal("uncertain cleanup grants success")
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("proof read wrote graph")
		}
		if err := invocation.ExecuteCopy(ctx, op, prep, callbacks); err == nil {
			t.Fatal("interrupted invocation replayed")
		}
		return
	}
	if len(fault) == 1 && fault[0] == "cleanup_foreign" {
		callbacks := s.typedPublicationCallbacks(c, permit, op)
		grant := callbacks.StartStep
		step := "temp_cleanup"
		path := prep.TempPath
		if operation == domain.OperationMove {
			step = "source_cleanup"
			path = op.SourcePath
		}
		callbacks.StartStep = func(ctx context.Context, session domain.PublicationSession, requested string) (bool, error) {
			ok, err := grant(ctx, session, requested)
			if ok && err == nil && requested == step {
				foreign := path + ".foreign"
				if err := os.WriteFile(foreign, []byte("foreign must survive"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(foreign, path); err != nil {
					t.Fatal(err)
				}
			}
			return ok, err
		}
		if err := invocation.ExecuteCopy(ctx, op, prep, callbacks); err == nil {
			t.Fatal("foreign cleanup accepted")
		}
		var slot string
		var closed, resolution bool
		if err := db.QueryRow(`SELECT publication_step_checkpoint->$2->>'state',publication_closed_proof IS NOT NULL,publication_resolution_proof IS NOT NULL FROM public.file_operations WHERE publication_permit_id=$1`, permit, step).Scan(&slot, &closed, &resolution); err != nil {
			t.Fatal(err)
		}
		if slot != "unknown" || !closed || resolution {
			t.Fatalf("unsafe cleanup proof %s %v %v", slot, closed, resolution)
		}
		if bytes, err := os.ReadFile(path); err != nil || string(bytes) != "foreign must survive" {
			t.Fatal("foreign object purged")
		}
		before := planGraphSnapshot(t, db)
		if _, err := s.loadPublicationProof(ctx, permit); err == nil {
			t.Fatal("unknown cleanup grants success")
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("reader wrote graph")
		}
		if err := invocation.ExecuteCopy(ctx, op, prep, callbacks); err == nil {
			t.Fatal("cleanup replayed")
		}
		return
	}
	if owFrozenChanged || owExchangeForeign {
		// The overwrite competition matrix: every round runs the real
		// chain and the real adapter; the hook only occupies a real window with
		// a real rename. The publication must refuse, the evidence must stay
		// unknown (dispatching, no closure), and the competing object must
		// survive exactly where the window left it.
		callbacks := s.typedPublicationCallbacks(c, permit, op)
		if err := invocation.ExecuteCopy(ctx, op, prep, callbacks); err == nil {
			t.Fatal("overwrite competition: publication accepted a mutated window")
		}
		var state, class string
		var closed, resolution bool
		if err := db.QueryRow(`SELECT publication_dispatch_state,publication_effect_class,publication_closed_proof IS NOT NULL,publication_resolution_proof IS NOT NULL FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&state, &class, &closed, &resolution); err != nil {
			t.Fatal(err)
		}
		// The invocation refused, but its synchronous channel closed (the join
		// defer always records closure): the evidence must be a CLOSED channel
		// with an UNKNOWN effect and no confirmable resolution.
		if state != "closed" || class != "unknown" || !closed || resolution {
			t.Fatalf("overwrite competition evidence state=%s class=%s closed=%v resolution=%v", state, class, closed, resolution)
		}
		if _, err := s.loadPublicationProof(ctx, permit); err == nil {
			t.Fatal("overwrite competition: unknown receipts grant success")
		}
		if owFrozenChanged {
			// Nothing was renamed: the competitor survives at the target and
			// the staged candidate stays at the permit temp.
			got, readErr := os.ReadFile(op.TargetPath)
			if readErr != nil || string(got) != "competing object" {
				t.Fatalf("frozen-target-yields target=%q err=%v", got, readErr)
			}
			staged, readErr := os.ReadFile(prep.TempPath)
			if readErr != nil || string(staged) != "source-content" {
				t.Fatalf("frozen-target-yields staged candidate=%q err=%v", staged, readErr)
			}
		}
		if owExchangeForeign {
			// The foreign temp was never published: the incumbent stays at the
			// target and the foreign object survives at the temp.
			got, readErr := os.ReadFile(op.TargetPath)
			if readErr != nil || string(got) != fixtureOverwriteIncumbentBytes {
				t.Fatalf("temp-exhausts-set target=%q err=%v", got, readErr)
			}
			foreign, readErr := os.ReadFile(prep.TempPath)
			if readErr != nil || string(foreign) != "foreign must survive" {
				t.Fatalf("temp-exhausts-set foreign temp=%q err=%v", foreign, readErr)
			}
		}
		return
	}
	execute := invocation.ExecuteHardlink
	if operation != domain.OperationHardlink || collision == domain.CollisionVersion {
		execute = invocation.ExecuteCopy
	}
	probeCallbacks := digestMatrixCallbacks(t, s, db, c, permit, op)
	probeCallbacks = probeFirstReceiptCallbacks(t, s, db, permit, op, probeCallbacks)
	if nestedClosureProbe {
		probeCallbacks = probeNestedClosureCallbacks(t, s, db, permit, op, probeCallbacks)
	}
	if err := execute(ctx, op, prep, probeCallbacks); err != nil {
		t.Fatal(err)
	}
	var state, class string
	var closed, resolution bool
	if err := db.QueryRow(`SELECT publication_dispatch_state,publication_effect_class,publication_closed_proof IS NOT NULL,publication_resolution_proof IS NOT NULL FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&state, &class, &closed, &resolution); err != nil {
		t.Fatal(err)
	}
	if state != "closed" || class != "confirmed" || !closed || !resolution {
		t.Fatalf("state=%s class=%s closed=%v resolution=%v", state, class, closed, resolution)
	}
	before := planGraphSnapshot(t, db)
	if err := invocation.ExecuteHardlink(ctx, op, prep, s.typedPublicationCallbacks(c, permit, op)); err == nil {
		t.Fatal("same invocation replayed")
	}
	if planGraphSnapshot(t, db) != before {
		t.Fatal("replay changed graph")
	}
	if collision == domain.CollisionVersion {
		var final string
		if err := db.QueryRow(`SELECT publication_step_checkpoint->'publish_target'->'proof'->'target'->'location'->>'canonical_path' FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&final); err != nil {
			t.Fatal(err)
		}
		old, err := os.ReadFile(op.TargetPath)
		if err != nil || string(old) != "occupied previous object" {
			t.Fatal("version replaced old target")
		}
		op.TargetPath = final
	}
	bytes, err := os.ReadFile(op.TargetPath)
	if err != nil || string(bytes) != "source-content" {
		t.Fatalf("target=%q err=%v", bytes, err)
	}
	if operation != domain.OperationHardlink || collision == domain.CollisionVersion {
		if _, err := os.Lstat(prep.TempPath); !os.IsNotExist(err) {
			t.Fatalf("temp not cleaned: %v", err)
		}
	}
	if operation == domain.OperationMove {
		if _, err := os.Lstat(op.SourcePath); !os.IsNotExist(err) {
			t.Fatalf("move source not cleaned: %v", err)
		}
	}
	// Path-level evidence: the frozen source is PathObjectV1 (location,
	// regular, size, identity, observed_at) with no content proof.
	var sourceKeys string
	if err := db.QueryRow(`SELECT string_agg(k,',' ORDER BY k) FROM file_operations o CROSS JOIN LATERAL jsonb_object_keys(o.publication_frozen_objects->'source') k WHERE o.publication_permit_id=$1`, permit).Scan(&sourceKeys); err != nil {
		t.Fatalf("persistent source evidence err=%v", err)
	}
	if sourceKeys != "file_type,identity,location,observed_at,size" {
		t.Fatalf("persistent source evidence keys=%s", sourceKeys)
	}
	if _, err := s.loadPublicationProof(ctx, permit); err != nil {
		t.Fatal(err)
	}
	if collision == domain.CollisionOverwrite {
		// The accepted overwrite chain must have renamed the staged candidate
		// over the frozen incumbent (atomic_method=replace) after
		// checking it is still a regular file of the frozen size; this asserts
		// the observable outcome as well as the recorded method.
		var method string
		if err := db.QueryRow(`SELECT publication_step_checkpoint->'publish_target'->'proof'->>'atomic_method' FROM file_operations WHERE publication_permit_id=$1`, permit).Scan(&method); err != nil {
			t.Fatal(err)
		}
		if method != "replace" {
			t.Fatalf("overwrite publication atomic_method=%s want replace", method)
		}
		var intent string
		if err := db.QueryRow(`SELECT i.input->'delivery'->>'intent' FROM file_operations o JOIN acquisitions i ON i.id=o.acquisition_id WHERE o.publication_permit_id=$1`, permit).Scan(&intent); err != nil {
			t.Fatal(err)
		}
		if want := map[bool]string{true: "ensure_present", false: "replace"}[unmanagedOverwrite]; intent != want {
			t.Fatalf("overwrite delivery intent=%s want %s", intent, want)
		}
		// The incumbent must be gone and the candidate in its place: one
		// rename, not a delete-then-create.
		got, readErr := os.ReadFile(op.TargetPath)
		if readErr != nil {
			t.Fatalf("read published overwrite target: %v", readErr)
		}
		if string(got) == "occupied previous object" {
			t.Fatal("overwrite target still holds the incumbent: the rename did not land")
		}
		// The displaced incumbent must be recorded as proof, not merely dropped.
		var displaced sql.NullInt64
		if err := db.QueryRow(`SELECT (publication_step_checkpoint->'publish_target'->'proof'->'displaced'->'value'->>'size')::bigint FROM file_operations WHERE publication_permit_id=$1`, permit).Scan(&displaced); err != nil {
			t.Fatal(err)
		}
		if !displaced.Valid || displaced.Int64 != int64(len(fixtureOverwriteIncumbentBytes)) {
			t.Fatalf("overwrite publication displaced incumbent size=%v", displaced)
		}
	}
	verifyFileFinalizationGates(t, ctx, db, c, permit, false)
	preparedResult, err := s.prepareSuccessfulFileResult(ctx, c, permit)
	if err != nil {
		t.Fatal(err)
	}
	verifyReceiptIdentityMatrix(t, ctx, s, db, permit)
	verifyReceiptClosureMatrix(t, ctx, s, db, permit)
	verifyRequiredStepMatrix(t, ctx, s, db, permit)
	verifyReceiptCanonicalMatrix(t, ctx, s, db, permit)
	verifyResolutionDigests(t, ctx, db, permit)
	beforeFixedCall := planGraphSnapshot(t, db)
	// The fixed caller routes through the runtime-callable wrapper (the
	// only runtime finish surface). The wrapper reaches the private gate — which
	// rejects on the real evidence under lock only when the claim qualifies.
	// This denial variant pins the wrapper's no-early-write property: a swapped
	// permit is rejected by the prelock with zero writes (the qualifying owner
	// finish below proves the gate itself admits on the real evidence).
	if _, err := s.finishPreparedFileSuccess(ctx, c, func() preparedFileResult { d := preparedResult; d.permit = permit + ".foreign"; return d }()); err == nil {
		t.Fatal("fixed caller accepted a foreign permit")
	}
	if planGraphSnapshot(t, db) != beforeFixedCall {
		t.Fatal("denied fixed caller changed graph")
	}
	if fixedFinishOwner {
		// Owner-role component evidence only; no runtime grant or handler admission.
		ownerDB := ownerTestDB(t)
		originalDB, originalStore := s.db, s.workItems
		s.db = ownerDB
		s.workItems = &workitems.Store{DB: ownerDB, RetentionDays: workitems.DefaultRetentionDays}
		defer func() { s.db, s.workItems = originalDB, originalStore }()
		// (b) A rolled-back attempt is verified against the
		// durable ledger and classified ErrFinishNotCommitted — retrying the same
		// fixed entry with the identical claim is safe (no half write, no new
		// grant, no budget consumed).
		badResult := preparedResult
		badResult.notificationPayload = `{"event.type":"file_operation_completed"}`
		if _, badErr := s.finishPreparedFileSuccess(ctx, c, badResult); !errors.Is(badErr, ErrFinishNotCommitted) {
			t.Fatalf("not-committed classification=%v", badErr)
		}
		t.Logf("producer_rejection Q13-commit/rollback_not_committed classified ErrFinishNotCommitted (retry-safe)")
		if _, err := s.finishPreparedFileSuccess(ctx, c, preparedResult); err != nil {
			t.Fatal(err)
		}
		var closed bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM work_item_history WHERE id=$1 AND state='succeeded') AND EXISTS(SELECT 1 FROM notification_admissions WHERE source_owner_id=$2 AND payload=$3::jsonb AND managed_anime_id IS NULL)`, c.ID, c.Owner.ID, preparedResult.notificationPayload).Scan(&closed); err != nil || !closed {
			t.Fatalf("fixed caller completion=%v err=%v", closed, err)
		}
		// (a) The commit landed but the caller lost the
		// response — re-entering the same fixed entry re-reads the durable
		// ledger and replays the outcome idempotently: no repeated admission,
		// no version bump, no half write.
		beforeReplay := planGraphSnapshot(t, db)
		if _, err := s.finishPreparedFileSuccess(ctx, c, preparedResult); err != nil {
			t.Fatalf("idempotent re-entry=%v", err)
		}
		var admissions int
		if err := db.QueryRow(`SELECT count(*) FROM notification_admissions WHERE source_owner_type='file_operation' AND source_owner_id=$1`, c.Owner.ID).Scan(&admissions); err != nil || admissions != 1 {
			t.Fatalf("replayed admissions=%d err=%v", admissions, err)
		}
		if planGraphSnapshot(t, db) != beforeReplay {
			t.Fatal("idempotent re-entry wrote graph")
		}
		t.Logf("sql_positive Q13-commit/response_lost_reentry idempotent (admissions=1, zero_write)")
		// (c) An unreadable ledger at replay time is
		// ambiguous — the fixed entry refuses to classify the outcome and
		// writes nothing (no blind replay, no in-process retry); the failure
		// stays with the operator/recovery path.
		badDB, badErr := sql.Open("pgx", "postgres://127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
		if badErr != nil {
			t.Fatal(badErr)
		}
		defer badDB.Close()
		s.db = badDB
		_, ambiguousErr := s.finishPreparedFileSuccess(ctx, c, preparedResult)
		s.db = ownerDB
		if ambiguousErr == nil || errors.Is(ambiguousErr, ErrFinishNotCommitted) || !strings.Contains(ambiguousErr.Error(), "unverifiable") {
			t.Fatalf("ambiguous classification=%v", ambiguousErr)
		}
		if planGraphSnapshot(t, db) != beforeReplay {
			t.Fatal("ambiguous replay wrote graph")
		}
		t.Logf("producer_rejection Q13-commit/unreadable_ledger ambiguous (zero_write, no blind replay)")
		return
	}
	if runtimeFinish && !runtimeFinishCancel {
		// Runtime lane: the REAL worker entry (Service.Handle on the
		// store-issued claim under the segregated runtime role) drives the
		// whole typed chain — typedExecutionPending routes to the fixed lane,
		// the typed chain runs attempt/prepare/permit/adapter/receipts (here
		// the chain has already produced the closed proof, so the fixed-lane
		// finish-only phase runs), then the fixed finish hands off.
		if placementWired {
			assertPlacementAbsent(t, db, c.Owner.ID)
			injectPlacementObligationFailure(t)
			beforeFailed := planGraphSnapshot(t, db)
			failedOut, failedErr := s.Handle(ctx, c)
			if failedErr == nil && failedOut.OwnerResult.State == "completed" {
				t.Fatalf("finish completed although the obligation insert failed: %+v", failedOut)
			}
			if planGraphSnapshot(t, db) != beforeFailed {
				t.Fatal("failed obligation insert left a partial finish")
			}
			assertPlacementAbsent(t, db, c.Owner.ID)
			removePlacementObligationFailure(t)
			t.Logf("sql_negative obligation insert failure -> whole finish rolled back (err=%v), zero writes", failedErr)
		}
		graphBefore := planGraphSnapshot(t, db)
		runtimeOut, runtimeErr := s.Handle(ctx, c)
		if runtimeErr != nil {
			t.Fatalf("runtime finish entry=%v", runtimeErr)
		}
		if runtimeOut.OwnerResult.State != "completed" || runtimeOut.Disposition != workitems.DispositionConfirmed {
			t.Fatalf("runtime finish outcome=%+v", runtimeOut)
		}
		if planGraphSnapshot(t, db) == graphBefore {
			t.Fatal("runtime finish wrote nothing (finish did not land)")
		}
		// Idempotent re-entry through the same worker entry: zero write.
		beforeReentry := planGraphSnapshot(t, db)
		if _, err := s.Handle(ctx, c); err != nil {
			t.Fatalf("runtime re-entry=%v", err)
		}
		if planGraphSnapshot(t, db) != beforeReentry {
			t.Fatal("runtime re-entry wrote graph")
		}
		if placementWired {
			assertPlacementHandoff(t, db, c.Owner.ID, c.ID)
			t.Logf("sql_positive runtime finish -> exactly one pending obligation + one placement continuation in the finish transaction; re-entry zero-write")
		} else {
			assertPlacementAbsent(t, db, c.Owner.ID)
		}
		t.Logf("sql_positive runtime_finish worker-entry fixed finish (completed/confirmed, idempotent re-entry zero-write)")
		return
	}
	if r63Handoff {
		// The ordinary owner finish over the legal
		// two-file graph (video + bound subtitle single-level dependency,
		// built through the production binder — never a fixture-inserted edge)
		// must hand off and create the dependent subtitle's first work, taking
		// the continuation count from 1 to 2. No fixture creates the second work.
		ops := loadOperations(t, db, f.artifactID)
		video := operationForFile(t, ops, f.fileID)
		sub := operationForFile(t, ops, "r63-subtitle")
		if !sub.boundVideoOperationID.Valid || sub.boundVideoOperationID.String != video.id {
			t.Fatalf("subtitle not bound to video via the legal prepare-time binder: %+v", sub.boundVideoOperationID)
		}
		if sub.state != ownerStatePending {
			t.Fatalf("subtitle op state=%s (must be pending; the prepare stage must not pre-create successor work)", sub.state)
		}
		countContinuations := func() int {
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_id IN ($1,$2)`, video.id, sub.id).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		if pre := countContinuations(); pre != 1 {
			t.Fatalf("pre-finish continuations=%d want 1 (only the video carries work)", pre)
		}
		// Owner-role fixed finish (the runtime lane was denied above).
		ownerDB := ownerTestDB(t)
		savedDB, savedStore := s.db, s.workItems
		s.db = ownerDB
		s.workItems = &workitems.Store{DB: ownerDB, RetentionDays: workitems.DefaultRetentionDays}
		out, finishErr := s.finishPreparedFileSuccess(ctx, c, preparedResult)
		if finishErr != nil {
			s.db, s.workItems = savedDB, savedStore
			t.Fatalf("owner finish=%v", finishErr)
		}
		if out.OwnerResult.State != "completed" {
			s.db, s.workItems = savedDB, savedStore
			t.Fatalf("finish state=%s", out.OwnerResult.State)
		}
		// The handoff created the dependent subtitle's first work.
		assertS4R63Continuations(t, countContinuations())
		// Idempotent re-entry: re-finishing the same fixed entry
		// neither creates a second successor work nor repeats any business write.
		beforeReplay := planGraphSnapshot(t, db)
		if _, err := s.finishPreparedFileSuccess(ctx, c, preparedResult); err != nil {
			s.db, s.workItems = savedDB, savedStore
			t.Fatalf("idempotent re-entry=%v", err)
		}
		s.db, s.workItems = savedDB, savedStore
		assertS4R63Continuations(t, countContinuations())
		var admissions int
		if err := db.QueryRow(`SELECT count(*) FROM notification_admissions WHERE source_owner_type='file_operation' AND source_owner_id=$1`, c.Owner.ID).Scan(&admissions); err != nil || admissions != 1 {
			t.Fatalf("re-entry admissions=%d err=%v", admissions, err)
		}
		if planGraphSnapshot(t, db) != beforeReplay {
			t.Fatal("idempotent re-entry wrote graph")
		}
		var subWork int
		if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_id=$1 AND kind='file_execute'`, sub.id).Scan(&subWork); err != nil || subWork != 1 {
			t.Fatalf("dependent subtitle successor work=%d err=%v", subWork, err)
		}
		t.Logf("sql_positive handoff continuations=2 (dependent subtitle first work created; idempotent re-entry zero-write, admissions=1)")
		return
	}
	if finishRollback {
		// Per-write-point rollback matrix over the ordinary owner finish
		// (handoff successor shape). See runFinishWritePointRollbackMatrix.
		subOp := operationForFile(t, loadOperations(t, db, f.artifactID), "r63-subtitle")
		runFinishWritePointRollbackMatrix(t, ctx, db, s, c, permit, subOp.id, preparedResult, "file_finish_runtime_entry(", false)
		return
	}
	verifyFileSuccessProjection(t, ctx, db, c, preparedResult)
	if preparedResult.operation != c.Owner.ID || preparedResult.originalWork != c.ID || preparedResult.finalObjectCanonical == "" || preparedResult.evidenceDigest == "" {
		t.Fatal("incomplete prepared result")
	}
	if preparedResult.evidenceDigest != protocol.Digest("acquisition-effect-resolution:v1", []byte(preparedResult.resolutionCanonical)) {
		t.Fatal("unregistered resolution hash domain")
	}
	fact, err := protocol.ParseStrict([]byte(preparedResult.fileFactCanonical))
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidateFileFact(fact); err != nil {
		t.Fatal(err)
	}
	if fact.Object["provenance"].Object["operation_ref"].Object["value"].String != c.Owner.ID || fact.Object["locator"].Object["source_cleanup"].String != preparedResult.sourceCleanup {
		t.Fatal("execution FileFact lost operation/cleanup")
	}
	if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE public.managed_anime SET title='Changed current management title' WHERE id=$1`, f.animeID); err != nil {
		t.Fatal(err)
	}
	recaptured, err := s.prepareSuccessfulFileResult(ctx, c, permit)
	if err != nil {
		t.Fatal(err)
	}
	if recaptured.fileFactCanonical != preparedResult.fileFactCanonical || recaptured.notificationPayload != preparedResult.notificationPayload {
		t.Fatal("current management changed frozen result projection")
	}
	verifyQ13ProvenanceNegatives(t, ctx, db, c, permit, false, preparedResult.notificationPayload, preparedResult.fileFactCanonical)
	var notification map[string]any
	if err := json.Unmarshal([]byte(preparedResult.notificationPayload), &notification); err != nil {
		t.Fatal(err)
	}
	// anime.id is the frozen config identity, not a current management row
	// (271); the SQL projector parity above already binds every field.
	if notification["event.type"] != "file_operation_completed" || notification["result.outcome"] != "success" {
		t.Fatalf("notification=%v", notification)
	}
	var factValid bool
	factTx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := factTx.QueryRow(`SELECT public.vars_filefact_valid($1,$2::jsonb) AND public.filefact_source_relation_valid($1,$3,$4,$2::jsonb)`, preparedResult.root, preparedResult.fileFactCanonical, f.artifactID, preparedResult.fileRef).Scan(&factValid); err != nil || !factValid {
		factTx.Rollback()
		t.Fatalf("FileFact consumers=%v err=%v", factValid, err)
	}
	if err := verifyPreparedFileResultTx(ctx, factTx, c, preparedResult); err != nil {
		factTx.Rollback()
		t.Fatal(err)
	}
	for _, mutate := range []func(*preparedFileResult){func(p *preparedFileResult) { p.finalObjectCanonical = "{}" }, func(p *preparedFileResult) { p.fileFactCanonical = "{}" }, func(p *preparedFileResult) { p.notificationPayload = "{}" }, func(p *preparedFileResult) { p.session = "foreign-session" }, func(p *preparedFileResult) { p.specDigest = "foreign-spec" }} {
		foreign := preparedResult
		mutate(&foreign)
		if err := verifyPreparedFileResultTx(ctx, factTx, c, foreign); err == nil {
			factTx.Rollback()
			t.Fatal("mutated prepared result accepted")
		}
	}
	factTx.Rollback()
	for _, mutate := range []func(*workitems.Claim){func(c *workitems.Claim) { c.FencingToken++ }, func(c *workitems.Claim) { c.ExpectedOwnerVersion++ }, func(c *workitems.Claim) { c.ID = "foreign-work" }, func(c *workitems.Claim) { c.Owner.ID = "foreign-operation" }, func(c *workitems.Claim) { c.InputSnapshot = json.RawMessage(`{}`) }, func(c *workitems.Claim) {
		c.OneShot = &workitems.OneShotRef{MarkerID: "foreign-marker", CorrelationID: "foreign-correlation", AdmissionEpoch: 1}
	}} {
		foreign := c
		mutate(&foreign)
		before := planGraphSnapshot(t, db)
		if _, err := s.prepareSuccessfulFileResult(ctx, foreign, permit); err == nil {
			t.Fatal("foreign prepared result accepted")
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("result capture wrote graph")
		}
	}
	if recoveryCapture {
		if _, err := s.prepareRecoveredSuccessfulFileResult(ctx, c, permit); err == nil {
			t.Fatal("ordinary claim impersonated recovery")
		}
		if err := s.workItems.RequestCancel(ctx, c, "cancel_requested"); err != nil {
			t.Fatal(err)
		}
		if err := s.workItems.BlockRecovery(ctx, c, "cancel_requested"); err != nil {
			t.Fatal(err)
		}
		if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE public.work_items SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, c.ID); err != nil {
			t.Fatal(err)
		}
		recovery, err := s.workItems.ClaimRecovery(ctx, []string{workitems.KindFileExecute}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		before := planGraphSnapshot(t, db)
		verifyFileFinalizationGates(t, ctx, db, recovery, permit, true)
		recovered, err := s.prepareRecoveredSuccessfulFileResult(ctx, recovery, permit)
		if err != nil {
			t.Fatal(err)
		}
		if !recovered.recoveryClaim || recovered.originalToken != c.FencingToken || recovered.claimToken != recovery.FencingToken || recovered.originalToken == recovered.claimToken || recovered.session != preparedResult.session || recovered.fileFactCanonical != preparedResult.fileFactCanonical || recovered.notificationPayload != preparedResult.notificationPayload {
			t.Fatal("recovery replaced original effect identity")
		}
		verifyQ13ProvenanceNegatives(t, ctx, db, recovery, permit, true, recovered.notificationPayload, recovered.fileFactCanonical)
		if d9 {
			verifyD9Suppression(t, ctx, db, recovery, recovered)
		}
		if runtimeFinishCancel && runtimeFinish && r63Cancel {
			// Runtime lane: the real worker entry (Service.Handle on the
			// store-issued recovery claim, NOT ownerRole finishPreparedFileSuccess
			// direct call) drives the cancel-won fixed entry through the full
			// handler dispatch — typedExecutionPending routes it, prepareTypedFileResult
			// re-captures through the recovery lane, and finishPreparedFileSuccess
			// picks file_finish_cancelled_success by the claim's cancel marker.
			runtimeOut, runtimeErr := s.Handle(ctx, recovery)
			if runtimeErr != nil {
				t.Fatalf("runtime cancel entry=%v", runtimeErr)
			}
			if runtimeOut.OwnerResult.State != "completed" || runtimeOut.Disposition != workitems.DispositionConfirmed {
				t.Fatalf("runtime cancel outcome=%+v", runtimeOut)
			}
			subOp := operationForFile(t, loadOperations(t, db, f.artifactID), "r63-subtitle")
			var cont int
			if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_id IN ($1,$2)`, recovery.Owner.ID, subOp.id).Scan(&cont); err != nil {
				t.Fatal(err)
			}
			wantCont := 1
			if placementWired {
				// The video's own placement continuation is not a successor:
				// the subtitle stays suppressed (no work for subOp).
				wantCont = 2
				assertPlacementHandoff(t, db, recovery.Owner.ID, recovery.ID)
				var subWork int
				if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_id=$1`, subOp.id).Scan(&subWork); err != nil || subWork != 0 {
					t.Fatalf("cancelled finish created subtitle work=%d err=%v", subWork, err)
				}
				t.Logf("sql_positive cancelled_success -> one pending obligation + one placement continuation; subtitle still suppressed")
			} else {
				assertPlacementAbsent(t, db, recovery.Owner.ID)
			}
			if cont != wantCont {
				t.Fatalf("runtime cancel continuations=%d want %d (suppression, no successor work)", cont, wantCont)
			}
			var subResult bool
			if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM acquisition_files WHERE acquisition_id=$1 AND file_ref='subtitle' AND outcome='failed' AND effect_class='no_external_effect' AND delivery_reason_code='DEPENDENCY_HANDOFF_CANCELLED')`, recovered.root).Scan(&subResult); err != nil || !subResult {
				t.Fatalf("runtime cancel subtitle result=%v err=%v", subResult, err)
			}
			// Idempotent re-entry through the same worker entry: zero write.
			beforeReentry := planGraphSnapshot(t, db)
			if _, err := s.Handle(ctx, recovery); err != nil {
				t.Fatalf("runtime cancel re-entry=%v", err)
			}
			if planGraphSnapshot(t, db) != beforeReentry {
				t.Fatal("runtime cancel re-entry wrote graph")
			}
			t.Logf("sql_positive runtime_cancel worker-entry cancelled finish (DEPENDENCY_HANDOFF_CANCELLED, continuations=1, idempotent re-entry zero-write)")
			return
		}
		if r63Cancel {
			// The full fixed cancelled completion. The
			// cancel-won recovery claim (cancel_requested) routes finishPreparedFileSuccess
			// to file_finish_cancelled_success. The video completes/succeeds, but the
			// dependent subtitle is suppressed (DEPENDENCY_HANDOFF_CANCELLED, no successor
			// work) — the contrast: cancel keeps continuations=1 (the ordinary
			// handoff reaches 2).
			cancelOwner := ownerTestDB(t)
			cancelSavedDB, cancelSavedStore := s.db, s.workItems
			s.db = cancelOwner
			s.workItems = &workitems.Store{DB: cancelOwner, RetentionDays: workitems.DefaultRetentionDays}
			cancelOut, cancelErr := s.finishPreparedFileSuccess(ctx, recovery, recovered)
			s.db, s.workItems = cancelSavedDB, cancelSavedStore
			if cancelErr != nil {
				t.Fatalf("cancelled finish=%v", cancelErr)
			}
			if cancelOut.OwnerResult.State != "completed" {
				t.Fatalf("cancelled finish state=%s", cancelOut.OwnerResult.State)
			}
			subOp := operationForFile(t, loadOperations(t, db, f.artifactID), "r63-subtitle")
			var cont int
			if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_id IN ($1,$2)`, recovery.Owner.ID, subOp.id).Scan(&cont); err != nil {
				t.Fatal(err)
			}
			if cont != 1 {
				t.Fatalf("D9 cancel continuations=%d want 1 (dependent subtitle suppressed, no successor work)", cont)
			}
			var subResult bool
			if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM acquisition_files WHERE acquisition_id=$1 AND file_ref='subtitle' AND outcome='failed' AND effect_class='no_external_effect' AND delivery_reason_code='DEPENDENCY_HANDOFF_CANCELLED')`, recovered.root).Scan(&subResult); err != nil || !subResult {
				t.Fatalf("D9 subtitle result=%v err=%v", subResult, err)
			}
			t.Logf("sql_positive d9_cancel continuations=1 (subtitle DEPENDENCY_HANDOFF_CANCELLED, no successor work)")
			return
		}
		if finishRollbackCancel {
			// Per-write-point rollback matrix over the cancel-won recovery
			// finish (suppression successor shape).
			subOp := operationForFile(t, loadOperations(t, db, f.artifactID), "r63-subtitle")
			runFinishWritePointRollbackMatrix(t, ctx, db, s, recovery, permit, subOp.id, recovered, "file_finish_runtime_entry(", true)
			return
		}
		verifyTx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyPreparedFileResultTx(ctx, verifyTx, recovery, recovered); err != nil {
			verifyTx.Rollback()
			t.Fatal(err)
		}
		verifyTx.Rollback()
		wrongCancel := recovery
		wrongCancel.CancelRequested = !wrongCancel.CancelRequested
		if _, err := s.prepareRecoveredSuccessfulFileResult(ctx, wrongCancel, permit); err == nil {
			t.Fatal("wrong recovery cancel marker accepted")
		}
		if _, err := s.prepareRecoveredSuccessfulFileResult(ctx, c, permit); err == nil {
			t.Fatal("stale original recovery accepted")
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("recovery capture wrote graph")
		}
		for _, mutate := range []func(*workitems.Claim){func(c *workitems.Claim) { c.FencingToken++ }, func(c *workitems.Claim) { c.ExpectedOwnerVersion++ }, func(c *workitems.Claim) { c.ID = "foreign-work" }, func(c *workitems.Claim) { c.Owner.ID = "foreign-operation" }} {
			foreign := recovery
			mutate(&foreign)
			if _, err := s.prepareRecoveredSuccessfulFileResult(ctx, foreign, permit); err == nil {
				t.Fatal("foreign recovery accepted")
			}
		}
		if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE public.work_items SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, recovery.ID); err != nil {
			t.Fatal(err)
		}
		beforeExpired := planGraphSnapshot(t, db)
		if _, err := s.prepareRecoveredSuccessfulFileResult(ctx, recovery, permit); err == nil {
			t.Fatal("expired recovery accepted")
		}
		if planGraphSnapshot(t, db) != beforeExpired {
			t.Fatal("expired capture wrote graph")
		}
		return
	}
	base, _, _, err := s.publicationReceiptBase(ctx, permit)
	if err != nil {
		t.Fatal(err)
	}
	before = planGraphSnapshot(t, db)
	base["observed_at"] = observationTime(time.Now().UTC())
	duplicate, err := publicationEnvelope(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.recordFileReceipt(ctx, permit, duplicate); err != nil {
		t.Fatal(err)
	}
	if planGraphSnapshot(t, db) != before {
		t.Fatal("same terminal proof changed graph")
	}
	base["observed_at"] = observationTime(time.Now().UTC())
	// PathIdentityV1 is the bare {kind, device, inode} object.
	proof := receiptObject(t, base, "payload", "checkpoint", "publish_target", "proof")
	receiptObject(t, proof, "target", "identity")["inode"] = "999999"
	envelope, err := publicationEnvelope(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.recordFileReceipt(ctx, permit, envelope); err != nil {
		t.Fatal(err)
	}
	var conflicts int
	if err := db.QueryRow(`SELECT publication_conflict_count FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&conflicts); err != nil || conflicts != 1 {
		t.Fatalf("conflicts=%d err=%v", conflicts, err)
	}
	before = planGraphSnapshot(t, db)
	if err := s.recordFileReceipt(ctx, permit, envelope); err != nil {
		t.Fatal(err)
	}
	if planGraphSnapshot(t, db) != before {
		t.Fatal("duplicate conflict changed graph")
	}
	if _, err := s.loadPublicationProof(ctx, permit); err == nil {
		t.Fatal("conflicting evidence accepted as success proof")
	}
	verifyTerminalDigestMatrix(t, ctx, s, db, permit)
	for _, step := range []string{"stage_temp", "reserve_version", "publish_target", "source_cleanup", "temp_cleanup"} {
		receiptBase, _, _, err := s.publicationReceiptBase(ctx, permit)
		if err != nil {
			t.Fatal(err)
		}
		receiptBase["observed_at"] = observationTime(time.Now().UTC())
		slot := receiptObject(t, receiptBase, "payload", "checkpoint", step)
		if slot["state"] != "confirmed" {
			continue
		}
		slot["state"] = "no_effect"
		slot["proof"] = map[string]any{"kind": "no_effect", "step": step, "condition_digest": "sha256:" + strings.Repeat("0", 64), "reason_code": "PROOF_UNAVAILABLE", "closure_digest": "sha256:" + strings.Repeat("0", 64)}
		opposite, err := publicationEnvelope(receiptBase)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.recordFileReceipt(ctx, permit, opposite); err != nil {
			t.Fatalf("opposite terminal %s: %v", step, err)
		}
		before = planGraphSnapshot(t, db)
		if err := s.recordFileReceipt(ctx, permit, opposite); err != nil {
			t.Fatal(err)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("same opposite terminal conflict wrote graph")
		}
	}
	before = planGraphSnapshot(t, db)
	if err := s.recordFileReceipt(ctx, permit, envelope); err != nil {
		t.Fatal(err)
	}
	if planGraphSnapshot(t, db) != before {
		t.Fatal("historical conflict replay wrote graph after intervening conflicts")
	}
	if operation == domain.OperationHardlink && collision == domain.CollisionBlock {
		for index := 0; index < 101; index++ {
			receiptObject(t, proof, "target", "identity")["inode"] = fmt.Sprint(1000000 + index)
			candidate, err := publicationEnvelope(base)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.recordFileReceipt(ctx, permit, candidate); err != nil {
				t.Fatal(err)
			}
		}
		var count int
		var exhausted bool
		if err := db.QueryRow(`SELECT publication_conflict_count,publication_conflict_index_exhausted FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&count, &exhausted); err != nil || count != 100 || !exhausted {
			t.Fatalf("conflict bound %d %v %v", count, exhausted, err)
		}
		before = planGraphSnapshot(t, db)
		if err := s.recordFileReceipt(ctx, permit, envelope); err != nil {
			t.Fatal(err)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("historical conflict replay after index exhaustion wrote graph")
		}
	}
	// The operation's proof is closed at this point, so a re-entry reaches
	// only the fixed finish (proven above).
	exists, existsErr := s.operationExists(ctx, c)
	if existsErr != nil || !exists {
		t.Fatalf("operation lookup=%v exists=%v", existsErr, exists)
	}
}

// capturePermitProof snapshots the durable publication proof the finish consumes;
// a rollback fault must never mutate it (real proof preserved).
func capturePermitProof(t *testing.T, db *sql.DB, permit string) string {
	t.Helper()
	var snap string
	if err := db.QueryRow(`SELECT concat_ws('|',publication_dispatch_state,publication_effect_class,(publication_closed_proof IS NOT NULL)::text,(publication_resolution_proof IS NOT NULL)::text,publication_conflict_count::text) FROM file_operations WHERE publication_permit_id=$1`, permit).Scan(&snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

// runFinishWritePointRollbackMatrix drives the fixed owner-role finish under
// per-write-point faults and asserts the ENTIRE atomic write set rolls back
// atomically (no half-write / no partial commit) with the real proof preserved,
// classifying each outcome: apply-phase / clean-rollback =>
// ErrFinishNotCommitted (retry-safe, same fixed entry, no new grant);
// committed-but-ack-lost => idempotent success; unreadable ledger => ambiguous, a
// plain error that MUST NOT wrap ErrFinishNotCommitted, zero-write, no blind
// replay, no Disposition recorded. `needle` selects the normal vs cancelled finish
// command; `cancelled` selects the suppression successor shape (no successor work,
// subtitle DEPENDENCY_HANDOFF_CANCELLED).
func runFinishWritePointRollbackMatrix(t *testing.T, ctx context.Context, db *sql.DB, s *Service, c workitems.Claim, permit, subID string, prepared preparedFileResult, needle string, cancelled bool) {
	t.Helper()
	videoID := c.Owner.ID
	proofBefore := capturePermitProof(t, db, permit)
	graphBefore := planGraphSnapshot(t, db)

	// Every finish body write point must be absent after a fault.
	assertWriteSetRolledBack := func(stage string) {
		t.Helper()
		var dr, sw, adm, subFail int
		var opState, workState string
		if err := db.QueryRow(`SELECT count(*) FROM acquisition_files WHERE operation_id=$1 AND outcome='succeeded'`, videoID).Scan(&dr); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_id=$1 AND kind='file_execute'`, subID).Scan(&sw); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT state FROM file_operations WHERE id=$1`, videoID).Scan(&opState); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM notification_admissions WHERE source_owner_type='file_operation' AND source_owner_id=$1`, videoID).Scan(&adm); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT state FROM work_item_history WHERE id=$1`, c.ID).Scan(&workState); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM acquisition_files WHERE operation_id=$1 AND outcome='failed' AND delivery_reason_code='DEPENDENCY_HANDOFF_CANCELLED'`, subID).Scan(&subFail); err != nil {
			t.Fatal(err)
		}
		if dr != 0 || sw != 0 || opState == "completed" || adm != 0 || workState == "succeeded" || subFail != 0 {
			t.Fatalf("%s: half-write after fault (delivery=%d successorWork=%d opState=%s admission=%d workState=%s subSuppressed=%d)", stage, dr, sw, opState, adm, workState, subFail)
		}
		if got := capturePermitProof(t, db, permit); got != proofBefore {
			t.Fatalf("%s: publication proof mutated by fault (%q -> %q)", stage, proofBefore, got)
		}
	}

	// Every write point must be present after a genuine commit.
	assertWriteSetCommitted := func(stage string) {
		t.Helper()
		var dr, sw, adm int
		var opState, workState string
		if err := db.QueryRow(`SELECT count(*) FROM acquisition_files WHERE operation_id=$1 AND outcome='succeeded'`, videoID).Scan(&dr); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_id=$1 AND kind='file_execute'`, subID).Scan(&sw); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT state FROM file_operations WHERE id=$1`, videoID).Scan(&opState); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM notification_admissions WHERE source_owner_type='file_operation' AND source_owner_id=$1`, videoID).Scan(&adm); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT state FROM work_item_history WHERE id=$1`, c.ID).Scan(&workState); err != nil {
			t.Fatal(err)
		}
		if dr != 1 || opState != "completed" || adm != 1 || workState != "succeeded" {
			t.Fatalf("%s: committed write set incomplete (delivery=%d opState=%s admission=%d workState=%s)", stage, dr, opState, adm, workState)
		}
		if cancelled {
			var subFail int
			if err := db.QueryRow(`SELECT count(*) FROM acquisition_files WHERE operation_id=$1 AND outcome='failed' AND delivery_reason_code='DEPENDENCY_HANDOFF_CANCELLED'`, subID).Scan(&subFail); err != nil {
				t.Fatal(err)
			}
			if sw != 0 || subFail != 1 {
				t.Fatalf("%s: cancelled successor shape (successorWork=%d subSuppressed=%d want 0/1)", stage, sw, subFail)
			}
		} else if sw != 1 {
			t.Fatalf("%s: normal handoff successor work=%d want 1", stage, sw)
		}
		if got := capturePermitProof(t, db, permit); got != proofBefore {
			t.Fatalf("%s: publication proof mutated by commit", stage)
		}
	}

	// ownerFinish runs the fixed finish on the owner lane; commitFault injects a
	// commit-boundary lost-ack via planLostCommitConnector (nil = no fault).
	ownerFinish := func(result preparedFileResult, commitFault *bool) (string, error, *planLostCommitConnector) {
		ownerDB := ownerTestDB(t)
		runDB := ownerDB
		var connector *planLostCommitConnector
		if commitFault != nil {
			cfg, err := pgx.ParseConfig(os.Getenv("BANSOKO_TEST_DATABASE_URL"))
			if err != nil {
				t.Fatal(err)
			}
			connector = &planLostCommitConnector{Connector: stdlib.GetConnector(*cfg), queryNeedle: needle, rollbackBeforeReply: *commitFault}
			runDB = sql.OpenDB(connector)
			defer runDB.Close()
		}
		savedDB, savedStore := s.db, s.workItems
		s.db = runDB
		s.workItems = &workitems.Store{DB: runDB, RetentionDays: workitems.DefaultRetentionDays}
		out, ferr := s.finishPreparedFileSuccess(ctx, c, result)
		s.db, s.workItems = savedDB, savedStore
		if ferr != nil {
			return "", ferr, connector
		}
		return out.OwnerResult.State, nil, connector
	}

	// (1) apply-phase fault: malformed notification -> validation rejects before any
	// write; clean rollback => ErrFinishNotCommitted (retry-safe).
	bad := prepared
	bad.notificationPayload = `{"event.type":"file_operation_completed"}`
	if _, err, _ := ownerFinish(bad, nil); !errors.Is(err, ErrFinishNotCommitted) {
		t.Fatalf("apply-phase fault=%v (want ErrFinishNotCommitted)", err)
	}
	assertWriteSetRolledBack("apply-phase")
	if planGraphSnapshot(t, db) != graphBefore {
		t.Fatal("apply-phase fault wrote graph")
	}

	// (2) commit-boundary rollback: server rolled back, ack lost -> whole write set
	// gone; confirm reads empty ledger => ErrFinishNotCommitted (retry-safe).
	rb := true
	if _, err, conn := ownerFinish(prepared, &rb); true {
		if conn.commits.Load() != 1 {
			t.Fatalf("commit-rollback fault not injected: commits=%d", conn.commits.Load())
		}
		if !errors.Is(err, ErrFinishNotCommitted) {
			t.Fatalf("commit-rollback=%v (want ErrFinishNotCommitted)", err)
		}
	}
	assertWriteSetRolledBack("commit-rollback")
	if planGraphSnapshot(t, db) != graphBefore {
		t.Fatal("commit-rollback wrote graph")
	}

	// (3) commit-boundary commit: server committed, ack lost -> confirm reads full
	// ledger => idempotent success; every write point present.
	rc := false
	state, err, conn := ownerFinish(prepared, &rc)
	if conn.commits.Load() != 1 {
		t.Fatalf("commit-commit fault not injected: commits=%d", conn.commits.Load())
	}
	if err != nil {
		t.Fatalf("commit-commit=%v (want idempotent success)", err)
	}
	if state != "completed" {
		t.Fatalf("commit-commit state=%s", state)
	}
	assertWriteSetCommitted("commit-commit")
	graphAfterCommit := planGraphSnapshot(t, db)

	// (4) ambiguous: confirm unreadable -> plain error, MUST NOT wrap
	// ErrFinishNotCommitted; zero-write, no blind replay, no Disposition.
	badDB, badErr := sql.Open("pgx", "postgres://127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if badErr != nil {
		t.Fatal(badErr)
	}
	defer badDB.Close()
	savedDB, savedStore := s.db, s.workItems
	s.db = badDB
	s.workItems = &workitems.Store{DB: badDB, RetentionDays: workitems.DefaultRetentionDays}
	_, ambErr := s.finishPreparedFileSuccess(ctx, c, prepared)
	s.db, s.workItems = savedDB, savedStore
	if ambErr == nil || errors.Is(ambErr, ErrFinishNotCommitted) || !strings.Contains(ambErr.Error(), "unverifiable") {
		t.Fatalf("ambiguous=%v (want unverifiable, never ErrFinishNotCommitted)", ambErr)
	}
	if planGraphSnapshot(t, db) != graphAfterCommit {
		t.Fatal("ambiguous re-entry wrote graph")
	}

	// (5) idempotent re-entry (restored owner lane): success, zero write.
	state, err, _ = ownerFinish(prepared, nil)
	if err != nil {
		t.Fatalf("idempotent re-entry=%v", err)
	}
	if state != "completed" {
		t.Fatalf("idempotent re-entry state=%s", state)
	}
	if planGraphSnapshot(t, db) != graphAfterCommit {
		t.Fatal("idempotent re-entry wrote graph")
	}
	t.Logf("sql_positive finish-rollback-matrix needle=%s cancelled=%v: apply+commit-rollback=>NotCommitted(retry-safe) zero half-write, commit=>idempotent full write set, unreadable=>ambiguous(no NotCommitted wrap), proof preserved", needle, cancelled)
}

const overwriteCompetitionRounds = 50

// receiptObject walks nested JSON objects of a decoded receipt and fails the
// test (instead of panicking the package) when a member is missing or not an
// object, naming the path that diverged from the receipt shape.
func receiptObject(t *testing.T, v any, path ...string) map[string]any {
	t.Helper()
	current, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("receipt value is %T, not an object", v)
	}
	for i, key := range path {
		next, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("receipt member %s is %T, not an object", strings.Join(path[:i+1], "."), current[key])
		}
		current = next
	}
	return current
}
