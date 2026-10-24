package transport

import (
	"encoding/json"

	filemaintenance "bansoko/internal/filemaintenance"
	"bansoko/internal/transport/httpapi/generated"
)

func PolicyInput(value generated.FileMaintenancePolicyInput) (filemaintenance.PolicyInput, error) {
	safety := json.RawMessage(`{"max_operations":100}`)
	if value.Safety != nil {
		raw, err := json.Marshal(*value.Safety)
		if err != nil {
			return filemaintenance.PolicyInput{}, err
		}
		safety = raw
	}
	return filemaintenance.PolicyInput{
		ExpectedVersion:     int64(value.ExpectedVersion),
		Enabled:             value.Enabled,
		ScanIntervalSeconds: cloneInt(value.ScanIntervalSeconds),
		AlignmentEnabled:    cloneBool(value.AlignmentEnabled),
		Safety:              safety,
	}, nil
}

func PolicyView(value filemaintenance.PolicyView) generated.FileMaintenancePolicyView {
	return generated.FileMaintenancePolicyView{
		ScopeKey:            value.ScopeKey,
		Enabled:             value.Enabled,
		ScanIntervalSeconds: cloneInt(value.ScanIntervalSeconds),
		AlignmentEnabled:    cloneBool(value.AlignmentEnabled),
		Safety:              rawMap(value.Safety),
		Version:             int(value.Version),
	}
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func rawMap(raw json.RawMessage) map[string]interface{} {
	var value map[string]interface{}
	_ = json.Unmarshal(raw, &value)
	if value == nil {
		return map[string]interface{}{}
	}
	return value
}
