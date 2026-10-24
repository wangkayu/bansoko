package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

	catalog "bansoko/internal/catalog/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"github.com/google/uuid"
)

// commitInspection owns the complete fenced transition. Preparation remains
// outside the root-held transaction; the SQL entry rechecks its binding.
func (s *Service) commitInspection(ctx context.Context, c workitems.Claim, plan inspectionPlan) (worker.Outcome, error) {
	var admission, payloadDigest, manifestDigest string
	var manifestRaw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT i.admission_digest,i.payload_digest,i.input->'source'->>'manifest_digest' FROM artifacts a JOIN acquisitions i ON i.id=a.acquisition_id WHERE a.id=$1`, c.Owner.ID).Scan(&admission, &payloadDigest, &manifestDigest); err != nil {
		return worker.Outcome{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.file_ref,f.ordinal,f.relative_path,f.exact_size,f.media_kind,f.identity_evidence FROM acquisition_files f JOIN artifacts a ON a.acquisition_id=f.acquisition_id WHERE a.id=$1 ORDER BY f.ordinal`, c.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	var manifest []map[string]any
	refs := map[string]string{}
	for rows.Next() {
		var ref, path, kind string
		var ordinal int
		var size int64
		var evidence json.RawMessage
		if err := rows.Scan(&ref, &ordinal, &path, &size, &kind, &evidence); err != nil {
			rows.Close()
			return worker.Outcome{}, err
		}
		manifest = append(manifest, map[string]any{"file_ref": ref, "ordinal": ordinal, "relative_path": path, "exact_size": size, "media_kind": kind, "identity_evidence": evidence})
		refs[path] = ref
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return worker.Outcome{}, rowErr
	}
	files := plan.filesOrFallback()
	buildManifest := len(manifest) == 0
	prepared := make([]map[string]any, 0, len(files))
	rebindings := make(map[string]string, len(files))
	for index, file := range files {
		ref := refs[file.relativePath]
		if ref == "" {
			ref = catalog.TorrentFileRef(payloadDigest, file.relativePath)
		}
		if buildManifest {
			manifest = append(manifest, map[string]any{"file_ref": ref, "ordinal": index, "relative_path": file.relativePath, "exact_size": file.sizeBytes, "media_kind": file.mediaKind, "identity_evidence": map[string]any{}})
		}
		rebindings[file.fileRef] = ref
		files[index].fileRef = ref
		files[index].id = stableID("artifact-file", c.Owner.ID, ref)
		prepared = append(prepared, map[string]any{"id": stableID("artifact-file", c.Owner.ID, ref), "file_ref": ref, "relative_path": file.relativePath, "source_path": filepath.Clean(file.sourcePath), "media_kind": file.mediaKind, "size_bytes": file.sizeBytes, "fingerprint": json.RawMessage(file.fingerprint), "facts": json.RawMessage(file.facts)})
	}
	plan.inventorySnapshot, err = rebindInspectionSnapshot(plan.inventorySnapshot, rebindings, false)
	if err != nil {
		return worker.Outcome{}, err
	}
	plan.fingerprintSnapshot, err = rebindInspectionSnapshot(plan.fingerprintSnapshot, rebindings, true)
	if err != nil {
		return worker.Outcome{}, err
	}
	manifestRaw, err = json.Marshal(manifest)
	if err != nil {
		return worker.Outcome{}, err
	}
	filesRaw, err := json.Marshal(prepared)
	if err != nil {
		return worker.Outcome{}, err
	}
	mediaSnapshot, media, err := s.prepareFileFacts(ctx, c.Owner.ID, files)
	if err != nil {
		return worker.Outcome{}, err
	}
	if media == nil {
		media = []map[string]any{}
	}
	mediaRaw, err := json.Marshal(media)
	if err != nil {
		return worker.Outcome{}, err
	}
	sum := sha256.Sum256(append(append([]byte(fmt.Sprintf("%s:%d:", c.Owner.ID, c.ExpectedOwnerVersion+1)), plan.inventorySnapshot...), plan.fingerprintSnapshot...))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	return s.inspectionTransaction(ctx, c, func(scope txscope.Tx) ([]byte, error) {
		var raw []byte
		err := scope.SQLTx().QueryRowContext(ctx, `SELECT public.artifact_commit_inspection($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8::jsonb,$9::jsonb,$10,$11,$12,$13,$14::jsonb)`, c.Owner.ID, c.ID, c.FencingToken, admission, manifestDigest, manifestRaw, plan.inventorySnapshot, plan.fingerprintSnapshot, filesRaw, uuid.NewString(), uuid.NewString(), digest, mediaSnapshot, mediaRaw).Scan(&raw)
		if err != nil {
			return nil, err
		}
		// The artifact_verified run-log entry is derived after commit by
		// the independent acquisition Activity consumer, not written here.
		return raw, nil
	})
}

func (s *Service) inspectionTransaction(ctx context.Context, c workitems.Claim, apply func(txscope.Tx) ([]byte, error)) (worker.Outcome, error) {
	var result workitems.OwnerResult
	store := &workitems.Store{DB: s.db}
	err := store.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		raw, err := apply(scope)
		if err != nil {
			return "", err
		}
		var reply struct {
			State string `json:"state"`
			Code  string `json:"reason_code"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return "", err
		}
		result = workitems.OwnerResult{State: reply.State, ReasonCode: reply.Code}
		return workitems.DispositionNoExternalEffect, nil
	})
	if err != nil {
		return worker.Outcome{}, err
	}
	return worker.Committed(c, result), nil
}

func (s *Service) retryInspection(ctx context.Context, c workitems.Claim, code, reason string, delaySeconds float64) (worker.Outcome, error) {
	return s.inspectionTransaction(ctx, c, func(scope txscope.Tx) ([]byte, error) {
		var raw []byte
		err := scope.SQLTx().QueryRowContext(ctx, `SELECT public.artifact_inspect_retry($1,$2,$3,$4,$5,$6)`, c.Owner.ID, c.ID, c.FencingToken, code, safeReason(reason), delaySeconds).Scan(&raw)
		return raw, err
	})
}
func (s *Service) failInspection(ctx context.Context, c workitems.Claim, code, reason string) (worker.Outcome, error) {
	return s.inspectionTransaction(ctx, c, func(scope txscope.Tx) ([]byte, error) {
		var raw []byte
		err := scope.SQLTx().QueryRowContext(ctx, `SELECT public.artifact_inspect_failed($1,$2,$3,$4,$5)`, c.Owner.ID, c.ID, c.FencingToken, code, safeReason(reason)).Scan(&raw)
		return raw, err
	})
}
