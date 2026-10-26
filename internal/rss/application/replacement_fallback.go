package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"bansoko/internal/workitems"
)

// releaseTriggerReplacementFallback is the release_evaluate input trigger the
// replacement fallback query writes for a release it promoted
// (filemaintenance.TriggerReleaseFallback).
const releaseTriggerReplacementFallback = "replacement_fallback"

// releaseEvaluationTrigger reads the trigger of a release_evaluate input.
func releaseEvaluationTrigger(raw json.RawMessage) string {
	var input struct {
		Trigger string `json:"trigger"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &input) != nil {
		return ""
	}
	return input.Trigger
}

// enqueueReplacementFallbackTx creates or merges the anime's
// replacement_fallback item in the release owner transaction (the subscription
// row is already locked by the owner writer). Only an active subscription
// enqueues it.
func enqueueReplacementFallbackTx(ctx context.Context, tx *sql.Tx, store *workitems.Store, animeID string) error {
	if store == nil {
		return errors.New("work item store is nil")
	}
	var (
		state   string
		version int64
	)
	err := tx.QueryRowContext(ctx, `SELECT state, version FROM subscriptions WHERE managed_anime_id = $1`, animeID).Scan(&state, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read subscription for replacement fallback: %w", err)
	}
	if state != "active" {
		return nil
	}
	if version < 1 {
		version = 1
	}
	if _, _, err := store.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: animeID},
		Kind:                 workitems.KindReplacementFallback,
		IdempotencyKey:       "replacement_fallback:" + animeID,
		InputSnapshot:        json.RawMessage(`{"trigger":"promotion_ended"}`),
		MaxAttempts:          3,
		ExpectedOwnerVersion: version,
	}); err != nil {
		return fmt.Errorf("enqueue replacement fallback: %w", err)
	}
	return nil
}
