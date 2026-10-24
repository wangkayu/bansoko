package filemaintenance

import (
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	"context"
	"errors"
)

// Negative contract double: records attempted invocation, never manufactures
// an applied final receipt from a path observation or executor error.
type offsetPendingReceiptExecutor struct {
	fakeOpsExecutor
	reads int
}

func (e *offsetPendingReceiptExecutor) ExecuteMaintenance(ctx context.Context, op domain.FileOperation, _ executorpkg.MaintenanceInvocation) (domain.FileOperation, error) {
	return e.Execute(ctx, op)
}
func (e *offsetPendingReceiptExecutor) ReadMaintenanceReceipt(context.Context, executorpkg.MaintenanceInvocation) (executorpkg.MaintenanceReceipt, error) {
	e.reads++
	return executorpkg.MaintenanceReceipt{}, errors.New("exact final receipt unavailable")
}
