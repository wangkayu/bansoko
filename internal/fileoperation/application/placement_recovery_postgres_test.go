package application

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"bansoko/internal/filemaintenance"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// Placement continuation recovery (B6): a worker crashed while the placement
// continuation of a typed-completed operation was leased. The recovery claim
// may only adopt a placement handoff whose committed identity it re-checks
// inside the settlement transaction, may only admit the missing replacement
// trigger inside that same transaction, and never calls the ordinary sink.

// countedRecoverySink is the production-shaped recovery sink: validate the
// live recovery claim, then admit the trigger in the caller's transaction.
type countedRecoverySink struct {
	trigger *filemaintenance.ReplacementTriggerService
	calls   atomic.Int32
	fail    bool
}

func (s *countedRecoverySink) AdmitFromFileOperation(context.Context, string) error {
	return errors.New("ordinary admission prohibited")
}

func (s *countedRecoverySink) AdmitPlacementRecoveryTx(ctx context.Context, scope txscope.Tx, c workitems.Claim) error {
	s.calls.Add(1)
	anime, release, err := ValidatePlacementRecoveryAdmissionTx(ctx, scope, c)
	if err != nil {
		return err
	}
	if err := s.trigger.TriggerPlacementRecoveryTx(ctx, scope, anime, release); err != nil {
		return err
	}
	if s.fail {
		return errors.New("injected failure after trigger admission")
	}
	return nil
}

type placementRecoveryCase struct {
	db      *sql.DB
	service *Service
	store   *workitems.Store
	claim   workitems.Claim
	anime   string
	release string
}

