package application

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"errors"
)

// ValidatePlacementRecoveryAdmissionTx is required by the typed sink, not just
// its caller. A terminal/cancelled/stale observation can never admit a trigger.
// Marker is locked before WorkItem, matching the store's marker-first order.
func ValidatePlacementRecoveryAdmissionTx(ctx context.Context, scope txscope.Tx, c workitems.Claim) (string, string, error) {
	if _, class := classifyPlacementContinuation(c); class != continuationIsContinuation || c.Kind != workitems.KindFileExecute || c.Owner.Type != workitems.OwnerFileOperation {
		return "", "", workitems.ErrInvalidSpec
	}
	tx := scope.SQLTx()
	if tx == nil {
		return "", "", workitems.ErrInvalidSpec
	}
	if c.OneShot != nil {
		var state string
		var epoch int64
		if err := tx.QueryRowContext(ctx, `SELECT state,admission_epoch FROM one_shot_acquisitions WHERE id=$1 FOR UPDATE`, c.OneShot.MarkerID).Scan(&state, &epoch); err != nil {
			return "", "", err
		}
		if state != workitems.OneShotStateRunning || epoch != c.OneShot.AdmissionEpoch {
			return "", "", workitems.ErrOneShotReclaiming
		}
	}
	var ownerVersion int64
	err := workitems.LockPlacementRecoveryTx(ctx, tx, c.ID, c.FencingToken, c.Owner.ID).Scan(&ownerVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", workitems.ErrStaleClaim
	}
	if err != nil {
		return "", "", err
	}
	if ownerVersion != c.ExpectedOwnerVersion {
		return "", "", workitems.ErrStaleClaim
	}
	var snapshot []byte
	if err := tx.QueryRowContext(ctx, `SELECT input_snapshot FROM work_item_history WHERE id=$1`, c.ID).Scan(&snapshot); err != nil {
		return "", "", err
	}
	storedRef, err := workitems.ParseOneShotRef(snapshot)
	if err != nil {
		return "", "", err
	}
	if (storedRef == nil) != (c.OneShot == nil) {
		return "", "", workitems.ErrStaleClaim
	}
	if storedRef != nil && *storedRef != *c.OneShot {
		return "", "", workitems.ErrStaleClaim
	}
	var version int64
	var state, path string
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT version,state,target_path,target_fingerprint FROM file_operations WHERE id=$1 FOR UPDATE`, c.Owner.ID).Scan(&version, &state, &path, &raw); err != nil {
		return "", "", err
	}
	if version != c.ExpectedOwnerVersion || state != ownerStateCompleted {
		return "", "", workitems.ErrStaleClaim
	}
	fp, err := decodeFingerprint(raw)
	if err != nil {
		return "", "", err
	}
	var anime, release, placementPath, digest, verification, placementState string
	err = tx.QueryRowContext(ctx, `SELECT managed_anime_id,COALESCE(quality_facts_ref,''),physical_path,target_fingerprint,verification_state,state FROM placements WHERE source_evidence_type='file_operation' AND source_owner_ref=$1 FOR UPDATE`, c.Owner.ID).Scan(&anime, &release, &placementPath, &digest, &verification, &placementState)
	if err != nil {
		return "", "", err
	}
	if release == "" || placementPath != path || (digest != fp.Value && digest != string(raw)) || verification != "verified" || placementState != "active" {
		return "", "", workitems.ErrStaleClaim
	}
	// Placement must belong to the frozen artifact/release chain, not an
	// arbitrary candidate supplied by a corrupted placement row.
	var chainAnime, chainRelease, artifactState string
	var artifactVersion, frozenArtifactVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT sub.managed_anime_id,rel.id,a.state,a.version,fo.artifact_version FROM file_operations fo JOIN artifacts a ON a.id=fo.artifact_id JOIN download_tasks dt ON dt.id=a.download_task_id JOIN releases rel ON rel.id=dt.release_id JOIN rss_sources src ON src.id=rel.rss_source_id JOIN subscriptions sub ON sub.id=src.subscription_id WHERE fo.id=$1 FOR UPDATE OF a,dt,rel,src`, c.Owner.ID).Scan(&chainAnime, &chainRelease, &artifactState, &artifactVersion, &frozenArtifactVersion); err != nil {
		return "", "", err
	}
	if chainAnime != anime || chainRelease != release || artifactState != "verified" || artifactVersion != frozenArtifactVersion {
		return "", "", workitems.ErrStaleClaim
	}
	var obligationState string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM placement_obligations WHERE file_operation_id=$1 FOR UPDATE`, c.Owner.ID).Scan(&obligationState); err != nil {
		return "", "", err
	}
	if obligationState != placementObligationPending && obligationState != placementObligationWaitingRetry {
		return "", "", workitems.ErrStaleClaim
	}
	return anime, release, nil
}
