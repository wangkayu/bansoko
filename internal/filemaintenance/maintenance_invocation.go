package filemaintenance

import (
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/workitems"
	"bytes"
	"encoding/json"
)

func maintenanceInvocation(c workitems.Claim, attempt json.RawMessage, source, target, fingerprint string) executorpkg.MaintenanceInvocation {
	return executorpkg.MaintenanceInvocation{OwnerType: c.Owner.Type, OwnerID: c.Owner.ID, Kind: c.Kind, WorkItemID: c.ID, OwnerVersion: c.ExpectedOwnerVersion, Attempt: normalizeAttemptJSON(attempt), Source: source, Target: target, Fingerprint: fingerprint}
}

// normalizeAttemptJSON re-encodes a persisted attempt canonically (sorted
// keys, no insignificant whitespace) so the invocation bytes do not depend on
// how the store rendered them. Numbers keep their exact literal: an attempt
// carries nanosecond file timestamps beyond float64 precision, and the
// filesystem adapter compares them with a fresh observation.
func normalizeAttemptJSON(attempt json.RawMessage) json.RawMessage {
	decoder := json.NewDecoder(bytes.NewReader(attempt))
	decoder.UseNumber()
	var parsed any
	_ = decoder.Decode(&parsed)
	normalized, _ := json.Marshal(parsed)
	return normalized
}
