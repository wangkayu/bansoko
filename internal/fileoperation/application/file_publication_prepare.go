package application

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

func publicationLocation(path string) (map[string]any, error) {
	root := filepath.Dir(path)
	ns, err := canonicalPlanValue(map[string]any{"root": root, "adapter_version": "local-stable-read-v1"})
	if err != nil {
		return nil, err
	}
	return map[string]any{"namespace_digest": protocol.Digest("local-filesystem-namespace:v1", []byte(ns)), "root": root, "relative_path": filepath.Base(path), "canonical_path": path}, nil
}

func preparedPublicationObjects(op domain.FileOperation, p domain.PublicationPreparation) (string, error) {
	sourceLocation, err := publicationLocation(op.SourcePath)
	if err != nil {
		return "", err
	}
	targetLocation, err := publicationLocation(op.TargetPath)
	if err != nil {
		return "", err
	}
	tempLocation, err := publicationLocation(p.TempPath)
	if err != nil {
		return "", err
	}
	source := pathObject(sourceLocation, p.Source)
	for name, value := range map[string]any{"PathObjectV1": source, "LocationV1": tempLocation} {
		canonical, err := canonicalPlanValue(value)
		if err != nil {
			return "", err
		}
		v, err := protocol.ParseStrict([]byte(canonical))
		if err != nil {
			return "", err
		}
		if err := protocol.Validate(name, v); err != nil {
			return "", err
		}
	}
	objects := map[string]any{"source": source, "source_parent": pathIdentity(p.SourceParent), "target": targetLocation, "target_parent": pathIdentity(p.TargetParent), "temp": tempLocation}
	if op.CollisionPolicy == domain.CollisionOverwrite {
		if p.ExpectedTarget == nil {
			return "", fmt.Errorf("overwrite target observation required")
		}
		objects["expected_target"] = pathObject(targetLocation, *p.ExpectedTarget)
	}
	canonical, err := canonicalPlanValue(objects)
	if err != nil {
		return "", err
	}
	value, err := protocol.ParseStrict([]byte(canonical))
	if err != nil {
		return "", err
	}
	if err := validatePublicationObjects(value, string(op.Operation), string(op.CollisionPolicy)); err != nil {
		return "", err
	}
	return canonical, nil
}

// prepareFilePermit consumes only the already-issued preparation ticket. A
// successful response is still not dispatch or step permission. Commit failure
// returns an empty result; no fallback to a queried prepared row grants I/O.
func (s *Service) prepareFilePermit(ctx context.Context, c workitems.Claim, session, permit string, op domain.FileOperation, p domain.PublicationPreparation) (bool, error) {
	if s == nil || s.db == nil || ctx == nil || op.ID != c.Owner.ID || c.Kind != workitems.KindFileExecute {
		return false, ErrInvalidClaim
	}
	objects, err := preparedPublicationObjects(op, p)
	if err != nil {
		return false, err
	}
	store, err := workitems.NewStore(s.db, 5, nil)
	if err != nil {
		return false, err
	}
	var reply struct {
		Created  bool   `json:"created"`
		PermitID string `json:"permit_id"`
	}
	err = store.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT public.file_prepare_permit($1,$2,$3,$4,$5,$6)`, c.Owner.ID, c.ID, c.FencingToken, session, permit, objects).Scan(&raw); err != nil {
			return workitems.DispositionAmbiguous, err
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return workitems.DispositionAmbiguous, err
		}
		if reply.Created && reply.PermitID != permit {
			return workitems.DispositionAmbiguous, fmt.Errorf("file prepare identity mismatch")
		}
		return workitems.DispositionAmbiguous, nil
	})
	if err != nil {
		return false, err
	}
	return reply.Created, nil
}
