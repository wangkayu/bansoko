package application

import (
	"context"
	"errors"
	"testing"
	"time"
)

// An attempt-deadline overrun during preparation precedes the permit, so it
// is a retryable budget overrun; a stopped handler or an ordinary failure is
// not. The expired deadline is injected; nothing sleeps.
func TestPreparationOverranOnlyForTicketDeadline(t *testing.T) {
	handler, cancelHandler := context.WithCancel(context.Background())
	defer cancelHandler()
	expired, cancelExpired := context.WithDeadline(handler, time.Now().Add(-time.Second))
	defer cancelExpired()
	live, cancelLive := context.WithDeadline(handler, time.Now().Add(time.Hour))
	defer cancelLive()
	failure := errors.New("read failed")
	if !preparationOverran(handler, expired, failure) {
		t.Fatal("ticket deadline overrun must be retryable")
	}
	if preparationOverran(handler, expired, nil) {
		t.Fatal("a successful preparation is not an overrun")
	}
	if preparationOverran(handler, live, failure) {
		t.Fatal("an ordinary failure inside the deadline is not an overrun")
	}
	cancelHandler()
	if preparationOverran(handler, expired, failure) {
		t.Fatal("a stopped handler is an interruption, not an overrun")
	}
}
