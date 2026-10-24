package application

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// B5 step 7: deployment reconcile of a typed operation is read-only; with
// a matching target it reports blocked/unknown, calls no observer or executor and writes
// nothing (a missing target is never classified as no_effect).
func TestReconcileOpenOperationWithMatchingTargetIsBlocked(t *testing.T) {
	assertTypedReconcileIsolation(t, "matching")
}

// B5 step 7: deployment reconcile of a typed operation is read-only; with
// a missing target it reports blocked/unknown, calls no observer or executor and writes
// nothing (a missing target is never classified as no_effect).
func TestReconcileOpenOperationWithoutTargetIsBlocked(t *testing.T) {
	assertTypedReconcileIsolation(t, "absent")
}

// B5 step 7: deployment reconcile of a typed operation is read-only; with
// a conflicting target it reports blocked/unknown, calls no observer or executor and writes
// nothing (a missing target is never classified as no_effect).
func TestReconcileOpenOperationWithConflictingTargetIsBlocked(t *testing.T) {
	assertTypedReconcileIsolation(t, "conflicting")
}

// fingerprintForTest returns the sha256 hex of the content, matching the
// fingerprint shape the recovery handler compares against.
func fingerprintForTest(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}
