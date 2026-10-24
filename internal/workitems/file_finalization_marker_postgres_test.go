package workitems

import (
	"bansoko/internal/persistence/txscope"
	"context"
	"errors"
	"testing"
)

func TestFileFinalizationRequiresMarkerAdmission(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "consume"
		if missing {
			name = "missing_rolls_back"
		}
		t.Run(name, func(t *testing.T) {
			f := newOneShotSettlementFixture(t, "m-file-finalization-"+name)
			ctx := context.Background()
			if missing {
				if err := f.store.WithTypedStageTransaction(ctx, f.claim, func(txscope.Tx) (EffectDisposition, error) { return DispositionConfirmed, nil }); err != nil {
					t.Fatal(err)
				}
			}
			before := readWorkItem(t, f.db, f.itemID)
			err := f.store.WithFileFinalizationTransaction(ctx, f.claim, func(scope txscope.Tx) (EffectDisposition, error) {
				_, err := scope.SQLTx().ExecContext(ctx, `SELECT public.work_item_finish('succeeded',$1,$2)`, f.claim.ExpectedOwnerVersion, f.itemID)
				return DispositionConfirmed, err
			})
			if missing {
				if !errors.Is(err, ErrInvalidOneShotLedger) {
					t.Fatalf("missing admission=%v", err)
				}
				after := readWorkItem(t, f.db, f.itemID)
				if after.state != before.state {
					t.Fatal("missing admission committed work closure")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if readWorkItem(t, f.db, f.itemID).state != StateSucceeded {
					t.Fatal("work not succeeded")
				}
			}
			if readOneShotSettlementMarker(t, f.db, f.markerID).count != 0 {
				t.Fatal("admission not consumed")
			}
		})
	}
}
