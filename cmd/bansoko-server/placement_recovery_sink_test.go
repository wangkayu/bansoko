package main

import (
	fileoperation "bansoko/internal/fileoperation/application"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"context"
	"testing"
)

func TestPlacementRecoverySinkRejectsUnfencedTransaction(t *testing.T) {
	a := &placementSinkAdapter{}
	if err := a.AdmitPlacementRecoveryTx(context.Background(), txscope.Tx{}, workitems.Claim{}); err == nil {
		t.Fatal("unconfigured recovery admitted")
	}
	if _, _, err := fileoperation.ValidatePlacementRecoveryAdmissionTx(context.Background(), txscope.Tx{}, workitems.Claim{}); err == nil {
		t.Fatal("nil transaction bypassed recovery fence")
	}
}
