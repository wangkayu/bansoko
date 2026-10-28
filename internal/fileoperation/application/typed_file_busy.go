package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"github.com/jackc/pgx/v5/pgconn"
)

// An overwrite publication whose rename onto the existing target was refused
// busy (held open over SMB/CIFS, or not permitted) is retried every
// fileBusyRetryAfter until fileBusyWindow has passed since the first refusal;
// then it fails with TARGET_BUSY.
const (
	fileBusyRetryAfter = 5 * time.Minute
	fileBusyWindow     = 24 * time.Hour
)

// busyPublication reports a publication refused busy whose staged temp the
// executor removed: the only shape that may be requeued.
func busyPublication(cause error) bool {
	return domain.FileBusy(cause) && domain.PublicationFailureCode(cause) == domain.CodeTargetBusy
}

// requeueBusyPublication resets the operation's publication evidence and
// attempt budget and hands it to a successor WorkItem due after
// fileBusyRetryAfter (file_busy_requeue), in the claim's typed stage
// transaction. The target was not replaced and the temp is gone, so the claim
// ends with no external effect. A refused requeue (the window passed, or the
// persisted evidence is not the busy shape) fails the operation with
// TARGET_BUSY instead.
func (s *Service) requeueBusyPublication(ctx context.Context, c workitems.Claim, cause error) (worker.Outcome, error) {
	reason := boundedReason(safeReason(cause), 1024)
	window := fmt.Sprintf("%d seconds", int64(fileBusyWindow/time.Second))
	err := s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT public.file_busy_requeue($1,$2,$3,$4,$5,$6::interval)`,
			c.Owner.ID, c.ID, c.FencingToken, reason, fileBusyRetryAfter.Seconds(), window).Scan(&raw); err != nil {
			return "", err
		}
		return workitems.DispositionNoExternalEffect, nil
	})
	if err != nil {
		var pg *pgconn.PgError
		// Any integrity refusal (the window passed, the evidence is not a
		// busy shape, or any guard or constraint refused the reset) ends the
		// operation like an exhausted window: nothing was replaced and no
		// temp is left, so it fails visibly instead of as an unknown error.
		if errors.As(err, &pg) && strings.HasPrefix(pg.Code, "23") {
			return s.finishTypedFailure(ctx, c, domain.CodeTargetBusy, cause)
		}
		return worker.Outcome{}, fmt.Errorf("typed busy publication requeue not committed: %w", err)
	}
	out := worker.Committed(c, workitems.OwnerResult{State: "pending", ReasonCode: domain.CodeTargetBusy, Reason: reason})
	out.Disposition = workitems.DispositionNoExternalEffect
	return out, nil
}
