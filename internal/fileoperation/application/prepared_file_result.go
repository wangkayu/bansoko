package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/workitems"
)

// preparedFileResult is an immutable in-process capture, not a caller supplied
// success flag. The eventual finish transaction must independently re-read proof
// under its complete root-first lock set before writing any business result.
// This constructor performs only a repeatable-read DB capture; it authorizes no I/O.
type preparedFileResult struct {
	root, operation, fileRef, permit, originalWork, session    string
	originalToken, originalOwnerVersion                        int64
	specDigest, planDigest, dependencyDigest                   string
	resolutionCanonical, checkpointCanonical, closureCanonical string
	finalObjectCanonical, sourceCleanup, evidenceDigest        string
	fileFactCanonical                                          string
	notificationPayload                                        string
	claimWork                                                  string
	claimInputCanonical                                        string
	oneShotMarker, oneShotCorrelation                          string
	oneShotEpoch                                               int64
	claimToken, claimOwnerVersion                              int64
	recoveryClaim                                              bool
}

func (s *Service) prepareSuccessfulFileResult(ctx context.Context, claim workitems.Claim, permit string) (preparedFileResult, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return preparedFileResult{}, err
	}
	defer tx.Rollback()
	prepared, err := captureSuccessfulFileResultTx(ctx, tx, claim, permit)
	if err != nil {
		return preparedFileResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return preparedFileResult{}, err
	}
	return prepared, nil
}

func captureSuccessfulFileResultTx(ctx context.Context, tx *sql.Tx, claim workitems.Claim, permit string) (preparedFileResult, error) {
	var p preparedFileResult
	if tx == nil || permit == "" || claim.Owner.Type != workitems.OwnerFileOperation || claim.Kind != workitems.KindFileExecute || claim.ID == "" || claim.FencingToken < 1 || claim.ExpectedOwnerVersion < 1 {
		return p, ErrInvalidClaim
	}
	resolution, err := loadPublicationProofTx(ctx, tx, permit)
	if err != nil {
		return p, err
	}
	err = tx.QueryRowContext(ctx, `SELECT acquisition_id,id,file_ref,publication_permit_id,publication_work_item_id,publication_executor_session,publication_fencing_token,publication_owner_version,publication_spec_digest,publication_plan_digest,publication_dependency_digest,publication_resolution_canonical,publication_resolution_checkpoint_canonical,publication_closure_canonical FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&p.root, &p.operation, &p.fileRef, &p.permit, &p.originalWork, &p.session, &p.originalToken, &p.originalOwnerVersion, &p.specDigest, &p.planDigest, &p.dependencyDigest, &p.resolutionCanonical, &p.checkpointCanonical, &p.closureCanonical)
	if err != nil {
		return preparedFileResult{}, err
	}
	var persistedInput []byte
	if err := tx.QueryRowContext(ctx, `SELECT input_snapshot FROM public.work_item_history WHERE id=$1`, claim.ID).Scan(&persistedInput); err != nil {
		return preparedFileResult{}, err
	}
	storedInput, err := protocol.ParseStrict(persistedInput)
	if err != nil {
		return preparedFileResult{}, err
	}
	claimedInput, err := protocol.ParseStrict(claim.InputSnapshot)
	if err != nil {
		return preparedFileResult{}, err
	}
	p.claimInputCanonical = string(protocol.Canonical(storedInput))
	if p.claimInputCanonical != string(protocol.Canonical(claimedInput)) {
		return preparedFileResult{}, fmt.Errorf("FILE_RESULT_WORK_INPUT_MISMATCH")
	}
	var workInput struct {
		OneShot *workitems.OneShotRef `json:"one_shot"`
	}
	if err := json.Unmarshal(persistedInput, &workInput); err != nil {
		return preparedFileResult{}, err
	}
	if (workInput.OneShot == nil) != (claim.OneShot == nil) || (workInput.OneShot != nil && *workInput.OneShot != *claim.OneShot) {
		return preparedFileResult{}, fmt.Errorf("FILE_RESULT_MARKER_MISMATCH")
	}
	if workInput.OneShot != nil {
		p.oneShotMarker, p.oneShotCorrelation, p.oneShotEpoch = workInput.OneShot.MarkerID, workInput.OneShot.CorrelationID, workInput.OneShot.AdmissionEpoch
	}
	p.claimWork, p.claimToken, p.claimOwnerVersion = claim.ID, claim.FencingToken, claim.ExpectedOwnerVersion
	if p.operation != claim.Owner.ID || p.originalWork != claim.ID || p.originalToken != claim.FencingToken || p.originalOwnerVersion != claim.ExpectedOwnerVersion {
		return preparedFileResult{}, fmt.Errorf("FILE_RESULT_ORIGINAL_CLAIM_MISMATCH")
	}
	if string(protocol.Canonical(resolution)) != p.resolutionCanonical {
		return preparedFileResult{}, fmt.Errorf("FILE_RESULT_RESOLUTION_CHANGED")
	}
	checkpoint, err := protocol.ParseStrict([]byte(p.checkpointCanonical))
	if err != nil {
		return preparedFileResult{}, err
	}
	p.finalObjectCanonical = string(protocol.Canonical(checkpoint.Object["publish_target"].Object["proof"].Object["target"]))
	p.sourceCleanup = "not_required"
	if checkpoint.Object["source_cleanup"].Object["state"].String == "confirmed" {
		p.sourceCleanup = "completed"
	}
	p.evidenceDigest = protocol.Digest("acquisition-effect-resolution:v1", []byte(p.resolutionCanonical))
	var planID string
	if err := tx.QueryRowContext(ctx, `SELECT postprocessing_plan_id FROM public.file_operations WHERE id=$1`, p.operation).Scan(&planID); err != nil {
		return preparedFileResult{}, err
	}
	capture, err := captureTypedPlan(ctx, tx, planID)
	if err != nil {
		return preparedFileResult{}, err
	}
	var frozen struct {
		Input json.RawMessage `json:"input"`
		Files []struct {
			ID    string          `json:"id"`
			Ref   string          `json:"file_ref"`
			Path  string          `json:"relative_path"`
			Kind  string          `json:"media_kind"`
			Size  int64           `json:"size_bytes"`
			Facts json.RawMessage `json:"facts"`
		} `json:"files"`
	}
	if err := json.Unmarshal(capture.Facts, &frozen); err != nil {
		return preparedFileResult{}, err
	}
	batch := &artifactPreparedBatch{capture: capture, input: frozen.Input}
	var selected artifactFileContext
	found := false
	for _, f := range frozen.Files {
		file := artifactFileContext{id: f.ID, fileRef: f.Ref, relativePath: f.Path, mediaKind: f.Kind, sizeBytes: f.Size, facts: f.Facts}
		batch.artifact.files = append(batch.artifact.files, file)
		if f.Ref == p.fileRef {
			selected = file
			found = true
		}
	}
	if !found {
		return preparedFileResult{}, fmt.Errorf("FILE_RESULT_ARTIFACT_FILE_MISSING")
	}
	var render []byte
	if err := tx.QueryRowContext(ctx, `SELECT render_snapshot FROM public.acquisition_files WHERE acquisition_id=$1 AND file_ref=$2`, p.root, p.fileRef).Scan(&render); err != nil {
		return preparedFileResult{}, err
	}
	batch.operations = []preparedOperation{{file: selected, plan: operationPlan{renderSnapshot: render}}}
	fact, err := observedSatisfactionFileFact(batch, selected, map[string]any{"digest": p.evidenceDigest, "final_object": json.RawMessage(p.finalObjectCanonical)})
	if err != nil {
		return preparedFileResult{}, err
	}
	v, err := protocol.ParseStrict([]byte(fact))
	if err != nil {
		return preparedFileResult{}, err
	}
	operationJSON, err := json.Marshal(p.operation)
	if err != nil {
		return preparedFileResult{}, err
	}
	knownOperation, err := protocol.ParseStrict([]byte(`{"state":"known","value":` + string(operationJSON) + `,"reason_code":null}`))
	if err != nil {
		return preparedFileResult{}, err
	}
	v.Object["provenance"].Object["operation_ref"] = knownOperation
	cleanupJSON, err := json.Marshal(p.sourceCleanup)
	if err != nil {
		return preparedFileResult{}, err
	}
	cleanup, err := protocol.ParseStrict(cleanupJSON)
	if err != nil {
		return preparedFileResult{}, err
	}
	v.Object["locator"].Object["source_cleanup"] = cleanup
	if err := protocol.ValidateFileFact(v); err != nil {
		return preparedFileResult{}, err
	}
	p.fileFactCanonical = string(protocol.Canonical(v))
	// The operation's own frozen collision (on the publication evidence) is
	// what the finish binds the notification to; an absent overwrite target is a
	// block operation inside an overwrite/replace delivery (migration 187).
	var collision string
	if err := tx.QueryRowContext(ctx, `SELECT collision_policy FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&collision); err != nil {
		return preparedFileResult{}, err
	}
	sourceFacts, err := frozenReleaseSourceFactsTx(ctx, tx, frozen.Input)
	if err != nil {
		return preparedFileResult{}, err
	}
	payload, err := successfulFileNotificationPayload(frozen.Input, selected, v, checkpoint, collision, sourceFacts, render)
	if err != nil {
		return preparedFileResult{}, err
	}
	p.notificationPayload = string(payload)
	return p, nil
}

