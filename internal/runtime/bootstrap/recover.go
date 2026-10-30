package bootstrap

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"bansoko/internal/runtime/deploymentidentity"
	updatefence "bansoko/internal/update/fence"
	embeddedupdate "bansoko/internal/update/journal"
)

const (
	recoverExitOK         = 0
	recoverExitRuntime    = 1
	recoverExitUsage      = 2
	recoveryStatusTimeout = 5 * time.Second
)

type recoveryExecutor interface {
	Status(context.Context) (map[string]any, error)
}

type recoveryCommandOptions struct {
	Action string
}

type recoveryCommandError struct {
	Verdict    string `json:"verdict"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	NextAction string `json:"next_action"`
}

type recoveryExecutorFactory func(action, referenceHash string) (recoveryExecutor, func(), error)

func runRecoverCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runRecoverCommandWithFactory(ctx, args, stdout, stderr, newRecoveryExecutor)
}

func runRecoverCommandWithFactory(ctx context.Context, args []string, stdout, stderr io.Writer, factory recoveryExecutorFactory) (exitCode int) {
	var bufferedStdout, bufferedStderr bytes.Buffer
	defer func() {
		if recover() != nil {
			bufferedStdout.Reset()
			bufferedStderr.Reset()
			writeRecoveryError(&bufferedStderr, "recovery_failed", "recovery command failed unexpectedly", "operator")
			exitCode = recoverExitRuntime
		}
		_, _ = io.Copy(stdout, &bufferedStdout)
		_, _ = io.Copy(stderr, &bufferedStderr)
	}()
	return runRecoverCommandInner(ctx, args, &bufferedStdout, &bufferedStderr, factory)
}

func runRecoverCommandInner(ctx context.Context, args []string, stdout, stderr io.Writer, factory recoveryExecutorFactory) int {
	options, err := parseRecoveryCommand(args)
	if err != nil {
		writeRecoveryError(stderr, "invalid_arguments", "recover supports only status", "fix_arguments")
		return recoverExitUsage
	}
	recovery, closeFn, err := factory(options.Action, "")
	if err != nil {
		writeRecoveryError(stderr, "recovery_failed", "durable recovery status is unavailable", "operator")
		return recoverExitRuntime
	}
	defer closeFn()
	statusCtx, cancel := context.WithTimeout(ctx, recoveryStatusTimeout)
	defer cancel()
	status, err := recovery.Status(statusCtx)
	if err != nil {
		writeRecoveryError(stderr, "recovery_failed", "durable recovery status is unavailable", "operator")
		return recoverExitRuntime
	}
	if err := writeRecoveryJSON(stdout, status); err != nil {
		writeRecoveryError(stderr, "output_failed", "recovery result could not be written", "operator")
		return recoverExitRuntime
	}
	return recoverExitOK
}

func parseRecoveryCommand(args []string) (recoveryCommandOptions, error) {
	if len(args) != 1 || args[0] != "status" {
		return recoveryCommandOptions{}, errors.New("recover supports only status")
	}
	return recoveryCommandOptions{Action: "status"}, nil
}

type fileRecoveryStatus struct {
	updatesRoot string
	db          *sql.DB
}

func (s fileRecoveryStatus) Status(ctx context.Context) (map[string]any, error) {
	path := filepath.Join(s.updatesRoot, journalFileName)
	record, found, err := embeddedupdate.Snapshot(path)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"verdict": "status", "present": found}
	if found {
		result["operation_id"] = record.OperationID
		result["step"] = record.Step
		result["manifest_digest"] = record.ManifestDigest
		result["release_sequence"] = record.TargetReleaseSequence
		result["journal_version"] = record.Version
		result["rollback_blocked"] = record.Step == embeddedupdate.StepRollbackBlocked
	}
	if s.db != nil && found && record.Channel == updatefence.AdmittedByEmbedded {
		row, rowFound, err := updatefence.New(s.db).Lookup(ctx, record.OperationID)
		if err != nil {
			return nil, err
		}
		if rowFound && row.OperationID == record.OperationID && row.ManifestDigest == record.ManifestDigest &&
			row.Sequence == record.TargetReleaseSequence && row.AdmittedBy == updatefence.AdmittedByEmbedded {
			result["fence_state"] = row.State
		}
	}
	return result, nil
}

func writeRecoveryJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func writeRecoveryError(writer io.Writer, code, message, nextAction string) {
	_ = writeRecoveryJSON(writer, recoveryCommandError{Verdict: "refused", Code: code, Message: message, NextAction: nextAction})
}

func newRecoveryExecutor(action, _ string) (recoveryExecutor, func(), error) {
	if action != "status" {
		return nil, func() {}, errors.New("recover supports only status")
	}
	updatesRoot := deploymentidentity.UpdatesRoot(os.Getenv)
	var db *sql.DB
	if dsn := maintenanceDatabaseURL(); dsn != "" {
		opened, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, func() {}, err
		}
		db = opened
	}
	closeFn := func() {
		if db != nil {
			_ = db.Close()
		}
	}
	return fileRecoveryStatus{updatesRoot: updatesRoot, db: db}, closeFn, nil
}
