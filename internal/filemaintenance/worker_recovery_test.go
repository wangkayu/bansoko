package filemaintenance

import (
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"testing"
)

func TestCalibrationRecoveryFactoriesAreTyped(t *testing.T) {
	s := &CalibrationService{}
	for _, h := range []worker.Handler{s.CalibrateHandler(), s.ConsumeAlignmentHandler(), s.ConsumeReplacementHandler()} {
		if _, ok := h.(worker.RecoveryObserver); !ok {
			t.Fatal("registry factory lacks observer")
		}
	}
}
func TestPGCalibrationRecoveryMissingCommitProofRemainsBlocked(t *testing.T) {
	f := newCalibrationFixture(t)
	seedAnimeSubscription(t, f.db, "anime-cal-0", "sub-cal-0", "active")
	seedCompletedGeneration(t, f.db, "anime-cal-0", 1, nil)
	c := f.claimDue(t, workitems.KindReconciliationCalibrate)
	o, err := f.calibration.CalibrateHandler().(worker.RecoveryObserver).ObserveRecovery(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind == worker.OutcomeFinish || o.Disposition != workitems.DispositionAmbiguous {
		t.Fatalf("absence guessed commit: %+v", o)
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM work_item_history WHERE kind IN ('reconciliation_consume_alignment','reconciliation_consume_replacement')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("recovery reran calibration")
	}
}
