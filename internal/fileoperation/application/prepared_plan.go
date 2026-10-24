package application

import (
	"bansoko/internal/workitems"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// PreparedPlanCapture contains only immutable database facts, copied before
// slow naming/path observation. No transaction or database handle escapes.
type PreparedPlanCapture struct {
	PlanID          string          `json:"plan_id"`
	PlanVersion     int64           `json:"plan_version"`
	PlanInputDigest string          `json:"plan_input_digest"`
	ArtifactID      string          `json:"artifact_id"`
	ArtifactVersion int64           `json:"artifact_version"`
	Config          json.RawMessage `json:"config"`
	Facts           json.RawMessage `json:"facts"`
}

// PreparedPlan is an in-memory artifact, not a new owner/workflow. Payload
// contains computed operation plans; commit writers may only persist it.
// Private bytes prevent mutation while naming runs or commit races replan.
type PreparedPlan struct {
	capture       PreparedPlanCapture
	captureDigest string
	payload       json.RawMessage
	payloadDigest string
}

func copyPlanCapture(c PreparedPlanCapture) PreparedPlanCapture {
	c.Config = append(json.RawMessage(nil), c.Config...)
	c.Facts = append(json.RawMessage(nil), c.Facts...)
	return c
}
func planCaptureDigest(c PreparedPlanCapture) (string, error) {
	if c.PlanID == "" || c.PlanVersion < 1 || c.PlanInputDigest == "" || c.ArtifactID == "" || c.ArtifactVersion < 1 || !json.Valid(c.Config) || !json.Valid(c.Facts) {
		return "", workitems.ErrInvalidSpec
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// PrepareCapturedPlan executes slow pure/OS preparation outside a transaction.
// build MUST NOT publish any external effect or mutate database owner state.
func PrepareCapturedPlan(ctx context.Context, c PreparedPlanCapture, build func(context.Context, PreparedPlanCapture) (json.RawMessage, error)) (PreparedPlan, error) {
	if build == nil {
		return PreparedPlan{}, workitems.ErrInvalidSpec
	}
	if err := ctx.Err(); err != nil {
		return PreparedPlan{}, err
	}
	frozen := copyPlanCapture(c)
	digest, err := planCaptureDigest(frozen)
	if err != nil {
		return PreparedPlan{}, err
	}
	payload, err := build(ctx, copyPlanCapture(frozen))
	if err != nil {
		return PreparedPlan{}, err
	}
	if err := ctx.Err(); err != nil {
		return PreparedPlan{}, err
	}
	if !json.Valid(payload) || string(payload) == "null" {
		return PreparedPlan{}, workitems.ErrInvalidSpec
	}
	sum := sha256.Sum256(payload)
	return PreparedPlan{frozen, digest, append(json.RawMessage(nil), payload...), hex.EncodeToString(sum[:])}, nil
}
func (p PreparedPlan) Capture() PreparedPlanCapture { return copyPlanCapture(p.capture) }
func (p PreparedPlan) Payload() json.RawMessage     { return append(json.RawMessage(nil), p.payload...) }
func (p PreparedPlan) ValidateCurrent(current PreparedPlanCapture) error {
	digest, err := planCaptureDigest(current)
	if err != nil {
		return err
	}
	if digest != p.captureDigest {
		return workitems.ErrStaleClaim
	}
	sum := sha256.Sum256(p.payload)
	if hex.EncodeToString(sum[:]) != p.payloadDigest {
		return errors.New("prepared plan payload changed")
	}
	return nil
}