// verifyPreparedFileResultTx is read-only. The caller must first acquire the
// complete root/owner/work/evidence lock set; this is not a Finish gate.
func verifyPreparedFileResultTx(ctx context.Context, tx *sql.Tx, claim workitems.Claim, prepared preparedFileResult) error {
	var current preparedFileResult
	var err error
	if prepared.recoveryClaim {
		current, err = captureRecoveredSuccessfulFileResultTx(ctx, tx, claim, prepared.permit)
	} else {
		current, err = captureSuccessfulFileResultTx(ctx, tx, claim, prepared.permit)
	}
	if err != nil {
		return err
	}
	if current != prepared {
		return fmt.Errorf("FILE_PREPARED_RESULT_CHANGED")
	}
	return nil
}

// frozenReleaseSourceFactsTx reads the source_facts of the release an
// accepted input names (frozen since submit); a missing release reads nil,
// exactly as the SQL projector's subquery does.
func frozenReleaseSourceFactsTx(ctx context.Context, tx *sql.Tx, input json.RawMessage) ([]byte, error) {
	var accepted struct {
		Source struct {
			Release struct {
				State string `json:"state"`
				Value struct {
					Ref string `json:"ref"`
				} `json:"value"`
			} `json:"release"`
		} `json:"source"`
	}
	if err := json.Unmarshal(input, &accepted); err != nil || accepted.Source.Release.State != "known" {
		return nil, err
	}
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT source_facts FROM public.releases WHERE id=$1`, accepted.Source.Release.Value.Ref).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}
