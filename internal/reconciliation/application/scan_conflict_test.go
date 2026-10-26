package application

import (
	"errors"
	"testing"

	"bansoko/internal/apperrors"
)

// A scan submitted while one is running is a typed conflict, not an opaque
// failure: the transport maps apperrors.Business onto the declared 409
// without a per-condition special case.
func TestScanInProgressErrorIsTypedConflict(t *testing.T) {
	err := scanInProgressError()
	var business *apperrors.Business
	if !errors.As(err, &business) {
		t.Fatalf("scan-in-progress is not a typed business error: %T", err)
	}
	if business.Status != 409 || business.Code != "SCAN_IN_PROGRESS" {
		t.Fatalf("scan-in-progress = %d %s, want 409 SCAN_IN_PROGRESS", business.Status, business.Code)
	}
	if business.Message == "" {
		t.Fatal("scan-in-progress carries no client-facing message")
	}
}