// placementRecoveryClaim completes one typed operation through the real typed
// worker entry (the fixed finish commits the pending obligation and its
// continuation), leases the continuation, optionally records the placement the
// crashed stage had already committed, and turns the lease into a recovery
// claim exactly as the runtime does after a crash.
func placementRecoveryClaim(t *testing.T, placementCommitted bool) placementRecoveryCase {
	t.Helper()
	db, service, _, operationID := typedPlacementCompletion(t, &flakyPlacementSink{})
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	ctx := context.Background()
	c, err := store.ClaimDue(ctx, "placement-before-crash", []string{workitems.KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, class := classifyPlacementContinuation(c); class != continuationIsContinuation || c.Owner.ID != operationID {
		t.Fatalf("claimed %+v, want the placement continuation of %s", c, operationID)
	}
	x := placementRecoveryCase{db: db, service: service, store: store}
	if err := db.QueryRow(`SELECT id FROM managed_anime LIMIT 1`).Scan(&x.anime); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM releases LIMIT 1`).Scan(&x.release); err != nil {
		t.Fatal(err)
	}
	if placementCommitted {
		op, err := service.loadOperation(ctx, operationID)
		if err != nil {
			t.Fatal(err)
		}
		fp, err := decodeFingerprint(op.targetFingerprint)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ownerTestDB(t).Exec(`INSERT INTO placements(id,physical_path,managed_anime_id,source_evidence_type,source_owner_ref,source_fingerprint,target_fingerprint,quality_facts_ref,facts_version) VALUES ('recovery-placement',$1,$2,'file_operation',$3,$4,$4,$5,'frozen')`, op.targetPath, x.anime, operationID, fp.Value, x.release); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.BlockRecovery(ctx, c, "effect_unknown"); err != nil {
		t.Fatal(err)
	}
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	x.claim, err = store.ClaimRecovery(ctx, []string{c.Kind}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if x.claim.Owner.ID != operationID {
		t.Fatalf("recovery claimed %s, want %s", x.claim.Owner.ID, operationID)
	}
	return x
}

func (x placementRecoveryCase) withRecoverySink() *countedRecoverySink {
	sink := &countedRecoverySink{trigger: filemaintenance.NewReplacementTriggerService(x.db, x.store)}
	x.service.SetPlacementSink(sink)
	return sink
}

func (x placementRecoveryCase) settle(t *testing.T, o worker.Outcome) error {
	t.Helper()
	return x.store.SettleRecovery(context.Background(), x.claim, o.TerminalState, o.OwnerResult, o.Disposition, o.WriteOwner, o.PostTransition)
}

func recoveryTriggerCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE kind='replacement_evaluate' AND input_snapshot->>'trigger'='placement_committed'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A trigger admitted by the recovery sink commits or rolls back together with
// the obligation settlement; the retried settlement converges exactly once.
func TestPGPlacementRecoveryTriggerAndObligationRollbackTogether(t *testing.T) {
	x := placementRecoveryClaim(t, true)
	sink := x.withRecoverySink()
	sink.fail = true
	o, err := x.service.ObserveRecovery(context.Background(), x.claim)
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != worker.OutcomeFinish {
		t.Fatalf("committed placement must be adoptable: %+v", o)
	}
	if err := x.settle(t, o); err == nil {
		t.Fatal("injected failure was swallowed")
	}
	state, _, _ := placementObligationState(t, x.db, x.claim.Owner.ID)
	if state != placementObligationPending || recoveryTriggerCount(t, x.db) != 0 || sink.calls.Load() != 1 {
		t.Fatalf("trigger leaked outside the recovery transaction: obligation=%s triggers=%d calls=%d", state, recoveryTriggerCount(t, x.db), sink.calls.Load())
	}
	sink.fail = false
	if err := x.settle(t, o); err != nil {
		t.Fatal(err)
	}
	state, _, _ = placementObligationState(t, x.db, x.claim.Owner.ID)
	if state != placementObligationAdmitted || recoveryTriggerCount(t, x.db) != 1 || sink.calls.Load() != 2 {
		t.Fatalf("retry did not converge exactly once: obligation=%s triggers=%d calls=%d", state, recoveryTriggerCount(t, x.db), sink.calls.Load())
	}
	assertOperationCompleted(t, x.db, x.claim.Owner.ID)
	if attempts := fileAttemptsStarted(t, x.db, x.claim.Owner.ID); attempts != 1 {
		t.Fatalf("placement recovery redid the file effect: attempts=%d", attempts)
	}
}

// The recovery sink validates the live claim itself, independently of the
// store: a stale generation, a one-shot epoch change or a cancelled claim
// never admits a trigger and leaves the obligation pending.
func TestPGPlacementRecoverySinkRejectsStaleClaim(t *testing.T) {
	t.Run("stale_generation", func(t *testing.T) {
		x := placementRecoveryClaim(t, true)
		sink := x.withRecoverySink()
		if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET fencing_token=fencing_token+1 WHERE id=$1`, x.claim.ID); err != nil {
			t.Fatal(err)
		}
		tx, err := x.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := sink.AdmitPlacementRecoveryTx(context.Background(), txscope.Wrap(tx), x.claim); !errors.Is(err, workitems.ErrStaleClaim) {
			t.Fatalf("sink bypassed generation: %v", err)
		}
		if recoveryTriggerCount(t, x.db) != 0 || sink.calls.Load() != 1 {
			t.Fatal("stale sink admitted trigger")
		}
	})
	t.Run("one_shot_epoch_changed", func(t *testing.T) {
		x := placementRecoveryClaim(t, true)
		sink := x.withRecoverySink()
		seedOneShotMarkerFixture(t, x.db, "placement-epoch")
		c := x.claim
		c.OneShot = &workitems.OneShotRef{MarkerID: "placement-epoch", CorrelationID: "corr-placement-epoch", AdmissionEpoch: 1}
		if _, err := ownerTestDB(t).Exec(`UPDATE one_shot_acquisitions SET admission_epoch=2 WHERE id='placement-epoch'`); err != nil {
			t.Fatal(err)
		}
		tx, err := x.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := sink.AdmitPlacementRecoveryTx(context.Background(), txscope.Wrap(tx), c); !errors.Is(err, workitems.ErrOneShotReclaiming) {
			t.Fatalf("epoch change admitted trigger: %v", err)
		}
		if recoveryTriggerCount(t, x.db) != 0 || sink.calls.Load() != 1 {
			t.Fatal("epoch guard bypassed")
		}
	})
	t.Run("cancel_requested", func(t *testing.T) {
		x := placementRecoveryClaim(t, true)
		sink := x.withRecoverySink()
		o, err := x.service.ObserveRecovery(context.Background(), x.claim)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET cancel_requested=true WHERE id=$1`, x.claim.ID); err != nil {
			t.Fatal(err)
		}
		if err := x.settle(t, o); !errors.Is(err, workitems.ErrStaleClaim) {
			t.Fatalf("cancelled admission accepted: %v", err)
		}
		if recoveryTriggerCount(t, x.db) != 0 {
			t.Fatal("cancel admitted downstream trigger")
		}
		if state, _, _ := placementObligationState(t, x.db, x.claim.Owner.ID); state != placementObligationPending {
			t.Fatalf("cancel discarded the obligation: %s", state)
		}
		if sink.calls.Load() != 1 {
			t.Fatalf("sink did not validate the current cancellation: calls=%d", sink.calls.Load())
		}
	})
}

// A committed placement plus its committed trigger is adopted without any
// sink call; the settlement re-checks the placement identity under lock, so a
// placement changed after observation is refused and the obligation stays.
func TestPGPendingPlacementRecoveryRechecksCommittedIdentity(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "adopt", true: "identity_race"}[changed], func(t *testing.T) {
			x := placementRecoveryClaim(t, true)
			sink := &flakyPlacementSink{}
			x.service.SetPlacementSink(sink)
			ctx := context.Background()
			tx, err := x.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := x.store.Create(ctx, tx, workitems.WorkItemSpec{Owner: workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: x.anime}, Kind: workitems.KindReplacementEvaluate, IdempotencyKey: "recovery-trigger", InputSnapshot: []byte(`{"trigger":"placement_committed","candidate_release_id":"` + x.release + `"}`), ExpectedOwnerVersion: 1, MaxAttempts: 3}); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			o, err := x.service.ObserveRecovery(ctx, x.claim)
			if err != nil {
				t.Fatal(err)
			}
			if o.Kind != worker.OutcomeFinish {
				t.Fatalf("committed handoff not adopted: %+v", o)
			}
			if changed {
				if _, err := ownerTestDB(t).Exec(`UPDATE placements SET physical_path='/different/identity' WHERE id='recovery-placement'`); err != nil {
					t.Fatal(err)
				}
			}
			err = x.settle(t, o)
			expected := placementObligationAdmitted
			if changed {
				expected = placementObligationPending
				if !errors.Is(err, workitems.ErrStaleClaim) {
					t.Fatalf("identity race settled: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if state, _, _ := placementObligationState(t, x.db, x.claim.Owner.ID); state != expected || sink.calls != 0 {
				t.Fatalf("unsafe admission: %s calls=%d", state, sink.calls)
			}
			if recoveryTriggerCount(t, x.db) != 1 {
				t.Fatal("adoption duplicated the committed trigger")
			}
			assertOperationCompleted(t, x.db, x.claim.Owner.ID)
		})
	}
}

// Without a committed placement (no phase proof) the recovery observation is
// ambiguous: it never reruns the placement sink, the obligation stays pending
// with no attempt counted, and the completed operation is untouched.
func TestPGPendingPlacementRecoveryWithoutPhaseProofNeverCallsSink(t *testing.T) {
	x := placementRecoveryClaim(t, false)
	sink := &flakyPlacementSink{}
	x.service.SetPlacementSink(sink)
	ctx := context.Background()
	recovered, err := x.service.ObserveRecovery(ctx, x.claim)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Kind == worker.OutcomeFinish || recovered.Disposition != workitems.DispositionAmbiguous || sink.calls != 0 {
		t.Fatalf("pending handoff blindly reran sink: %+v calls=%d", recovered, sink.calls)
	}
	if err := x.store.DeferRecovery(ctx, x.claim, "recovery_effect_unknown", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	state, attempts, _ := placementObligationState(t, x.db, x.claim.Owner.ID)
	if state != placementObligationPending || attempts != 0 {
		t.Fatalf("pending evidence lost: %s %d", state, attempts)
	}
	assertOperationCompleted(t, x.db, x.claim.Owner.ID)
	if attempts := fileAttemptsStarted(t, x.db, x.claim.Owner.ID); attempts != 1 {
		t.Fatalf("placement recovery redid the file effect: attempts=%d", attempts)
	}
}
