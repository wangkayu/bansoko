package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ProvisionFrozenBusinessTx never reads Global defaults. Caller owns transaction.
func (p Provisioner) ProvisionFrozenBusinessTx(ctx context.Context, tx *sql.Tx, animeID string, revision CandidateRevision) error {
	if tx == nil || revision.Business == nil || revision.BusinessDigest == nil {
		return errors.New("complete frozen business revision is required")
	}
	if err := revision.VerifyIntegrity(); err != nil {
		return err
	}
	business, err := revision.Business.Freeze()
	if err != nil {
		return err
	}
	resolved, err := revision.Resolved()
	if err != nil {
		return err
	}
	business, err = p.ProvisionBusinessAxesTx(ctx, tx, animeID, business)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(business)
	if err != nil {
		return err
	}
	// The merged row is the one configuration source: the axes written above,
	// the frozen business and the candidate revision it came from live in one
	// row, so the acceptance digest check and the policy axes can never
	// disagree.
	if _, err := tx.ExecContext(ctx, `
UPDATE anime_business_configs
SET candidate_id = $2, revision = $3, business_snapshot = $4, business_digest = $5,
    business_version = 1, updated_at = NOW()
WHERE managed_anime_id = $1`,
		animeID, revision.CandidateID, revision.Revision, raw, *revision.BusinessDigest); err != nil {
		return err
	}
	return materializeResolvedOverrides(ctx, tx, animeID, resolved)
}
