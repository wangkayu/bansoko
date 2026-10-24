package filesystem

import (
	"bansoko/internal/library/domain"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
)

// ExecuteWithPublication binds callbacks to this invocation, not global hooks.
// Mutation hooks only trigger for the final target, never overwrite staging.
func (e OSFileExecutor) ExecuteWithPublication(ctx context.Context, op domain.FileOperation, checkpoint domain.FileOperationCheckpoint, callbacks domain.PublicationCallbacks) (result domain.FileOperation, resultErr error) {
	if callbacks.Begin == nil || callbacks.Finish == nil {
		return op, errors.New("publication callbacks required")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(op.TargetPath))
	if err != nil {
		return op, err
	}
	if !filepath.IsAbs(op.TargetPath) || filepath.Clean(op.TargetPath) != op.TargetPath || filepath.Join(parent, filepath.Base(op.TargetPath)) != op.TargetPath {
		return op, errors.New("frozen publication target is not canonical")
	}
	var token string
	begin := func(source, target string) error {
		if target != op.TargetPath || token != "" {
			return nil
		}
		evidence, err := observeFile(source)
		if err != nil {
			return err
		}
		token, err = callbacks.Begin(ctx, op, evidence)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(map[string]any{"token": token, "operation_id": op.ID, "source_path": source, "target_path": target, "source_observation": evidence, "fencing_token": op.FencingToken, "executor_final": false})
		if err != nil {
			return err
		}
		_, err = WritePublicationMarker(target, token, raw)
		return err
	}
	oldPublish := e.Hooks.BeforePublish
	e.Hooks.BeforePublish = func(source, target string) error {
		if oldPublish != nil {
			if err := oldPublish(source, target); err != nil {
				return err
			}
		}
		return begin(source, target)
	}
	oldExchange := e.Hooks.BeforeExchange
	e.Hooks.BeforeExchange = func(source, target string) error {
		if oldExchange != nil {
			if err := oldExchange(source, target); err != nil {
				return err
			}
		}
		return begin(source, target)
	}
	e.Checkpoint = checkpoint
	defer func() {
		if token == "" {
			return
		}
		observation, err := observeFile(op.TargetPath)
		if err != nil {
			observation = domain.FileObservation{}
		}
		// Failure remains uncertain even if the target is absent. Only durable
		// executor-quiescence/reconciliation may release an ambiguous publication.
		if resultErr == nil && err != nil {
			resultErr = err
		}
		receiptErr := callbacks.Finish(context.WithoutCancel(ctx), token, op, observation, resultErr)
		resultErr = errors.Join(resultErr, receiptErr)
	}()
	return e.Execute(ctx, op)
}
