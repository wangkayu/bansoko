package application

import (
	"bansoko/internal/workitems"
	"context"
	"errors"
	"testing"
)

func TestHandlerStaticValidationPrecedesIsolationDatabase(t *testing.T) {
	for _, s := range []*Service{nil, {}} {
		_, err := s.Handle(context.Background(), workitems.Claim{Kind: "invalid"})
		if !errors.Is(err, ErrInvalidKind) {
			t.Fatalf("kind requires database: %v", err)
		}
		_, err = s.Handle(nil, workitems.Claim{Kind: workitems.KindFileExecute})
		if err == nil || err.Error() != "file operation handler context is nil" {
			t.Fatalf("nil context: %v", err)
		}
		_, err = s.Handle(context.Background(), workitems.Claim{Kind: workitems.KindFileExecute})
		if !errors.Is(err, ErrInvalidClaim) {
			t.Fatalf("owner requires database: %v", err)
		}
	}
}
