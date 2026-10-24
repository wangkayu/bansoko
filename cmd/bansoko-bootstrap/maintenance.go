package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bansoko/internal/transport/httpapi/generated"
	embeddedupdate "bansoko/internal/update/journal"
)

const defaultListenAddress = "0.0.0.0:8080"

func maintenanceHandler(journalPath string, fallbackStep, fallbackOperation string, now func() time.Time) http.Handler {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v1/livez":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
				return
			}
			writeMaintenanceJSON(w, http.StatusOK, generated.LivezReport{Status: generated.LivezReportStatusAlive})
		case "/api/v1/readyz":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
				return
			}
			detail := maintenanceDetail(journalPath, fallbackStep, fallbackOperation)
			code := "maintenance"
			report := generated.ReadinessReport{
				Ready:              false,
				Reason:             "not_ready:maintenance",
				CheckedAt:          now(),
				DeploymentIdentity: nil,
				Checks: map[string]generated.ReadinessCheck{
					"maintenance": {Ok: false, Code: &code, Detail: &detail},
				},
			}
			writeMaintenanceJSON(w, http.StatusServiceUnavailable, report)
		default:
			http.NotFound(w, r)
		}
	})
}

func maintenanceDetail(journalPath, fallbackStep, fallbackOperation string) string {
	record, found, err := embeddedupdate.Snapshot(journalPath)
	if err == nil && found && safeMaintenanceContext(string(record.Step), record.OperationID) {
		return fmt.Sprintf("step=%s operation=%s", record.Step, record.OperationID)
	}
	if err == nil && safeMaintenanceContext(fallbackStep, fallbackOperation) {
		return fmt.Sprintf("step=%s operation=%s", fallbackStep, fallbackOperation)
	}
	return "maintenance state unavailable"
}

func safeMaintenanceContext(step, operation string) bool {
	return strings.TrimSpace(step) != "" && embeddedupdate.ValidID(operation) &&
		updateapplicationIsKnownStep(embeddedupdate.Step(step))
}

func updateapplicationIsKnownStep(step embeddedupdate.Step) bool {
	if string(step) == "pending_backup" {
		return true
	}
	switch step {
	case embeddedupdate.StepPrepared, embeddedupdate.StepAccepted, embeddedupdate.StepFetched,
		embeddedupdate.StepStaged, embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration, embeddedupdate.StepMigrationSucceeded, embeddedupdate.StepPromoted,
		embeddedupdate.StepReadinessFailed, embeddedupdate.StepRollbackStarted, embeddedupdate.StepDatabaseRestored,
		embeddedupdate.StepFenceClosurePending, embeddedupdate.StepSucceeded, embeddedupdate.StepFailed,
		embeddedupdate.StepRolledBack, embeddedupdate.StepRollbackBlocked, embeddedupdate.StepAbortPending:
		return true
	default:
		return false
	}
}

func writeMaintenanceJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		logf("maintenance response encode failed: %v", err)
	}
}

func maintenanceListenAddress() string {
	if address := strings.TrimSpace(os.Getenv(envListenAddress)); address != "" {
		return address
	}
	return defaultListenAddress
}

func startMaintenanceResponder(updatesRoot, step, operation string) (func() error, error) {
	listener, err := net.Listen("tcp", maintenanceListenAddress())
	if err != nil {
		return nil, fmt.Errorf("listen for maintenance responder: %w", err)
	}
	server := &http.Server{
		Handler:           maintenanceHandler(filepath.Join(updatesRoot, journalFileName), step, operation, nil),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logf("MAINTENANCE responder failed closed: %v", serveErr)
		}
	}()
	logf("MAINTENANCE responder listening on %s (livez only; readyz remains not ready)", listener.Addr())
	return func() error {
		closeErr := server.Close()
		<-serveDone
		return closeErr
	}, nil
}

// serveMaintenance blocks forever behind a freshly bound responder. It is the
// terminal state of the process on those paths, so the stop handle is not kept.
func serveMaintenance(updatesRoot, step, operation string) error {
	if _, err := startMaintenanceResponder(updatesRoot, step, operation); err != nil {
		return err
	}
	select {}
}
