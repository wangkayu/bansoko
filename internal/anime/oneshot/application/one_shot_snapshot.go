package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	acquisitiontarget "bansoko/internal/acquisition/target"
)

func encodeOneShotInputSnapshot(snapshot OneShotInputSnapshot) (json.RawMessage, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal one-shot input snapshot: %w", err)
	}
	if len(raw) == 0 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		return nil, errors.New("one-shot input snapshot must be a JSON object")
	}
	if len(raw) > maxOneShotInputSnapshotBytes {
		return nil, torrentTooLargeError()
	}
	return raw, nil
}

func encodeOneShotTargetFacts(plan acquisitiontarget.PlanResult, configured, failed bool) (json.RawMessage, error) {
	value := acquisitiontarget.UnplannedFrozenFacts()
	if configured {
		// ReasonCode is the typed planner admission reason (e.g.
		// target_not_absolute, parser_identity_unresolved); Reason keeps the
		// human-readable plan detail.
		value = acquisitiontarget.FrozenFacts{
			Admission:  string(plan.Admission.Admission),
			ReasonCode: strings.TrimSpace(plan.Admission.Reason),
			Reason:     plan.PlanReason,
		}
		if !failed {
			value.TargetSetDigest = plan.Set.Digest
			value.TargetCount = len(plan.Set.Targets)
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode one-shot target facts: %w", err)
	}
	return raw, nil
}
