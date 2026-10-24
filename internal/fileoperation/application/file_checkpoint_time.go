package application

import (
	"encoding/json"
	"time"
)

// DB timestamptz JSON uses an offset and may retain trailing fractional zeros.
// Normalize only timestamps issued by StartStep, before strict wire validation.
func normalizeCheckpointTimes(raw []byte) ([]byte, error) {
	var slots map[string]json.RawMessage
	if err := json.Unmarshal(raw, &slots); err != nil {
		return nil, err
	}
	for key, slot := range slots {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(slot, &fields); err != nil {
			return nil, err
		}
		var state string
		json.Unmarshal(fields["state"], &state)
		if state != "started" {
			continue
		}
		var at string
		if err := json.Unmarshal(fields["observed_at"], &at); err != nil {
			return nil, err
		}
		instant, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		fields["observed_at"], err = json.Marshal(observationTime(instant))
		if err != nil {
			return nil, err
		}
		slots[key], err = json.Marshal(fields)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(slots)
}
