package main

import (
	fileoperation "bansoko/internal/fileoperation/application"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"context"
	"errors"
)

func (a *placementSinkAdapter) AdmitPlacementRecoveryTx(ctx context.Context, scope txscope.Tx, c workitems.Claim) error {
	if a == nil || a.triggers == nil {
		return errors.New("placement recovery trigger is not wired")
	}
	anime, release, err := fileoperation.ValidatePlacementRecoveryAdmissionTx(ctx, scope, c)
	if err != nil {
		return err
	}
	return a.triggers.TriggerPlacementRecoveryTx(ctx, scope, anime, release)
}
func (s *placementFaultSink) AdmitPlacementRecoveryTx(ctx context.Context, scope txscope.Tx, c workitems.Claim) error {
	if s == nil || s.inner == nil {
		return errors.New("placement recovery sink is not wired")
	}
	if s.take(ctx, c.Owner.ID) {
		return errors.New("fixture placement recovery admission rejected")
	}
	sink, ok := s.inner.(fileoperation.PlacementRecoverySink)
	if !ok {
		return errors.New("placement recovery typed sink unavailable")
	}
	return sink.AdmitPlacementRecoveryTx(ctx, scope, c)
}

var _ fileoperation.PlacementRecoverySink = (*placementSinkAdapter)(nil)
var _ fileoperation.PlacementRecoverySink = (*placementFaultSink)(nil)
