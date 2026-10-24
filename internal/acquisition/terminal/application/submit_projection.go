package application

import (
	"bansoko/internal/workitems"
	"encoding/json"
	"errors"
)

// submitProjection carries the caller-encoded frozen input/policy and the
// download submit WorkItem identity. Encoders remain owned by each caller; this
// pure projection copies their bytes and builds the submit WorkItem input
// snapshot without reinterpretation.
type submitProjection struct {
	frozenInput, frozenPolicy                          json.RawMessage
	effectKey, correlation, causationType, causationID string
	trigger                                            string
	oneShot                                            *workitems.OneShotRef
}

// projectSubmit validates the projection and returns a defensive copy plus the
// submit WorkItem input snapshot.
func projectSubmit(p submitProjection) (submitProjection, json.RawMessage, error) {
	if !json.Valid(p.frozenInput) || !json.Valid(p.frozenPolicy) || p.effectKey == "" || p.correlation == "" || p.causationType == "" || p.causationID == "" {
		return p, nil, errors.New("incomplete submit projection")
	}
	snapshot, err := workitems.SubmitInputSnapshot(p.trigger, p.oneShot)
	p.frozenInput = append(json.RawMessage(nil), p.frozenInput...)
	p.frozenPolicy = append(json.RawMessage(nil), p.frozenPolicy...)
	return p, snapshot, err
}
