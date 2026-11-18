package application

import (
	"context"
	"testing"

	hardeningdomain "bansoko/internal/hardening/domain"
	observabilityport "bansoko/internal/observability/port"
)

type chainResultLogger struct {
	observabilityport.NopRuntimeLogger
	warnings []observabilityport.RuntimeEvent
}

func (l *chainResultLogger) Warn(_ context.Context, e observabilityport.RuntimeEvent) {
	l.warnings = append(l.warnings, e)
}

func TestChainFailureLogsStageAndOperationBeforeHandoff(t *testing.T) {
	logger := &chainResultLogger{}
	s := &Service{updatesRoot: t.TempDir(), logger: logger, chainRunning: true, done: make(chan struct{})}
	s.runChain(hardeningdomain.ReleaseManifest{ReleaseID: "release"}, "digest", "", "operation", nil)
	if s.chainRunning || s.lastStatus.Step != "failed" || len(logger.warnings) != 1 {
		t.Fatalf("running=%v, status=%+v, warnings=%+v", s.chainRunning, s.lastStatus, logger.warnings)
	}
	e := logger.warnings[0]
	if e.OwnerID != "operation" || e.Stage != "release_input" || e.Outcome != "failure" || s.lastStatus.LastError == nil || e.Message != *s.lastStatus.LastError {
		t.Fatalf("status=%+v, warning=%+v", s.lastStatus, e)
	}
	restarted := &Service{updatesRoot: s.updatesRoot}
	status, found, err := restarted.GetUpdateStatus(context.Background())
	if err != nil || !found || status.OperationID != "operation" || status.Step != "failed" || status.LastError == nil || *status.LastError != e.Message {
		t.Fatalf("restarted status=%+v found=%v err=%v", status, found, err)
	}
	select {
	case <-s.done:
		t.Fatal("failed preparation requested a restart")
	default:
	}
}
