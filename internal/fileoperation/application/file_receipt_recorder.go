package application

import (
	"context"
	"encoding/json"
	"fmt"

	protocol "bansoko/internal/acquisition/terminal/domain"
)

// recordFileReceipt is an internal trusted-adapter sink, never an HTTP input.
// It supplements the original permit even after its claim has changed. No
// owner/work locks or Finish/Ensure are acquired from this evidence path.
func (s *Service) recordFileReceipt(ctx context.Context, permit string, envelope protocol.Value) error {
	if s == nil || s.db == nil || ctx == nil {
		return ErrInvalidClaim
	}
	if err := protocol.Validate("FileReceiptEnvelopeV1", envelope); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var captureRaw []byte
	if err := tx.QueryRowContext(ctx, `SELECT public.file_receipt_capture($1)`, permit).Scan(&captureRaw); err != nil {
		return err
	}
	var capture struct {
		Identity   json.RawMessage `json:"identity"`
		Checkpoint json.RawMessage `json:"checkpoint"`
	}
	if err := json.Unmarshal(captureRaw, &capture); err != nil {
		return err
	}
	identityRaw, checkpointRaw := capture.Identity, capture.Checkpoint
	identity, err := protocol.ParseStrict(identityRaw)
	if err != nil {
		return err
	}
	checkpointRaw, err = normalizeCheckpointTimes(checkpointRaw)
	if err != nil {
		return err
	}
	checkpoint, err := protocol.ParseStrict(checkpointRaw)
	if err != nil {
		return err
	}
	if err := protocol.ValidateFileReceiptTransition(envelope, identity, checkpoint); err != nil {
		return err
	}
	var response []byte
	if err := tx.QueryRowContext(ctx, `SELECT public.file_record_receipt($1,$2)`, permit, string(protocol.Canonical(envelope))).Scan(&response); err != nil {
		return err
	}
	var reply struct {
		Recorded bool  `json:"recorded"`
		Version  int64 `json:"evidence_version"`
	}
	if err := json.Unmarshal(response, &reply); err != nil {
		return err
	}
	if reply.Version < 1 {
		return fmt.Errorf("invalid file receipt acknowledgement")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}
