package deployment

import (
	"context"
	"testing"
)

func TestDeploymentGateAllowsTicketedWorkerHandoff(t *testing.T) {
	handler := handlerFunc(func(context.Context, ReconcileRequest) (ReconcileResult, error) {
		return ReconcileResult{State: StateCompleted, Effect: EffectUnknown, EvidenceRef: "submission-permit"}, nil
	})
	registry, err := NewRegistry(DefaultRequiredKeys, fakeRegistration("qb_download_task", handler), fakeRegistration("artifact", handlerReturning(StateCompleted)), fakeRegistration("file_operation", handlerReturning(StateCompleted)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewOrchestrator(registry, &memStore{}).Run(context.Background(), testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Gate.Passed || result.Run.State != "completed" {
		t.Fatalf("handoff blocked worker startup: %+v", result)
	}
	call, ok := result.Run.Checkpoint.CallFor("qb_download_task")
	if !ok || call.Result.Effect != EffectUnknown || call.Result.EvidenceRef != "submission-permit" {
		t.Fatalf("handoff identity lost: %+v", call)
	}
}
