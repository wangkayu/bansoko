package filemaintenance

import (
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/workitems"
	"encoding/json"
)

func maintenanceInvocation(c workitems.Claim, attempt json.RawMessage, source, target, fingerprint string) executorpkg.MaintenanceInvocation {
	var parsed any
	_ = json.Unmarshal(attempt, &parsed)
	normalized, _ := json.Marshal(parsed)
	return executorpkg.MaintenanceInvocation{OwnerType: c.Owner.Type, OwnerID: c.Owner.ID, Kind: c.Kind, WorkItemID: c.ID, OwnerVersion: c.ExpectedOwnerVersion, Attempt: normalized, Source: source, Target: target, Fingerprint: fingerprint}
}
