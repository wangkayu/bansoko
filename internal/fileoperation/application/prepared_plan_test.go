package application

import (
	"bansoko/internal/workitems"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func preparedCaptureFixture() PreparedPlanCapture {
	return PreparedPlanCapture{PlanID: "plan", PlanVersion: 1, PlanInputDigest: "sha256:input", ArtifactID: "artifact", ArtifactVersion: 2, Config: []byte(`{"vars":{"path":"frozen"}}`), Facts: []byte(`{"files":["video"]}`)}
}
func TestPreparedPlanRejectsReplanConfigAndFactsDrift(t *testing.T) {
	c := preparedCaptureFixture()
	p, err := PrepareCapturedPlan(context.Background(), c, func(context.Context, PreparedPlanCapture) (json.RawMessage, error) {
		return []byte(`{"operations":[]}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*PreparedPlanCapture){func(c *PreparedPlanCapture) { c.PlanVersion++ }, func(c *PreparedPlanCapture) { c.ArtifactVersion++ }, func(c *PreparedPlanCapture) { c.Config = []byte(`{"vars":{"path":"changed"}}`) }, func(c *PreparedPlanCapture) { c.Facts = []byte(`{"files":["different"]}`) }} {
		current := p.Capture()
		mutate(&current)
		if err := p.ValidateCurrent(current); !errors.Is(err, workitems.ErrStaleClaim) {
			t.Fatalf("drift accepted: %v", err)
		}
	}
}
func TestPreparedPlanCaptureCannotBeMutatedBySlowBuilder(t *testing.T) {
	c := preparedCaptureFixture()
	original := copyPlanCapture(c)
	payload := []byte(`{"operations":[]}`)
	p, err := PrepareCapturedPlan(context.Background(), c, func(_ context.Context, input PreparedPlanCapture) (json.RawMessage, error) {
		input.Config[0] = 'x'
		return payload, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Config[0] = 'y'
	payload[0] = 'z'
	exported := p.Payload()
	exported[0] = 'q'
	if err := p.ValidateCurrent(original); err != nil {
		t.Fatal("mutable builder escaped capture", err)
	}
	if !json.Valid(p.Payload()) {
		t.Fatal("payload escaped immutable artifact")
	}
}
func TestPreparedPlanCancelledPreparationCannotCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, err := PrepareCapturedPlan(ctx, preparedCaptureFixture(), func(context.Context, PreparedPlanCapture) (json.RawMessage, error) {
		cancel()
		return []byte(`{}`), nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preparation returned committable artifact: %v", err)
	}
}
